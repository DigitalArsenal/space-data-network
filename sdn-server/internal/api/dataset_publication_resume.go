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
// publication-runs folder, action "publish", with the time the publication was
// requested and the number of attempts made to publish it again. The mark is
// removed when the run completes or fails, and kept when the node's stop
// cancels it or the process ends. A request refused because the node is
// stopping (for an update or any other stop) leaves a mark too, action
// "refused", unless the same publication already has one.
//
// The marks are read when the publication service starts, before any flow
// can start a publication, and handled once the node answers ready
// (ResumeInterruptedPublications), newest first, one at a time. For each, one
// row of the store is read: the newest series of the mark's lane
// (NewestDatasetPublicationSeries). No record is read to decide.
//   - The lane has a series that started at or after the mark's publication
//     was requested: if it is of the mark's own batch, the publication
//     completed (its series is recorded before its mark is removed);
//     otherwise the lane has moved on to newer data, and publishing the
//     mark's batch now would make it the lane's newest series and move the
//     lane's IPNS pointer back to it. Either way the mark is removed and
//     nothing is published.
//   - The mark is older than publicationMarkMaxAge: removed, not published.
//   - publicationRedoMaxAttempts attempts were already started (counted in
//     the mark, written durably before each attempt starts, so an attempt
//     that ends the process counts too): the publication is reported FAILED,
//     its alert stays raised, and the mark is removed.
//   - Otherwise an interrupted run is first reported as a failure at the step
//     "interrupted by the node's stop", which raises the lane's
//     publication_failed alert, and the publication is made again; when it
//     completes, or when its mark is dropped by the first rule, the report
//     is cleared. A failed attempt is retried with the auto-publisher's
//     backoff, and the two rules above are checked again before each attempt.
//
// A publication is identified by its request, so publishing a mark again is
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

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// publicationRunsDir is the folder of the marks under the publication
// directory. Its name is no schema's, so the publication-file sweep passes it.
const publicationRunsDir = "publication-runs"

// The actions of a mark.
const (
	publicationMarkRunning = "publish"
	publicationMarkRefused = "refused"
)

// publicationRedoMaxAttempts bounds the attempts to publish a mark again,
// across every start: a publication that ends the process (out of memory, a
// panic) is tried this many times, not at every boot.
const publicationRedoMaxAttempts = 3

// publicationMarkMaxAge bounds how long after it was requested a publication
// is published again: an older mark (left by a node that was stopped for a
// day, or by this build before a rollback to one that ignores marks) is
// removed without a publication.
const publicationMarkMaxAge = 24 * time.Hour

// The faults of a test build (dataset_publication_faults_e2e.go); nil in
// every release build. publicationRunFault runs at the start of every
// publication run; publicationRedoDelay is how long the handling of a mark
// waits first.
var (
	publicationRunFault  func(req DatasetPublicationRequest)
	publicationRedoDelay func(sourceName string) time.Duration
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

// publicationMark is one mark.
type publicationMark struct {
	req    DatasetPublicationRequest
	action string
	// started is when the publication was first requested.
	started time.Time
	// attempts is how many attempts to publish it again were started.
	attempts int
	path     string
}

// redoContextKey marks the context of an attempt to publish a mark again:
// its run keeps the mark the attempt wrote instead of writing a new one.
type redoContextKey struct{}

func publicationRedo(ctx context.Context) bool {
	redo, _ := ctx.Value(redoContextKey{}).(bool)
	return redo
}

// mark records req, whose run started at started, as running. It reports
// whether req has a mark.
func (r *publicationRuns) mark(req DatasetPublicationRequest, started time.Time) bool {
	return r.writeMark(publicationMark{req: req, action: publicationMarkRunning, started: started}, true)
}

func (r *publicationRuns) writeMark(m publicationMark, replace bool) bool {
	if r == nil || r.marks == "" || !markablePublication(m.req) {
		return false
	}
	path := r.markPath(m.req)
	if !replace {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	req := markRequest(m.req)
	raw := publicationstate.Encode(&pst.BodyT{Type: pst.BodyBatchAction, Value: &pst.BatchActionT{
		Schema: req.Schema, Provider: req.ProviderID, Source: req.SourceName, Batch: req.BatchID, Action: m.action,
		Started: m.started.UnixNano(), Attempts: int32(m.attempts),
	}})
	if err := writeFileDurably(path, raw); err != nil {
		log.Warnf("Dataset publication %s batch %q: its mark could not be written, so the next start does not publish it if the process ends first: %v",
			datasetPublicationLaneName(req), req.BatchID, err)
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

// refuseForRestart is the error of a new publication refused at now because
// the node is stopping or restarting for an update (cause), after leaving a
// mark for it.
func (r *publicationRuns) refuseForRestart(req DatasetPublicationRequest, cause error, now time.Time) error {
	lane, batch := datasetPublicationLaneName(req), strings.TrimSpace(req.BatchID)
	if r.writeMark(publicationMark{req: req, action: publicationMarkRefused, started: now}, false) {
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

// readMark reads the mark at path.
func (r *publicationRuns) readMark(path string) (publicationMark, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return publicationMark{}, err
	}
	body, err := publicationstate.Decode(raw, pst.BodyBatchAction)
	if err != nil {
		return publicationMark{}, err
	}
	action := body.Value.(*pst.BatchActionT)
	req := DatasetPublicationRequest{Schema: action.Schema, ProviderID: action.Provider, SourceName: action.Source, BatchID: action.Batch}
	if !markablePublication(req) || r.markPath(req) != path {
		return publicationMark{}, fmt.Errorf("it does not name the publication its file name is for")
	}
	if action.Started <= 0 {
		return publicationMark{}, fmt.Errorf("it states no time the publication was requested")
	}
	return publicationMark{req: req, action: action.Action, started: time.Unix(0, action.Started).UTC(), attempts: int(action.Attempts), path: path}, nil
}

// readPublicationMarks reads the marks, newest first. An unreadable mark is
// logged and removed: it names no publication that could be made. Temporary
// files of a write the process ended during are removed.
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
		path := filepath.Join(r.marks, entry.Name())
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".write-") {
			_ = os.Remove(path)
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fb") {
			continue
		}
		m, err := r.readMark(path)
		if err != nil {
			log.Errorf("Dataset publications: mark %s is unreadable and is removed; the publication it names is not published again: %v", path, err)
			_ = os.Remove(path)
			continue
		}
		marks = append(marks, m)
	}
	// Newest first: a lane's newest publication made again supersedes the
	// lane's older marks (outdatedMark).
	sort.Slice(marks, func(i, j int) bool { return marks[i].started.After(marks[j].started) })
	return marks
}

// ReadInterruptedPublications reads the marks a previous process left, for
// ResumeInterruptedPublications, and returns how many there are. The daemon
// calls it when the service starts, before any flow can start a
// publication, so no mark of this process is among them.
func (s *ConcreteDatasetPublicationService) ReadInterruptedPublications() int {
	if s == nil || s.runs == nil {
		return 0
	}
	marks := s.runs.readPublicationMarks()
	s.runs.mu.Lock()
	s.runs.resume = marks
	s.runs.mu.Unlock()
	if len(marks) > 0 {
		log.Infof("Dataset publications: %d publication(s) did not complete before the node stopped; each is published again once the node is ready, unless its lane has published since or it is older than %s",
			len(marks), publicationMarkMaxAge)
	}
	return len(marks)
}

// ResumeInterruptedPublications handles, one at a time, the marks
// ReadInterruptedPublications read. The daemon runs it once the node answers
// ready, on the node's context.
func (s *ConcreteDatasetPublicationService) ResumeInterruptedPublications(ctx context.Context) {
	if s == nil || s.runs == nil {
		return
	}
	s.runs.mu.Lock()
	marks := s.runs.resume
	s.runs.resume = nil
	s.runs.mu.Unlock()
	if len(marks) > 0 && s.store == nil {
		log.Errorf("Dataset publications: %d publication(s) did not complete before the node stopped, and the node has no store to publish them from; their marks are kept", len(marks))
		return
	}
	for _, mark := range marks {
		if publicationRedoDelay != nil {
			if wait := publicationRedoDelay(mark.req.SourceName); wait > 0 {
				log.Warnf("test build: the mark of %s batch %q is handled after %s", datasetPublicationLaneName(mark.req), mark.req.BatchID, wait)
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !s.resumePublication(ctx, mark) {
			return
		}
	}
}

// outdatedMark says why m's publication is not made again ("" when it is):
// its lane has a series that started at or after it was requested, or it is
// older than publicationMarkMaxAge. It reads one row of the store.
func (s *ConcreteDatasetPublicationService) outdatedMark(ctx context.Context, m publicationMark) (reason string, superseded bool, err error) {
	lane := storage.DatasetPublicationLane{SchemaName: m.req.Schema, ProviderID: m.req.ProviderID, SourceName: m.req.SourceName}
	batch, at, ok, err := s.store.NewestDatasetPublicationSeries(ctx, lane)
	if err != nil {
		return "", false, err
	}
	requested := m.started.Format(time.RFC3339Nano)
	if ok && !at.Before(m.started) {
		if batch == m.req.BatchID {
			return fmt.Sprintf("its publication completed before the node stopped (series of %s, requested %s)", at.Format(time.RFC3339Nano), requested), false, nil
		}
		return fmt.Sprintf("the lane published %s at %s, after this publication was requested at %s; publishing it now would replace newer data",
			scopeName(batch), at.Format(time.RFC3339Nano), requested), true, nil
	}
	if age := s.now().Sub(m.started); age > publicationMarkMaxAge {
		return fmt.Sprintf("it was requested %s ago (%s), longer than the %s after which a publication is not made again",
			age.Round(time.Second), requested, publicationMarkMaxAge), false, nil
	}
	return "", false, nil
}

// reportRedo reports a failure of a mark's publication that no run of this
// process reports: the run the process ended during, or giving up on it.
func (s *ConcreteDatasetPublicationService) reportRedo(req DatasetPublicationRequest, since time.Time, step string, cause error) {
	s.reportDatasetPublication(req, s.now().Sub(since), nil, atPublicationStep(step, cause), false)
}

// clearRedoReport clears what reportRedo and the failed attempts raised for
// req's scope.
func (s *ConcreteDatasetPublicationService) clearRedoReport(req DatasetPublicationRequest) {
	lane := datasetPublicationLaneName(req)
	if left := s.runs.coverScope(lane, strings.TrimSpace(req.BatchID)); left == 0 {
		s.alerts.Clear(ops.KindPublicationFailed, lane)
	}
}

// resumePublication handles one mark read at start. It returns false when the
// node is stopping.
func (s *ConcreteDatasetPublicationService) resumePublication(ctx context.Context, boot publicationMark) bool {
	req := boot.req
	lane, batch := datasetPublicationLaneName(req), req.BatchID
	reported := false
	for {
		m, err := s.runs.readMark(boot.path)
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		if err != nil {
			log.Errorf("Dataset publications: mark %s is unreadable and is removed; the publication it names is not published again: %v", boot.path, err)
			_ = os.Remove(boot.path)
			return true
		}
		if !m.started.Equal(boot.started) {
			// A publication of this process wrote it: that run keeps it.
			return true
		}
		reason, superseded, err := s.outdatedMark(ctx, m)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			log.Errorf("Dataset publication %s batch %q: whether it was published since cannot be read (%v); its mark is kept for the next start", lane, batch, err)
			return true
		}
		if reason != "" {
			s.runs.unmark(req)
			cleared := ""
			if reported {
				s.clearRedoReport(req)
				cleared = "; its report as interrupted is cleared"
			}
			if superseded {
				log.Warnf("Dataset publication %s batch %q is NOT published again: %s. Its mark is removed%s", lane, batch, reason, cleared)
			} else {
				log.Infof("Dataset publication %s batch %q is not published again: %s. Its mark is removed%s", lane, batch, reason, cleared)
			}
			return true
		}
		if m.attempts >= publicationRedoMaxAttempts {
			s.runs.unmark(req)
			s.reportRedo(req, m.started, "publish again after the node's stop", fmt.Errorf(
				"given up after %d attempts to publish it again (requested %s); the last one did not complete; its mark is removed and it is not tried again",
				m.attempts, m.started.Format(time.RFC3339Nano)))
			return true
		}
		// Counted before the attempt starts: an attempt that ends the
		// process counts too.
		m.attempts++
		if !s.runs.writeMark(m, true) {
			log.Errorf("Dataset publication %s batch %q: its attempt count cannot be recorded, so it is not published again by this start", lane, batch)
			return true
		}
		if !reported {
			reported = true
			if m.action == publicationMarkRefused {
				log.Warnf("Dataset publication %s batch %q was refused while the node stopped (%s ago); it is published now (attempt %d of %d)",
					lane, batch, s.now().Sub(m.started).Round(time.Second), m.attempts, publicationRedoMaxAttempts)
			} else {
				s.reportRedo(req, m.started, "interrupted by the node's stop", fmt.Errorf(
					"it was running when the node stopped and did not complete; it is published again (attempt %d of %d)", m.attempts, publicationRedoMaxAttempts))
			}
		}
		_, err = s.PublishDatasetUpdate(context.WithValue(ctx, redoContextKey{}, true), req)
		if err == nil {
			// Also a completion with no records (its batch was retired
			// meanwhile), which clears no alert by itself.
			s.clearRedoReport(req)
			return true
		}
		if PublicationRefusedForRestart(err) {
			// The attempt did not start.
			m.attempts--
			s.runs.writeMark(m, true)
			return false
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return false
		}
		if m.attempts >= publicationRedoMaxAttempts {
			log.Errorf("Dataset publication %s batch %q: given up after %d attempts to publish it again: %v; it is not tried again", lane, batch, m.attempts, err)
			s.runs.unmark(req)
			return true
		}
		// Kept while it waits, so a stop during the wait does not lose it.
		s.runs.writeMark(m, true)
		wait := autoPublishRetryBackoff(m.attempts)
		log.Warnf("Dataset publication %s batch %q: attempt %d of %d to publish it again failed; next in %s", lane, batch, m.attempts, publicationRedoMaxAttempts, wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
