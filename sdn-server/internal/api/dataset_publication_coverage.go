package api

// dataset_publication_coverage.go — which records of a failed batch no
// completed publication carries, followed off the publications' own path.
//
// A failed batch's alert (dataset_publication_runs.go) clears once every
// record of the batch is in a completed publication of its lane. Finding
// that out reads the store, so it is done here, after the publication was
// reported done and its callers answered, by one worker per service that
// runs one job at a time in the order the publications ended, on a context
// the node's stop (StopPublications) cancels:
//
//   - when a batch's publication fails, its records are counted once
//     (storage.UnpublishedBatchRecords: one read of the batch's CIDs and
//     their tags, of the lane's series, and a fingerprint of each batch
//     tagging them that has a completed series). A record counts as
//     published only when tagged to a batch whose completed series exported
//     the batch's records as the store holds them now. The CIDs no completed
//     publication carries are kept in memory, at most
//     maxFollowedUnpublishedRecords per batch and maxFollowedLaneRecords
//     per lane. A batch with none is published, and its scope is removed. A
//     batch that fails again is not counted again.
//   - when a whole batch of the lane is published, the CIDs of that batch
//     that a followed batch holds are read (one read of its CID list, no
//     tags), and then the batch is fingerprinted: only when it still holds
//     exactly what its completed series exported are they removed from the
//     followed batches. A batch that gained records after the publication
//     read it removes nothing. A followed batch with none left is
//     published, and when no failed scope of the lane is left the alert
//     clears.
//
// So a stuck batch costs one full count when it fails, and each later
// whole-batch publication of its lane costs one read of the published
// batch's CID list and one fingerprint of it, whatever the number of stuck
// batches; a publication of a lane with no followed batch costs nothing
// here. The followed CIDs are in memory, as the failed scopes are: a
// restart forgets both, and with them the alert.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// maxFollowedUnpublishedRecords bounds the unpublished CIDs followed for one
// failed batch, and maxFollowedLaneRecords for all failed batches of one
// lane. A batch with more than its share, or counted when its lane follows
// its bound already, is not followed: it keeps its alert until a whole-batch
// publication of it that read the lane after its failure completes. One
// followed CID takes about 110 bytes of memory (a 59-character CID in a
// set), so a lane follows at most about 44 MB.
const (
	maxFollowedUnpublishedRecords = 200_000
	maxFollowedLaneRecords        = 400_000
)

// errLaneFollowBound is the state of a failed batch that is not followed
// because its lane follows maxFollowedLaneRecords already.
var errLaneFollowBound = fmt.Errorf("its lane follows the unpublished records of other failed batches up to its bound of %d", maxFollowedLaneRecords)

// failedBatchCoverage is what is known of one failed batch's records.
type failedBatchCoverage struct {
	counted     bool
	err         error               // the count failed or is not possible: the batch stays unpublished
	unpublished map[string]struct{} // CIDs no completed publication carries
	total       int64
}

// publicationCoverage is the coverage worker's state.
type publicationCoverage struct {
	mu      sync.Mutex
	queue   []func(context.Context)
	running bool
	// batches is, per lane, the failed batches followed.
	batches map[string]map[string]*failedBatchCoverage
	// failure is, per lane, the text of its last failed publication, which
	// the alert keeps while the counts after it change.
	failure map[string]string
}

// noteFailure keeps the text of a lane's failed publication.
func (s *ConcreteDatasetPublicationService) noteFailure(lane, text string) {
	if s.runs == nil {
		return
	}
	c := &s.runs.coverage
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure == nil {
		c.failure = make(map[string]string)
	}
	c.failure[lane] = text
}

// failureText is the text of a lane's last failed publication.
func (s *ConcreteDatasetPublicationService) failureText(lane string) string {
	if s.runs == nil {
		return ""
	}
	c := &s.runs.coverage
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure[lane]
}

// enqueueCoverage runs job after the jobs before it, on the worker, unless
// the node is stopping.
func (s *ConcreteDatasetPublicationService) enqueueCoverage(job func(context.Context)) {
	runs := s.runs
	if runs == nil {
		return
	}
	c := &runs.coverage
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = append(c.queue, job)
	if c.running {
		return
	}
	runs.mu.Lock()
	if runs.stopped {
		runs.mu.Unlock()
		c.queue = nil
		return
	}
	runs.active.Add(1)
	runs.mu.Unlock()
	c.running = true
	go func() {
		defer runs.active.Done()
		for {
			c.mu.Lock()
			if len(c.queue) == 0 || runs.life.Err() != nil {
				c.queue = nil
				c.running = false
				c.mu.Unlock()
				return
			}
			next := c.queue[0]
			c.queue = c.queue[1:]
			c.mu.Unlock()
			next(runs.life)
		}
	}()
}

// followFailedBatch counts the records of a lane's failed batch that no
// completed publication carries, once, and restates the lane's alert.
func (s *ConcreteDatasetPublicationService) followFailedBatch(req DatasetPublicationRequest, lane, batch string) {
	if batch == "" || s.runs == nil {
		return
	}
	c := &s.runs.coverage
	c.mu.Lock()
	if known := c.batches[lane][batch]; known != nil && known.counted {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	s.enqueueCoverage(func(ctx context.Context) {
		c.mu.Lock()
		known := c.batches[lane][batch]
		c.mu.Unlock()
		if (known != nil && known.counted) || !s.runs.isFailed(lane, batch) {
			return
		}
		state := &failedBatchCoverage{counted: true}
		var cids []string
		// The CIDs the lane may still follow; the worker is the only writer
		// of the followed sets, so the room it reads stays until it writes.
		room := maxFollowedLaneRecords - s.followedLaneRecords(lane)
		limit := min(maxFollowedUnpublishedRecords, room)
		switch {
		case s.store == nil || strings.TrimSpace(req.DatastoreKey) != "":
			state.err = storage.ErrBatchCoverageUnsupported
		case room <= 0:
			state.err = errLaneFollowBound
		default:
			cids, state.total, state.err = s.store.UnpublishedBatchRecords(ctx, normalizeDatasetPublicationSchema(req.Schema), req.ProviderID, req.SourceName, batch, limit)
			if errors.Is(state.err, storage.ErrTooManyUnpublished) && limit < maxFollowedUnpublishedRecords {
				state.err = errLaneFollowBound
			}
		}
		if ctx.Err() != nil {
			return
		}
		if state.err == nil {
			state.unpublished = make(map[string]struct{}, len(cids))
			for _, cid := range cids {
				state.unpublished[cid] = struct{}{}
			}
			log.Infof("Dataset publication %s: failed batch %q: %d of its %d records are in no completed publication of the lane", lane, batch, len(cids), state.total)
		} else {
			log.Warnf("Dataset publication %s: the records of failed batch %q are not followed, so its alert stays until a publication of the whole batch completes: %v", lane, batch, state.err)
		}
		if state.err == nil && len(cids) == 0 && s.runs.isFailed(lane, batch) {
			// Every record of the batch is in a completed publication of
			// the lane that exported its batch as the store holds it.
			log.Infof("Dataset publication %s: every record of failed batch %q (%d) is in a completed publication of the lane", lane, batch, state.total)
			s.runs.dropScope(lane, batch)
			s.forgetCoverage(lane, batch)
			s.restateOrClear(lane, fmt.Sprintf("dataset publication %s: after the count of failed batch %q, ", lane, batch))
			return
		}
		c.mu.Lock()
		if s.runs.isFailed(lane, batch) {
			if c.batches == nil {
				c.batches = make(map[string]map[string]*failedBatchCoverage)
			}
			if c.batches[lane] == nil {
				c.batches[lane] = make(map[string]*failedBatchCoverage)
			}
			c.batches[lane][batch] = state
		}
		c.mu.Unlock()
		if scopes := s.runs.failedScopes(lane); len(scopes) > 0 {
			text := s.alertText(lane, scopes)
			if failure := s.failureText(lane); failure != "" {
				text = failure + " (" + text + ")"
			}
			s.alerts.Restate(ops.KindPublicationFailed, lane, text)
		}
	})
}

// followedLaneRecords is how many unpublished CIDs the lane's followed
// failed batches hold.
func (s *ConcreteDatasetPublicationService) followedLaneRecords(lane string) int {
	c := &s.runs.coverage
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, state := range c.batches[lane] {
		n += len(state.unpublished)
	}
	return n
}

// restateOrClear clears the lane's alert when no failed scope of it is left,
// or restates it, the text starting with prefix.
func (s *ConcreteDatasetPublicationService) restateOrClear(lane, prefix string) {
	left := s.runs.failedScopes(lane)
	if len(left) == 0 {
		s.forgetCoverage(lane, "")
		s.alerts.Clear(ops.KindPublicationFailed, lane)
		return
	}
	text := prefix + s.alertText(lane, left)
	log.Warnf("Dataset publication %s: %d failed scope(s) are still unpublished: %s", lane, len(left), text)
	s.alerts.Restate(ops.KindPublicationFailed, lane, text)
}

// coverByPublishedBatch removes the records of a lane's batch whose whole
// publication completed from the lane's followed failed batches, marks a
// followed batch with none left published, and clears the lane's alert when
// no failed scope is left (or restates it).
func (s *ConcreteDatasetPublicationService) coverByPublishedBatch(req DatasetPublicationRequest, lane, batch string) {
	if batch == "" || s.runs == nil {
		return
	}
	c := &s.runs.coverage
	s.enqueueCoverage(func(ctx context.Context) {
		c.mu.Lock()
		followed := 0
		for _, state := range c.batches[lane] {
			if state.err == nil && len(state.unpublished) > 0 {
				followed++
			}
		}
		c.mu.Unlock()
		if followed == 0 {
			return
		}
		schema := normalizeDatasetPublicationSchema(req.Schema)
		// The CIDs tagged to the batch that a followed batch holds. They are
		// what the publication exported only when the batch holds what its
		// completed series exported, which is checked after the read: a tag
		// stays while the store holds its record, so a CID read here is in a
		// set the check then finds equal to the series' set.
		found := make(map[string]struct{})
		err := s.store.EachLaneBatchCIDPage(ctx, schema, req.ProviderID, req.SourceName, batch, func(cids []string) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, state := range c.batches[lane] {
				for _, cid := range cids {
					if _, ok := state.unpublished[cid]; ok {
						found[cid] = struct{}{}
					}
				}
			}
			return nil
		})
		if err == nil {
			var held bool
			if held, err = s.store.BatchPublishedAsHeld(ctx, schema, req.ProviderID, req.SourceName, batch); err == nil && !held {
				log.Infof("Dataset publication %s: batch %q holds records its completed publication did not export (it gained records after it was read), so it takes no record off the failed batches", lane, batch)
				return
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				log.Warnf("Dataset publication %s: the records of published batch %q could not be read, so no failed batch is cleared by it: %v", lane, batch, err)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		var done []string
		for failed, state := range c.batches[lane] {
			if state.err != nil {
				continue
			}
			for cid := range found {
				delete(state.unpublished, cid)
			}
			if len(state.unpublished) == 0 {
				done = append(done, failed)
				delete(c.batches[lane], failed)
				log.Infof("Dataset publication %s: every record of failed batch %q (%d) is in a completed publication of the lane", lane, failed, state.total)
			}
		}
		c.mu.Unlock()
		for _, failed := range done {
			s.runs.dropScope(lane, failed)
		}
		s.restateOrClear(lane, fmt.Sprintf("dataset publication %s: after the publication of batch %q, ", lane, batch))
	})
}

// forgetCoverage drops what is followed of a lane's batch ("" for every
// batch of the lane), once it is published.
func (s *ConcreteDatasetPublicationService) forgetCoverage(lane, batch string) {
	if s.runs == nil {
		return
	}
	c := &s.runs.coverage
	c.mu.Lock()
	defer c.mu.Unlock()
	if batch == "" {
		delete(c.batches, lane)
		delete(c.failure, lane)
		return
	}
	delete(c.batches[lane], batch)
	if len(c.batches[lane]) == 0 {
		delete(c.batches, lane)
	}
}

// alertText names a lane's failed scopes for its alert: each batch with how
// many of its records no completed publication carries, as far as known.
func (s *ConcreteDatasetPublicationService) alertText(lane string, scopes []string) string {
	var c *publicationCoverage
	if s.runs != nil {
		c = &s.runs.coverage
		c.mu.Lock()
		defer c.mu.Unlock()
	}
	parts := make([]string, 0, len(scopes))
	for _, batch := range scopes {
		if batch == "" {
			parts = append(parts, "the whole lane is unpublished")
			continue
		}
		var state *failedBatchCoverage
		if c != nil {
			state = c.batches[lane][batch]
		}
		switch {
		case state == nil || !state.counted:
			parts = append(parts, fmt.Sprintf("batch %q is unpublished (its records are being counted)", batch))
		case errors.Is(state.err, storage.ErrTooManyUnpublished):
			parts = append(parts, fmt.Sprintf("batch %q is unpublished: more than %d of its records are in no completed publication of the lane", batch, maxFollowedUnpublishedRecords))
		case errors.Is(state.err, errLaneFollowBound):
			parts = append(parts, fmt.Sprintf("batch %q is unpublished (its records are not followed: %v; it stays until a publication of the whole batch completes)", batch, state.err))
		case state.err != nil:
			parts = append(parts, fmt.Sprintf("batch %q is unpublished (which of its records are published is unknown: %v)", batch, state.err))
		default:
			parts = append(parts, fmt.Sprintf("batch %q is unpublished: %d of its %d records are in no completed publication of the lane", batch, len(state.unpublished), state.total))
		}
	}
	return strings.Join(parts, "; ")
}
