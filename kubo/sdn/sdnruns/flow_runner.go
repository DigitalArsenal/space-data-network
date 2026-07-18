package sdnruns

// flow_runner.go — the OD run engine driven by the baked $PIV OD flow + FlowPool,
// the cut-over from the Fitter/StoreEphemerisSource path. On its cron tick it, for
// each enabled provider, invokes the provider module (in-memory $OEM stream), fits
// every object through the pool (feeder -> od.fit -> store), and records the run
// from the produced $OMM rows the CollectingSink captured. Ephemeris is never
// stored; only the fit RESULTS ($OMM/$OCM/$OBD) are persisted by the flow's store
// node. Registers with the node cron scheduler under the SAME ModuleID as the old
// runner, so it takes its place (default hourly).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/plugins"
)

// FlowRunner is a sdncron.CronModule.
type FlowRunner struct {
	engine  *FlowRunEngine
	sink    *CollectingSink
	runs    *Store
	resolve func() RunConfig
	log     Logger
}

// NewFlowRunner builds the flow-driven run engine. engine, sink and runs are
// required; resolve supplies the effective RunConfig (nil => ConfigDefault).
func NewFlowRunner(engine *FlowRunEngine, sink *CollectingSink, runs *Store, resolve func() RunConfig, log Logger) (*FlowRunner, error) {
	if engine == nil {
		return nil, fmt.Errorf("sdnruns: FlowRunner requires a FlowRunEngine")
	}
	if sink == nil {
		return nil, fmt.Errorf("sdnruns: FlowRunner requires a CollectingSink")
	}
	if runs == nil {
		return nil, fmt.Errorf("sdnruns: FlowRunner requires a run Store")
	}
	return &FlowRunner{engine: engine, sink: sink, runs: runs, resolve: resolve, log: log}, nil
}

func (r *FlowRunner) logf(format string, args ...interface{}) {
	if r.log != nil {
		r.log(format, args...)
	}
}

func (r *FlowRunner) resolveConfig() RunConfig {
	cfg := ConfigDefault()
	if r.resolve != nil {
		got := r.resolve()
		if len(got.EnabledProviders) > 0 {
			cfg.EnabledProviders = got.EnabledProviders
		}
		if strings.TrimSpace(got.ProducedSource) != "" {
			cfg.ProducedSource = got.ProducedSource
		}
		if got.ObjectCap > 0 {
			cfg.ObjectCap = got.ObjectCap
		}
	}
	return cfg
}

// --- sdncron.CronModule ---

func (r *FlowRunner) ID() string { return ModuleID }

func (r *FlowRunner) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{
		Method:          RunTimerMethod,
		Description:     "Fit enabled providers' in-memory $OEM through the baked OD flow and record a run.",
		DefaultInterval: defaultInterval,
		Input:           "none",
		Output:          "json",
	}}
}

func (r *FlowRunner) InvokeCron(ctx context.Context, method string, _ []byte) ([]byte, error) {
	if method != RunTimerMethod {
		return nil, fmt.Errorf("sdnruns: unknown timer method %q", method)
	}
	run, err := r.Run(ctx)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(`{"ok":true,"run":%q,"objects":%d}`, run.ID, run.ObjectsDone)), nil
}

// Run executes one run over the currently-enabled providers, recording it. Cron
// entry point + the API run-now path.
func (r *FlowRunner) Run(ctx context.Context) (*Run, error) {
	return r.RunProviders(ctx, r.resolveConfig())
}

// RunProviders fits cfg.EnabledProviders through the baked OD flow and records the
// run. The run is StartRun'd (status "running") before the fits, so a long
// constellation run is visible in /sdn/v1/runs while it proceeds; each produced
// $OMM becomes an object row (objects_done) once the providers finish.
func (r *FlowRunner) RunProviders(ctx context.Context, cfg RunConfig) (*Run, error) {
	started := time.Now().UTC()
	providers := dedupe(cfg.EnabledProviders)
	r.sink.Reset()

	run := &Run{
		ID:        NewRunID(started),
		Started:   started,
		Status:    StatusRunning,
		Providers: providers,
		Objects:   []ObjectResult{},
	}
	if err := r.runs.StartRun(run); err != nil {
		return nil, fmt.Errorf("sdnruns: start run: %w", err)
	}
	r.logf("sdnruns: flow run %s started: providers=%v", run.ID, providers)

	for _, p := range providers {
		res, err := r.engine.RunProvider(ctx, p, cfg.ObjectCap)
		if err != nil {
			r.logf("sdnruns: provider %q run failed: %v", p, err)
			continue
		}
		r.logf("sdnruns: provider %q: %d objects, %d fitted", p, res.Objects, res.Fitted)
	}

	// Each produced $OMM the store node persisted -> one run object row.
	for _, obj := range r.sink.Collected() {
		if err := r.runs.AppendObject(run.ID, obj); err != nil {
			r.logf("sdnruns: run %s append object %d failed: %v", run.ID, obj.Norad, err)
		}
	}

	if err := r.runs.FinishRun(run.ID, StatusCompleted, ""); err != nil {
		return nil, fmt.Errorf("sdnruns: finish run: %w", err)
	}
	final, err := r.runs.Get(run.ID)
	if err != nil {
		return nil, err
	}
	r.logf("sdnruns: flow run %s completed: providers=%d objects_done=%d", final.ID, len(providers), final.ObjectsDone)
	return &final, nil
}
