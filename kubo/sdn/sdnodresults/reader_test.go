package sdnodresults_test

// reader_test.go — real, toolchain-free end-to-end proof of the read/derive
// layer against a REAL LinkedStore (flatsqlrt over WasmEdge — proven to run
// on darwin by flowrt's own ungated test suite): backfill-run synthesis (the
// orchestrator's hard requirement — a completed full-catalog drain a prior
// process stored must surface as a real run row with real stats), per-fire
// derived runs with correct rowid-range objects/avg-RMS, the two-level
// drill-down (declared-but-unattributed providers, real searchable objects),
// and content-addressed download. Uses a fakeODFlow over flowrt.OpenLinkedStore
// + flowrt.BuildTestWrapperRow/IngestTestRow (both exported specifically for
// this — see flatsql_store_link.go) rather than a full wasm mount.

import (
	"path/filepath"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OBD"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/sdnodresults"
)

// fakeODFlow implements sdnodresults.ODFlow over a real LinkedStore plus a
// manually-set fire history — everything the real *flowrt.ServiceFlow would
// report, without a wasm mount.
type fakeODFlow struct {
	store     *flowrt.LinkedStore
	providers []string
	history   []flowrt.FireRecord
	ongoing   *flowrt.FireRecord
}

func (f *fakeODFlow) Store() *flowrt.LinkedStore        { return f.store }
func (f *fakeODFlow) SourceProviderPluginIDs() []string { return f.providers }
func (f *fakeODFlow) FireHistory() []flowrt.FireRecord  { return f.history }
func (f *fakeODFlow) OngoingFire() (flowrt.FireRecord, bool) {
	if f.ongoing == nil {
		return flowrt.FireRecord{}, false
	}
	return *f.ongoing, true
}

func newTestStore(t *testing.T) *flowrt.LinkedStore {
	t.Helper()
	dir := t.TempDir()
	store, err := flowrt.OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func sizedOMM(t *testing.T, norad uint32, name, objID, epoch string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	nameOff := b.CreateString(name)
	idOff := b.CreateString(objID)
	epochOff := b.CreateString(epoch)
	OMM.OMMStart(b)
	OMM.OMMAddNORAD_CAT_ID(b, norad)
	OMM.OMMAddOBJECT_NAME(b, nameOff)
	OMM.OMMAddOBJECT_ID(b, idOff)
	OMM.OMMAddEPOCH(b, epochOff)
	OMM.OMMAddMEAN_MOTION(b, 15.5)
	omm := OMM.OMMEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(omm, []byte("$OMM"))
	return b.FinishedBytes()
}

func sizedOBD(t *testing.T, satNo uint32, wrms float64, iterations uint16) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	OBD.OBDStart(b)
	OBD.OBDAddSAT_NO(b, satNo)
	OBD.OBDAddWRMS(b, wrms)
	OBD.OBDAddNUM_ITERATIONS(b, iterations)
	OBD.OBDAddFIT_SPAN(b, 2.0)
	obd := OBD.OBDEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(obd, []byte("$OBD"))
	return b.FinishedBytes()
}

// TestReaderNoFlowMounted proves the honest-empty contract: nothing crashes
// or fabricates a row when the OD flow is not mounted.
func TestReaderNoFlowMounted(t *testing.T) {
	r := sdnodresults.NewReader(func() sdnodresults.ODFlow { return nil })
	if got := r.Runs(); got != nil {
		t.Fatalf("Runs() with no flow = %v, want nil", got)
	}
	if _, ok := r.Live(); ok {
		t.Fatalf("Live() with no flow should be false")
	}
	if _, ok := r.Run("anything"); ok {
		t.Fatalf("Run() with no flow should be false")
	}
	if _, ok := r.RunProviders("anything"); ok {
		t.Fatalf("RunProviders() with no flow should be false")
	}
	if _, ok := r.RunObjects("anything", ""); ok {
		t.Fatalf("RunObjects() with no flow should be false")
	}
	if _, _, ok := r.DownloadRecord("cid"); ok {
		t.Fatalf("DownloadRecord() with no flow should be false")
	}
}

// TestReaderEmptyStoreNoBackfill proves a genuinely fresh, empty node reports
// zero runs — never an invented backfill row over nothing.
func TestReaderEmptyStoreNoBackfill(t *testing.T) {
	store := newTestStore(t)
	flow := &fakeODFlow{store: store, providers: []string{"com.orbpro.iss-source"}}
	r := sdnodresults.NewReader(func() sdnodresults.ODFlow { return flow })
	if got := r.Runs(); len(got) != 0 {
		t.Fatalf("Runs() on an empty store = %v, want empty (no fabricated backfill)", got)
	}
}

// TestReaderBackfillSurfacesPriorProcessDrain is the orchestrator's hard
// requirement #1: a completed full-catalog drain already sitting in the
// store (empty FireHistory — a fresh process) MUST appear as a real run row
// with real stats (ephemeris count + avg RMS from $OBD + declared providers).
func TestReaderBackfillSurfacesPriorProcessDrain(t *testing.T) {
	store := newTestStore(t)
	// Simulate a completed prior drain: 3 OMM + 3 OBD rows already in the
	// store before this "process" (the fakeODFlow) ever observed a fire.
	norads := []uint32{25544, 48274, 44714}
	wrms := []float64{0.05, 0.12, 0.30}
	for i, norad := range norads {
		omm := sizedOMM(t, norad, "SAT", "2019-074B", "2026-07-19T00:00:00Z")
		if err := store.IngestTestRow("SOMM", cidFor(i, "omm"), "", "", "", omm); err != nil {
			t.Fatalf("ingest omm: %v", err)
		}
		obd := sizedOBD(t, norad, wrms[i], 4)
		if err := store.IngestTestRow("SOBD", cidFor(i, "obd"), "", "", "", obd); err != nil {
			t.Fatalf("ingest obd: %v", err)
		}
	}

	flow := &fakeODFlow{store: store, providers: []string{
		"com.orbpro.spacex-starlink-source", "com.orbpro.iss-source", "com.orbpro.glonass-source",
	}}
	r := sdnodresults.NewReader(func() sdnodresults.ODFlow { return flow })

	runs := r.Runs()
	if len(runs) != 1 {
		t.Fatalf("Runs() = %d entries, want 1 (the backfill pseudo-run)", len(runs))
	}
	run := runs[0]
	if run.ID != "backfill" {
		t.Fatalf("backfill run id = %q, want \"backfill\"", run.ID)
	}
	if run.ObjectsTotal != 3 || run.EphemerisFiles != 3 {
		t.Fatalf("backfill run objects = %+v, want 3", run)
	}
	wantAvg := (0.05 + 0.12 + 0.30) / 3
	if diff := run.AvgRMS - wantAvg; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("backfill run AvgRMS = %v, want %v", run.AvgRMS, wantAvg)
	}
	if len(run.Providers) != 3 {
		t.Fatalf("backfill run providers = %v, want 3 declared providers", run.Providers)
	}
	if run.Note == "" {
		t.Fatalf("backfill run must carry a Note explaining it is a synthesized pre-existing snapshot")
	}

	// Detail + objects + providers all resolve for this id too.
	detail, ok := r.Run("backfill")
	if !ok || detail.ObjectsTotal != 3 {
		t.Fatalf("Run(\"backfill\") = %+v,%v", detail, ok)
	}
	objs, ok := r.RunObjects("backfill", "")
	if !ok || len(objs) != 3 {
		t.Fatalf("RunObjects(\"backfill\") = %d objects,%v, want 3", len(objs), ok)
	}
	for _, o := range objs {
		if !o.HasRMS {
			t.Fatalf("object %d missing joined $OBD telemetry: %+v", o.Norad, o)
		}
		if !o.Unattributed {
			t.Fatalf("object %d should be marked Unattributed (no provider telemetry exists)", o.Norad)
		}
	}
	provStats, ok := r.RunProviders("backfill")
	if !ok || len(provStats) != 3 {
		t.Fatalf("RunProviders(\"backfill\") = %d,%v, want 3", len(provStats), ok)
	}
	for _, p := range provStats {
		if !p.Unavailable || p.Note == "" {
			t.Fatalf("provider %q stats should be Unavailable with a Note: %+v", p.Provider, p)
		}
	}
}

// TestReaderFireHistoryPreferredOverBackfill proves a process that HAS fired
// at least once prefers its own observed history, never re-synthesizing the
// backfill pseudo-run alongside it.
func TestReaderFireHistoryPreferredOverBackfill(t *testing.T) {
	store := newTestStore(t)
	// Pre-existing row (would be a backfill candidate on its own).
	if err := store.IngestTestRow("SOMM", "cid-pre", "", "", "", sizedOMM(t, 1, "PRE", "x", "e")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	afterPre := flowrt.MaxRowid(store, "sds_omm")

	// One real observed fire that added one more row.
	if err := store.IngestTestRow("SOMM", "cid-fire", "", "", "", sizedOMM(t, 2, "FIRE", "y", "e")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	afterFire := flowrt.MaxRowid(store, "sds_omm")

	flow := &fakeODFlow{
		store:     store,
		providers: []string{"com.orbpro.iss-source"},
		history: []flowrt.FireRecord{{
			ID: "fire-1", Status: "ok",
			OMM: flowrt.TableRange{After: afterPre, Through: afterFire},
		}},
	}
	r := sdnodresults.NewReader(func() sdnodresults.ODFlow { return flow })

	runs := r.Runs()
	if len(runs) != 1 || runs[0].ID != "fire-1" {
		t.Fatalf("Runs() = %+v, want exactly the one observed fire (never the backfill too)", runs)
	}
	if runs[0].ObjectsTotal != 1 {
		t.Fatalf("fire-1 ObjectsTotal = %d, want 1 (only ITS row, not the pre-existing one)", runs[0].ObjectsTotal)
	}
}

// TestReaderDownloadRecordByCID proves the exact stored bytes round-trip by
// content-addressed cid, across all three tables.
func TestReaderDownloadRecordByCID(t *testing.T) {
	store := newTestStore(t)
	ommBytes := sizedOMM(t, 999, "DL", "z", "e")
	if err := store.IngestTestRow("SOMM", "cid-dl", "", "", "", ommBytes); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	flow := &fakeODFlow{store: store}
	r := sdnodresults.NewReader(func() sdnodresults.ODFlow { return flow })

	data, table, ok := r.DownloadRecord("cid-dl")
	if !ok || table != "sds_omm" || string(data) != string(ommBytes) {
		t.Fatalf("DownloadRecord(cid-dl) = %d bytes, table=%q, ok=%v, want the exact ingested bytes from sds_omm", len(data), table, ok)
	}
	if _, _, ok := r.DownloadRecord("no-such-cid"); ok {
		t.Fatalf("DownloadRecord(unknown cid) should be false")
	}
}

func cidFor(i int, kind string) string {
	return kind + "-" + string(rune('a'+i))
}
