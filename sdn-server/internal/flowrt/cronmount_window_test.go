package flowrt

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

// A trigger's interval and its publisher window are the same length, so a
// scheduled tick can land just inside the window. That tick waits the window
// out and pulls rather than dropping the cycle; an operator's run-now inside
// the window is still refused at once; and a tick far inside the window still
// skips once its wait runs out.
func TestScheduledPullWaitsOutAClosingWindow(t *testing.T) {
	saved := windowRecheck
	windowRecheck = 10 * time.Millisecond
	t.Cleanup(func() { windowRecheck = saved })

	// A gate that refuses the first `refusals` asks, then allows.
	gate := func(refusals int32) (RetrievalGate, *atomic.Int32) {
		calls := &atomic.Int32{}
		return func() (bool, string) {
			if calls.Add(1) <= refusals {
				return false, "last attempt 2h59m40s ago is inside the 3h0m0s debounce window"
			}
			return true, ""
		}, calls
	}
	// A one-second trigger: the wait runs for up to 50 ms. The flow itself is
	// closed, so a run that gets past the gate fails as "closed", and one the
	// gate stops comes back as a skip.
	flow := func(g RetrievalGate) *ServiceFlow {
		sf := &ServiceFlow{programID: "celestrak-gp", triggers: []serviceTrigger{{TriggerID: "timer-gp", IntervalMs: 1000}}}
		sf.SetRetrievalGate(g)
		return sf
	}
	skipped := func(out []byte) bool {
		var summary struct {
			Skipped bool `json:"skipped"`
		}
		return json.Unmarshal(out, &summary) == nil && summary.Skipped
	}
	scheduled := plugins.WithScheduledRun(context.Background())

	g, calls := gate(2)
	out, err := flow(g).InvokeCron(scheduled, "timer-gp", nil)
	if skipped(out) || err == nil {
		t.Fatalf("a scheduled pull whose window closes within its wait was dropped (out %s, err %v)", out, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("gate asked %d times, want 3 (two refusals, then the pull)", calls.Load())
	}

	g, calls = gate(2)
	out, _ = flow(g).InvokeCron(context.Background(), "timer-gp", nil)
	if !skipped(out) || calls.Load() != 1 {
		t.Fatalf("a run-now inside the window was not refused at once (out %s, gate asked %d times)", out, calls.Load())
	}

	g, calls = gate(1 << 30)
	start := time.Now()
	out, _ = flow(g).InvokeCron(scheduled, "timer-gp", nil)
	if !skipped(out) {
		t.Fatalf("a scheduled pull far inside its window ran (out %s)", out)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the wait ran %s, past its bound of a twentieth of the interval", waited)
	}
}
