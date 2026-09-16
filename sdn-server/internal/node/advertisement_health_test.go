package node

import (
	"errors"
	"testing"
	"time"
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
	h.record(0, boom)
	if !h.justStartedFailing() {
		t.Error("the first failure must be reported as the transition into failure")
	}
	if got := h.snapshot(); got.Findable || got.ConsecutiveFailures != 1 || got.LastError != boom.Error() {
		t.Fatalf("after one failure: %+v", got)
	}

	// Still failing: a transition must not re-fire, or the log becomes noise.
	h.record(0, boom)
	if h.justStartedFailing() {
		t.Error("the second consecutive failure must not re-announce the transition")
	}

	// Recovery is the line an operator actually wants.
	h.record(3*time.Hour, nil)
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
	h.record(3*time.Hour, nil)
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

// A published provider record outlives the announce that published it, so one
// timed-out re-announce must NOT report the node as unfindable.
//
// Measured on a publicly reachable node: 104 successes in 285 attempts. Tying
// findability to the last attempt made that node report findable=false while
// other nodes were discovering it perfectly well, which sends an operator to
// debug something that is not broken. The degradation still has to be visible,
// so the failure transition and the counters must both still fire.
func TestAdvertisementHealthSurvivesOneFailedReannounce(t *testing.T) {
	var h advertisementHealth
	h.record(3*time.Hour, nil)
	h.record(0, errors.New("context deadline exceeded"))

	if !h.justStartedFailing() {
		t.Fatal("a failing re-announce must still be reported to the operator")
	}
	got := h.snapshot()
	if !got.Findable {
		t.Fatal("the provider record is still within its TTL; the node is still findable")
	}
	if got.ConsecutiveFailures != 1 || got.LastError == "" {
		t.Fatalf("degradation must remain visible in the status: %+v", got)
	}
	if got.FindableUntil.IsZero() {
		t.Fatal("FindableUntil must report when the record lapses")
	}
}

// Findability does lapse: once the record's TTL passes with no fresh success,
// the node really is gone from the rendezvous and must say so.
func TestAdvertisementHealthLapsesAfterTTL(t *testing.T) {
	var h advertisementHealth
	h.record(time.Hour, nil)
	if !h.snapshot().Findable {
		t.Fatal("findable immediately after a success")
	}

	// Backdate the success past its TTL.
	h.mu.Lock()
	h.lastOK = time.Now().UTC().Add(-2 * time.Hour)
	h.mu.Unlock()

	if got := h.snapshot(); got.Findable {
		t.Fatalf("record expired an hour ago, still claiming findable: %+v", got)
	}
}

// With no TTL reported by Advertise, fall back to the default rather than
// treating the record as instantly expired.
func TestAdvertisementHealthDefaultsTTLWhenUnreported(t *testing.T) {
	var h advertisementHealth
	h.record(0, nil)
	got := h.snapshot()
	if !got.Findable {
		t.Fatal("a success with no reported TTL must still count as findable")
	}
	if want := h.lastOK.Add(sdnAdvertiseDefaultTTL); !got.FindableUntil.Equal(want) {
		t.Fatalf("FindableUntil = %v, want %v", got.FindableUntil, want)
	}
}
