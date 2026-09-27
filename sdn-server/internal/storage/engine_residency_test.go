package storage

import (
	"path/filepath"
	"testing"
)

func TestTBSResidencyUsesRegisteredSource(t *testing.T) {
	for _, tags := range []SourceTags{{}, {ProviderID: "opencellid", SourceName: "cell-tower-bulk"}} {
		t.Run(tags.SourceName, func(t *testing.T) {
			t.Setenv(checkpointIntervalEnv, "0")
			store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
			defer store.Close()
			data := newTBSRecord("310-410-7-500", "opencellid", 310, 51.5, -0.12)
			var cid string
			var err error
			if tags.SourceName == "" {
				cid, err = store.Store("TBS.fbs", data, "peer", nil)
			} else {
				cid, err = store.StoreWithSourceTags("TBS.fbs", data, "peer", nil, tags)
			}
			if err != nil {
				t.Fatal(err)
			}
			rows, err := store.engineResidencyRowsForCIDs("TBS.fbs", []string{cid}, nil)
			if err != nil || len(rows) != 1 {
				t.Fatalf("residency rows=%v, err=%v", rows, err)
			}
			row := rows[0]
			if row.cid != cid || row.source == "" || !store.engineSources[row.source] {
				t.Fatalf("TBS residency does not name its registered partition: %+v", row)
			}
			if err := store.Delete("TBS.fbs", cid); err != nil {
				t.Fatal(err)
			}
			if count, err := store.EngineRecordCount("TBS.fbs"); err != nil || count != 0 {
				t.Fatalf("engine count after delete=%d, err=%v", count, err)
			}
		})
	}
}

// At the wasm32 memory ceiling the old string reader returned empty CID and
// source values for real ledger rows. Deleting those values matched nothing,
// yet every retry claimed another removal and drifted the residency count.
func TestStaleResidencyCountsOnlyDeletedRows(t *testing.T) {
	t.Setenv(checkpointIntervalEnv, "0")
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()
	const schema = "TBS.fbs"
	if _, err := store.db.Exec(`INSERT INTO sdn_engine_rows (schema_name, cid, source, seq) VALUES (?, ?, ?, ?)`, schema, "actual-cid", "retired-source", 42); err != nil {
		t.Fatal(err)
	}
	store.engineResidentSet(schema, 1)

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, tc := range []struct {
		name      string
		row       engineResidencyRow
		want      int
		remaining int64
	}{
		{"misread strings", engineResidencyRow{seq: 42}, 0, 1},
		{"stale source", engineResidencyRow{cid: "actual-cid", source: "retired-source", seq: 42}, 1, 0},
		{"retry", engineResidencyRow{cid: "actual-cid", source: "retired-source", seq: 42}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			removed, err := store.tombstoneResidencyRowsLocked(schema, "TBS", []engineResidencyRow{tc.row}, nil)
			if err != nil || removed != tc.want {
				t.Fatalf("removed=%d, err=%v; want %d", removed, err, tc.want)
			}
			count, err := store.engineResidencyCount(schema)
			if err != nil || count != tc.remaining {
				t.Fatalf("ledger count=%d, err=%v; want %d", count, err, tc.remaining)
			}
			if got := store.engineResidentCount(schema); got != tc.remaining {
				t.Fatalf("resident count=%d; want %d", got, tc.remaining)
			}
			if store.engine.Poisoned() {
				t.Fatal("stale source reached the engine tombstone API")
			}
		})
	}
}
