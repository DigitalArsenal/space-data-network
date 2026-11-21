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
// each failed scope and, for a failed batch, how many of its records no
// completed publication of the lane exported. When the alert clears is
// decided from the store (dataset_publication_alert.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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
	// startStamp and heldStamp order the run's start and its first hold of
	// its lane (before it read the lane; 0 until then) among the service's
	// publications (publicationRuns.order). Under publicationRuns.mu.
	startStamp, heldStamp uint64
	done                  chan struct{}
	result                *DatasetPublicationResult
	err                   error
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
	// order is the last stamp given to a run's start or hold of its lane.
	order uint64
	// alert is the failed scopes of each lane and the worker that counts
	// their records (dataset_publication_alert.go).
	alert publicationAlertState
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
	return &publicationRuns{byKey: make(map[string]*publicationRun), life: life, stop: stop}
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
		runs.order++
		run = &publicationRun{started: s.now(), startStamp: runs.order, done: make(chan struct{})}
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
			s.reportDatasetPublication(req, run, result, err, errors.Is(context.Cause(workCtx), errPublicationsStopped))
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

// reportDatasetPublication logs a finished publication and raises its lane's
// publication_failed alert when it failed, or hands a completed one to what
// clears the alert. A publication the node's stop cancelled is logged as
// that and changes no alert. On a completed publication's path it reads no
// store: what needs the store runs on the alert worker afterwards
// (dataset_publication_alert.go).
func (s *ConcreteDatasetPublicationService) reportDatasetPublication(req DatasetPublicationRequest, run *publicationRun, result *DatasetPublicationResult, err error, stopped bool) {
	lane := datasetPublicationLaneName(req)
	batch := strings.TrimSpace(req.BatchID)
	took := s.now().Sub(run.started).Round(time.Millisecond)
	if err != nil && stopped {
		log.Warnf("Dataset publication %s batch %q stopped with the node after %s, at step %q", lane, batch, took, publicationStepOf(err))
		return
	}
	if err != nil {
		log.Errorf("Dataset publication FAILED: %s batch %q, step %q, after %s: %v", lane, batch, publicationStepOf(err), took, err)
		text := fmt.Sprintf("dataset publication %s batch %q failed at step %q after %s: %v", lane, batch, publicationStepOf(err), took, err)
		s.failPublication(req, lane, s.runs.readStamp(run), text)
		return
	}
	records, manifest := 0, ""
	if result != nil {
		records, manifest = result.RecordCount, result.ManifestCID
	}
	log.Infof("Dataset publication %s batch %q completed in %s: %d records, manifest %s", lane, batch, took, records, manifest)
	s.publicationCompleted(req, lane, s.runs.readStamp(run))
}

// markHeld records that run holds its lane, before it reads it.
func (r *publicationRuns) markHeld(run *publicationRun) {
	if r == nil || run == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.heldStamp == 0 {
		r.order++
		run.heldStamp = r.order
	}
}

// readStamp orders run's read of its lane among the service's publications:
// its hold of the lane (publications of a lane hold it one at a time, and
// read it only while they hold it), or, for a run that never held it, its
// start (a failed run that never held its lane read nothing; its scope is
// what the lane held when it was requested). It is an order, not a clock: a
// step of the wall clock does not change it.
func (r *publicationRuns) readStamp(run *publicationRun) uint64 {
	if r == nil || run == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run.heldStamp != 0 {
		return run.heldStamp
	}
	return run.startStamp
}
