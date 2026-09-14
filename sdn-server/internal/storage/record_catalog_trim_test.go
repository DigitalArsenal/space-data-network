package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Trimming the journal below a resume mark must reclaim the dead prefix WITHOUT
// losing a single row, and the store must still open correctly afterwards.
//
// The bytes below a mark are provably redundant: a mark is only written after
// the engine record stream is flushed, so everything it covers is already in the
// durable control tables. Nothing deleted them, which is how a journal reached
// 89M frames to describe ~140k live records on host-02 and made the fallback
// replay a multi-day operation.
func TestJournalTrimReclaimsTheAppliedPrefixWithoutLosingRows(t *testing.T) {
	const frames = 20_000
	base := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	journalPath := filepath.Join(base, "record-catalog.flatsqlmeta")

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

	// Replay everything, then close cleanly so a mark covering the whole
	// journal is written.
	first := open()
	if _, err := first.ReplayRecordCatalogContext(context.Background(), false, nil); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	wantRows := catalogRowCount(t, first)
	if wantRows == 0 {
		t.Fatal("fixture produced no catalog rows")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	before, err := os.Stat(journalPath)
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	if before.Size() == 0 {
		t.Fatal("journal is empty before the trim")
	}

	// Second boot trims: everything is covered by the mark.
	prev := recordCatalogTrimThreshold
	recordCatalogTrimThreshold = 1024
	defer func() { recordCatalogTrimThreshold = prev }()

	second := open()
	after, err := os.Stat(journalPath)
	if err != nil {
		t.Fatalf("stat journal after trim: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("journal was not trimmed: %d bytes before, %d after", before.Size(), after.Size())
	}
	if got := catalogRowCount(t, second); got != wantRows {
		t.Fatalf("after trim the catalog holds %d rows, want %d — the trim lost data", got, wantRows)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close after trim: %v", err)
	}
	t.Logf("journal trimmed %d -> %d bytes, %d catalog rows intact", before.Size(), after.Size(), wantRows)

	// THIRD boot on the trimmed journal: still correct, still every row. This is
	// the one that would catch a stale mark naming bytes that no longer exist.
	third := open()
	defer third.Close()
	if _, err := third.ReplayRecordCatalogContext(context.Background(), false, nil); err != nil {
		t.Fatalf("hydrate on the trimmed journal: %v", err)
	}
	if got := catalogRowCount(t, third); got != wantRows {
		t.Fatalf("third boot holds %d rows, want %d", got, wantRows)
	}
}
