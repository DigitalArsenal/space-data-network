package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func seedCatalogDeleteBatch(t *testing.T, count int) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	table, err := store.ensureProducerStandardTable(routedProducerID("peer-a"), "OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < count; i++ {
		event := recordCatalogEvent{SchemaName: "OMM.fbs", CID: fmt.Sprintf("cid-%06d", i), PeerID: "peer-a", Timestamp: 100, CreatedAt: 100, RecordLength: 200,
			Tags: SourceTags{ProviderID: "provider", SourceName: "catalog", BatchID: "old"}}
		if err := store.applyRecordCatalogRecordUpsertTo(tx, event, table); err != nil {
			t.Fatal(err)
		}
		if err := store.applyRecordCatalogTagUpsertTo(tx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCatalogQuotaWaitsForCompleteReplay(t *testing.T) {
	for _, state := range []struct {
		name                string
		hydrated, hydrating bool
	}{{"before replay", false, false}, {"cold replay", false, true}, {"forced replay", true, true}} {
		t.Run(state.name, func(t *testing.T) {
			store := seedCatalogDeleteBatch(t, 7)
			before, err := store.recordCatalog.f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			store.recordCatalogHydrated.Store(state.hydrated)
			store.recordCatalogHydrating.Store(state.hydrating)
			defer func() {
				store.recordCatalogHydrated.Store(true)
				store.recordCatalogHydrating.Store(false)
			}()
			if deleted, err := store.GarbageCollectToQuota(200); err != nil || deleted != 0 {
				t.Fatalf("quota must defer until the full catalog is known: deleted=%d err=%v", deleted, err)
			}
			after, err := store.recordCatalog.f.Stat()
			if err != nil || after.Size() != before.Size() {
				t.Fatalf("partial replay appended a durable eviction: before=%d after=%v err=%v", before.Size(), after, err)
			}
			var remaining int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&remaining); err != nil || remaining != 7 {
				t.Fatalf("records changed before recovery: count=%d err=%v", remaining, err)
			}
			store.recordCatalogHydrated.Store(true)
			store.recordCatalogHydrating.Store(false)
			if deleted, err := store.GarbageCollectToQuota(200); err != nil || deleted == 0 {
				t.Fatalf("quota must resume after recovery: deleted=%d err=%v", deleted, err)
			}
		})
	}
}

func catalogDeleteEvent(kind byte) recordCatalogEvent {
	return recordCatalogEvent{Kind: kind, SchemaName: "OMM.fbs", CutoffUnix: 200,
		Tags: SourceTags{ProviderID: "provider", SourceName: "catalog", BatchID: "new"}}
}

func TestCatalogQuotaAfterCloseReturnsError(t *testing.T) {
	store := seedCatalogDeleteBatch(t, 1)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.GarbageCollectToQuota(1); deleted != 0 || !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("closed store quota: deleted=%d err=%v", deleted, err)
	}
}

func TestCatalogDeleteBatchRollsBackEveryProjectionOnFailure(t *testing.T) {
	for _, kind := range []byte{recordCatalogEventGCOlderThan, recordCatalogEventSourceKeep} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			store := seedCatalogDeleteBatch(t, 7)
			// Fail after the routed rows (and, for SourceKeep, source tags)
			// have been deleted. One failed statement must not strand partial
			// metadata, and dropping the fault must allow a complete retry.
			if _, err := store.db.Exec(`ALTER TABLE sdn_record_index RENAME TO saved_record_index`); err != nil {
				t.Fatal(err)
			}
			// A read-only view preserves staging reads but rejects the later
			// index delete without relying on trigger support in the engine.
			if _, err := store.db.Exec(`CREATE VIEW sdn_record_index AS SELECT * FROM saved_record_index`); err != nil {
				t.Fatal(err)
			}
			if err := store.applyRecordCatalogEvent(context.Background(), catalogDeleteEvent(kind), nil); err == nil || !strings.Contains(err.Error(), "cannot modify sdn_record_index because it is a view") {
				t.Fatalf("expected the injected index deletion failure, got %v", err)
			}
			source, err := store.recordReadSource("OMM.fbs")
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"sdn_record_index", "sdn_record_source_tags", source} {
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 7 {
					t.Fatalf("failed batch changed %s: count=%d err=%v", table, count, err)
				}
			}
			if _, err := store.db.Exec(`DROP VIEW sdn_record_index`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`ALTER TABLE saved_record_index RENAME TO sdn_record_index`); err != nil {
				t.Fatal(err)
			}
			if err := store.applyRecordCatalogEvent(context.Background(), catalogDeleteEvent(kind), nil); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"sdn_record_index", "sdn_record_source_tags", source} {
				var count int
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("retry did not finish %s: count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestCatalogDeleteBatchesPreserveLateLiveWrites(t *testing.T) {
	for _, kind := range []byte{recordCatalogEventGCOlderThan, recordCatalogEventSourceKeep} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			const count = 700
			store := seedCatalogDeleteBatch(t, count)
			store.hydrationShield.activate()
			defer store.hydrationShield.deactivate()
			// Re-add a CID already in the staged deletion set after the first
			// batch. Its old source tag also has to survive historical replay.
			liveCID := fmt.Sprintf("cid-%06d", count-1)
			yielded := false
			err := store.applyRecordCatalogEvent(context.Background(), catalogDeleteEvent(kind), func() {
				if yielded {
					return
				}
				yielded = true
				event := recordCatalogEvent{Kind: recordCatalogEventRecordUpsert, SchemaName: "OMM.fbs", CID: liveCID, PeerID: "peer-a", Timestamp: 100}
				if err := store.applyRecordCatalogRecordUpsert(event); err != nil {
					t.Fatal(err)
				}
				store.hydrationShield.note([]recordCatalogEvent{event})
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"sdn_record_index", "sdn_record_source_tags"} {
				var surviving int
				var cid string
				if err := store.db.QueryRow(`SELECT COUNT(*), MIN(cid) FROM `+table+` WHERE schema_name = 'OMM.fbs'`).Scan(&surviving, &cid); err != nil {
					t.Fatal(err)
				}
				if surviving != 1 || cid != liveCID {
					t.Fatalf("%s: count=%d cid=%s; live record and its tags must survive", table, surviving, cid)
				}
			}
			source, err := store.recordReadSource("OMM.fbs")
			if err != nil {
				t.Fatal(err)
			}
			var remaining int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + source).Scan(&remaining); err != nil || remaining != 1 {
				t.Fatalf("routed records must agree with the index: count=%d err=%v", remaining, err)
			}
		})
	}
}

func TestCatalogDeleteBatchesCancellationAndScope(t *testing.T) {
	for _, kind := range []byte{recordCatalogEventGCOlderThan, recordCatalogEventSourceKeep} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			store := seedCatalogDeleteBatch(t, 700)
			// The latest edition and another source retain their own records.
			for _, cid := range []string{"keep-new", "keep-other"} {
				event := recordCatalogEvent{SchemaName: "OMM.fbs", CID: cid, PeerID: "peer-a", Timestamp: 300,
					Tags: SourceTags{ProviderID: "provider", SourceName: "catalog", BatchID: "new"}}
				if cid == "keep-other" {
					event.Tags.SourceName = "other"
					event.Tags.BatchID = "old"
				}
				if err := store.applyRecordCatalogRecordUpsert(event); err != nil {
					t.Fatal(err)
				}
				if err := store.applyRecordCatalogTagUpsert(event); err != nil {
					t.Fatal(err)
				}
			}
			// A different source shares an old record. SourceKeep removes only
			// the retired attribution, while age-based GC removes the record.
			if err := store.applyRecordCatalogTagUpsert(recordCatalogEvent{SchemaName: "OMM.fbs", CID: "cid-000000",
				Tags: SourceTags{ProviderID: "other", SourceName: "catalog", BatchID: "old"}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := store.applyRecordCatalogEvent(ctx, catalogDeleteEvent(kind), cancel)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was not honored during the destructive frame: %v", err)
			}
			var remaining int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining <= 2 || remaining >= 702 {
				t.Fatalf("cancellation did not interrupt partial progress: %d records remain", remaining)
			}
			if err := store.applyRecordCatalogEvent(context.Background(), catalogDeleteEvent(kind), nil); err != nil {
				t.Fatal(err)
			}
			want := 2
			if kind == recordCatalogEventSourceKeep {
				want = 3
			}
			for _, table := range []string{"sdn_record_index", "sdn_record_source_tags"} {
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&remaining); err != nil {
					t.Fatal(err)
				}
				if remaining != want {
					t.Fatalf("%s: surviving records=%d, want %d", table, remaining, want)
				}
			}
		})
	}
}

func TestCatalogDeleteReplayReleasesStoreAndJournalLocks(t *testing.T) {
	store := seedCatalogDeleteBatch(t, 4000)
	event := catalogDeleteEvent(recordCatalogEventGCOlderThan)
	if err := store.recordCatalog.AppendAll([]recordCatalogEvent{event}); err != nil {
		t.Fatal(err)
	}
	info, err := store.recordCatalog.f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := store.recordCatalog.replayWindow(ctx, store, 0, info.Size(), map[string]bool{}, nil, true)
		done <- err
	}()
	observedPartial := false
	for ctx.Err() == nil {
		store.mu.Lock()
		var remaining int
		err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&remaining)
		if err == nil && remaining > 0 && remaining < 4000 {
			// A live writer needs BOTH locks. Cancellation must then let the
			// replay exit with those locks balanced.
			err = store.recordCatalog.AppendAll([]recordCatalogEvent{{Kind: recordCatalogEventRecordDelete, SchemaName: "OMM.fbs", CID: "unrelated"}})
			observedPartial = true
			cancel()
		}
		store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if observedPartial || remaining == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-done; !errors.Is(err, context.Canceled) || !observedPartial {
		t.Fatalf("live writer could not interrupt the destructive frame: observed=%v replay=%v", observedPartial, err)
	}
}
