package flowrt

// TestAOTMountRepro guards the WasmEdge nested-AOT fix from loop C.5b.
//
// History (loop C.4): the data-retrieval flow artifact (emception
// linked-direct bundle) AOT-compiled without error but the compiled code
// trapped "out of bounds memory access" inside
// space_data_module_runtime_dispatch_current_invocation_direct on the first
// dispatch that issued a storage.flatsql_* hostcall. ROOT CAUSE (C.5b): not
// an artifact bug — libwasmedge 0.14 keeps per-thread executor state that is
// clobbered when a SECOND VM executes AOT-compiled code nested inside a host
// function while the first VM's AOT frame is suspended on the same OS thread
// (AOT flow -> hostcall -> AOT engine). Interpreted-inside-AOT and
// AOT-inside-interpreted are unaffected. Fix: the flatsql engine runtime
// always executes on its own locked OS thread
// (wasmrt.WithDedicatedThread; see docs/wasmedge-aot-nested-execution.md),
// so no other VM's AOT frames are ever suspended beneath it.
//
// This test mounts the real flow bundle AOT-compiled and asserts the request
// SUCCEEDS. Env-gated only because it needs the compiled dist bundle +
// seeded store (slow), not because it is expected to fail:
//
//	SDN_C4_AOT_REPRO=1 go test ./internal/flowrt/ -run TestAOTMountRepro -v

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

func TestAOTMountRepro(t *testing.T) {
	if os.Getenv("SDN_C4_AOT_REPRO") == "" {
		t.Skip("set SDN_C4_AOT_REPRO=1")
	}
	dist := dataRetrievalFlowDist(t)
	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	store := newSeededMountStore(t, epoch1, epoch1+2*86400)
	reg := modulert.NewCapabilityRegistry()
	reg.Register("storage_query", caps.NewStorageCapFactory(store))
	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/test/data/", Flow: dist, Pool: 1, MemoryPages: 2048}},
		FlowMountDeps{
			CapRegistry: reg,
			NodeCtx:     &modulert.NodeContext{},
			AOTCacheDir: t.TempDir(),
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer mounted[0].Close()
	if !mounted[0].AOT() {
		t.Fatal("flow mount did not AOT-compile — expected AOT execution")
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(fmt.Sprintf("%s/test/data/omm/bulk?epoch=%d&limit=100&profile=nearest", srv.URL, epoch1+36*3600))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("status=%d bytes=%d", resp.StatusCode, len(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("AOT flow mount failed: status=%d body[:200]=%.200s "+
			"(nested AOT-in-AOT regression? see docs/wasmedge-aot-nested-execution.md)",
			resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Fatal("AOT flow mount returned an empty body")
	}
}
