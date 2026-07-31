package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// TestReplayUnderLiveWritesNeverReissuesARowID is the regression test for the
// defect that kept host-01's record catalog from EVER finishing hydration.
//
// The chunked replay releases the store write lock between windows on purpose,
// so readers and live publishes are not starved for the length of a 1.3M-frame
// replay. But rowid ownership was snapshotted ONCE at replay start, and the live
// index insert took the engine's MAX(rowid)+1 — of a table the replay has only
// half filled. So a live write mid-replay claims a rowid the replay has not
// reached yet and will later insert BY NAME:
//
//	replay record index batch: flatsqlrt: query_params:
//	  SQL execution error: UNIQUE constraint failed: sdn_record_index.rowid
//
// Off-host, on host-01's real 450 MB journal, that fired at ~285,000 frames
// (TestReproHost01JournalReplayWithLiveWrites). This test reproduces the SAME
// mechanism deterministically in a couple of seconds: a crafted journal whose
// rowids span more than one replay window, and one live publish in the gap
// between windows — which is exactly where the live writer can take rowid
// window+1 out from under the replay.
//
// Without the shared allocator this test fails on the SECOND window every run.
func TestReplayUnderLiveWritesNeverReissuesARowID(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	// The journal must span MORE than one replay window, or the lock is never
	// released mid-replay and there is no gap for a live write to land in.
	const frames = recordCatalogReplayWindow + 3000
	events := make([]recordCatalogEvent, 0, frames)
	for i := 1; i <= frames; i++ {
		events = append(events, recordCatalogEvent{
			Kind: recordCatalogEventRecordUpsert, SchemaName: "OMM.fbs",
			CID:       fmt.Sprintf("cid-hist-%06d", i),
			PeerID:    "peer-hist",
			Timestamp: int64(1746835200 + i), CreatedAt: int64(1746835200 + i),
			Index: recordCatalogIndex{RowID: int64(i)},
		})
	}
	if err := store.recordCatalog.AppendAll(events); err != nil {
		t.Fatalf("append crafted catalog frames: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen exactly as the daemon does for the post-boot background hydration.
	reopened, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	// While a replay is in flight the live path must allocate explicitly; before
	// it starts, it must not (steady state is unchanged).
	if _, explicit := reopened.recordIndexRowIDs.allocateLive(); explicit {
		t.Fatal("live index inserts must not allocate an explicit rowid outside a replay")
	}

	liveCIDs := make([]string, 0, 4)
	var liveErr error
	windows := 0
	replayed, err := reopened.ReplayRecordCatalogContext(context.Background(), false, func(done int) {
		windows++
		// One live publish per window, in the gap where the replay does NOT hold
		// the store write lock. This is host-01's actual condition.
		cid, werr := reopened.Store("OMM.fbs", buildEngineOMM(t, uint32(90000+windows), fmt.Sprintf("LIVE-%d", windows), int64(1746921600+windows)), "peer-live", nil)
		if werr != nil {
			if liveErr == nil {
				liveErr = werr
			}
			return
		}
		liveCIDs = append(liveCIDs, cid)
	})
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "unreachable") || strings.Contains(low, "poison") {
			t.Fatalf("guest trapped under live writes after %d frames: %v", replayed, err)
		}
		t.Fatalf("replay must survive live writes; failed after %d frames: %v", replayed, err)
	}
	if liveErr != nil {
		t.Fatalf("live publish during replay failed: %v", liveErr)
	}
	if replayed != frames {
		t.Fatalf("replayed = %d frames, want %d", replayed, frames)
	}
	if windows < 2 {
		t.Fatalf("replay ran %d window(s); the test needs >=2 or it cannot exercise the gap", windows)
	}
	if len(liveCIDs) == 0 {
		t.Fatal("no live write landed during the replay")
	}

	// The invariant: one row per record, and every rowid unique.
	var rows, distinct int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&rows); err != nil {
		t.Fatalf("count index rows: %v", err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(DISTINCT rowid) FROM sdn_record_index`).Scan(&distinct); err != nil {
		t.Fatalf("count distinct rowids: %v", err)
	}
	if want := frames + len(liveCIDs); rows != want {
		t.Fatalf("sdn_record_index has %d rows, want %d (%d historical + %d live)", rows, want, frames, len(liveCIDs))
	}
	if distinct != rows {
		t.Fatalf("sdn_record_index has %d rows but only %d distinct rowids", rows, distinct)
	}

	// Historical rowids are reproduced VERBATIM — the journal's rowid is the
	// durable datasync cursor and the fix must not perturb it.
	for _, probe := range []int{1, recordCatalogReplayWindow, frames} {
		var got int64
		if err := reopened.db.QueryRow(
			`SELECT rowid FROM sdn_record_index WHERE schema_name = ? AND cid = ?`,
			"OMM.fbs", fmt.Sprintf("cid-hist-%06d", probe)).Scan(&got); err != nil {
			t.Fatalf("read historical rowid %d: %v", probe, err)
		}
		if got != int64(probe) {
			t.Fatalf("historical rowid for frame %d = %d; journal rowids must be reproduced exactly", probe, got)
		}
	}

	// Live rows land ABOVE the entire historical band, which is both the reason
	// they cannot collide and the truthful cursor ordering: they are newer than
	// every pre-boot frame.
	for _, cid := range liveCIDs {
		var got int64
		if err := reopened.db.QueryRow(
			`SELECT rowid FROM sdn_record_index WHERE schema_name = ? AND cid = ?`,
			"OMM.fbs", cid).Scan(&got); err != nil {
			t.Fatalf("read live rowid for %s: %v", cid, err)
		}
		if got <= int64(frames) {
			t.Fatalf("live row took rowid %d, inside the replay's band (<= %d)", got, frames)
		}
	}

	// The band closes with the replay: steady-state writes go back to the
	// engine's own MAX(rowid)+1.
	if _, explicit := reopened.recordIndexRowIDs.allocateLive(); explicit {
		t.Fatal("the shared rowid band must close when the replay ends")
	}
	if _, err := reopened.Store("OMM.fbs", buildEngineOMM(t, 91999, "POST-REPLAY", 1747008000), "peer-live", nil); err != nil {
		t.Fatalf("post-replay live write: %v", err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(DISTINCT rowid) FROM sdn_record_index`).Scan(&distinct); err != nil {
		t.Fatalf("recount distinct rowids: %v", err)
	}
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&rows); err != nil {
		t.Fatalf("recount index rows: %v", err)
	}
	if distinct != rows {
		t.Fatalf("after the replay, %d rows but only %d distinct rowids", rows, distinct)
	}
}

// TestRecordIndexRowIDsPartition pins the allocator contract on its own, with no
// engine in the way: one counter, two lanes, never the same rowid twice.
func TestRecordIndexRowIDsPartition(t *testing.T) {
	var a recordIndexRowIDs

	if _, explicit := a.allocateLive(); explicit {
		t.Fatal("idle allocator must leave the live path alone")
	}

	a.begin(1000) // journal will ask for rowids up to 1000
	seen := map[int64]string{}
	claim := func(rid int64, lane string) {
		if prev, dup := seen[rid]; dup {
			t.Fatalf("rowid %d handed to %s after %s", rid, lane, prev)
		}
		if rid <= 1000 {
			t.Fatalf("%s got rowid %d inside the replay's exclusive band", lane, rid)
		}
		seen[rid] = lane
	}
	for i := 0; i < 200; i++ {
		claim(a.reserve(), "replay")
		rid, explicit := a.allocateLive()
		if !explicit {
			t.Fatal("live path must allocate explicitly while a replay is in flight")
		}
		claim(rid, "live")
	}

	// A second, overlapping replay must not rewind the counter.
	a.begin(5)
	after := a.reserve()
	if after <= 1400 {
		t.Fatalf("nested begin rewound the counter to %d", after)
	}
	a.end()
	if _, explicit := a.allocateLive(); !explicit {
		t.Fatal("the band must stay open while the outer replay is still running")
	}
	a.end()
	if _, explicit := a.allocateLive(); explicit {
		t.Fatal("the band must close when the last replay ends")
	}
}
