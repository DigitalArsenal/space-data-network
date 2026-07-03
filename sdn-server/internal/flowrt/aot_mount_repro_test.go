package flowrt

// TestAOTMountRepro documents a KNOWN WasmEdge AOT incompatibility found in
// loop C.4: the data-retrieval flow artifact (emception linked-direct bundle)
// AOT-COMPILES without error, but the compiled code traps with "out of
// bounds memory access" inside
// space_data_module_runtime_dispatch_current_invocation_direct on the FIRST
// dispatch, at any scale (the store's no-EH flatsql engine artifact AOTs and
// runs fine through the same WithAOTCache path). Until fixed, flow mounts
// run interpreted: node.MountFlows does not set FlowMountDeps.AOTCacheDir.
//
// Env-gated so the default suite stays green:
//
//	SDN_C4_AOT_REPRO=1 go test ./internal/flowrt/ -run TestAOTMountRepro -v

import (
	"io"
	"net/http"
	"net/http/httptest"
	"fmt"
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
	t.Logf("aot=%v", mounted[0].AOT())
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(fmt.Sprintf("%s/test/data/omm/bulk?epoch=%d&limit=100&profile=nearest", srv.URL, epoch1+36*3600))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("status=%d bytes=%d body[:120]=%.120s", resp.StatusCode, len(body), body)
	if resp.StatusCode == http.StatusOK {
		t.Log("AOT flow served the request — the WasmEdge trap is FIXED; " +
			"re-enable AOTCacheDir in node.MountFlows and re-measure")
	}
}
