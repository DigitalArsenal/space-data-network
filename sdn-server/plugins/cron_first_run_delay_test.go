package plugins

import (
	"context"
	"sync"
	"testing"
	"time"
)

// phasedCronPlugin is a lateCronPlugin that anchors its first run.
type phasedCronPlugin struct {
	lateCronPlugin
	delay time.Duration

	mu       sync.Mutex
	asked    []time.Duration
	askedFor []string
}

func (p *phasedCronPlugin) CronFirstRunDelay(method string, interval time.Duration) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, interval)
	p.askedFor = append(p.askedFor, method)
	return p.delay
}

func startCronManager(t *testing.T, p Plugin) {
	t.Helper()
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = m.Close()
	})
	if err := m.StartAll(ctx, RuntimeContext{BaseDataPath: t.TempDir()}); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	if err := m.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if started, err := m.StartLateRegistered(p); err != nil || !started {
		t.Fatalf("StartLateRegistered: started=%v err=%v", started, err)
	}
}

func waitTicks(p *lateCronPlugin, want int64, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.ticks.Load() >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p.ticks.Load() >= want
}

// A weekly method whose plugin says its last run was almost a week ago runs at
// the remaining delay, not a week after scheduling.
func TestCronFirstRunDelayRunsTheFirstTickAtThePluginsPhase(t *testing.T) {
	p := &phasedCronPlugin{
		lateCronPlugin: lateCronPlugin{id: "phased.flow.service", interval: "168h"},
		delay:          50 * time.Millisecond,
	}
	startCronManager(t, p)

	if !waitTicks(&p.lateCronPlugin, 1, 5*time.Second) {
		t.Fatal("a phased first run never came; the ticker ignored CronFirstRunDelay")
	}
	// Exactly one: the cadence after the phased run is the weekly interval.
	time.Sleep(200 * time.Millisecond)
	if got := p.ticks.Load(); got != 1 {
		t.Fatalf("ticks = %d after the phased first run, want 1 (the next run is a week out)", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.asked) != 1 || p.asked[0] != 168*time.Hour || p.askedFor[0] != "tick" {
		t.Fatalf("CronFirstRunDelay asked with %v for %v, want the resolved 168h interval for tick", p.asked, p.askedFor)
	}
}

// Without the extension, and with a delay outside (0, interval), the first run
// stays one interval out: nothing fires early.
func TestCronFirstRunDelayOutsideTheIntervalKeepsThePlainTicker(t *testing.T) {
	plain := &lateCronPlugin{id: "plain.flow.service", interval: "1h"}
	beyond := &phasedCronPlugin{lateCronPlugin: lateCronPlugin{id: "late.phase.service", interval: "1h"}, delay: 2 * time.Hour}
	negative := &phasedCronPlugin{lateCronPlugin: lateCronPlugin{id: "negative.phase.service", interval: "1h"}, delay: -time.Minute}

	startCronManager(t, plain)
	startCronManager(t, beyond)
	startCronManager(t, negative)
	time.Sleep(250 * time.Millisecond)

	for name, ticks := range map[string]int64{
		"no extension":              plain.ticks.Load(),
		"delay beyond the interval": beyond.ticks.Load(),
		"negative delay":            negative.ticks.Load(),
	} {
		if ticks != 0 {
			t.Fatalf("%s: %d early run(s); the first run must stay one interval out", name, ticks)
		}
	}
}
