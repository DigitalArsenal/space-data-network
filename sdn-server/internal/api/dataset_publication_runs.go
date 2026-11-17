package api

// A dataset publication that has started finishes whatever its caller does.
//
// The publish route answers with the completed publication (its manifest and
// PNM CIDs), and the flows that call it read that answer. A flow's HTTP
// request has a deadline (the http capability clamps it to five minutes), and
// a publication of a large lane can outlast it: when the publication ran on
// the request's context, the client's departure cancelled it after the shard
// and index were written and before the manifest, the publication row and the
// announcement, with nothing logged and the flow reporting success (host-02,
// 2026-11-01 to 2026-11-17: no $OMM or $MPE publication completed).
//
// So a publication runs on its own context, which keeps the caller's values
// and drops its cancellation, bounded by datasetPublicationDeadline. The
// caller waits for the result; a caller that leaves gets its context's error
// and the publication goes on. A second request for the same publication
// while one runs waits for that one's result instead of starting another.
// Publications of one lane run one at a time (publicationHygiene.lockLane).
//
// Every publication that fails is logged as an error naming its lane, batch
// and step, and raises the publication_failed alert of its lane; the lane's
// next completed publication clears it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// datasetPublicationDeadline bounds one publication run.
const datasetPublicationDeadline = time.Hour

// publicationStepError names the step of a publication an error came from.
// Its text is the underlying error's.
type publicationStepError struct {
	step string
	err  error
}

func (e *publicationStepError) Error() string { return e.err.Error() }
func (e *publicationStepError) Unwrap() error { return e.err }

// atPublicationStep names the step of err, keeping the innermost step an
// error already carries.
func atPublicationStep(step string, err error) error {
	if err == nil {
		return nil
	}
	var named *publicationStepError
	if errors.As(err, &named) {
		return err
	}
	return &publicationStepError{step: step, err: err}
}

// publicationStepOf is the step err names, or "start".
func publicationStepOf(err error) string {
	var named *publicationStepError
	if errors.As(err, &named) {
		return named.step
	}
	return "start"
}

// publicationRun is one running publication.
type publicationRun struct {
	started time.Time
	schema  string
	done    chan struct{}
	result  *DatasetPublicationResult
	err     error
}

// publicationRuns is the service's running publications, by request and by
// schema (the file sweep keeps what a running publication of the schema may
// still be writing).
type publicationRuns struct {
	mu    sync.Mutex
	byKey map[string]*publicationRun
}

func newPublicationRuns() *publicationRuns {
	return &publicationRuns{byKey: make(map[string]*publicationRun)}
}

// oldestStart is the start of the oldest running publication of schema other
// than self, or the zero time.
func (r *publicationRuns) oldestStart(schema string, self *publicationRun) time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var oldest time.Time
	for _, run := range r.byKey {
		if run == self || run.schema != schema {
			continue
		}
		if oldest.IsZero() || run.started.Before(oldest) {
			oldest = run.started
		}
	}
	return oldest
}

type publicationRunKey struct{}

// runOf is the publication run a context belongs to.
func runOf(ctx context.Context) *publicationRun {
	run, _ := ctx.Value(publicationRunKey{}).(*publicationRun)
	return run
}

// SetAlerts binds the node's operational alert registry.
func (s *ConcreteDatasetPublicationService) SetAlerts(alerts *ops.Registry) {
	if s != nil {
		s.alerts = alerts
	}
}

// datasetPublicationRunKey identifies a request: two requests with the same
// fields are the same publication.
func datasetPublicationRunKey(req DatasetPublicationRequest) string {
	req.DatastoreKey = strings.TrimSpace(req.DatastoreKey)
	req.Schema = normalizeDatasetPublicationSchema(req.Schema)
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.SourceName = strings.TrimSpace(req.SourceName)
	req.BatchID = strings.TrimSpace(req.BatchID)
	req.DatasetID = strings.TrimSpace(req.DatasetID)
	key, _ := json.Marshal(req)
	return string(key)
}

// datasetPublicationLaneName is how logs and alerts name a request's lane.
func datasetPublicationLaneName(req DatasetPublicationRequest) string {
	schema := normalizeDatasetPublicationSchema(req.Schema)
	if key := strings.TrimSpace(req.DatastoreKey); key != "" {
		return strings.TrimSpace(schema + " datastore " + key)
	}
	return fmt.Sprintf("%s %s/%s", schema, strings.TrimSpace(req.ProviderID), strings.TrimSpace(req.SourceName))
}

// runDatasetPublication runs publish for req on a context of its own and
// waits for it, or for the caller to leave.
func (s *ConcreteDatasetPublicationService) runDatasetPublication(ctx context.Context, req DatasetPublicationRequest, publish func(context.Context) (*DatasetPublicationResult, error)) (*DatasetPublicationResult, error) {
	lane := datasetPublicationLaneName(req)
	batch := strings.TrimSpace(req.BatchID)
	key := datasetPublicationRunKey(req)
	runs := s.runs
	if runs == nil {
		runs = newPublicationRuns()
	}

	runs.mu.Lock()
	run, joined := runs.byKey[key]
	if !joined {
		run = &publicationRun{started: time.Now(), schema: normalizeDatasetPublicationSchema(req.Schema), done: make(chan struct{})}
		runs.byKey[key] = run
	}
	runs.mu.Unlock()

	if joined {
		log.Infof("Dataset publication %s batch %q is already running (started %s ago): this request waits for its result",
			lane, batch, time.Since(run.started).Round(time.Millisecond))
	} else {
		workCtx, cancel := context.WithTimeout(context.WithValue(context.WithoutCancel(ctx), publicationRunKey{}, run), datasetPublicationDeadline)
		go func() {
			defer cancel()
			result, err := publish(workCtx)
			if err == nil && workCtx.Err() != nil {
				err = workCtx.Err()
			}
			run.result, run.err = result, err
			s.reportDatasetPublication(lane, batch, time.Since(run.started), result, err)
			runs.mu.Lock()
			delete(runs.byKey, key)
			runs.mu.Unlock()
			close(run.done)
		}()
	}

	select {
	case <-run.done:
		return run.result, run.err
	case <-ctx.Done():
		log.Warnf("Dataset publication %s batch %q: the requesting client left after %s; the publication continues",
			lane, batch, time.Since(run.started).Round(time.Millisecond))
		return nil, fmt.Errorf("the client left before dataset publication %s batch %q completed; it continues: %w", lane, batch, ctx.Err())
	}
}

// reportDatasetPublication logs a finished publication and sets or clears
// its lane's publication_failed alert.
func (s *ConcreteDatasetPublicationService) reportDatasetPublication(lane, batch string, took time.Duration, result *DatasetPublicationResult, err error) {
	took = took.Round(time.Millisecond)
	if err != nil {
		text := fmt.Sprintf("dataset publication %s batch %q failed at step %q after %s: %v", lane, batch, publicationStepOf(err), took, err)
		log.Errorf("Dataset publication FAILED: %s batch %q, step %q, after %s: %v", lane, batch, publicationStepOf(err), took, err)
		s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text)
		return
	}
	records, manifest := 0, ""
	if result != nil {
		records, manifest = result.RecordCount, result.ManifestCID
	}
	log.Infof("Dataset publication %s batch %q completed in %s: %d records, manifest %s", lane, batch, took, records, manifest)
	s.alerts.Clear(ops.KindPublicationFailed, lane)
}
