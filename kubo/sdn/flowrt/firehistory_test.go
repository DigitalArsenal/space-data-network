package flowrt

// firehistory_test.go — real, toolchain-free proof of the run-log derivation
// primitives (firehistory.go) against a REAL LinkedStore (flatsqlrt over
// WasmEdge, proven to run on darwin by flatsql_query_test.go): beginFire/
// endFire correctly bracket a fire's rowid range, FireHistory/OngoingFire
// report the observed state honestly, and BackfillRange surfaces rows a PRIOR
// process already stored before this process ever called beginFire.

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestFireHistoryBracketsRowidRange(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	defer store.Close()

	sf := &ServiceFlow{}

	// Idle: no ongoing fire, empty history.
	if _, ok := sf.OngoingFire(); ok {
		t.Fatalf("OngoingFire() before any fire should be false")
	}
	if h := sf.FireHistory(); len(h) != 0 {
		t.Fatalf("FireHistory() before any fire = %v, want empty", h)
	}

	rec := sf.beginFire("t0", store)
	if rec.OMM.After != 0 || rec.OCM.After != 0 || rec.OBD.After != 0 {
		t.Fatalf("beginFire on an empty store should snapshot After=0 everywhere: %+v", rec)
	}
	if ong, ok := sf.OngoingFire(); !ok || ong.ID != rec.ID {
		t.Fatalf("OngoingFire() mid-fire = %+v,%v, want the started record", ong, ok)
	}

	// Simulate the wasm store node's real work during this fire: 2 OMM + 1 OBD row land.
	store.ingestRecord(queryTestStoreRow("SOMM", "cid-omm-1", "", "", "", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 1}))
	store.ingestRecord(queryTestStoreRow("SOMM", "cid-omm-2", "", "", "", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 2}))
	store.ingestRecord(queryTestStoreRow("SOBD", "cid-obd-1", "", "", "", []byte{0, 0, 0, 0, '$', 'O', 'B', 'D', 1}))

	sf.endFire(rec, store, nil)

	if _, ok := sf.OngoingFire(); ok {
		t.Fatalf("OngoingFire() after endFire should be false")
	}
	hist := sf.FireHistory()
	if len(hist) != 1 {
		t.Fatalf("FireHistory() after one fire = %d entries, want 1", len(hist))
	}
	got := hist[0]
	if got.Status != "ok" || got.Error != "" {
		t.Fatalf("completed fire status = %q err=%q, want ok/empty", got.Status, got.Error)
	}
	if CountInRange(store, "sds_omm", got.OMM) != 2 {
		t.Fatalf("OMM.Added() = %d, want 2 (%+v)", CountInRange(store, "sds_omm", got.OMM), got.OMM)
	}
	if CountInRange(store, "sds_ocm", got.OCM) != 0 {
		t.Fatalf("OCM.Added() = %d, want 0 (no OCM rows landed)", CountInRange(store, "sds_ocm", got.OCM))
	}
	if CountInRange(store, "sds_obd", got.OBD) != 1 {
		t.Fatalf("OBD.Added() = %d, want 1 (%+v)", CountInRange(store, "sds_obd", got.OBD), got.OBD)
	}
	if got.FinishedAt.Before(got.StartedAt) {
		t.Fatalf("FinishedAt (%v) before StartedAt (%v)", got.FinishedAt, got.StartedAt)
	}

	// A SECOND fire only sees rows landing AFTER the first fire's Through.
	rec2 := sf.beginFire("t0", store)
	if rec2.OMM.After != got.OMM.Through {
		t.Fatalf("second fire's OMM.After = %d, want %d (the first fire's Through)", rec2.OMM.After, got.OMM.Through)
	}
	store.ingestRecord(queryTestStoreRow("SOMM", "cid-omm-3", "", "", "", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 3}))
	sf.endFire(rec2, store, errors.New("boom"))

	hist = sf.FireHistory()
	if len(hist) != 2 {
		t.Fatalf("FireHistory() after two fires = %d entries, want 2", len(hist))
	}
	if hist[1].Status != "error" || hist[1].Error != "boom" {
		t.Fatalf("second fire = %+v, want status=error err=boom", hist[1])
	}
	if CountInRange(store, "sds_omm", hist[1].OMM) != 1 {
		t.Fatalf("second fire OMM.Added() = %d, want 1 (only the row landed during THIS fire)", CountInRange(store, "sds_omm", hist[1].OMM))
	}
}

func TestFireHistoryBoundedRing(t *testing.T) {
	sf := &ServiceFlow{}
	for i := 0; i < maxFireHistory+10; i++ {
		rec := sf.beginFire("t0", nil)
		sf.endFire(rec, nil, nil)
	}
	if got := len(sf.FireHistory()); got != maxFireHistory {
		t.Fatalf("FireHistory() length = %d, want capped at %d", got, maxFireHistory)
	}
}

func TestBackfillRangeSurfacesPreExistingRows(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	defer store.Close()

	// A "prior process" (or an earlier session of THIS one) already stored 3
	// OMM rows before anything here ever called beginFire.
	for i, cid := range []string{"cid-a", "cid-b", "cid-c"} {
		store.ingestRecord(queryTestStoreRow("SOMM", cid, "", "", "", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', byte(i)}))
	}

	rng := BackfillRange(store, "sds_omm")
	if rng.After != 0 || CountInRange(store, "sds_omm", rng) != 3 {
		t.Fatalf("BackfillRange(sds_omm) = %+v, want After=0 Added=3", rng)
	}
	if empty := BackfillRange(store, "sds_ocm"); CountInRange(store, "sds_ocm", empty) != 0 {
		t.Fatalf("BackfillRange(sds_ocm) on an empty table = %+v, want Added=0", empty)
	}
	// A nil store (bridge-mode flow) is a safe, honest zero — never a panic.
	if z := BackfillRange(nil, "sds_omm"); CountInRange(nil, "sds_omm", z) != 0 {
		t.Fatalf("BackfillRange(nil store) = %+v, want zero", z)
	}
}
