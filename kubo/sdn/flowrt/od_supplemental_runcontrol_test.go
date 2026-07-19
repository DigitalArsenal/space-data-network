package flowrt

import (
	"context"
	"testing"
)

// TestServiceFlowRunControlGuards covers the operator lifecycle primitives'
// guard semantics WITHOUT a runtime: AbortFire on an idle flow is a no-op;
// FireNow and ClearBatch reject with ErrFireInFlight while a fire holds sf.mu;
// AbortFire invokes the installed fire-cancel exactly once. The full mid-drain
// abort + reset path is exercised end-to-end by TestODSupplementalWasmEdgeDrive
// (RESET assertion) under a real WasmEdge composed flow.
func TestServiceFlowRunControlGuards(t *testing.T) {
	sf := &ServiceFlow{programID: "test-flow"}

	// Idle: nothing to abort.
	if sf.AbortFire() {
		t.Fatal("AbortFire on an idle flow must return false")
	}

	// Simulate a fire in flight by holding sf.mu (as fireLocked would).
	sf.mu.Lock()
	if _, err := sf.FireNow(context.Background(), "t0"); err != ErrFireInFlight {
		t.Fatalf("FireNow while a fire is in flight: want ErrFireInFlight, got %v", err)
	}
	if _, err := sf.ClearBatch("batch-x"); err != ErrFireInFlight {
		t.Fatalf("ClearBatch while a fire is in flight: want ErrFireInFlight, got %v", err)
	}
	sf.mu.Unlock()

	// A live fire-cancel: AbortFire invokes it and reports true.
	var calls int
	sf.setFireCancel(func() { calls++ })
	if !sf.AbortFire() {
		t.Fatal("AbortFire with a live cancel must return true")
	}
	if calls != 1 {
		t.Fatalf("AbortFire invoked the fire cancel %d times, want 1", calls)
	}
	sf.clearFireCancel()
	if sf.AbortFire() {
		t.Fatal("AbortFire after clear must return false")
	}
}
