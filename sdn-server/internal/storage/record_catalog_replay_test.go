package storage

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// TestReplayRecordCatalogToleratesCollidingJournalRowIDs is the regression test
// for the wedge that made the daemon unusable after boot.
//
// sdn_record_index is one global table and the journal carries each row's rowid
// so a replay can reproduce the datasync cursor. But real journals do NOT hold
// globally unique rowids: while the catalog was never replayed at boot, the
// index started EMPTY on every restart, so every boot handed out rowids from 1
// again. A journal spanning several boots therefore contains the same rowid for
// different (schema, cid) rows — the live dev catalog has OMM.fbs rowid=1 and
// PRR.fbs rowid=1 six frames apart.
//
// Replaying those into one table violates the rowid PRIMARY KEY, and
// ON CONFLICT(schema_name, cid) does not cover it. The FlatSQL engine is built
// WITHOUT exceptions, so this does not come back as a Go error: it aborts the
// guest, poisons the WASM runtime, and every later engine call spins at 100% CPU
// forever — which is why /api/v1/stats (and any other engine-touching route)
// hung for the whole "replay", and why the process ignored SIGTERM.
//
// The replay must therefore never emit a colliding rowid.
func TestReplayRecordCatalogToleratesCollidingJournalRowIDs(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	// A journal exactly as a multi-boot store writes it: rowid 1 reused across
	// two schemas, and rowid 2 reused by a later re-publish of a different cid.
	events := []recordCatalogEvent{
		{
			Kind: recordCatalogEventRecordUpsert, SchemaName: "OMM.fbs", CID: "cid-omm-1",
			PeerID: "peer-a", Timestamp: 100, CreatedAt: 100,
			Index: recordCatalogIndex{RowID: 1},
		},
		{
			Kind: recordCatalogEventRecordUpsert, SchemaName: "PRR.fbs", CID: "cid-prr-1",
			PeerID: "peer-a", Timestamp: 101, CreatedAt: 101,
			Index: recordCatalogIndex{RowID: 1}, // collides with OMM rowid 1
		},
		{
			Kind: recordCatalogEventRecordUpsert, SchemaName: "OMM.fbs", CID: "cid-omm-2",
			PeerID: "peer-a", Timestamp: 102, CreatedAt: 102,
			Index: recordCatalogIndex{RowID: 2},
		},
		{
			Kind: recordCatalogEventRecordUpsert, SchemaName: "PRR.fbs", CID: "cid-prr-2",
			PeerID: "peer-a", Timestamp: 103, CreatedAt: 103,
			Index: recordCatalogIndex{RowID: 2}, // collides with OMM rowid 2
		},
	}
	if err := store.recordCatalog.AppendAll(events); err != nil {
		t.Fatalf("append crafted catalog frames: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen the way the daemon does, then run the background hydration's replay.
	reopened, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	replayed, err := reopened.ReplayRecordCatalog(false, nil)
	if err != nil {
		t.Fatalf("replay must tolerate colliding journal rowids, got: %v", err)
	}
	if replayed != len(events) {
		t.Fatalf("replayed = %d, want %d", replayed, len(events))
	}

	var rows, distinctRowIDs int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&rows); err != nil {
		t.Fatalf("count index rows: %v", err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(DISTINCT rowid) FROM sdn_record_index`).Scan(&distinctRowIDs); err != nil {
		t.Fatalf("count distinct rowids: %v", err)
	}
	if rows != len(events) {
		t.Fatalf("sdn_record_index has %d rows, want %d (a dropped row means the replay lost a record)", rows, len(events))
	}
	if distinctRowIDs != rows {
		t.Fatalf("sdn_record_index has %d rows but only %d distinct rowids", rows, distinctRowIDs)
	}

	// The first claimant of a rowid keeps it, so the datasync cursor is preserved
	// for the rows that are not in conflict.
	var ommRowID int64
	if err := reopened.db.QueryRow(
		`SELECT rowid FROM sdn_record_index WHERE schema_name = ? AND cid = ?`,
		"OMM.fbs", "cid-omm-1").Scan(&ommRowID); err != nil {
		t.Fatalf("read OMM rowid: %v", err)
	}
	if ommRowID != 1 {
		t.Fatalf("first claimant of rowid 1 = %d, want 1 (journal rowids must be preserved where they do not collide)", ommRowID)
	}
}

// TestHydrationShieldProtectsLiveReAddFromHistoricalDelete pins the ordering rule
// that makes it safe to release the store write lock between replay windows: a
// historical destructive frame must never delete a record that LIVE traffic
// re-added while the replay was in flight.
func TestHydrationShieldProtectsLiveReAddFromHistoricalDelete(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	base := int64(1746835200)
	records := [][]byte{buildEngineOMM(t, 4242, "SHIELD-1", base)}
	tags := SourceTags{ProviderID: "prov-a", SourceName: "live-source", BatchID: "batch-live"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-a", nil, tags); err != nil {
		t.Fatalf("store live batch: %v", err)
	}

	var cid string
	if err := store.db.QueryRow(`SELECT cid FROM sdn_record_index WHERE schema_name = ?`, "OMM.fbs").Scan(&cid); err != nil {
		t.Fatalf("read live cid: %v", err)
	}

	// Without a replay in flight the shield is inert: a delete deletes.
	if store.hydrationShield.has("OMM.fbs", cid) {
		t.Fatal("shield must be inactive outside a replay")
	}

	// Now model the race: a replay is in flight, and live traffic re-adds this
	// record (every live catalog write funnels through the journal append, which
	// is what notes the shield).
	store.hydrationShield.activate()
	defer store.hydrationShield.deactivate()
	store.hydrationShield.note([]recordCatalogEvent{
		{Kind: recordCatalogEventRecordUpsert, SchemaName: "OMM.fbs", CID: cid},
	})

	// The replay now reaches a historical "delete this cid" frame. It must be a
	// no-op: the live re-add is strictly newer than any pre-boot journal frame.
	if err := store.applyRecordCatalogRecordDelete("OMM.fbs", cid); err != nil {
		t.Fatalf("apply historical delete: %v", err)
	}

	var surviving int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = ? AND cid = ?`,
		"OMM.fbs", cid).Scan(&surviving); err != nil {
		t.Fatalf("count surviving: %v", err)
	}
	if surviving != 1 {
		t.Fatal("a historical delete frame clobbered a record that live traffic re-added during the replay")
	}
}
