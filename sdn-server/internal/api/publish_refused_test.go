package api

// Cutover review B2 (sdn-format2-cutover-fixes-20260930): on store format 2
// the engine refuses a record (here, one without its file identifier) that
// the rest of its batch is stored with. The batch publish must report that
// frame as failed, not stored: it answered 201 with the refused record's CID.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

func TestFormat2PublishBatchReportsRefusedFrames(t *testing.T) {
	if !flatsqlrt.NativeHostIOSupported() {
		t.Skip("the C host I/O module is not available on this platform")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatalf("prewarm the partition-store engine: %v", err)
	}
	t.Setenv(format2.FormatEnv, "2")
	t.Setenv("SDN_FLATSQL_CHECKPOINT_INTERVAL", "0")
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.Format2() {
		t.Fatal("SDN_STORE_FORMAT=2 opened a format-1 store")
	}

	cfg := &config.PublishingConfig{Enabled: true, DefaultQuotaBytes: 1 << 24, MaxRecordBytes: 1 << 20, MinTrustLevel: "untrusted"}
	h := NewPublishHandler(store, nil, NewStorageQuotaManager(store, cfg.DefaultQuotaBytes), cfg, nil)
	mux := http.NewServeMux()
	h.RegisterUnauthenticatedRoutes(mux, "16Uiu2HAmPublishRefusals")

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	omm := func(norad uint32, name string) []byte {
		return sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectName(name).
			WithEpoch(base.Add(time.Duration(norad) * time.Second).Format("2006-01-02T15:04:05Z")).WithMeanMotion(15.5).Build()[4:]
	}
	good, other := omm(27001, "PUBLISHED"), omm(27002, "PUBLISHED-TOO")
	bad := append([]byte(nil), omm(27003, "REFUSED")...)
	copy(bad[4:8], []byte{0, 0, 0, 0})
	var body bytes.Buffer
	for _, f := range [][]byte{good, bad, other} {
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(f)))
		body.Write(f)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/data/publish/batch/OMM.fbs", &body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Results []map[string]interface{} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 3 {
		t.Fatalf("%d results: %s", len(out.Results), rec.Body.String())
	}
	for i, f := range [][]byte{good, bad, other} {
		r := out.Results[i]
		stored := i != 1
		if _, hasErr := r["error"]; hasErr == stored || (stored && r["cid"] != storage.ComputeCID(f)) {
			t.Fatalf("frame %d (stored %v): %v", i, stored, r)
		}
		_, gerr := store.GetRecord("OMM.fbs", storage.ComputeCID(f))
		if (gerr == nil) != stored {
			t.Fatalf("frame %d: readable %v, want %v (%v)", i, gerr == nil, stored, gerr)
		}
	}
	t.Logf("refused frame: %s", fmt.Sprint(out.Results[1]["error"]))
}
