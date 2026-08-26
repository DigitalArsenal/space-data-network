package sourcemetrics

import "testing"

// A lane given its own base window backs off exactly the way the default one
// does — doubling per consecutive failure — so a configured cadence buys
// pace, never immunity from a refusing publisher.
func TestEffectiveDebounceHoursFromDoublesTheConfiguredBase(t *testing.T) {
	const base = 0.0125 // 45s
	for failures, want := range map[int]float64{0: base, 1: base * 2, 2: base * 4, 3: base * 8} {
		if got := EffectiveDebounceHoursFrom(base, failures); got != want {
			t.Fatalf("failures=%d: got %v, want %v", failures, got, want)
		}
	}
}

// The ceiling is the ceiling: a configured base cannot escalate past it, and
// a base already above it is clamped rather than doubled.
func TestEffectiveDebounceHoursFromRespectsTheCap(t *testing.T) {
	if got := EffectiveDebounceHoursFrom(0.0125, 1000); got != MaxDebounceHours {
		t.Fatalf("runaway failures: got %v, want the %v cap", got, MaxDebounceHours)
	}
	if got := EffectiveDebounceHoursFrom(MaxDebounceHours*4, 0); got != MaxDebounceHours {
		t.Fatalf("over-wide base: got %v, want the %v cap", got, MaxDebounceHours)
	}
}

// "No window" is not a value this function will produce: a zero or negative
// base falls back to the node default instead of removing the gate.
func TestEffectiveDebounceHoursFromRefusesToRemoveTheGate(t *testing.T) {
	for _, base := range []float64{0, -1} {
		if got := EffectiveDebounceHoursFrom(base, 0); got != DefaultDebounceHours {
			t.Fatalf("base=%v: got %v, want the %v default", base, got, DefaultDebounceHours)
		}
	}
}

// The existing entry point is exactly the new one at the default base, so the
// refactor cannot have moved any lane that did not ask to move.
func TestEffectiveDebounceHoursIsTheDefaultBase(t *testing.T) {
	for failures := 0; failures < 12; failures++ {
		if got, want := EffectiveDebounceHours(failures), EffectiveDebounceHoursFrom(DefaultDebounceHours, failures); got != want {
			t.Fatalf("failures=%d: %v != %v", failures, got, want)
		}
	}
}
