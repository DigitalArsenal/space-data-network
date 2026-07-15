package sdncron

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/plugins"
)

// fakeModule is a minimal CronModule test double: it declares one timer and
// counts scheduled fires. It stands in for a real modulert.Module (which
// satisfies the same seam — see the compile-time assertion in
// sdnservices.go).
type fakeModule struct {
	id       string
	interval string // manifest default interval (Go duration string)
	fires    atomic.Int64
}

func (f *fakeModule) ID() string { return f.id }

func (f *fakeModule) CronMethods() []plugins.CronMethodSpec {
	return []plugins.CronMethodSpec{{
		Method:          "tick",
		Description:     "test timer",
		DefaultInterval: f.interval,
		Input:           "none",
		Output:          "json",
	}}
}

func (f *fakeModule) InvokeCron(_ context.Context, _ string, _ []byte) ([]byte, error) {
	f.fires.Add(1)
	return []byte("{}"), nil
}

func waitForFires(get func() int64, target int64, within time.Duration) int64 {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if get() >= target {
			return get()
		}
		time.Sleep(5 * time.Millisecond)
	}
	return get()
}

// TestCronSchedulerFiresAndReschedules is the primary acceptance test: a
// registered module's timer fires on schedule; changing the interval through
// the config-store/settings path (ApplyConfig) re-schedules the live ticker;
// and the change is persisted to the home-dir config file.
func TestCronSchedulerFiresAndReschedules(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	s := NewScheduler(store, nil)

	mod := &fakeModule{id: "test-mod", interval: "25ms"} // fast manifest default
	if err := s.Register(Registration{Module: mod, Name: "Test", Version: "1.0.0"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// ── Phase 1: fires on schedule (>=2 within a bounded window) ──────────────
	if got := waitForFires(func() int64 { return mod.fires.Load() }, 2, 2*time.Second); got < 2 {
		t.Fatalf("initial cadence: expected >=2 fires within 2s, got %d", got)
	}

	// ── Phase 2: reschedule SLOWER via the settings path; the live cadence must
	// drop (a fresh Ticker.Reset starts a new long interval). ─────────────────
	if _, err := s.ApplyConfig("test-mod", ModuleConfig{"interval_ms": float64(2000)}); err != nil {
		t.Fatalf("ApplyConfig(slow): %v", err)
	}
	time.Sleep(30 * time.Millisecond) // let the reset land
	baseline := mod.fires.Load()
	time.Sleep(400 * time.Millisecond)
	slowFires := mod.fires.Load() - baseline
	if slowFires > 1 {
		t.Fatalf("after reschedule to 2s: expected <=1 fire in 400ms, got %d (reschedule did not take effect)", slowFires)
	}

	// ── Phase 3: reschedule FASTER via the settings path; the new cadence must
	// take effect (firing resumes at the fast rate). ─────────────────────────
	if _, err := s.ApplyConfig("test-mod", ModuleConfig{"interval_ms": float64(25)}); err != nil {
		t.Fatalf("ApplyConfig(fast): %v", err)
	}
	base2 := mod.fires.Load()
	got := waitForFires(func() int64 { return mod.fires.Load() - base2 }, 2, 2*time.Second)
	if got < 2 {
		t.Fatalf("new (fast) cadence: expected >=2 fires within 2s after reschedule, got %d", got)
	}

	// ── Phase 4: the change persisted to the home-dir config file. ────────────
	path := store.Path("test-mod")
	if path == "" {
		t.Fatal("expected a non-empty config path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persisted config file at %s: %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var persisted ModuleConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("parse persisted config %q: %v", string(data), err)
	}
	if ms, ok := persisted.intervalMs(); !ok || ms != 25 {
		t.Fatalf("persisted interval_ms = %v (ok=%v), want 25; file=%s", ms, ok, string(data))
	}

	// Verify the mode-0600 permission contract on the persisted file.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat persisted config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("persisted config perms = %v, want 0600", perm)
	}
}

// TestScheduleCronHostCallReschedules drives the schedule_cron capability
// handler (the path a running module uses to register/update its OWN schedule)
// and asserts it both persists a config override AND re-schedules the live
// timer.
func TestScheduleCronHostCallReschedules(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	s := NewScheduler(store, nil)

	mod := &fakeModule{id: "selfsched", interval: "2s"} // slow default: ~no fires until rescheduled
	if err := s.Register(Registration{Module: mod, Name: "Self", Version: "1.0.0"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	// The module calls schedule.cron to speed itself up to 25ms.
	handler := s.CronCapHandler("selfsched")
	resp, err := handler("schedule.cron", []byte(`{"method":"tick","interval_ms":25}`))
	if err != nil {
		t.Fatalf("schedule.cron handler: %v", err)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("parse schedule.cron response %q: %v", string(resp), err)
	}
	if !env.OK {
		t.Fatalf("schedule.cron returned error envelope: %s", env.Error.Message)
	}

	// Live cadence sped up: >=2 fires within a bounded window.
	if got := waitForFires(func() int64 { return mod.fires.Load() }, 2, 2*time.Second); got < 2 {
		t.Fatalf("schedule_cron reschedule: expected >=2 fires within 2s, got %d", got)
	}

	// Persisted the per-timer override to the home-dir file.
	cfg, err := store.Load("selfsched")
	if err != nil {
		t.Fatalf("Load persisted config: %v", err)
	}
	if ms, ok := cfg.timerIntervalMs("tick"); !ok || ms != 25 {
		t.Fatalf("persisted timers[tick] = %v (ok=%v), want 25; cfg=%+v", ms, ok, cfg)
	}

	// A malformed / non-positive interval is rejected in the envelope, not by a
	// Go error, and does not disturb the schedule.
	bad, _ := handler("schedule.cron", []byte(`{"interval_ms":0}`))
	if err := json.Unmarshal(bad, &env); err != nil {
		t.Fatalf("parse bad response: %v", err)
	}
	if env.OK {
		t.Fatal("expected schedule.cron with interval_ms=0 to return an error envelope")
	}
}

// TestApplyConfigErrors pins the sentinel errors the settings API maps to HTTP
// status codes.
func TestApplyConfigErrors(t *testing.T) {
	store, _ := NewConfigStore(t.TempDir())
	s := NewScheduler(store, nil)
	if err := s.Register(Registration{Module: &fakeModule{id: "m", interval: "1s"}}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Unknown module -> ErrUnknownModule.
	if _, err := s.ApplyConfig("nope", ModuleConfig{"interval_ms": float64(10)}); !errors.Is(err, ErrUnknownModule) {
		t.Fatalf("ApplyConfig(unknown) err = %v, want ErrUnknownModule", err)
	}
	// Invalid interval (non-positive) -> ErrInvalidConfig.
	if _, err := s.ApplyConfig("m", ModuleConfig{"interval_ms": float64(-5)}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("ApplyConfig(neg) err = %v, want ErrInvalidConfig", err)
	}
	// Invalid timers type -> ErrInvalidConfig.
	if _, err := s.ApplyConfig("m", ModuleConfig{"timers": "not-an-object"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("ApplyConfig(bad timers) err = %v, want ErrInvalidConfig", err)
	}
}
