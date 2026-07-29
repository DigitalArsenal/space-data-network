package flatsqlrt

import "testing"

// A poisoned runtime must never be entered again — not even to free.
//
// This is the host-02 wedge of 2026-07-29, pinned. The record-catalog replay
// trapped inside flatsql_query_params; execErr correctly marked the runtime
// poisoned and returned an error, but every alloc site in this package releases
// its guest buffer through `defer`, so the deferred free then ran on the guest
// whose allocator the trap had just corrupted. Module.Deallocate calls the
// guest's free() on context.Background() — no budget, no cancellation, error
// discarded — so it entered a five-instruction infinite loop in the AOT
// artifact (99.7% of perf samples inside a 14-byte address range) while the
// caller still held the module lock. The trap error therefore never returned,
// RecoverPoisonedEngine was never reached, and a 1-vCPU production producer sat
// at 98% CPU from boot with no flow running at all.
//
// These pin the guard's DECISION rather than driving a real engine: free()
// itself is one line on top of mayCallGuest(), and a Runtime with no module
// segfaults inside cgo instead of panicking, so "prove it with a nil module"
// crashes the whole test binary. That is not a hypothetical — it is how the
// first version of this file failed.
func TestPoisonedRuntimeIsNeverEnteredAgain(t *testing.T) {
	r := &Runtime{poisoned: true}
	if r.mayCallGuest() {
		t.Fatal("a poisoned runtime still admits guest calls: the deferred free will wedge a core, as it did on host-02")
	}
}

// The guard must be exactly that — a guard. If it also refuses healthy
// runtimes it trades a wedge for an unbounded leak of every buffer this
// package allocates.
func TestHealthyRuntimeStillFrees(t *testing.T) {
	r := &Runtime{poisoned: false}
	if !r.mayCallGuest() {
		t.Fatal("a healthy runtime was refused: the guard is too wide and leaks every allocation")
	}
}

// Error paths free buffers on runtimes that were never started.
func TestNilRuntimeIsNotEntered(t *testing.T) {
	var r *Runtime
	if r.mayCallGuest() {
		t.Fatal("a nil runtime admitted a guest call")
	}
	r.free(1234) // must not panic
}
