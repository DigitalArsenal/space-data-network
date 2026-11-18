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
//     their tags, and of the lane's series), and the CIDs no completed
//     publication carries are kept in memory, at most
//     maxFollowedUnpublishedRecords per batch. A batch that fails again is
//     not counted again.
//   - when a whole batch of the lane is published, the CIDs of that batch
//     (one read of its CID list, no tags) are removed from every followed
//     batch of the lane. A followed batch with none left is published, and
//     when no failed scope of the lane is left the alert clears.
//
// So a stuck batch costs one full count when it fails, and each later
// publication of its lane costs one read of the published batch's CID list,
// whatever the number of stuck batches; a publication of a lane with no
// failed batch costs nothing here. The followed CIDs are in memory, as the
// failed scopes are: a restart forgets both, and with them the alert.

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
// failed batch (about 100 bytes each in memory). A batch with more keeps its
// alert until the batch or the whole lane is published.
const maxFollowedUnpublishedRecords = 200_000

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
		if s.store == nil || strings.TrimSpace(req.DatastoreKey) != "" {
			state.err = storage.ErrBatchCoverageUnsupported
		} else {
			cids, state.total, state.err = s.store.UnpublishedBatchRecords(ctx, normalizeDatasetPublicationSchema(req.Schema), req.ProviderID, req.SourceName, batch, maxFollowedUnpublishedRecords)
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
			log.Warnf("Dataset publication %s: whether the records of failed batch %q are published is unknown, so its alert stays until the batch or the lane is published: %v", lane, batch, state.err)
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
		if followed > 0 {
			err := s.store.EachLaneBatchCIDPage(ctx, normalizeDatasetPublicationSchema(req.Schema), req.ProviderID, req.SourceName, batch, func(cids []string) error {
				c.mu.Lock()
				defer c.mu.Unlock()
				for _, state := range c.batches[lane] {
					for _, cid := range cids {
						delete(state.unpublished, cid)
					}
				}
				return nil
			})
			if err != nil {
				if ctx.Err() == nil {
					log.Warnf("Dataset publication %s: the records of published batch %q could not be read, so no failed batch is cleared by it: %v", lane, batch, err)
				}
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		var done []string
		for failed, state := range c.batches[lane] {
			if state.err == nil && len(state.unpublished) == 0 {
				done = append(done, failed)
				delete(c.batches[lane], failed)
				log.Infof("Dataset publication %s: every record of failed batch %q (%d) is in a completed publication of the lane", lane, failed, state.total)
			}
		}
		c.mu.Unlock()
		for _, failed := range done {
			s.runs.coverScope(lane, failed)
		}
		left := s.runs.failedScopes(lane)
		if len(left) == 0 {
			s.forgetCoverage(lane, "")
			s.alerts.Clear(ops.KindPublicationFailed, lane)
			return
		}
		text := fmt.Sprintf("dataset publication %s: after the publication of batch %q, %s", lane, batch, s.alertText(lane, left))
		log.Warnf("Dataset publication %s: %d failed scope(s) are still unpublished: %s", lane, len(left), text)
		s.alerts.Restate(ops.KindPublicationFailed, lane, text)
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
		case state.err != nil:
			parts = append(parts, fmt.Sprintf("batch %q is unpublished (which of its records are published is unknown: %v)", batch, state.err))
		default:
			parts = append(parts, fmt.Sprintf("batch %q is unpublished: %d of its %d records are in no completed publication of the lane", batch, len(state.unpublished), state.total))
		}
	}
	return strings.Join(parts, "; ")
}
