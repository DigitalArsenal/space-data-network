package plugins

// manual_runs.go — an operator's manual run of a schedule belongs to the
// node, not to the request that started it.
//
// The run-now route ran the schedule on its request's context, so the run
// lived exactly as long as the HTTP request. host-01, 2026-11-17: `apps run`
// of the deep-space catalogs ingest flow gave up after its client's 60 s
// timeout, the request's context ended, and the node cancelled the run
// ("context canceled") although the flow needed longer.
//
// So a manual run runs on a context of its own, which keeps the request's
// values and drops its cancellation (the dataset publication runs of
// internal/api do the same since 2026-11-17). Whoever started it may wait for
// it, poll it by its id or leave: the run continues to its end. It ends
// early when an operator cancels it (CancelRuntimeModuleScheduleRun), when
// its bound passes (the provider's ManualRunBound, else
// DefaultManualRunBound, counted from its start and including any wait for
// an exclusive group), or when the node stops (StopManualRuns, Close). A
// second request to run the same schedule while one runs joins that run
// instead of starting another. Scheduled (cron) runs are not touched: they
// run on the cron context, which only a schedule change or the node's stop
// ends.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// ErrManualRunCancelled is the cause of a manual run an operator cancelled.
var ErrManualRunCancelled = errors.New("cancelled by the operator")

// ErrManualRunsStopped is the cause of a manual run the node's stop ended,
// and the answer to a caller still waiting for one when the node stops.
var ErrManualRunsStopped = errors.New("the node is stopping")

// ErrNoManualRun answers a status or cancel request for a schedule with no
// manual run, or for a run id the node does not know.
var ErrNoManualRun = errors.New("no manual run")

// ErrManualRunNotRunning answers a cancel request that names a run that is
// not the one running (it ended, or another run of the schedule runs).
var ErrManualRunNotRunning = errors.New("it is not the run running")

// DefaultManualRunBound bounds a manual run whose provider names no bound of
// its own (a flow service with no execution_timeout).
const DefaultManualRunBound = 2 * time.Hour

// ManualRunBounder is an optional CronProvider extension: how long an
// operator's manual run of method may take in all, from its start, including
// any wait for a slot it shares with other runs. Zero or less: the default.
type ManualRunBounder interface {
	ManualRunBound(method string) time.Duration
}

// manualRun is one running manual run of a schedule.
type manualRun struct {
	run    RuntimeModuleScheduleRun // under Manager.manualMu; Status "running" until done
	forced bool                     // started with {"force":true}
	done   chan struct{}
	cancel context.CancelCauseFunc
	err    error // under Manager.manualMu: the run's error, for the synchronous caller
}

func manualRunKey(moduleID, methodID string) string { return moduleID + "\x00" + methodID }

// manualRunForced reads the {"force":true} escape of a manual run's input,
// as the providers do: anything unparseable is not a force.
func manualRunForced(input []byte) bool {
	if len(input) == 0 {
		return false
	}
	var payload struct {
		Force bool `json:"force"`
	}
	return json.Unmarshal(input, &payload) == nil && payload.Force
}

// manualInitLocked readies the manual-run state; m.manualMu is held.
func (m *Manager) manualInitLocked() {
	if m.manualRuns == nil {
		m.manualRuns = make(map[string]*manualRun)
	}
	if m.manualLast == nil {
		m.manualLast = make(map[string]RuntimeModuleScheduleRun)
	}
	if m.manualLife == nil {
		m.manualLife, m.manualStop = context.WithCancelCause(context.Background())
	}
	if m.manualReleased == nil {
		m.manualReleased = make(chan struct{})
	}
}

// manualRunBound is how long a manual run of methodID on cp may take.
func manualRunBound(cp CronProvider, methodID string) time.Duration {
	if bounder, ok := cp.(ManualRunBounder); ok {
		if bound := bounder.ManualRunBound(methodID); bound > 0 {
			return bound
		}
	}
	return DefaultManualRunBound
}

// StartRuntimeModuleScheduleRun starts an operator's manual run of a
// schedule on the node, or joins the one already running (Joined set), and
// returns its state ("running", with its ID) and a channel closed when it
// ends. The run does not end with ctx.
func (m *Manager) StartRuntimeModuleScheduleRun(ctx context.Context, moduleID, methodID string, input []byte) (RuntimeModuleScheduleRun, <-chan struct{}, error) {
	active, run, err := m.startManualRun(ctx, moduleID, methodID, input)
	if err != nil {
		return RuntimeModuleScheduleRun{}, nil, err
	}
	return run, active.done, nil
}

// startManualRun is StartRuntimeModuleScheduleRun, also answering the run.
// The state it answers is a copy taken under m.manualMu.
func (m *Manager) startManualRun(ctx context.Context, moduleID, methodID string, input []byte) (*manualRun, RuntimeModuleScheduleRun, error) {
	if m == nil {
		return nil, RuntimeModuleScheduleRun{}, errors.New("plugin manager is nil")
	}
	cp, err := m.cronMethod(moduleID, methodID)
	if err != nil {
		return nil, RuntimeModuleScheduleRun{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := manualRunKey(moduleID, methodID)
	forced := manualRunForced(input)

	m.manualMu.Lock()
	m.manualInitLocked()
	if m.manualClosed {
		m.manualMu.Unlock()
		return nil, RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q: %w", moduleID, methodID, ErrManualRunsStopped)
	}
	if existing, ok := m.manualRuns[key]; ok {
		run := existing.run
		run.Joined = true
		// The input of a joining request is not used: the run already
		// started with its own. A force asked for here does not apply.
		run.ForceNotApplied = forced && !existing.forced
		m.manualMu.Unlock()
		log.Infof("Plugin %q manual run %q (id %s) is already running (started %s): this request joins it", moduleID, methodID, run.ID, run.StartedAt)
		if run.ForceNotApplied {
			log.Warnf("Plugin %q manual run %q (id %s): a forced request joined a run that was not forced; the force does not apply", moduleID, methodID, run.ID)
		}
		return existing, run, nil
	}
	now := time.Now().UTC()
	bound := manualRunBound(cp, methodID)
	runCtx, cancel := context.WithCancelCause(WithManualRun(context.WithoutCancel(ctx)))
	boundCtx, cancelBound := context.WithTimeoutCause(runCtx, bound, fmt.Errorf("the manual run's bound of %s passed", bound))
	active := &manualRun{
		run: RuntimeModuleScheduleRun{
			ID:        runtimeModuleCommandID(now, 1),
			MethodID:  methodID,
			Trigger:   "manual",
			StartedAt: now.Format(time.RFC3339),
			Status:    "running",
		},
		forced: forced,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	started := active.run
	m.manualRuns[key] = active
	m.manualWg.Add(1)
	life := m.manualLife
	m.manualMu.Unlock()

	stopWithNode := context.AfterFunc(life, func() { cancel(context.Cause(life)) })
	log.Infof("Plugin %q manual run %q start (id %s, bound %s)", moduleID, methodID, started.ID, bound)
	go func() {
		defer m.manualWg.Done()
		defer cancel(nil)
		defer cancelBound()
		defer stopWithNode()
		output, err := invokeManualRun(boundCtx, cp, moduleID, methodID, input)
		run := started
		run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		var deferred *DeferredError
		switch {
		case errors.As(err, &deferred):
			run.Status = "deferred"
			run.Message = err.Error()
			err = nil
		case err != nil:
			run.Status = "error"
			run.Message = err.Error()
			// Say why the run's context ended: "context canceled" alone does
			// not tell an operator's cancel from the node's stop or the
			// run's bound.
			if cause := context.Cause(boundCtx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(err, cause) {
				run.Message += " (" + cause.Error() + ")"
				err = fmt.Errorf("%w (%v)", err, cause)
			}
		default:
			run.Status = "ok"
			run.OutputSize = len(output)
		}
		log.Infof("Plugin %q manual run %q end status=%s output_bytes=%d duration=%s error=%q",
			moduleID, methodID, run.Status, run.OutputSize, time.Since(now), run.Message)
		m.recordRuntimeModuleScheduleRun(moduleID, methodID, run)
		m.manualMu.Lock()
		active.run, active.err = run, err
		delete(m.manualRuns, key)
		m.manualLast[key] = run
		m.manualMu.Unlock()
		close(active.done)
	}()
	return active, started, nil
}

// invokeManualRun runs the schedule. A panic in it ends the run with an
// error naming the panic (the request's handler used to catch it; the run's
// own goroutine must, or the node crashes).
func invokeManualRun(ctx context.Context, cp CronProvider, moduleID, methodID string, input []byte) (output []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("Plugin %q manual run %q panicked: %v\n%s", moduleID, methodID, recovered, debug.Stack())
			output, err = nil, fmt.Errorf("the run panicked: %v", recovered)
		}
	}()
	return cp.InvokeCron(ctx, methodID, input)
}

// RuntimeModuleScheduleRunState is a schedule's running manual run, or else
// its last one (from the schedule's run history, which outlives a restart).
// With runID set it is that run only: the running one, the last one, or one
// in the history; ErrNoManualRun when the node does not know it (a run a
// restart cut before it was recorded). Without a runID it answers
// ErrNoManualRun when the schedule has no manual run.
func (m *Manager) RuntimeModuleScheduleRunState(moduleID, methodID, runID string) (RuntimeModuleScheduleRun, error) {
	if m == nil {
		return RuntimeModuleScheduleRun{}, errors.New("plugin manager is nil")
	}
	if _, err := m.cronMethod(moduleID, methodID); err != nil {
		return RuntimeModuleScheduleRun{}, err
	}
	matches := func(run RuntimeModuleScheduleRun) bool { return runID == "" || run.ID == runID }
	key := manualRunKey(moduleID, methodID)
	m.manualMu.Lock()
	m.manualInitLocked()
	if active, ok := m.manualRuns[key]; ok && matches(active.run) {
		run := active.run
		m.manualMu.Unlock()
		return run, nil
	}
	if run, ok := m.manualLast[key]; ok && matches(run) {
		m.manualMu.Unlock()
		return run, nil
	}
	m.manualMu.Unlock()
	m.inputStateMu.RLock()
	defer m.inputStateMu.RUnlock()
	history := m.inputState[moduleID].Schedules[methodID].RunHistory
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Trigger == "manual" && matches(history[i]) {
			return history[i], nil
		}
	}
	if runID != "" {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q: run %s is not known to this node: %w", moduleID, methodID, runID, ErrNoManualRun)
	}
	return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q: %w", moduleID, methodID, ErrNoManualRun)
}

// CancelRuntimeModuleScheduleRun cancels a schedule's running manual run and
// waits for it to end, at most until ctx ends. It returns the run's state:
// ended, or still "running" when ctx ended first. With no run running it
// answers ErrNoManualRun. A runID ("" for whichever runs) names the run to
// cancel: when the running run is another one, or none runs, it cancels
// nothing and answers ErrManualRunNotRunning.
func (m *Manager) CancelRuntimeModuleScheduleRun(ctx context.Context, moduleID, methodID, runID string) (RuntimeModuleScheduleRun, error) {
	if m == nil {
		return RuntimeModuleScheduleRun{}, errors.New("plugin manager is nil")
	}
	if _, err := m.cronMethod(moduleID, methodID); err != nil {
		return RuntimeModuleScheduleRun{}, err
	}
	key := manualRunKey(moduleID, methodID)
	m.manualMu.Lock()
	m.manualInitLocked()
	active, ok := m.manualRuns[key]
	var started RuntimeModuleScheduleRun
	if ok {
		started = active.run
	}
	m.manualMu.Unlock()
	if runID = strings.TrimSpace(runID); runID != "" && (!ok || started.ID != runID) {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q: run %s: %w", moduleID, methodID, runID, ErrManualRunNotRunning)
	}
	if !ok {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q has no running manual run: %w", moduleID, methodID, ErrNoManualRun)
	}
	log.Infof("Plugin %q manual run %q (id %s, started %s): cancelled by the operator", moduleID, methodID, started.ID, started.StartedAt)
	active.cancel(ErrManualRunCancelled)
	select {
	case <-active.done:
	case <-ctx.Done():
	}
	m.manualMu.Lock()
	defer m.manualMu.Unlock()
	return active.run, nil
}

// waitManualRun waits for a manual run to end, for ctx to end, or for the
// node to release its waiters (StopManualRuns), and answers the run's state
// and error. When it stops waiting before the run ended it answers the run's
// state when it started and why it stopped waiting.
func (m *Manager) waitManualRun(ctx context.Context, active *manualRun, started RuntimeModuleScheduleRun) (RuntimeModuleScheduleRun, error) {
	m.manualMu.Lock()
	m.manualInitLocked()
	released := m.manualReleased
	m.manualMu.Unlock()
	select {
	case <-active.done:
	case <-ctx.Done():
		return started, ctx.Err()
	case <-released:
		select {
		case <-active.done:
		default:
			return started, fmt.Errorf("manual run %s did not end before the node stopped waiting for it: %w", started.ID, ErrManualRunsStopped)
		}
	}
	m.manualMu.Lock()
	defer m.manualMu.Unlock()
	run := active.run
	run.Joined = started.Joined
	run.ForceNotApplied = started.ForceNotApplied
	return run, active.err
}

// StopManualRuns is the first step of the node's stop for manual runs: it
// refuses new ones, cancels every running one, waits for them to end until
// ctx ends, and then releases every caller still waiting for one (their
// answer: ErrManualRunsStopped), so no request waits on a run while the
// node's HTTP servers shut down. A run that has not ended by then is waited
// for by Close, inside the node's bounded drain.
func (m *Manager) StopManualRuns(ctx context.Context) {
	if m == nil {
		return
	}
	m.manualMu.Lock()
	m.manualInitLocked()
	m.manualClosed = true
	running := len(m.manualRuns)
	stop := m.manualStop
	released := m.manualReleased
	m.manualMu.Unlock()
	stop(ErrManualRunsStopped)
	defer func() {
		m.manualReleaseOnce.Do(func() { close(released) })
	}()
	if running == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		m.manualWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		log.Infof("%d manual run(s) stopped with the node", running)
	case <-ctx.Done():
		m.manualMu.Lock()
		left := len(m.manualRuns)
		m.manualMu.Unlock()
		log.Warnf("%d manual run(s) still running when the node stopped waiting for them; their callers are released and the node's drain waits for the runs", left)
	}
}

// stopManualRuns is Close's step for manual runs: StopManualRuns if the node
// has not called it, then a wait for every manual run to end, as Close waits
// for cron runs. The wait has no bound of its own: Close runs inside the
// node's bounded drain (node.StopContext), which leaves the store open when
// a run outlives it, rather than closing plugins and the store under it.
func (m *Manager) stopManualRuns() {
	m.StopManualRuns(context.Background())
	m.manualWg.Wait()
}

// cronMethod is the schedule provider of moduleID that declares methodID.
func (m *Manager) cronMethod(moduleID, methodID string) (CronProvider, error) {
	plugin := m.Get(moduleID)
	if plugin == nil {
		return nil, fmt.Errorf("module %q not found", moduleID)
	}
	cp, ok := plugin.(CronProvider)
	if !ok {
		return nil, fmt.Errorf("module %q does not expose provider schedules", moduleID)
	}
	if _, ok := findCronMethodSpec(cp.CronMethods(), methodID); !ok {
		return nil, fmt.Errorf("module %q schedule %q not found", moduleID, methodID)
	}
	return cp, nil
}
