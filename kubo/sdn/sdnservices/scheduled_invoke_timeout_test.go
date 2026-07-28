package sdnservices

import (
	"testing"
	"time"
)

// The override must widen the scheduled-invoke budget, and a bad value must
// never become a new failure mode — an unparseable duration falls back to the
// modulert default (0 = "use the default") rather than pinning the budget at
// something absurd or refusing to start.
func TestScheduledInvokeTimeoutFromEnv(t *testing.T) {
	for name, tc := range map[string]struct {
		set   bool
		value string
		want  time.Duration
	}{
		"unset":       {set: false, want: 0},
		"empty":       {set: true, value: "", want: 0},
		"whitespace":  {set: true, value: "   ", want: 0},
		"45m":         {set: true, value: "45m", want: 45 * time.Minute},
		"padded":      {set: true, value: "  90m  ", want: 90 * time.Minute},
		"hours":       {set: true, value: "2h", want: 2 * time.Hour},
		"garbage":     {set: true, value: "soon", want: 0},
		"bare number": {set: true, value: "45", want: 0},
		"zero":        {set: true, value: "0s", want: 0},
		"negative":    {set: true, value: "-5m", want: 0},
		"unit typo":   {set: true, value: "45min", want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.set {
				t.Setenv(SchedInvokeTimeoutEnv, tc.value)
			} else {
				t.Setenv(SchedInvokeTimeoutEnv, "")
			}
			if got := scheduledInvokeTimeoutFromEnv(); got != tc.want {
				t.Errorf("scheduledInvokeTimeoutFromEnv() = %s, want %s", got, tc.want)
			}
		})
	}
}

// The reason this knob exists: host-01's OD flow needs a budget well past the
// 10m default, because the flow stores a whole catalog fit in one
// append_records call. Lock the shape an operator will actually set.
func TestHostOneRevivalBudgetParses(t *testing.T) {
	t.Setenv(SchedInvokeTimeoutEnv, "45m")
	got := scheduledInvokeTimeoutFromEnv()
	if got <= 10*time.Minute {
		t.Fatalf("revival budget = %s, which does not widen the 10m default that was killing the run", got)
	}
}
