package plugins

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// lateCronPlugin is a minimal CronProvider that counts its ticks.
type lateCronPlugin struct {
	id       string
	interval string
	ticks    atomic.Int64
	started  atomic.Bool
}

func (p *lateCronPlugin) ID() string          { return p.id }
func (p *lateCronPlugin) Name() string        { return p.id }
func (p *lateCronPlugin) Version() string     { return "0.0.1" }
func (p *lateCronPlugin) Description() string { return "late-registered cron test plugin" }
func (p *lateCronPlugin) Start(context.Context, RuntimeContext) error {
	p.started.Store(true)
	return nil
}
func (p *lateCronPlugin) Close() error                    { return nil }
func (p *lateCronPlugin) RegisterRoutes(_ *http.ServeMux) {}

func (p *lateCronPlugin) CronMethods() []CronMethodSpec {
	return []CronMethodSpec{{Method: "tick", DefaultInterval: p.interval}}
}

func (p *lateCronPlugin) InvokeCron(_ context.Context, _ string, _ []byte) ([]byte, error) {
	p.ticks.Add(1)
	return []byte("{}"), nil
}

// A plugin registered AFTER StartAll must still be started AND scheduled.
//
// Flow services are loaded from config after the node is up, so before
// StartLateRegistered existed they were registered and then ignored: status
// "registered" forever, cron never attached to a ticker. On host-01 that showed
// as three CelesTrak ingest flows at run_count 0 / "never-run" — not waiting out
// a first interval, simply never wired.
func TestLateRegisteredPluginIsStartedAndScheduled(t *testing.T) {
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := m.StartAll(ctx, RuntimeContext{BaseDataPath: t.TempDir()}); err != nil {
		t.Fatalf("StartAll: %v", err)
	}

	// 1s is the shortest cadence resolveCronSchedule honours (anything
	// under a second falls back to its 30s floor).
	p := &lateCronPlugin{id: "late.flow.service", interval: "1s"}
	if err := m.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Registration alone starts nothing.
	if status, _ := m.pluginStatus(p.ID()); status != "registered" {
		t.Fatalf("status after Register = %q, want registered", status)
	}

	started, err := m.StartLateRegistered(p)
	if err != nil {
		t.Fatalf("StartLateRegistered: %v", err)
	}
	if !started {
		t.Fatal("StartLateRegistered reported the manager as not started, but StartAll ran")
	}
	if !p.started.Load() {
		t.Fatal("late-registered plugin was never started")
	}
	if status, _ := m.pluginStatus(p.ID()); status != "running" {
		t.Fatalf("status after StartLateRegistered = %q, want running", status)
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if p.ticks.Load() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("late-registered plugin's cron method never fired; its timer was never scheduled")
}

// Before StartAll, a late registration is a no-op — StartAll picks it up.
func TestStartLateRegisteredBeforeStartAllIsNoOp(t *testing.T) {
	m := New()
	p := &lateCronPlugin{id: "early.plugin", interval: "1h"}
	if err := m.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	started, err := m.StartLateRegistered(p)
	if err != nil {
		t.Fatalf("StartLateRegistered: %v", err)
	}
	if started {
		t.Fatal("StartLateRegistered started a plugin before the manager itself started")
	}
	if p.started.Load() {
		t.Fatal("plugin was started before StartAll")
	}
}
