package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// An INTERRUPTED record-catalog replay must resume, not restart.
//
// The failure this pins, observed on host-01 2026-09-14: the full metadata
// replay only ever checkpointed when it finished, so a replay that was
// interrupted — by a restart, a deploy, or simply by being too slow to finish —
// had written no resume mark and no flushed engine record stream. The next boot
// found a populated control database with no stream, discarded it outright
// ("populated control database … has no flushed engine record stream") and
// re-derived from byte zero. A 21 GB database was thrown away on every boot and
// the replay could never climb out of the hole, because the thing that would end
// it was the thing being interrupted.
func TestInterruptedRecordCatalogReplayResumesInsteadOfRestarting(t *testing.T) {
	const frames = 40_000
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	for i := 0; i < frames; i += 10_000 {
		if err := seed.recordCatalog.AppendAll(synthCatalogFrames(i, 10_000)); err != nil {
			t.Fatalf("append frames at %d: %v", i, err)
		}
	}
	simulateCrash(t, seed)

	open := func() *FlatSQLStore {
		t.Helper()
		t.Setenv(checkpointIntervalEnv, "0")
		s, err := NewFlatSQLStore(base, v, WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return s
	}

	// FIRST BOOT: cancel the replay partway, then crash without a clean close —
	// exactly what a restart during hydration looks like.
	first := open()
	ctx, cancel := context.WithCancel(context.Background())
	interruptAt := frames / 4
	applied, err := first.ReplayRecordCatalogContext(ctx, false, func(done int) {
		if done >= interruptAt {
			cancel()
		}
	})
	cancel()
	if err == nil {
		t.Fatalf("expected the interrupted replay to report cancellation; applied %d frames", applied)
	}
	if applied <= 0 {
		t.Fatal("the interrupted replay applied nothing, so there is no progress to resume from")
	}
	if applied >= frames {
		t.Fatalf("the replay finished (%d frames) instead of being interrupted", applied)
	}
	simulateCrash(t, first)

	// SECOND BOOT: must pick up from the mark the interrupted replay left.
	second := open()
	defer second.Close()
	if !second.BootReplay().Warm {
		t.Fatal("the second boot did not resume: it found no usable mark and will re-derive from zero")
	}
	resumed, err := second.ReplayRecordCatalogContext(context.Background(), false, nil)
	if err != nil {
		t.Fatalf("resumed hydration: %v", err)
	}
	if resumed >= frames {
		t.Fatalf("the second boot replayed %d of %d frames — it restarted instead of resuming", resumed, frames)
	}
	if resumed <= 0 {
		t.Fatalf("the second boot applied %d frames; the tail past the mark was never replayed", resumed)
	}
	// The two passes together must cover the journal exactly once: the mark may
	// only ever claim a prefix the engine can already serve, so re-replaying a
	// little is allowed, losing any is not.
	if applied+resumed < frames {
		t.Fatalf("frames applied across both boots = %d + %d = %d, fewer than the %d in the journal",
			applied, resumed, applied+resumed, frames)
	}
	t.Logf("interrupted at %d frames, resumed with %d more (journal %d) — no re-derivation from zero",
		applied, resumed, frames)
}

// A replay that is interrupted before its FIRST checkpoint window still must not
// cost the control database: the boot may replay from zero, but it must not
// discard a populated database and force a full re-derive of everything else.
func TestVeryEarlyInterruptLeavesTheControlDatabaseUsable(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	if err := seed.recordCatalog.AppendAll(synthCatalogFrames(0, 2_000)); err != nil {
		t.Fatalf("append frames: %v", err)
	}
	simulateCrash(t, seed)

	t.Setenv(checkpointIntervalEnv, "0")
	first, err := NewFlatSQLStore(base, v, WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before a single window lands
	_, _ = first.ReplayRecordCatalogContext(ctx, false, nil)
	simulateCrash(t, first)

	second, err := NewFlatSQLStore(base, v, WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("reopen after an immediately cancelled replay: %v", err)
	}
	defer second.Close()
	applied, err := second.ReplayRecordCatalogContext(context.Background(), false, nil)
	if err != nil {
		t.Fatalf("hydration after early interrupt: %v", err)
	}
	if applied != 2_000 {
		t.Fatalf("applied %d frames, want the whole journal (2000) replayed from zero", applied)
	}
}
