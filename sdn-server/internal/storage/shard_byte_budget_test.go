package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// storeRealOMMs writes n genuine size-prefixed $OMM records and returns the
// frame length they all share. Real records, because the whole point of a byte
// budget is that it is computed from the lengths the store actually recorded.
func storeRealOMMs(t *testing.T, store *FlatSQLStore, n int) int64 {
	t.Helper()
	var frame int64
	for i := 0; i < n; i++ {
		data := sds.NewOMMBuilder().
			WithObjectName(fmt.Sprintf("BYTE-BUDGET-%05d", i)).
			WithNoradCatID(uint32(90000 + i)).
			WithEpoch("2026-08-07T00:00:00.000Z").
			Build()
		if _, err := store.Store("OMM.fbs", data, "12D3KooWByteBudget", nil); err != nil {
			t.Fatalf("Store record %d: %v", i, err)
		}
		// record_length is the stored payload, which for an SDN record is the
		// size-prefixed buffer itself; the shard frame adds ITS length prefix
		// on top (DatasetShardFrameOverheadBytes), which is exactly what the
		// probe counts.
		if got := int64(len(data)) + DatasetShardFrameOverheadBytes; frame == 0 {
			frame = got
		} else if got != frame {
			t.Fatalf("record %d frame = %d, want a uniform %d for this fixture", i, got, frame)
		}
	}
	return frame
}

// A shard boundary must be able to stop on BYTES, not only on a record count.
// Before this the two were unrelated: 250 records was 33 KB of $CAT or 32 GiB
// of large buffers, indistinguishably.
func TestIndexedRecordWindowLimitForBytesCutsOnBytes(t *testing.T) {
	store := newFixtureStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	const records = 40
	frame := storeRealOMMs(t, store, records)

	filter := IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: records, AllowLargeResultSet: true, OrderByCID: true}

	// A budget of exactly ten frames admits exactly ten records.
	got, _, err := store.IndexedRecordWindowLimitForBytes(filter, frame*10)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if got != 10 {
		t.Fatalf("budget of 10 frames (%d bytes) admitted %d records, want 10", frame*10, got)
	}

	// One byte short of eleven frames still admits ten: the cut lands BETWEEN
	// frames, never inside one.
	got, _, err = store.IndexedRecordWindowLimitForBytes(filter, frame*11-1)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if got != 10 {
		t.Fatalf("budget one byte short of 11 frames admitted %d records, want 10 (never split a buffer)", got)
	}

	// A budget larger than the window is not a promotion: the record limit
	// still binds.
	got, _, err = store.IndexedRecordWindowLimitForBytes(filter, frame*10_000)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if got != records {
		t.Fatalf("generous budget admitted %d records, want the window's %d", got, records)
	}

	// No budget = the old behaviour, exactly.
	got, _, err = store.IndexedRecordWindowLimitForBytes(filter, 0)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if got != records {
		t.Fatalf("unbudgeted window admitted %d records, want %d", got, records)
	}
}

// A record bigger than the whole budget must still be publishable — as its own
// one-record shard. Refusing it, or splitting it, would both be worse than a
// shard that overshoots.
func TestIndexedRecordWindowLimitForBytesNeverStallsOnAnOversizedRecord(t *testing.T) {
	store := newFixtureStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	frame := storeRealOMMs(t, store, 5)
	filter := IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 5, AllowLargeResultSet: true, OrderByCID: true}

	got, _, err := store.IndexedRecordWindowLimitForBytes(filter, frame/4)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if got != 1 {
		t.Fatalf("budget smaller than one record admitted %d records, want exactly 1", got)
	}
}

// The probe must describe the SAME rows the export then materialises — a
// boundary computed over a different window is a mis-cut shard.
func TestIndexedRecordWindowLimitMatchesTheWindowItBounds(t *testing.T) {
	store := newFixtureStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	const records = 24
	frame := storeRealOMMs(t, store, records)
	filter := IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: records, Offset: 7, AllowLargeResultSet: true, OrderByCID: true}

	bounded, _, err := store.IndexedRecordWindowLimitForBytes(filter, frame*6)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if bounded != 6 {
		t.Fatalf("offset window admitted %d records, want 6", bounded)
	}

	filter.Limit = bounded
	got, err := store.QueryIndexedRecords(filter)
	if err != nil {
		t.Fatalf("QueryIndexedRecords: %v", err)
	}
	if len(got) != bounded {
		t.Fatalf("export window returned %d records, probe said %d", len(got), bounded)
	}
	var total int64
	for _, record := range got {
		total += int64(len(record.Data)) + DatasetShardFrameOverheadBytes
	}
	if total > frame*6 {
		t.Fatalf("materialised window is %d bytes, over the %d-byte budget it was cut to", total, frame*6)
	}
}

// A SHORT WINDOW IS NOT A BYTE CUT.
//
// This is the regression a first cut of the byte budget actually caused: a
// window asking for 10 records over a store holding 2 came back "bounded to
// 2", the caller narrowed filter.Limit from 10 to 2, and the publication was
// filed under a different (offset, limit) key than its consumers and its own
// reuse lookup expect — "published shard registry entry was not stored"
// (dataset_publication_test.go:315). The probe must report WHY it stopped,
// not just where.
func TestIndexedRecordWindowLimitDistinguishesShortWindowFromByteCut(t *testing.T) {
	store := newFixtureStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	const records = 2
	frame := storeRealOMMs(t, store, records)
	filter := IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 10, AllowLargeResultSet: true, OrderByCID: true}

	got, truncated, err := store.IndexedRecordWindowLimitForBytes(filter, frame*10_000)
	if err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if got != records {
		t.Fatalf("short window reported %d records, want %d", got, records)
	}
	if truncated {
		t.Fatal("a window that simply ran out of rows must NOT be reported as a byte cut")
	}

	// And a genuine cut still says so.
	if _, truncated, err = store.IndexedRecordWindowLimitForBytes(filter, frame); err != nil {
		t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
	}
	if !truncated {
		t.Fatal("a budget that stopped the probe with rows remaining must report a cut")
	}
}
