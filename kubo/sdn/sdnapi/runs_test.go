package sdnapi_test

// runs_test.go — HTTP-level proof of the supplemental-OMM run API's REAL
// data source: sdn/sdnodresults over a real LinkedStore (flatsqlrt/WasmEdge,
// proven to run on darwin by flowrt's + sdnodresults' own test suites), NOT
// the disconnected sdnruns.Store. Exercises the honest-empty (no OD flow
// mounted) path and the real backfill-run + drill-down + download paths end
// to end through the actual mux NewRunsHandler builds.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OBD"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/ipfs/kubo/sdn/flowrt"
	"github.com/ipfs/kubo/sdn/sdnapi"
	"github.com/ipfs/kubo/sdn/sdnodresults"
)

type fakeODFlow struct {
	store     *flowrt.LinkedStore
	providers []string
	history   []flowrt.FireRecord
}

func (f *fakeODFlow) Store() *flowrt.LinkedStore        { return f.store }
func (f *fakeODFlow) SourceProviderPluginIDs() []string { return f.providers }
func (f *fakeODFlow) FireHistory() []flowrt.FireRecord  { return f.history }
func (f *fakeODFlow) OngoingFire() (flowrt.FireRecord, bool) {
	return flowrt.FireRecord{}, false
}

func newODTestStore(t *testing.T) *flowrt.LinkedStore {
	t.Helper()
	dir := t.TempDir()
	store, err := flowrt.OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func sizedTestOMM(t *testing.T, norad uint32) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	nameOff := b.CreateString("TESTSAT")
	OMM.OMMStart(b)
	OMM.OMMAddNORAD_CAT_ID(b, norad)
	OMM.OMMAddOBJECT_NAME(b, nameOff)
	omm := OMM.OMMEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(omm, []byte("$OMM"))
	return b.FinishedBytes()
}

func sizedTestOBD(t *testing.T, satNo uint32, wrms float64) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	OBD.OBDStart(b)
	OBD.OBDAddSAT_NO(b, satNo)
	OBD.OBDAddWRMS(b, wrms)
	obd := OBD.OBDEnd(b)
	b.FinishSizePrefixedWithFileIdentifier(obd, []byte("$OBD"))
	return b.FinishedBytes()
}

func getJSON(t *testing.T, mux http.Handler, path string, status int, out interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("GET %s = %d, want %d (body=%s)", path, rec.Code, status, rec.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: decode %s: %v", path, rec.Body.String(), err)
		}
	}
}

func TestRunsHandlerNoODFlowMounted(t *testing.T) {
	mux := sdnapi.NewRunsHandler(sdnapi.RunsDeps{
		Reader: func() *sdnodresults.Reader { return nil },
	})

	var list struct {
		Runs []sdnodresults.RunSummary `json:"runs"`
		Live *sdnodresults.LiveRun     `json:"live"`
	}
	getJSON(t, mux, "/sdn/v1/runs", http.StatusOK, &list)
	if len(list.Runs) != 0 || list.Live != nil {
		t.Fatalf("runs list with no OD flow = %+v, want empty (honest, never fabricated)", list)
	}

	req := httptest.NewRequest(http.MethodGet, "/sdn/v1/runs/backfill", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /sdn/v1/runs/backfill with no OD flow = %d, want 503", rec.Code)
	}
}

func TestRunsHandlerRealBackfillDrillDownAndDownload(t *testing.T) {
	store := newODTestStore(t)
	if err := store.IngestTestRow("SOMM", "cid-omm-1", "", "", "", sizedTestOMM(t, 25544)); err != nil {
		t.Fatalf("ingest omm: %v", err)
	}
	if err := store.IngestTestRow("SOBD", "cid-obd-1", "", "", "", sizedTestOBD(t, 25544, 0.222)); err != nil {
		t.Fatalf("ingest obd: %v", err)
	}
	flow := &fakeODFlow{store: store, providers: []string{"com.orbpro.iss-source"}}
	reader := sdnodresults.NewReader(func() sdnodresults.ODFlow { return flow })
	mux := sdnapi.NewRunsHandler(sdnapi.RunsDeps{Reader: func() *sdnodresults.Reader { return reader }})

	var list struct {
		Runs []sdnodresults.RunSummary `json:"runs"`
	}
	getJSON(t, mux, "/sdn/v1/runs", http.StatusOK, &list)
	if len(list.Runs) != 1 || list.Runs[0].ID != "backfill" || list.Runs[0].ObjectsTotal != 1 {
		t.Fatalf("runs list = %+v, want one real backfill run with ObjectsTotal=1", list.Runs)
	}

	var detail sdnodresults.RunSummary
	getJSON(t, mux, "/sdn/v1/runs/backfill", http.StatusOK, &detail)
	if detail.ObjectsTotal != 1 || detail.AvgRMS != 0.222 {
		t.Fatalf("run detail = %+v, want ObjectsTotal=1 AvgRMS=0.222", detail)
	}

	var provResp struct {
		Providers []sdnodresults.ProviderStat `json:"providers"`
	}
	getJSON(t, mux, "/sdn/v1/runs/backfill/providers", http.StatusOK, &provResp)
	if len(provResp.Providers) != 1 || !provResp.Providers[0].Unavailable {
		t.Fatalf("providers (Level 1) = %+v, want one declared, honestly Unavailable provider", provResp.Providers)
	}

	var objResp struct {
		Objects []sdnodresults.ObjectRow `json:"objects"`
	}
	getJSON(t, mux, "/sdn/v1/runs/backfill/objects", http.StatusOK, &objResp)
	if len(objResp.Objects) != 1 || objResp.Objects[0].Norad != 25544 || !objResp.Objects[0].HasRMS {
		t.Fatalf("objects (Level 2) = %+v, want one real joined object", objResp.Objects)
	}
	omdCID := objResp.Objects[0].OMMCid

	// Search filters it out.
	getJSON(t, mux, "/sdn/v1/runs/backfill/objects?search=99999", http.StatusOK, &objResp)
	if len(objResp.Objects) != 0 {
		t.Fatalf("objects with a non-matching search = %+v, want empty", objResp.Objects)
	}

	// Download by cid returns the exact stored bytes.
	req := httptest.NewRequest(http.MethodGet, "/sdn/v1/runs/backfill/download?cid="+omdCID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download by cid = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); string(got) != string(sizedTestOMM(t, 25544)) {
		t.Fatalf("downloaded bytes mismatch: %d bytes", len(got))
	}
	if rec.Header().Get("X-SDS-Type") != "OMM" {
		t.Fatalf("X-SDS-Type = %q, want OMM", rec.Header().Get("X-SDS-Type"))
	}

	// Unknown run id -> 404 everywhere.
	req = httptest.NewRequest(http.MethodGet, "/sdn/v1/runs/no-such-run", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown run = %d, want 404", rec.Code)
	}
}
