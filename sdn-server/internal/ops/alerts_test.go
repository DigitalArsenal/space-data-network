package ops

import (
	"sync"
	"testing"
	"time"
)

func fixedClock(start time.Time) (func() time.Time, func(time.Duration)) {
	var mu sync.Mutex
	now := start
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(d)
		}
}

// A repeat of the same failure must bump the counter and the last error while
// leaving Since alone: how long a lane has been down is the number an operator
// acts on, and the 31-hour outage this package exists for would have looked
// one second old at every glance if a repeat restarted the clock.
func TestAlertRepeatBumpsCountAndKeepsSince(t *testing.T) {
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	clock, advance := fixedClock(start)
	r := NewRegistry()
	r.SetClock(clock)

	r.Raise(KindLaneFailing, "celestrak-satcat", SeverityWarning, "HTTP 400")
	advance(90 * time.Second)
	r.Raise(KindLaneFailing, "celestrak-satcat", SeverityWarning, "HTTP 403")

	active := r.Active()
	if len(active) != 1 {
		t.Fatalf("active = %d alerts, want 1", len(active))
	}
	got := active[0]
	if got.Count != 2 {
		t.Errorf("Count = %d, want 2", got.Count)
	}
	if !got.Since.Equal(start) {
		t.Errorf("Since = %s, want %s (a repeat must not restart the clock)", got.Since, start)
	}
	if want := start.Add(90 * time.Second); !got.LastAt.Equal(want) {
		t.Errorf("LastAt = %s, want %s", got.LastAt, want)
	}
	if got.LastError != "HTTP 403" {
		t.Errorf("LastError = %q, want %q", got.LastError, "HTTP 403")
	}
	if got.Severity != SeverityWarning {
		t.Errorf("Severity = %q, want %q", got.Severity, SeverityWarning)
	}
}

// Clearing removes the alert; clearing something that is not active is a no-op
// so a success path can call it unconditionally.
func TestAlertClearRemovesAndIsIdempotent(t *testing.T) {
	r := NewRegistry()
	r.Raise(KindPublicationRejected, "12D3Koo", SeverityError, "signature type mismatch")
	if len(r.Active()) != 1 {
		t.Fatalf("alert did not go active")
	}
	r.Clear(KindPublicationRejected, "12D3Koo")
	if got := r.Active(); len(got) != 0 {
		t.Fatalf("Active() = %v, want empty after clear", got)
	}
	r.Clear(KindPublicationRejected, "12D3Koo")
	r.Clear(KindLaneFailing, "never-raised")
	if got := r.Active(); len(got) != 0 {
		t.Fatalf("Active() = %v, want empty", got)
	}
}

// Errors sort before warnings, then kind, then subject — a stable order so the
// operator surface does not reshuffle between scrapes.
func TestAlertActiveOrderingIsStable(t *testing.T) {
	r := NewRegistry()
	r.Raise(KindLaneFailing, "zulu", SeverityWarning, "")
	r.Raise(KindUpdateFailed, "bundle", SeverityError, "")
	r.Raise(KindLaneFailing, "alpha", SeverityWarning, "")
	r.Raise(KindEnginePoisoned, "", SeverityError, "")

	want := []struct{ kind, subject string }{
		{KindEnginePoisoned, ""},
		{KindUpdateFailed, "bundle"},
		{KindLaneFailing, "alpha"},
		{KindLaneFailing, "zulu"},
	}
	for round := 0; round < 3; round++ {
		active := r.Active()
		if len(active) != len(want) {
			t.Fatalf("active = %d alerts, want %d", len(active), len(want))
		}
		for i, w := range want {
			if active[i].Kind != w.kind || active[i].Subject != w.subject {
				t.Fatalf("active[%d] = %s/%s, want %s/%s", i, active[i].Kind, active[i].Subject, w.kind, w.subject)
			}
		}
	}
}

// The transition hook fires on activation, on an escalation, and on clear —
// and NOT on a plain repeat, which would turn a failing lane into a log flood.
func TestAlertTransitionHook(t *testing.T) {
	type transition struct {
		alert  Alert
		active bool
	}
	r := NewRegistry()
	var (
		mu   sync.Mutex
		seen []transition
	)
	r.OnTransition(func(a Alert, active bool) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, transition{alert: a, active: active})
	})

	r.Raise(KindLaneFailing, "celestrak-spw", SeverityWarning, "first")
	r.Raise(KindLaneFailing, "celestrak-spw", SeverityWarning, "second")
	r.Raise(KindLaneFailing, "celestrak-spw", SeverityError, "fourth")
	r.Clear(KindLaneFailing, "celestrak-spw")

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("hook fired %d times, want 3 (activate, escalate, clear): %+v", len(seen), seen)
	}
	if !seen[0].active || seen[0].alert.Severity != SeverityWarning {
		t.Errorf("first transition = %+v, want active warning", seen[0])
	}
	if !seen[1].active || seen[1].alert.Severity != SeverityError {
		t.Errorf("second transition = %+v, want active error escalation", seen[1])
	}
	if seen[2].active {
		t.Errorf("third transition = %+v, want a clear", seen[2])
	}
	if seen[2].alert.Count != 3 {
		t.Errorf("cleared alert Count = %d, want 3", seen[2].alert.Count)
	}
}

// Snapshot is what the probes serialize: a status word and a severity count.
func TestAlertSnapshotCountsAndStatus(t *testing.T) {
	r := NewRegistry()
	if snap := r.Snapshot(); snap.Status != StatusOK || snap.Counts.Error != 0 || snap.Counts.Warning != 0 {
		t.Fatalf("empty Snapshot() = %+v, want ok/0/0", snap)
	}

	r.Raise(KindLaneFailing, "a", SeverityWarning, "")
	r.Raise(KindLaneFailing, "b", SeverityError, "boom")
	r.Raise(KindLaneFailing, "b", SeverityError, "boom again")

	snap := r.Snapshot()
	if snap.Status != StatusDegraded {
		t.Errorf("Status = %q, want %q", snap.Status, StatusDegraded)
	}
	if snap.Counts.Error != 1 || snap.Counts.Warning != 1 {
		t.Errorf("Counts = %+v, want 1 error / 1 warning", snap.Counts)
	}
	if len(snap.Alerts) != 2 {
		t.Errorf("Alerts = %d, want 2", len(snap.Alerts))
	}

	// A warning on its own is still degraded: the outage that motivated this
	// package began as two consecutive lane failures.
	r.Clear(KindLaneFailing, "b")
	if snap := r.Snapshot(); snap.Status != StatusDegraded || snap.Counts.Warning != 1 {
		t.Errorf("warning-only Snapshot() = %+v, want degraded with 1 warning", snap)
	}
	r.Clear(KindLaneFailing, "a")
	if snap := r.Snapshot(); snap.Status != StatusOK {
		t.Errorf("cleared Snapshot() = %+v, want ok", snap)
	}
}

// KindCounts is the gauge's shape: one row per (kind, severity).
func TestAlertKindCounts(t *testing.T) {
	r := NewRegistry()
	r.Raise(KindLaneFailing, "a", SeverityWarning, "")
	r.Raise(KindLaneFailing, "b", SeverityWarning, "")
	r.Raise(KindLaneFailing, "c", SeverityError, "")
	r.Raise(KindUpdateFailed, "bundle", SeverityError, "")

	counts := r.KindCounts()
	got := map[string]int{}
	for _, c := range counts {
		got[c.Kind+"/"+c.Severity] = c.Count
	}
	want := map[string]int{
		KindLaneFailing + "/" + SeverityWarning: 2,
		KindLaneFailing + "/" + SeverityError:   1,
		KindUpdateFailed + "/" + SeverityError:  1,
	}
	if len(got) != len(want) {
		t.Fatalf("KindCounts() = %+v, want %d rows", counts, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("KindCounts()[%s] = %d, want %d", k, got[k], v)
		}
	}
}

// A nil registry is a working no-op so a subsystem that was never handed one
// needs no nil check at every call site.
func TestAlertNilRegistryIsInert(t *testing.T) {
	var r *Registry
	r.Raise(KindLaneFailing, "a", SeverityError, "boom")
	r.Clear(KindLaneFailing, "a")
	r.OnTransition(func(Alert, bool) { t.Fatal("hook on a nil registry must never fire") })
	r.SetClock(time.Now)
	if got := r.Active(); got != nil {
		t.Errorf("Active() = %v, want nil", got)
	}
	if snap := r.Snapshot(); snap.Status != StatusOK {
		t.Errorf("Snapshot() = %+v, want ok", snap)
	}
	if got := r.KindCounts(); len(got) != 0 {
		t.Errorf("KindCounts() = %v, want empty", got)
	}
}

// Concurrent raises and clears must not race and must leave a consistent set.
func TestAlertRegistryIsConcurrencySafe(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.Raise(KindLaneFailing, "shared", SeverityWarning, "boom")
				r.Raise(KindPublicationRejected, "peer", SeverityError, "boom")
				r.Clear(KindPublicationRejected, "peer")
				_ = r.Snapshot()
			}
		}(worker)
	}
	wg.Wait()

	active := r.Active()
	if len(active) != 1 || active[0].Kind != KindLaneFailing {
		t.Fatalf("Active() = %+v, want the single lane_failing alert", active)
	}
	if active[0].Count != 8*200 {
		t.Errorf("Count = %d, want %d", active[0].Count, 8*200)
	}
}
