package api

// A PUBLICATION THE PROCESS ENDED DURING IS PUBLISHED AT THE NEXT START
// (stack issue 1044).
//
// On 2026-11-21 an update's restart stopped the node while the $MPE series of
// a GP run was being published; its $OMM series had completed. The running
// publications lived only in memory, so after the restart nothing knew the
// $MPE publication had not completed, and the two products of one run
// differed until the next run, three hours later.
//
// So every publication of a lane or of one batch, with every other request
// field at its default (the request a flow's IngestResult makes, and the
// auto-publisher's for a lane with no shard size of its own), keeps a durable
// mark while it runs: a $BatchAction FlatBuffer in the publication directory's
// publication-runs folder, action "publish". The mark is removed when the run
// completes or fails, and kept when the node's stop cancels it or the process
// ends. A request refused because the node is stopping or restarting for an
// update leaves a mark too, action "refused", unless the same publication
// already has one.
//
// At start (ResumeInterruptedPublications) each mark is published again, one
// at a time, oldest first. A mark whose set is the one its last publication
// exported (the publication completed before the process ended, or before
// its mark was removed) is dropped without a publication. An interrupted run
// is first reported as a failure at the step "interrupted by the node's
// stop", which raises the lane's publication_failed alert; the completed
// publication clears it. A publication that fails again is retried with the
// auto-publisher's backoff, at most autoPublishMaxAttempts times. A
// publication is identified by its request, so publishing a mark again is
// the same publication: a request for it that arrives meanwhile waits for it.
//
// Other publications (a datastore's, a dataset id's, a window with a limit or
// chunk size, a shard size, the full catalog, an announcement of existing
// publications) keep no mark: one the process ends during is logged as
// stopped with the node and is not published again.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

// publicationRunsDir is the folder of the marks under the publication
// directory. Its name is no schema's, so the publication-file sweep passes it.
const publicationRunsDir = "publication-runs"

// The actions of a mark.
const (
	publicationMarkRunning = "publish"
	publicationMarkRefused = "refused"
)

// markablePublication reports whether req names a whole lane or one batch
// with every other field at its default: what a mark records is enough to
// make it again.
func markablePublication(req DatasetPublicationRequest) bool {
	return strings.TrimSpace(req.DatastoreKey) == "" && strings.TrimSpace(req.DatasetID) == "" &&
		req.Limit == 0 && req.ChunkSize == 0 && req.MaxShardBytes == 0 && !req.FullCatalog &&
		!req.AnnounceExisting && len(req.AnnouncementSchemas) == 0 && normalizeDatasetPublicationSchema(req.Schema) != ""
}

// markRequest is the request a mark records.
func markRequest(req DatasetPublicationRequest) DatasetPublicationRequest {
	return DatasetPublicationRequest{
		Schema:     normalizeDatasetPublicationSchema(req.Schema),
		ProviderID: strings.TrimSpace(req.ProviderID),
		SourceName: strings.TrimSpace(req.SourceName),
		BatchID:    strings.TrimSpace(req.BatchID),
	}
}

func (r *publicationRuns) markPath(req DatasetPublicationRequest) string {
	sum := sha256.Sum256([]byte(datasetPublicationRunKey(markRequest(req))))
	return filepath.Join(r.marks, hex.EncodeToString(sum[:16])+".fb")
}

// mark records req as running. It reports whether req has a mark.
func (r *publicationRuns) mark(req DatasetPublicationRequest) bool {
	return r.writeMark(req, publicationMarkRunning, true)
}

func (r *publicationRuns) writeMark(req DatasetPublicationRequest, action string, replace bool) bool {
	if r == nil || r.marks == "" || !markablePublication(req) {
		return false
	}
	path := r.markPath(req)
	if !replace {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	m := markRequest(req)
	raw := publicationstate.Encode(&pst.BodyT{Type: pst.BodyBatchAction, Value: &pst.BatchActionT{
		Schema: m.Schema, Provider: m.ProviderID, Source: m.SourceName, Batch: m.BatchID, Action: action,
	}})
	if err := writeFileDurably(path, raw); err != nil {
		log.Warnf("Dataset publication %s batch %q: its mark could not be written, so the next start does not publish it if the process ends first: %v",
			datasetPublicationLaneName(req), m.BatchID, err)
		return false
	}
	return true
}

// unmark removes req's mark.
func (r *publicationRuns) unmark(req DatasetPublicationRequest) {
	if r == nil || r.marks == "" || !markablePublication(req) {
		return
	}
	if err := os.Remove(r.markPath(req)); err != nil && !os.IsNotExist(err) {
		log.Warnf("Dataset publication %s batch %q: remove its mark: %v", datasetPublicationLaneName(req), strings.TrimSpace(req.BatchID), err)
	}
}

// refuseForRestart is the error of a new publication refused because the
// node is stopping or restarting for an update (cause), after leaving a mark
// for it.
func (r *publicationRuns) refuseForRestart(req DatasetPublicationRequest, cause error) error {
	lane, batch := datasetPublicationLaneName(req), strings.TrimSpace(req.BatchID)
	if r.writeMark(req, publicationMarkRefused, false) {
		log.Warnf("Dataset publication %s batch %q refused: %v; the node publishes it when it has restarted", lane, batch, cause)
		return fmt.Errorf("dataset publication %s batch %q: %w; the node publishes it when it has restarted", lane, batch, cause)
	}
	log.Warnf("Dataset publication %s batch %q refused: %v; request it again once the node has restarted", lane, batch, cause)
	return fmt.Errorf("dataset publication %s batch %q: %w; request it again once the node has restarted", lane, batch, cause)
}

// writeFileDurably writes raw to path through a synced temporary file and a
// rename, then syncs the folder.
func writeFileDurably(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// publicationMark is one mark found at start.
type publicationMark struct {
	req     DatasetPublicationRequest
	action  string
	written time.Time
	path    string
}

// readPublicationMarks reads the marks, oldest first. An unreadable mark is
// logged and removed: it names no publication that could be made.
func (r *publicationRuns) readPublicationMarks() []publicationMark {
	if r == nil || r.marks == "" {
		return nil
	}
	entries, err := os.ReadDir(r.marks)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Errorf("Dataset publications: the marks of publications that were running when the node stopped cannot be read: %v", err)
		}
		return nil
	}
	var marks []publicationMark
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fb") {
			continue
		}
		path := filepath.Join(r.marks, entry.Name())
		info, infoErr := entry.Info()
		raw, readErr := os.ReadFile(path)
		var body *pst.BodyT
		if readErr == nil && infoErr == nil {
			body, readErr = publicationstate.Decode(raw, pst.BodyBatchAction)
		}
		if readErr == nil && infoErr != nil {
			readErr = infoErr
		}
		if readErr != nil {
			log.Errorf("Dataset publications: mark %s is unreadable and is removed; the publication it names is not published again: %v", path, readErr)
			_ = os.Remove(path)
			continue
		}
		action := body.Value.(*pst.BatchActionT)
		req := DatasetPublicationRequest{Schema: action.Schema, ProviderID: action.Provider, SourceName: action.Source, BatchID: action.Batch}
		if !markablePublication(req) || r.markPath(req) != path {
			log.Errorf("Dataset publications: mark %s does not name the publication its file name is for, and is removed", path)
			_ = os.Remove(path)
			continue
		}
		marks = append(marks, publicationMark{req: req, action: action.Action, written: info.ModTime().UTC(), path: path})
	}
	sort.Slice(marks, func(i, j int) bool { return marks[i].written.Before(marks[j].written) })
	return marks
}

// ResumeInterruptedPublications publishes, one at a time, every publication
// whose mark a previous process left: those it ended during and those it
// refused while it stopped. The daemon runs it once at start, on the node's
// context.
func (s *ConcreteDatasetPublicationService) ResumeInterruptedPublications(ctx context.Context) {
	if s == nil || s.runs == nil {
		return
	}
	marks := s.runs.readPublicationMarks()
	if len(marks) > 0 {
		log.Infof("Dataset publications: %d publication(s) did not complete before the node stopped; they are published again", len(marks))
	}
	for _, mark := range marks {
		if ctx.Err() != nil {
			return
		}
		if !s.resumePublication(ctx, mark) {
			return
		}
	}
}

// resumePublication publishes one mark's publication. It returns false when
// the node is stopping.
func (s *ConcreteDatasetPublicationService) resumePublication(ctx context.Context, mark publicationMark) bool {
	req := mark.req
	lane, batch := datasetPublicationLaneName(req), req.BatchID
	if unchanged, err := s.DatasetPublicationUnchanged(ctx, req); err == nil && unchanged {
		log.Infof("Dataset publication %s batch %q did not end before the node stopped, and its set is the one its last publication exported: it is not published again", lane, batch)
		s.runs.unmark(req)
		return true
	}
	if mark.action == publicationMarkRefused {
		log.Warnf("Dataset publication %s batch %q was refused while the node stopped (%s ago); it is published now", lane, batch, s.now().Sub(mark.written).Round(time.Second))
	} else {
		cause := fmt.Errorf("it was running when the node stopped and did not complete; it is published again")
		s.reportDatasetPublication(req, s.now().Sub(mark.written), nil, atPublicationStep("interrupted by the node's stop", cause), false)
	}
	for attempt := 1; ; attempt++ {
		_, err := s.PublishDatasetUpdate(ctx, req)
		if err == nil {
			return true
		}
		if PublicationRefusedForRestart(err) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return false
		}
		if attempt >= autoPublishMaxAttempts {
			log.Errorf("Dataset publication %s batch %q: given up after %d attempts since the node started: %v", lane, batch, attempt, err)
			return true
		}
		// Kept while it waits, so a stop during the wait does not lose it.
		s.runs.mark(req)
		wait := autoPublishRetryBackoff(attempt)
		log.Warnf("Dataset publication %s batch %q: attempt %d of %d since the node started failed; next in %s", lane, batch, attempt, autoPublishMaxAttempts, wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
