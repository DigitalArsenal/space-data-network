package storage

import (
	"testing"
	"time"
)

// A FIXED 64 LEFT 6x ON THE TABLE, and a fixed 1024 would have broken the
// guarantee it was protecting. Measured 2026-09-16 (OMM, 4 MiB, engine AOT):
// 64-record windows sustained 233 rec/s and 1024-record windows 1417 rec/s,
// because the cost is the per-window commit — sixteen times fewer commits is
// six times the throughput. But the comment that set 64 records a cold CI
// runner taking 1.7 s for a 128-record window, which is ~13 s at 1024: far
// outside the two-second reader budget the constant exists to hold.
//
// So the window targets the BUDGET, not a record count.
func TestAdaptiveStoreChunkGrowsOnFastHardware(t *testing.T) {
	window := storeWriteChunkSize
	// Windows landing well inside budget must grow toward the cap.
	for i := 0; i < 12; i++ {
		window = adaptiveStoreChunk(window, 50*time.Millisecond)
	}
	if window != storeWriteChunkMax {
		t.Fatalf("window = %d after repeated fast windows, want the %d cap", window, storeWriteChunkMax)
	}
}

// The 2026-07-06 blackout was a window that ran for MINUTES while readers
// waited on RLock. A window approaching the budget must shrink, and must never
// go below the floor that was always safe.
func TestAdaptiveStoreChunkShrinksWhenWindowsApproachTheBudget(t *testing.T) {
	window := storeWriteChunkMax
	for i := 0; i < 12; i++ {
		window = adaptiveStoreChunk(window, storeWriteWindowBudget)
	}
	if window != storeWriteChunkSize {
		t.Fatalf("window = %d after repeated slow windows, want the %d floor", window, storeWriteChunkSize)
	}
	if got := adaptiveStoreChunk(storeWriteChunkSize, 10*time.Second); got != storeWriteChunkSize {
		t.Fatalf("window = %d, must never fall below the %d floor", got, storeWriteChunkSize)
	}
}

// The first window on unknown hardware must behave exactly as it always did:
// no history means no growth.
func TestAdaptiveStoreChunkStartsAtTheSafeFloor(t *testing.T) {
	if got := adaptiveStoreChunk(0, 0); got != storeWriteChunkSize {
		t.Fatalf("initial window = %d, want %d", got, storeWriteChunkSize)
	}
	if got := adaptiveStoreChunk(storeWriteChunkSize, 0); got != storeWriteChunkSize {
		t.Fatalf("window with no measurement = %d, want it unchanged at %d", got, storeWriteChunkSize)
	}
}

// A cold CI runner: 1.7 s for 128 records. It must not grow from there, or the
// reader budget goes with it.
func TestAdaptiveStoreChunkDoesNotGrowOnASlowRunner(t *testing.T) {
	if got := adaptiveStoreChunk(128, 1700*time.Millisecond); got >= 128 {
		t.Fatalf("window = %d on a 1.7 s window; it must shrink, not hold or grow", got)
	}
}
