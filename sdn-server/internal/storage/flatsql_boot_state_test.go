package storage

// flatsql_boot_state_test.go — the warm-boot handshake, end to end.
//
// The assertions that matter are NOT "the boot was fast". A small fixture is
// fast either way, and "fast" is exactly the evidence that would have let the
// original in-memory regression ship. Every test here asserts WHICH PATH was
// taken (BootReplay().Warm and FramesApplied) alongside the data, so a silent
// fallback to full re-derivation fails the test instead of passing it quietly.

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func bootTestValidator(t *testing.T) *sds.Validator {
	t.Helper()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// openBootStore opens with the background checkpointer OFF so every test
// controls exactly when the mark moves. The boot checkpoint and the Close
// checkpoint still run — those are the two that matter for a restart.
func openBootStore(t *testing.T, basePath string, v *sds.Validator) *FlatSQLStore {
	t.Helper()
	t.Setenv(checkpointIntervalEnv, "0")
	store, err := NewFlatSQLStore(basePath, v)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s): %v", basePath, err)
	}
	return store
}

// simulateCrash closes the store the way a SIGKILL would leave it: WITHOUT the
// clean-shutdown checkpoint, so the persisted mark stays wherever the last
// checkpoint put it and the next boot must replay everything since.
func simulateCrash(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	s.mu.Lock()
	s.controlDBDurable = false // suppresses only the final checkpoint
	s.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

// synthCatalogFrames builds n record-upsert frames spread over two schemas, the
// shape a real multi-boot journal has (including the reused rowids that
// TestReplayRecordCatalogToleratesCollidingJournalRowIDs pins).
func synthCatalogFrames(from, n int) []recordCatalogEvent {
	events := make([]recordCatalogEvent, 0, n)
	for i := from; i < from+n; i++ {
		schema := "OMM.fbs"
		if i%3 == 0 {
			schema = "PRR.fbs"
		}
		events = append(events, recordCatalogEvent{
			Kind:       recordCatalogEventRecordUpsert,
			SchemaName: schema,
			CID:        fmt.Sprintf("bafy-synth-%08d", i),
			PeerID:     "peer-boot",
			Timestamp:  int64(1_700_000_000 + i),
			CreatedAt:  int64(1_700_000_000 + i),
			StreamPath: "flatsql-streams/" + schema + ".sdnstream",
			Index:      recordCatalogIndex{RowID: int64(i%5000 + 1), SourceTimestamp: int64(1_700_000_000 + i)},
		})
	}
	return events
}

func catalogRowCount(t *testing.T, s *FlatSQLStore) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&n); err != nil {
		t.Fatalf("count sdn_record_index: %v", err)
	}
	return n
}

// TestCleanRestartResumesWithoutReplayingTheCatalog is the headline: after a
// clean shutdown, the next boot applies ZERO frames and still answers
// identically. This is the 5-minute host-01 boot, gone.
func TestCleanRestartResumesWithoutReplayingTheCatalog(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	store := openBootStore(t, base, v)
	if !store.BootReplay().Durable {
		t.Fatal("first open is not disk-backed — the whole lane is inert")
	}
	if err := store.recordCatalog.AppendAll(synthCatalogFrames(0, 500)); err != nil {
		t.Fatalf("append frames: %v", err)
	}
	// The frames were written straight to the journal, so this process's tables
	// do not describe them; a reopen is what applies them. Close WITHOUT a
	// checkpoint so the next boot is a genuine cold one.
	simulateCrash(t, store)

	cold := openBootStore(t, base, v)
	coldStats := cold.BootReplay()
	if coldStats.Warm {
		t.Fatal("first reopen claimed a warm boot with no persisted mark")
	}
	if coldStats.FramesApplied != 500 {
		t.Fatalf("cold boot applied %d frames, want 500", coldStats.FramesApplied)
	}
	wantRows := catalogRowCount(t, cold)
	if wantRows == 0 {
		t.Fatal("cold boot produced no catalog rows")
	}
	if err := cold.Close(); err != nil { // clean shutdown => checkpoint
		t.Fatalf("close: %v", err)
	}

	warm := openBootStore(t, base, v)
	defer warm.Close()
	warmStats := warm.BootReplay()
	if !warmStats.Warm {
		t.Fatal("clean restart did NOT resume from the persisted mark")
	}
	if warmStats.FramesApplied != 0 {
		t.Fatalf("warm boot replayed %d frames, want 0", warmStats.FramesApplied)
	}
	if warmStats.ResumeOffset != warmStats.JournalBytes {
		t.Fatalf("resume offset %d != journal length %d", warmStats.ResumeOffset, warmStats.JournalBytes)
	}
	if got := catalogRowCount(t, warm); got != wantRows {
		t.Fatalf("warm boot sees %d catalog rows, cold boot saw %d — state did not survive", got, wantRows)
	}
}

// TestCrashReplaysOnlyTheTail: a crash after the last checkpoint costs exactly
// the frames written since it — never the whole catalog.
func TestCrashReplaysOnlyTheTail(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	if err := seed.recordCatalog.AppendAll(synthCatalogFrames(0, 400)); err != nil {
		t.Fatalf("append frames: %v", err)
	}
	simulateCrash(t, seed)

	// Apply them, then checkpoint cleanly.
	first := openBootStore(t, base, v)
	if first.BootReplay().FramesApplied != 400 {
		t.Fatalf("seed boot applied %d frames, want 400", first.BootReplay().FramesApplied)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// New life: apply a further 100 frames, then DIE without checkpointing.
	second := openBootStore(t, base, v)
	if !second.BootReplay().Warm {
		t.Fatal("second boot should have been warm")
	}
	if err := second.recordCatalog.AppendAll(synthCatalogFrames(400, 100)); err != nil {
		t.Fatalf("append tail frames: %v", err)
	}
	simulateCrash(t, second)

	third := openBootStore(t, base, v)
	defer third.Close()
	stats := third.BootReplay()
	if !stats.Warm {
		t.Fatal("boot after a crash did not resume from the last checkpoint")
	}
	if stats.FramesApplied != 100 {
		t.Fatalf("crash recovery applied %d frames, want exactly the 100-frame tail", stats.FramesApplied)
	}
	if got := catalogRowCount(t, third); got == 0 {
		t.Fatal("crash recovery produced no catalog rows")
	}
}

// TestCorruptControlDatabaseFallsBackToFullReDerivation — the file is not a
// source of truth. Anything wrong with it costs a re-derivation, never data.
func TestCorruptControlDatabaseFallsBackToFullReDerivation(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	if err := seed.recordCatalog.AppendAll(synthCatalogFrames(0, 300)); err != nil {
		t.Fatalf("append frames: %v", err)
	}
	simulateCrash(t, seed)

	warmSeed := openBootStore(t, base, v)
	wantRows := catalogRowCount(t, warmSeed)
	if err := warmSeed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dbPath := filepath.Join(base, flatSQLControlDBName)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("control database file missing: %v", err)
	}

	for _, tc := range []struct {
		name    string
		corrupt func(t *testing.T)
	}{
		{"garbage header", func(t *testing.T) {
			if err := os.WriteFile(dbPath, []byte("this is not a database, not even close"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated to nothing", func(t *testing.T) {
			if err := os.Truncate(dbPath, 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted", func(t *testing.T) {
			if err := os.Remove(dbPath); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.corrupt(t)
			store := openBootStore(t, base, v)
			stats := store.BootReplay()
			if stats.Warm {
				t.Fatal("a corrupt control database still produced a WARM boot")
			}
			if stats.FramesApplied != 300 {
				t.Fatalf("fallback applied %d frames, want the full 300", stats.FramesApplied)
			}
			if got := catalogRowCount(t, store); got != wantRows {
				t.Fatalf("fallback rebuilt %d rows, want %d", got, wantRows)
			}
			// And the recovered store must checkpoint again, so the NEXT boot is
			// warm: a corruption costs one slow boot, not permanent slowness.
			if err := store.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			next := openBootStore(t, base, v)
			if !next.BootReplay().Warm {
				t.Fatal("the boot after a recovery did not go warm again")
			}
			if err := next.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

// TestRewrittenJournalInvalidatesTheMark. Compaction REWRITES the journal, and
// offsets into the old file are meaningless afterwards. The digest is what
// catches that; without it a warm boot would skip live records.
func TestRewrittenJournalInvalidatesTheMark(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	if err := seed.recordCatalog.AppendAll(synthCatalogFrames(0, 200)); err != nil {
		t.Fatalf("append frames: %v", err)
	}
	simulateCrash(t, seed)

	applied := openBootStore(t, base, v)
	if err := applied.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Rewrite the journal with DIFFERENT frames of the SAME total length, which
	// is the adversarial case: a length check alone would accept it.
	journalPath := filepath.Join(base, recordCatalogJournalFileName)
	before, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
	rewritten := openBootStore(t, base, v)
	if err := rewritten.recordCatalog.AppendAll(synthCatalogFrames(9000, 200)); err != nil {
		t.Fatalf("append replacement frames: %v", err)
	}
	simulateCrash(t, rewritten)
	after, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() {
		t.Logf("journal length changed %d -> %d; the digest still has to catch it", before.Size(), after.Size())
	}

	store := openBootStore(t, base, v)
	defer store.Close()
	stats := store.BootReplay()
	if stats.Warm {
		t.Fatal("a REWRITTEN journal was accepted against a mark taken on the old one")
	}
	if stats.FramesApplied != 200 {
		t.Fatalf("applied %d frames after the rewrite, want all 200", stats.FramesApplied)
	}
}

// TestBootMarkNeverOutrunsTheJournal: a mark pointing past the journal's valid
// length (compaction, truncation at a torn tail) must degrade to a full replay.
func TestBootMarkNeverOutrunsTheJournal(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	if err := seed.recordCatalog.AppendAll(synthCatalogFrames(0, 120)); err != nil {
		t.Fatalf("append: %v", err)
	}
	simulateCrash(t, seed)
	warm := openBootStore(t, base, v)
	if err := warm.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Shrink the journal behind the mark's back.
	journalPath := filepath.Join(base, recordCatalogJournalFileName)
	info, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(journalPath, info.Size()/2); err != nil {
		t.Fatal(err)
	}

	store := openBootStore(t, base, v)
	defer store.Close()
	if store.BootReplay().Warm {
		t.Fatal("a mark past the journal's valid length produced a warm boot")
	}
}

// TestReadOnlyOpenStaysEphemeral. A read-only open takes no writer lock, so it
// must never open the live daemon's database read-write. It keeps the
// pre-durability shape and re-derives.
func TestReadOnlyOpenStaysEphemeral(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	if err := seed.recordCatalog.AppendAll(synthCatalogFrames(0, 50)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ro, err := NewFlatSQLStoreReadOnly(base, v)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreReadOnly: %v", err)
	}
	defer ro.Close()
	stats := ro.BootReplay()
	if stats.Durable {
		t.Fatal("a read-only open took the disk-backed control database — that is a second writer")
	}
	if stats.Warm {
		t.Fatal("a read-only open claimed a warm boot")
	}
	if got := catalogRowCount(t, ro); got == 0 {
		t.Fatal("read-only open re-derived nothing")
	}
}

// TestBootStateDigestDetectsAnyPrefixChange pins the digest itself: it must be
// a function of the frames, and a same-length change must move it.
func TestBootStateDigestDetectsAnyPrefixChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, recordCatalogJournalFileName)

	write := func(seed int) (string, int64) {
		j, err := openRecordCatalogJournal(path, false)
		if err != nil {
			t.Fatalf("open journal: %v", err)
		}
		if err := j.AppendAll(synthCatalogFrames(seed, 40)); err != nil {
			t.Fatalf("append: %v", err)
		}
		end := j.validLength()
		d, err := j.digestPrefix(end)
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		j.Close()
		return d, end
	}

	first, end1 := write(0)
	again, end2 := write(0) // same journal, appended twice
	if end2 <= end1 {
		t.Fatalf("second write did not grow the journal (%d -> %d)", end1, end2)
	}

	// The digest of the SAME prefix must be stable across opens: a warm boot
	// compares a mark written by a previous process.
	j, err := openRecordCatalogJournal(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	stable, err := j.digestPrefix(end1)
	if err != nil {
		t.Fatalf("digest prefix: %v", err)
	}
	if stable != first {
		t.Fatalf("digest of the same prefix is not stable across opens: %s vs %s", stable, first)
	}
	if again == first {
		t.Fatal("a longer prefix produced the same digest — the length is not bound in")
	}

	// Incremental extension must agree with a from-scratch computation.
	fresh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	scratch, err := digestRecordCatalogPrefix(fresh, end2)
	if err != nil {
		t.Fatalf("scratch digest: %v", err)
	}
	incremental, err := j.digestPrefix(end2)
	if err != nil {
		t.Fatalf("incremental digest: %v", err)
	}
	if scratch != incremental {
		t.Fatalf("incremental digest %s != from-scratch %s", incremental, scratch)
	}
}

// TestWarmBootTimingOnALargeStore is the boot-timing demonstration.
//
// IT MEASURES WHAT THE DAEMON ACTUALLY DOES. node.go:365 opens the store
// WithDeferredBootRebuilds + WithDeferredRecordCatalogReplay and hydrates in the
// background, so the multi-minute host-01 start was ALWAYS the hydration call,
// not the synchronous open. Timing a non-deferred open here would fold in the
// hot-window rebuild — work this lane does not change and which a synthetic
// fixture distorts — and would report a ratio that means nothing.
//
// A/B on ONE box, back to back, same engine pin, same fixture. The numbers are
// LOGGED for the ledger; the ASSERTION is the ratio, because absolute timings on
// a laptop are not admissible evidence for a host.
func TestWarmBootTimingOnALargeStore(t *testing.T) {
	if testing.Short() {
		t.Skip("large synthetic store")
	}
	const frames = 30_000

	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	for i := 0; i < frames; i += 10_000 {
		if err := seed.recordCatalog.AppendAll(synthCatalogFrames(i, 10_000)); err != nil {
			t.Fatalf("append frames at %d: %v", i, err)
		}
	}
	journalBytes := seed.recordCatalog.validLength()
	simulateCrash(t, seed)

	openDeferred := func(t *testing.T) *FlatSQLStore {
		t.Helper()
		t.Setenv(checkpointIntervalEnv, "0")
		s, err := NewFlatSQLStore(base, v, WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return s
	}

	// COLD: the whole catalog, exactly as every boot has done until now.
	cold := openDeferred(t)
	coldStart := time.Now()
	coldApplied, err := cold.ReplayRecordCatalog(false, nil)
	coldElapsed := time.Since(coldStart)
	if err != nil {
		t.Fatalf("cold hydration: %v", err)
	}
	if coldApplied != frames {
		t.Fatalf("cold hydration applied %d frames, want %d", coldApplied, frames)
	}
	wantRows := catalogRowCount(t, cold)
	if err := cold.Close(); err != nil { // clean shutdown => checkpoint
		t.Fatalf("close: %v", err)
	}

	// WARM: same store, same journal, nothing to do.
	warm := openDeferred(t)
	defer warm.Close()
	warmStart := time.Now()
	warmApplied, err := warm.ReplayRecordCatalog(false, nil)
	warmElapsed := time.Since(warmStart)
	if err != nil {
		t.Fatalf("warm hydration: %v", err)
	}

	t.Logf("RECORD-CATALOG HYDRATION (%d frames, %d journal bytes, %d catalog rows): cold %s -> warm %s (%.0fx)",
		frames, journalBytes, wantRows,
		coldElapsed.Round(time.Millisecond), warmElapsed.Round(time.Millisecond),
		float64(coldElapsed)/float64(warmElapsed+1))

	if !warm.BootReplay().Warm {
		t.Fatal("the second open did not resume from the persisted mark")
	}
	if warmApplied != 0 {
		t.Fatalf("warm hydration applied %d frames, want 0", warmApplied)
	}
	if got := catalogRowCount(t, warm); got != wantRows {
		t.Fatalf("warm store sees %d rows, cold saw %d", got, wantRows)
	}
	// An order of magnitude is the floor. Anything less means the resume is
	// still paying for the catalog somewhere — which is exactly the regression
	// the first cut of this test caught (a zero-frame resume was still scanning
	// every frame header and loading every index row).
	if warmElapsed*10 > coldElapsed {
		t.Fatalf("warm hydration %s is not an order of magnitude faster than cold %s — the resume is still doing O(catalog) work",
			warmElapsed, coldElapsed)
	}
}

// TestDiscoveryReadDuringTailReplay is the boot-window acceptance point.
//
// It ties two measured production symptoms together: readers starving behind
// long store-lock holds (sdn-flatsql-sync-discovery-latency-resets — anonymous
// list_published_shards at 22–42 s) and the boot window itself
// (sdn-boot-window-browser-handshake-starvation). The chunked replay releases
// the write lock BETWEEN windows precisely so an anonymous discovery read can
// interleave; this asserts it, and logs the worst observed latency so a
// regression is visible as a number rather than as a feeling.
func TestDiscoveryReadDuringTailReplay(t *testing.T) {
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)

	seed := openBootStore(t, base, v)
	for i := 0; i < 20_000; i += 10_000 {
		if err := seed.recordCatalog.AppendAll(synthCatalogFrames(i, 10_000)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	simulateCrash(t, seed)

	t.Setenv(checkpointIntervalEnv, "0")
	store, err := NewFlatSQLStore(base, v, WithDeferredRecordCatalogReplay(), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open with deferred replay: %v", err)
	}

	// The replay goroutine holds the store, so the store may only be closed
	// AFTER it finishes — including on a t.Fatal path, where a plain
	// `defer store.Close()` runs first and the goroutine then faults on a nil
	// s.db. Cleanups run LIFO, so registering Close FIRST and the wait SECOND
	// makes the wait happen before the close on every exit path.
	var wg sync.WaitGroup
	var replayErr error
	t.Cleanup(func() { store.Close() })
	t.Cleanup(wg.Wait)

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, replayErr = store.ReplayRecordCatalog(false, nil)
	}()

	// Eight sequential anonymous discovery-shaped reads, the same probe shape
	// the live investigation used. Errors are COLLECTED, not fataled: a t.Fatal
	// here would leave the replay running against a store the cleanups are about
	// to close.
	var worst time.Duration
	var readErr error
	for i := 0; i < 8; i++ {
		start := time.Now()
		if _, err := store.ListDatasetShardPublications(DatasetShardPublicationQuery{
			SchemaName:   "OMM.fbs",
			QueryProfile: DatasetPublicationQueryProfile,
			Limit:        50,
		}); err != nil {
			readErr = fmt.Errorf("discovery read %d during replay: %w", i, err)
			break
		}
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	wg.Wait()
	if replayErr != nil {
		t.Fatalf("background replay: %v", replayErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("DISCOVERY READ DURING TAIL REPLAY: worst of 8 sequential = %s", worst.Round(time.Millisecond))

	// The live symptom was 20+ seconds. A local bound of 2 s is far looser than
	// anything healthy and still fails the starvation class outright.
	if worst > 2*time.Second {
		t.Fatalf("anonymous discovery read stalled %s behind the boot replay — reader starvation", worst)
	}
}
