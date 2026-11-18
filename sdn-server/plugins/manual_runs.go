package plugins

// manual_runs.go — an operator's manual run of a schedule belongs to the
// node, not to the request that started it.
//
// The run-now route ran the schedule on its request's context, so the run
// lived exactly as long as the HTTP request. host-01, 2026-11-17: `apps run`
// of the deep-space catalogs ingest flow gave up after its client's 60 s
// timeout, the request's context ended, and the node cancelled the run
// ("context canceled") although the flow needed longer; the cancelled run had
// already booked the provider's once-a-day attempt.
//
// So a manual run runs on a context of its own, which keeps the request's
// values and drops its cancellation (the dataset publication runs of
// internal/api do the same since 2026-11-17). Whoever started it may wait for
// it, poll it or leave: the run continues to its end. It ends early only when
// an operator cancels it (CancelRuntimeModuleScheduleRun) or the node stops
// (Close). A second request to run the same schedule while one runs joins
// that run instead of starting another. Scheduled (cron) runs are not
// touched: they run on the cron context, which only a schedule change or the
// node's stop ends.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrManualRunCancelled is the cause of a manual run an operator cancelled.
var ErrManualRunCancelled = errors.New("cancelled by the operator")

// errManualRunsStopped is the cause of a manual run the node's stop ended.
var errManualRunsStopped = errors.New("the node is stopping")

// ErrNoManualRun answers a status or cancel request for a schedule with no
// manual run.
var ErrNoManualRun = errors.New("no manual run")

// manualRunStopWait bounds how long Close waits for cancelled manual runs.
const manualRunStopWait = 20 * time.Second

// manualRun is one running manual run of a schedule.
type manualRun struct {
	run    RuntimeModuleScheduleRun // Status "running" until done
	done   chan struct{}
	cancel context.CancelCauseFunc
	err    error // the run's error, for the synchronous caller
}

func manualRunKey(moduleID, methodID string) string { return moduleID + "\x00" + methodID }

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
}

// StartRuntimeModuleScheduleRun starts an operator's manual run of a
// schedule on the node, or joins the one already running (Joined set), and
// returns its state ("running") and a channel closed when it ends. The run
// does not end with ctx: only its own end, CancelRuntimeModuleScheduleRun or
// the node's stop end it.
func (m *Manager) StartRuntimeModuleScheduleRun(ctx context.Context, moduleID, methodID string, input []byte) (RuntimeModuleScheduleRun, <-chan struct{}, error) {
	active, run, err := m.startManualRun(ctx, moduleID, methodID, input)
	if err != nil {
		return RuntimeModuleScheduleRun{}, nil, err
	}
	return run, active.done, nil
}

// startManualRun is StartRuntimeModuleScheduleRun, also answering the run.
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

	m.manualMu.Lock()
	m.manualInitLocked()
	if m.manualClosed {
		m.manualMu.Unlock()
		return nil, RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q: %w", moduleID, methodID, errManualRunsStopped)
	}
	if existing, ok := m.manualRuns[key]; ok {
		run := existing.run
		run.Joined = true
		m.manualMu.Unlock()
		log.Infof("Plugin %q manual run %q is already running (started %s): this request joins it", moduleID, methodID, run.StartedAt)
		return existing, run, nil
	}
	now := time.Now().UTC()
	runCtx, cancel := context.WithCancelCause(WithManualRun(context.WithoutCancel(ctx)))
	active := &manualRun{
		run: RuntimeModuleScheduleRun{
			ID:        runtimeModuleCommandID(now, 1),
			MethodID:  methodID,
			Trigger:   "manual",
			StartedAt: now.Format(time.RFC3339),
			Status:    "running",
		},
		done:   make(chan struct{}),
		cancel: cancel,
	}
	m.manualRuns[key] = active
	m.manualWg.Add(1)
	life := m.manualLife
	m.manualMu.Unlock()

	stopWithNode := context.AfterFunc(life, func() { cancel(context.Cause(life)) })
	log.Infof("Plugin %q manual run %q start", moduleID, methodID)
	go func() {
		defer m.manualWg.Done()
		defer cancel(nil)
		defer stopWithNode()
		output, err := cp.InvokeCron(runCtx, methodID, input)
		run := active.run
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
			// not tell an operator's cancel from the node's stop.
			if cause := context.Cause(runCtx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(err, cause) {
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
	return active, active.run, nil
}

// RuntimeModuleScheduleRunState is a schedule's running manual run, or else
// its last one (from the schedule's run history, which outlives a restart).
// It answers ErrNoManualRun when the schedule has none.
func (m *Manager) RuntimeModuleScheduleRunState(moduleID, methodID string) (RuntimeModuleScheduleRun, error) {
	if m == nil {
		return RuntimeModuleScheduleRun{}, errors.New("plugin manager is nil")
	}
	if _, err := m.cronMethod(moduleID, methodID); err != nil {
		return RuntimeModuleScheduleRun{}, err
	}
	key := manualRunKey(moduleID, methodID)
	m.manualMu.Lock()
	m.manualInitLocked()
	if active, ok := m.manualRuns[key]; ok {
		run := active.run
		m.manualMu.Unlock()
		return run, nil
	}
	if run, ok := m.manualLast[key]; ok {
		m.manualMu.Unlock()
		return run, nil
	}
	m.manualMu.Unlock()
	m.inputStateMu.RLock()
	defer m.inputStateMu.RUnlock()
	history := m.inputState[moduleID].Schedules[methodID].RunHistory
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Trigger == "manual" {
			return history[i], nil
		}
	}
	return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q: %w", moduleID, methodID, ErrNoManualRun)
}

// CancelRuntimeModuleScheduleRun cancels a schedule's running manual run and
// waits for it to end, at most until ctx ends. It returns the run's state:
// ended, or still "running" when ctx ended first. With no run running it
// answers ErrNoManualRun.
func (m *Manager) CancelRuntimeModuleScheduleRun(ctx context.Context, moduleID, methodID string) (RuntimeModuleScheduleRun, error) {
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
	m.manualMu.Unlock()
	if !ok {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q has no running manual run: %w", moduleID, methodID, ErrNoManualRun)
	}
	log.Infof("Plugin %q manual run %q (started %s): cancelled by the operator", moduleID, methodID, active.run.StartedAt)
	active.cancel(ErrManualRunCancelled)
	select {
	case <-active.done:
	case <-ctx.Done():
	}
	m.manualMu.Lock()
	defer m.manualMu.Unlock()
	return active.run, nil
}

// stopManualRuns cancels every running manual run, refuses new ones and
// waits for them to end, at most manualRunStopWait.
func (m *Manager) stopManualRuns() {
	m.manualMu.Lock()
	m.manualInitLocked()
	m.manualClosed = true
	running := len(m.manualRuns)
	stop := m.manualStop
	m.manualMu.Unlock()
	stop(errManualRunsStopped)
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
	case <-time.After(manualRunStopWait):
		log.Warnf("%d manual run(s) still running when the node stopped waiting for them", running)
	}
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
