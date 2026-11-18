package api

// A dataset publication that has started finishes whatever its caller does.
//
// The publish route answers with the completed publication (its manifest and
// PNM CIDs), and the flows that call it read that answer. A publication of a
// large lane can outlast a client's deadline: when the publication ran on
// the request's context, the client's departure cancelled it after the shard
// and index were written and before the manifest, the publication row and the
// announcement, with nothing logged and the flow reporting success (host-02,
// 2026-11-01 to 2026-11-17: no $OMM or $MPE publication completed).
//
// So a publication runs on its own context, which keeps the caller's values
// and drops its cancellation. It waits for its lane at most
// storage.DatasetPublicationLaneWait and runs at most
// storage.DatasetPublicationDeadline once it holds the lane; the node's
// stop cancels it (StopPublications). The caller waits for the result, also
// after its client has left (the answer then reaches no one, and the
// publication is complete). A second request for the same publication while
// one runs waits for that one's result instead of starting another.
// Publications of one lane run one at a time (publicationHygiene.lockLane).
// A caller bound to its publications (the auto-publisher) stops waiting at
// its own deadline, and a run no caller waits for any more is cancelled
// (runDatasetPublication).
//
// Every publication that fails is logged as an error naming its lane, batch
// and step, and raises the publication_failed alert of its lane, which names
// each failed batch and how many of its records no completed publication of
// the lane carries. The alert clears once no
// scope that failed in the lane is unpublished: the same batch, or the whole
// lane (a publication with no batch), is published with at least one
// record, or every record of the failed batch is in a completed publication
// of the lane. That last case is how a later run clears it: a later pull of
// the same source that returns a record again tags it to the later batch,
// whose publication carries it (storage.UnpublishedBatchRecords). A record
// of the failed batch that no later pull returned is in no publication, so
// the alert stays and its text, restated after each publication of the
// lane, says how many records are still unpublished. A request that
// publishes no record (an empty batch) or announces existing publications
// clears nothing. A failed announcement of existing publications raises the
// alert and leaves no scope unpublished.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// errPublicationsStopped is the cause of a publication cancelled by the
// node's stop.
var errPublicationsStopped = errors.New("the node is stopping")

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
	done    chan struct{}
	result  *DatasetPublicationResult
	err     error
	// Under publicationRuns.mu: the callers waiting for the result, how to
	// cancel the run when the last of them leaves, and the files the run
	// reused whose time could not be refreshed (the sweep keeps them).
	waiters int
	cancel  context.CancelCauseFunc
	kept    map[string]bool
}

// keepFiles keeps paths of run from the publication-file sweep until the
// run ends.
func (r *publicationRuns) keepFiles(run *publicationRun, paths []string) {
	if r == nil || len(paths) == 0 {
		return
	}
	if run == nil {
		log.Warnf("publication files %v were reused without a refreshed time outside a publication run: a sweep may remove them", paths)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.kept == nil {
		run.kept = make(map[string]bool)
	}
	for _, path := range paths {
		run.kept[path] = true
	}
}

// keptFiles is every running publication's kept files.
func (r *publicationRuns) keptFiles() map[string]bool {
	kept := map[string]bool{}
	if r == nil {
		return kept
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.byKey {
		for path := range run.kept {
			kept[path] = true
		}
	}
	return kept
}

// publicationRuns is the service's running publications and what they share:
// the publication-file sweep's exclusion of exports, the failed scopes of
// each lane, and the node's stop.
type publicationRuns struct {
	mu    sync.Mutex
	byKey map[string]*publicationRun
	// failed is, per lane, the batches (or "" for the whole lane) whose
	// publication failed and that no publication has covered since.
	failed map[string]map[string]bool
	// files is held shared while an export writes or reuses files in the
	// publication directory and exclusively by a sweep of it.
	files sync.RWMutex
	// stop ends every run's context; stopped refuses new runs.
	life    context.Context
	stop    context.CancelCauseFunc
	stopped bool
	active  sync.WaitGroup
}

func newPublicationRuns() *publicationRuns {
	life, stop := context.WithCancelCause(context.Background())
	return &publicationRuns{byKey: make(map[string]*publicationRun), failed: make(map[string]map[string]bool), life: life, stop: stop}
}

// oldestStart is the start of the oldest running publication other than
// self, or the zero time.
func (r *publicationRuns) oldestStart(self *publicationRun) time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var oldest time.Time
	for _, run := range r.byKey {
		if run == self {
			continue
		}
		if oldest.IsZero() || run.started.Before(oldest) {
			oldest = run.started
		}
	}
	return oldest
}

// writingFiles holds off the publication-file sweep until the returned func
// is called.
func (r *publicationRuns) writingFiles() func() {
	if r == nil {
		return func() {}
	}
	r.files.RLock()
	return r.files.RUnlock
}

type publicationRunKey struct{}

// runOf is the publication run a context belongs to.
func runOf(ctx context.Context) *publicationRun {
	run, _ := ctx.Value(publicationRunKey{}).(*publicationRun)
	return run
}

type publicationBoundToCallerKey struct{}

// withPublicationBoundToCaller marks a caller whose deadline and
// cancellation bound the publications it requests.
func withPublicationBoundToCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicationBoundToCallerKey{}, true)
}

// SetAlerts binds the node's operational alert registry.
func (s *ConcreteDatasetPublicationService) SetAlerts(alerts *ops.Registry) {
	if s != nil {
		s.alerts = alerts
	}
}

// StopPublications cancels every running publication, refuses new ones, and
// waits until the running ones have returned or ctx ends. The node calls it
// before it closes its store.
func (s *ConcreteDatasetPublicationService) StopPublications(ctx context.Context) {
	if s == nil || s.runs == nil {
		return
	}
	runs := s.runs
	runs.mu.Lock()
	runs.stopped = true
	running := len(runs.byKey)
	runs.mu.Unlock()
	runs.stop(errPublicationsStopped)
	done := make(chan struct{})
	go func() {
		runs.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		if running > 0 {
			log.Infof("Dataset publications: %d running publication(s) stopped with the node", running)
		}
	case <-ctx.Done():
		runs.mu.Lock()
		left := len(runs.byKey)
		runs.mu.Unlock()
		log.Warnf("Dataset publications: %d publication(s) still running when the node stopped waiting for them", left)
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
// waits for its result.
//
// The run lives while any caller waits for it. A caller that is not bound to
// its publication (an HTTP client) waits for the result even after it leaves,
// so a run with such a caller is never cancelled by a caller. A bound caller
// (withPublicationBoundToCaller: the auto-publisher) stops waiting when its
// own context ends and answers its context's error; when it was the last
// caller waiting, the run is cancelled. So when the caller that started the
// run is bound and its deadline passes while another caller still waits, the
// run continues for that caller, within the run's own bounds (lane wait and
// deadline) and the node's stop.
func (s *ConcreteDatasetPublicationService) runDatasetPublication(ctx context.Context, req DatasetPublicationRequest, publish func(context.Context) (*DatasetPublicationResult, error)) (*DatasetPublicationResult, error) {
	lane := datasetPublicationLaneName(req)
	batch := strings.TrimSpace(req.BatchID)
	key := datasetPublicationRunKey(req)
	bound, _ := ctx.Value(publicationBoundToCallerKey{}).(bool)
	runs := s.runs
	if runs == nil {
		runs = newPublicationRuns()
	}

	runs.mu.Lock()
	if runs.stopped {
		runs.mu.Unlock()
		return nil, fmt.Errorf("dataset publication %s batch %q: %w", lane, batch, errPublicationsStopped)
	}
	run, joined := runs.byKey[key]
	if !joined {
		run = &publicationRun{started: s.now(), done: make(chan struct{})}
		runs.byKey[key] = run
		runs.active.Add(1)
	}
	run.waiters++
	var workCtx context.Context
	if !joined {
		workCtx, run.cancel = context.WithCancelCause(context.WithValue(context.WithoutCancel(ctx), publicationRunKey{}, run))
	}
	runs.mu.Unlock()

	if joined {
		log.Infof("Dataset publication %s batch %q is already running (started %s ago): this request waits for its result",
			lane, batch, s.now().Sub(run.started).Round(time.Millisecond))
	} else {
		cancel := run.cancel
		stopWithNode := context.AfterFunc(runs.life, func() { cancel(context.Cause(runs.life)) })
		go func() {
			defer runs.active.Done()
			defer cancel(nil)
			defer stopWithNode()
			// A publication that returned success is reported as one, also
			// when its context ended after it completed.
			result, err := publish(workCtx)
			run.result, run.err = result, err
			s.reportDatasetPublication(req, s.now().Sub(run.started), result, err, errors.Is(context.Cause(workCtx), errPublicationsStopped))
			runs.mu.Lock()
			delete(runs.byKey, key)
			runs.mu.Unlock()
			close(run.done)
		}()
	}

	leave := func() (last bool) {
		runs.mu.Lock()
		defer runs.mu.Unlock()
		run.waiters--
		return run.waiters == 0
	}
	select {
	case <-run.done:
		leave()
		return run.result, run.err
	case <-ctx.Done():
	}
	if !bound {
		log.Warnf("Dataset publication %s batch %q: the requesting client left after %s; the publication continues",
			lane, batch, s.now().Sub(run.started).Round(time.Millisecond))
		<-run.done
		leave()
		return run.result, run.err
	}
	cause := context.Cause(ctx)
	if leave() {
		run.cancel(fmt.Errorf("every caller waiting for it stopped waiting: %w", cause))
		log.Warnf("Dataset publication %s batch %q: its caller stopped waiting after %s (%v); no caller waits, so it is cancelled",
			lane, batch, s.now().Sub(run.started).Round(time.Millisecond), cause)
	} else {
		log.Warnf("Dataset publication %s batch %q: a caller stopped waiting after %s (%v); the publication continues for the others",
			lane, batch, s.now().Sub(run.started).Round(time.Millisecond), cause)
	}
	return nil, fmt.Errorf("dataset publication %s batch %q: %w", lane, batch, cause)
}

// reportDatasetPublication logs a finished publication and raises or clears
// its lane's publication_failed alert. A publication the node's stop
// cancelled is logged as that and changes no alert.
func (s *ConcreteDatasetPublicationService) reportDatasetPublication(req DatasetPublicationRequest, took time.Duration, result *DatasetPublicationResult, err error, stopped bool) {
	lane := datasetPublicationLaneName(req)
	batch := strings.TrimSpace(req.BatchID)
	took = took.Round(time.Millisecond)
	if err != nil && stopped {
		log.Warnf("Dataset publication %s batch %q stopped with the node after %s, at step %q", lane, batch, took, publicationStepOf(err))
		return
	}
	if err != nil {
		log.Errorf("Dataset publication FAILED: %s batch %q, step %q, after %s: %v", lane, batch, publicationStepOf(err), took, err)
		text := fmt.Sprintf("dataset publication %s batch %q failed at step %q after %s: %v", lane, batch, publicationStepOf(err), took, err)
		// An announcement of existing publications leaves nothing
		// unpublished: the lane's next publication clears its alert.
		if !req.AnnounceExisting {
			s.runs.failScope(lane, batch)
			text += " (" + s.unpublishedScopesText(req, lane, s.runs.failedScopes(lane)) + ")"
		}
		s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text)
		return
	}
	records, manifest := 0, ""
	if result != nil {
		records, manifest = result.RecordCount, result.ManifestCID
	}
	log.Infof("Dataset publication %s batch %q completed in %s: %d records, manifest %s", lane, batch, took, records, manifest)
	if req.AnnounceExisting || records == 0 {
		return
	}
	left := s.runs.coverScope(lane, batch)
	if len(left) > 0 && batch != "" && wholeBatchPublication(req) {
		left = s.coverByPublishedRecords(req, lane, left)
	}
	if len(left) == 0 {
		s.alerts.Clear(ops.KindPublicationFailed, lane)
		return
	}
	text := fmt.Sprintf("dataset publication %s: after the publication of batch %q, %s", lane, batch, s.unpublishedScopesText(req, lane, left))
	log.Warnf("Dataset publication %s: %d failed scope(s) are still unpublished: %s", lane, len(left), text)
	s.alerts.Restate(ops.KindPublicationFailed, lane, text)
}

// wholeBatchPublication reports a publication of every record of one batch
// of the node's own store: what its completion publishes is the batch's
// records, which the store's tags name.
func wholeBatchPublication(req DatasetPublicationRequest) bool {
	return strings.TrimSpace(req.DatastoreKey) == "" && datasetPublicationScopedToWholeBatch(req)
}

// coverByPublishedRecords removes from failed (batches of the request's
// lane whose publication failed) each batch whose every record a completed
// publication of the lane carries: a later pull that returned a record again
// tagged it to its own batch, and that batch's publication carried it. A
// batch with records no completed publication carries stays, as does every
// batch when the store cannot say (an error, or a store format that does not
// report it). It returns the batches left.
func (s *ConcreteDatasetPublicationService) coverByPublishedRecords(req DatasetPublicationRequest, lane string, failed []string) []string {
	var left []string
	for _, batch := range failed {
		if batch == "" {
			left = append(left, batch)
			continue
		}
		unpublished, total, err := s.unpublishedBatchRecords(req, batch)
		if err != nil {
			log.Warnf("Dataset publication %s: whether batch %q is published is unknown, so its alert stays: %v", lane, batch, err)
			left = append(left, batch)
			continue
		}
		if unpublished > 0 {
			left = append(left, batch)
			continue
		}
		log.Infof("Dataset publication %s: every record of failed batch %q (%d) is in a completed publication of the lane", lane, batch, total)
		s.runs.coverScope(lane, batch)
	}
	return left
}

// unpublishedBatchRecords counts the records of batch in the request's lane
// that no completed publication of the lane carries.
func (s *ConcreteDatasetPublicationService) unpublishedBatchRecords(req DatasetPublicationRequest, batch string) (int64, int64, error) {
	if s.store == nil || strings.TrimSpace(req.DatastoreKey) != "" {
		return 0, 0, storage.ErrBatchCoverageUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return s.store.UnpublishedBatchRecords(ctx, normalizeDatasetPublicationSchema(req.Schema), req.ProviderID, req.SourceName, batch)
}

// unpublishedScopesText names a lane's failed scopes for its alert: each
// batch with how many of its records no completed publication carries.
func (s *ConcreteDatasetPublicationService) unpublishedScopesText(req DatasetPublicationRequest, lane string, scopes []string) string {
	parts := make([]string, 0, len(scopes))
	for _, batch := range scopes {
		if batch == "" {
			parts = append(parts, "the whole lane is unpublished")
			continue
		}
		part := fmt.Sprintf("batch %q is unpublished", batch)
		if unpublished, total, err := s.unpublishedBatchRecords(req, batch); err == nil {
			part = fmt.Sprintf("batch %q is unpublished: %d of its %d records are in no completed publication of the lane", batch, unpublished, total)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// failScope records a failed scope of a lane.
func (r *publicationRuns) failScope(lane, batch string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed[lane] == nil {
		r.failed[lane] = make(map[string]bool)
	}
	r.failed[lane][batch] = true
}

// failedScopes is a lane's failed scopes, each a batch or "" for the whole
// lane, in order.
func (r *publicationRuns) failedScopes(lane string) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	scopes := make([]string, 0, len(r.failed[lane]))
	for scope := range r.failed[lane] {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

// coverScope removes the failed scopes a completed publication of batch
// ("" for the whole lane) covers and returns the scopes left.
func (r *publicationRuns) coverScope(lane, batch string) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if batch == "" {
		delete(r.failed, lane)
		r.mu.Unlock()
		return nil
	}
	delete(r.failed[lane], batch)
	if len(r.failed[lane]) == 0 {
		delete(r.failed, lane)
	}
	r.mu.Unlock()
	return r.failedScopes(lane)
}
