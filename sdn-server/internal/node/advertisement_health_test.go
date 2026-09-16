package node

import (
	"errors"
	"testing"
)

// The failure this exists for: on a fresh node the SDN rendezvous announce
// fails with "failed to find any peer in table" because it fires before the DHT
// routing table has anyone in it. Both outcomes were Debugf, so a node that was
// never findable looked completely healthy.
func TestAdvertisementHealthReportsFindability(t *testing.T) {
	var h advertisementHealth

	// Nothing attempted yet: not findable, and not claiming to be.
	if h.snapshot().Findable {
		t.Fatal("findable before any attempt")
	}

	boom := errors.New("failed to find any peer in table")
	h.record(boom)
	if !h.justStartedFailing() {
		t.Error("the first failure must be reported as the transition into failure")
	}
	if got := h.snapshot(); got.Findable || got.ConsecutiveFailures != 1 || got.LastError != boom.Error() {
		t.Fatalf("after one failure: %+v", got)
	}

	// Still failing: a transition must not re-fire, or the log becomes noise.
	h.record(boom)
	if h.justStartedFailing() {
		t.Error("the second consecutive failure must not re-announce the transition")
	}

	// Recovery is the line an operator actually wants.
	h.record(nil)
	if !h.justRecovered() {
		t.Error("recovery after failures must be reported")
	}
	if h.priorFailures() != 2 {
		t.Errorf("priorFailures = %d, want 2", h.priorFailures())
	}
	got := h.snapshot()
	if !got.Findable || got.ConsecutiveFailures != 0 || got.LastError != "" {
		t.Fatalf("after recovery: %+v", got)
	}
	if got.Successes != 1 || got.Failures != 2 || got.Attempts != 3 {
		t.Fatalf("counters wrong: %+v", got)
	}
}

// A node whose first attempt works should say so once, and not pretend to have
// recovered from something.
func TestAdvertisementHealthFirstSuccessIsNotARecovery(t *testing.T) {
	var h advertisementHealth
	h.record(nil)
	if !h.isFirstSuccess() {
		t.Error("the first success must be reported")
	}
	if h.justRecovered() {
		t.Error("a first success is not a recovery")
	}
	if !h.snapshot().Findable {
		t.Error("a successful announce means findable")
	}
}

// Going from working to broken must be reported: a node that WAS findable and
// silently stopped being so is the worst case, because nothing looks wrong.
func TestAdvertisementHealthReportsRegression(t *testing.T) {
	var h advertisementHealth
	h.record(nil)
	h.record(errors.New("context deadline exceeded"))
	if !h.justStartedFailing() {
		t.Fatal("losing findability must be reported")
	}
	if h.snapshot().Findable {
		t.Fatal("still claiming findable while the announce is failing")
	}
}
