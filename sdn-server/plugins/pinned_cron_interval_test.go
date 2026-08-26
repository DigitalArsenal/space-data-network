package plugins

import (
	"testing"
	"time"
)

// Measured live on host-02 2026-08-26: the node config declared
// flows.services[].intervals timer-cell-ingest: 1m, /api/apps reported
// interval_ms 60000, and the cron ticker fired every 5m0s — a schedule saved
// once through the dashboard and persisted in
// data/modules/runtime-inputs.json outranked the operator's config file,
// silently and invisibly.
func TestConfigPinnedIntervalOutranksPersistedSchedule(t *testing.T) {
	m := &Manager{}
	spec := CronMethodSpec{
		Method:          "timer-cell-ingest",
		DefaultInterval: "60s",
		IntervalPinned:  true,
	}
	persisted := map[string]CronScheduleConfig{
		"timer-cell-ingest": {Enabled: true, Interval: "5m0s"},
	}

	interval, enabled := m.resolveCronSchedule(spec, persisted)
	if !enabled {
		t.Fatal("the lane is enabled in both surfaces and must stay enabled")
	}
	if interval != time.Minute {
		t.Fatalf("interval = %s, want 1m0s (the config file's pinned cadence)", interval)
	}
}

// An UNPINNED cadence is only the bundle's suggestion, so the dashboard still
// owns it. Losing this would take the runtime schedule surface away entirely.
func TestUnpinnedIntervalStillYieldsToPersistedSchedule(t *testing.T) {
	m := &Manager{}
	spec := CronMethodSpec{
		Method:          "timer-cell-ingest",
		DefaultInterval: "300s",
	}
	persisted := map[string]CronScheduleConfig{
		"timer-cell-ingest": {Enabled: true, Interval: "15m0s"},
	}

	interval, enabled := m.resolveCronSchedule(spec, persisted)
	if !enabled {
		t.Fatal("enabled must survive")
	}
	if interval != 15*time.Minute {
		t.Fatalf("interval = %s, want 15m0s (the persisted schedule)", interval)
	}
}

// Enable/disable is a SEPARATE axis: pinning the cadence must not resurrect a
// lane an operator turned off.
func TestPinnedIntervalDoesNotReEnableADisabledLane(t *testing.T) {
	m := &Manager{}
	spec := CronMethodSpec{
		Method:          "timer-cell-ingest",
		DefaultInterval: "60s",
		IntervalPinned:  true,
	}
	persisted := map[string]CronScheduleConfig{
		"timer-cell-ingest": {Enabled: false, Interval: "5m0s"},
	}

	if _, enabled := m.resolveCronSchedule(spec, persisted); enabled {
		t.Fatal("a disabled schedule must stay disabled")
	}
}

// With nothing persisted, a pinned cadence is simply the cadence.
func TestPinnedIntervalWithNoPersistedStateIsTheConfiguredCadence(t *testing.T) {
	m := &Manager{}
	spec := CronMethodSpec{
		Method:          "timer-cell-ingest",
		DefaultInterval: "60s",
		IntervalPinned:  true,
	}

	interval, enabled := m.resolveCronSchedule(spec, nil)
	if !enabled || interval != time.Minute {
		t.Fatalf("interval=%s enabled=%v, want 1m0s/true", interval, enabled)
	}
}
