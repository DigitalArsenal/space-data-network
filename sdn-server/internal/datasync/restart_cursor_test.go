package datasync

// restart_cursor_test.go (loop B.4): the deployed-peer sync contract must
// survive a store restart. With the engine store, control-table state
// (including sdn_record_index rowids — the cursor space) is rebuilt at boot
// by statement-journal replay; these tests prove a cursor issued before a
// restart resumes EXACTLY where it left off afterwards, with identical
// bytes, hashes, and snapshot identity — i.e. a deployed peer never notices
// the restart.

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func buildSyncOMM(norad uint32) []byte {
	return sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectID(fmt.Sprintf("2026-%03d", norad%1000)).
		WithObjectName(fmt.Sprintf("SAT-%d", norad)).
		WithEpoch("2026-05-12T12:00:00Z").
		Build()
}

func openSyncStore(t *testing.T, basePath string) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	return store
}

func seedSyncStore(t *testing.T, store *storage.FlatSQLStore, tags storage.SourceTags) {
	t.Helper()
	// Mix batch and single-record writes (both production paths).
	var batch [][]byte
	for _, norad := range []uint32{20001, 20002, 20003} {
		batch = append(batch, buildSyncOMM(norad))
	}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", batch, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}
	for _, norad := range []uint32{20004, 20005} {
		if _, err := store.StoreWithSourceTags("OMM.fbs", buildSyncOMM(norad), "source:celestrak", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags %d: %v", norad, err)
		}
	}
}

type scanPage struct {
	resp    *ScanResponse
	records []*storage.Record
	hash    string
}

func scanOnce(t *testing.T, store *storage.FlatSQLStore, cursorFrom *ScanResponse) scanPage {
	t.Helper()
	req := QueryRequest{
		Schema:     "OMM.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		Limit:      2,
	}
	if cursorFrom != nil {
		req.Cursor = cursorFrom.NextCursor
		req.SnapshotID = cursorFrom.SnapshotID
		req.Head = cursorFrom.Head
		req.HighWaterMark = cursorFrom.HighWaterMark
		req.TotalCount = cursorFrom.TotalCount
	}
	resp, records, err := Scan(store, req, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return scanPage{resp: resp, records: records, hash: ScanHash("OMM.fbs", records)}
}

func TestScanCursorSurvivesStoreRestart(t *testing.T) {
	tags := storage.SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-restart",
		ProducerPeerID:    "peer-celestrak",
		ProducerPublicKey: "public-celestrak",
	}

	// Control store: no restart, three pages of 2/2/1.
	controlDir := filepath.Join(t.TempDir(), "control")
	control := openSyncStore(t, controlDir)
	defer control.Close()
	seedSyncStore(t, control, tags)
	cPage1 := scanOnce(t, control, nil)
	cPage2 := scanOnce(t, control, cPage1.resp)
	cPage3 := scanOnce(t, control, cPage2.resp)

	// Restart store: page 1, RESTART (close + reopen -> journal replay +
	// engine rebuild), then resume pages 2 and 3 with the pre-restart cursor.
	restartDir := filepath.Join(t.TempDir(), "restart")
	store := openSyncStore(t, restartDir)
	seedSyncStore(t, store, tags)
	rPage1 := scanOnce(t, store, nil)

	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	store = openSyncStore(t, restartDir)
	defer store.Close()

	rPage2 := scanOnce(t, store, rPage1.resp)
	rPage3 := scanOnce(t, store, rPage2.resp)

	// The restarted sequence must be indistinguishable from the control run.
	for i, pair := range []struct{ c, r scanPage }{
		{cPage1, rPage1}, {cPage2, rPage2}, {cPage3, rPage3},
	} {
		if pair.c.resp.Count != pair.r.resp.Count ||
			pair.c.resp.TotalCount != pair.r.resp.TotalCount ||
			(pair.c.resp.NextCursor == "") != (pair.r.resp.NextCursor == "") {
			t.Fatalf("page %d shape diverged: control %d/%d next=%q vs restart %d/%d next=%q",
				i+1, pair.c.resp.Count, pair.c.resp.TotalCount, pair.c.resp.NextCursor,
				pair.r.resp.Count, pair.r.resp.TotalCount, pair.r.resp.NextCursor)
		}
		if pair.c.hash != pair.r.hash {
			t.Fatalf("page %d ScanHash diverged after restart: %s vs %s", i+1, pair.c.hash, pair.r.hash)
		}
		if len(pair.c.records) != len(pair.r.records) {
			t.Fatalf("page %d record count diverged", i+1)
		}
		for j := range pair.c.records {
			c, r := pair.c.records[j], pair.r.records[j]
			if c.CID != r.CID || c.RowID != r.RowID {
				t.Fatalf("page %d record %d identity diverged: control (%s,%d) vs restart (%s,%d)",
					i+1, j, c.CID, c.RowID, r.CID, r.RowID)
			}
			if !bytes.Equal(c.Data, r.Data) {
				t.Fatalf("page %d record %d payload bytes diverged after restart", i+1, j)
			}
		}
	}
	if rPage3.resp.NextCursor != "" {
		t.Fatalf("final page not terminal after restart: %q", rPage3.resp.NextCursor)
	}

	// Post-restart snapshot head must match the control store's (rowid space
	// reproduced exactly by journal replay).
	cHead, err := control.RawRecordHead(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("control head: %v", err)
	}
	rHead, err := store.RawRecordHead(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("restart head: %v", err)
	}
	if cHead.MaxRowID != rHead.MaxRowID {
		t.Fatalf("MaxRowID diverged after restart: %d vs %d", cHead.MaxRowID, rHead.MaxRowID)
	}

	// And new writes after the restart continue the rowid sequence.
	if _, err := store.StoreWithSourceTags("OMM.fbs", buildSyncOMM(20006), "source:celestrak", nil, tags); err != nil {
		t.Fatalf("post-restart write: %v", err)
	}
	newHead, err := store.RawRecordHead(storage.RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("post-write head: %v", err)
	}
	if newHead.MaxRowID <= rHead.MaxRowID {
		t.Fatalf("post-restart rowid did not advance: %d <= %d", newHead.MaxRowID, rHead.MaxRowID)
	}
}
