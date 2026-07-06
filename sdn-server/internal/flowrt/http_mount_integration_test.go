package flowrt

// Loop C.3(d) integration: the REAL compiled data-retrieval flow bundle
// (space-data-network-modules/flows/data-retrieval/dist, linked-direct — 8
// nodes / 13 edges, $PLG + FLOW exports) is mounted on a TEST path through the
// config mount table and served by a real HTTP listener. The Go host is pure
// socket plumbing: requests are piped in as verbatim $HTQ frames, responses
// come back as verbatim $HTR frames; every routing / format / caching / ETag
// decision below is made INSIDE the wasm flow. The flow's storage_query
// capability is satisfied by the modulert/caps storage handler over a real
// FlatSQLStore seeded through the normal ingest path.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// dataRetrievalFlowDist locates the compiled flow bundle. Override with
// SDN_DATA_RETRIEVAL_FLOW_DIST; defaults to the sibling
// space-data-network-modules checkout.
func dataRetrievalFlowDist(t *testing.T) string {
	t.Helper()
	dist := os.Getenv("SDN_DATA_RETRIEVAL_FLOW_DIST")
	if dist == "" {
		dist = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "data-retrieval", "dist")
	}
	if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err != nil {
		t.Skipf("data-retrieval flow bundle not found at %s (set SDN_DATA_RETRIEVAL_FLOW_DIST): %v", dist, err)
	}
	return dist
}

func buildMountTestOMM(t *testing.T, norad uint32, name string, epochUnix int64) []byte {
	t.Helper()
	epoch := time.Unix(epochUnix, 0).UTC().Format("2006-01-02T15:04:05Z")
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(epochUnix)).
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
	return data[4:] // builders emit size-prefixed bytes; the store takes unprefixed
}

// newSeededMountStore creates a FlatSQLStore and seeds it through the normal
// batch ingest path: 3 objects × 2 epochs under a real provider/source tag.
func newSeededMountStore(t *testing.T, epoch1, epoch2 int64) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	var records [][]byte
	for _, norad := range []uint32{1001, 1002, 1003} {
		records = append(records,
			buildMountTestOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch1),
			buildMountTestOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch2),
		)
	}
	tags := storage.SourceTags{ProviderID: "prov-mount", SourceName: "celestrak-gp", BatchID: "batch-mount-1"}
	inserted, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-mount-test", nil, tags)
	if err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}
	if inserted != 6 {
		t.Fatalf("seeded %d records, want 6", inserted)
	}
	return store
}

func TestHTTPMountedDataRetrievalFlow(t *testing.T) {
	dist := dataRetrievalFlowDist(t)

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	store := newSeededMountStore(t, epoch1, epoch2)

	// Capability registry: exactly what the node wires for modules. The
	// LINKED flow (loop C.7) submits queries DIRECTLY to the store engine
	// in-wasm; the storage handler stays provisioned (the manifest still
	// declares storage_query) but a recording wrapper proves NO
	// storage.flatsql_* hostcall crosses the bridge on ANY path — the last
	// host-mediated crossing is gone.
	var storageHostcalls atomic.Int64
	storageFac := caps.NewStorageCapFactory(store)
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		inner := storageFac(mod, bridge)
		return func(operation string, payload []byte) ([]byte, error) {
			if strings.HasPrefix(operation, "storage.") {
				storageHostcalls.Add(1)
			}
			return inner(operation, payload)
		}
	})

	// Config mount table on a TEST path (the /api/v1/data cutover is C.4).
	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/test/data/", Flow: dist}},
		FlowMountDeps{
			CapRegistry:    reg,
			NodeCtx:        &modulert.NodeContext{},
			MaxMemoryPages: 2048,
			EngineLink:     store,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()
	if len(mounted) != 1 {
		t.Fatalf("mounted %d flows, want 1", len(mounted))
	}
	if got := mounted[0].ProgramID(); got != "com.digitalarsenal.flows.data-retrieval" {
		t.Fatalf("mounted flow id = %q", got)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Target 36h after epoch1 = 12h before epoch2 → nearest picks the epoch2
	// row for every object.
	target := epoch1 + 36*3600
	bulkURL := fmt.Sprintf("%s/test/data/omm/bulk?epoch=%d&limit=100&profile=nearest", srv.URL, target)

	get := func(url string, headers map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp, body
	}

	var etag string

	t.Run("flatbuffers default streams the aligned stream verbatim", func(t *testing.T) {
		resp, body := get(bulkURL, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("content-type = %q", ct)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "3" {
			t.Fatalf("x-sdn-record-count = %q, want 3", rc)
		}
		etag = resp.Header.Get("ETag")
		if !strings.HasPrefix(etag, `W/"fnv1a64-`) {
			t.Fatalf("etag = %q", etag)
		}

		// Verbatim: the HTTP body must be byte-identical to the engine's own
		// aligned size-prefixed stream for the same profile query.
		expected, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(target), 100)
		if err != nil {
			t.Fatalf("QueryEpochRawStream: %v", err)
		}
		if string(body) != string(expected.Bytes) {
			t.Fatalf("HTTP body (%d bytes) != engine aligned stream (%d bytes)", len(body), len(expected.Bytes))
		}

		// The stream parses as aligned size-prefixed $OMM frames with the
		// epoch2 row per object.
		frames, err := flatsqlrt.DecodeSizePrefixedStream(body)
		if err != nil {
			t.Fatalf("DecodeSizePrefixedStream: %v", err)
		}
		if len(frames) != 3 {
			t.Fatalf("frames = %d, want 3", len(frames))
		}
		seen := map[uint32]float64{}
		for i, frame := range frames {
			if !OMM.OMMBufferHasIdentifier(frame) {
				t.Fatalf("frame %d is not $OMM", i)
			}
			omm := OMM.GetRootAsOMM(frame, 0)
			seen[omm.NORAD_CAT_ID()] = omm.USER_DEFINED_EPOCH_TIMESTAMP()
		}
		for _, norad := range []uint32{1001, 1002, 1003} {
			if got, ok := seen[norad]; !ok || got != float64(epoch2) {
				t.Fatalf("norad %d epoch = %v (present=%v), want %d", norad, got, ok, epoch2)
			}
		}

		// Direct linkage (loop C.7): the byte-verbatim body above came
		// through an ENGINE body-reference harvested under the store engine
		// lock — zero storage.* hostcalls crossed the bridge.
		if calls := storageHostcalls.Load(); calls != 0 {
			t.Fatalf("linked flow issued %d storage.* hostcalls; query submission must be in-wasm", calls)
		}
	})

	t.Run("json branch", func(t *testing.T) {
		resp, body := get(bulkURL+"&format=json", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type = %q", ct)
		}
		// format=json is a BARE top-level array — the same record stream as
		// the flatbuffer format in a different encoding. Metadata travels in
		// headers (x-sdn-record-count), never a body envelope.
		if got := resp.Header.Get("X-Sdn-Record-Count"); got != "3" {
			t.Fatalf("x-sdn-record-count = %q, want 3", got)
		}
		if len(body) == 0 || body[0] != '[' {
			t.Fatalf("json body must be a bare top-level array, got %q...", body[:min(len(body), 40)])
		}
		var records []struct {
			NoradCatID uint32 `json:"norad_cat_id"`
			ObjectName string `json:"object_name"`
		}
		if err := json.Unmarshal(body, &records); err != nil {
			t.Fatalf("json body: %v (%q)", err, body)
		}
		if len(records) != 3 {
			t.Fatalf("records = %d, want 3", len(records))
		}
		names := map[uint32]string{}
		for _, r := range records {
			names[r.NoradCatID] = r.ObjectName
		}
		if names[1001] != "SAT-1001" || names[1002] != "SAT-1002" || names[1003] != "SAT-1003" {
			t.Fatalf("records = %+v", records)
		}
	})

	t.Run("304 via If-None-Match", func(t *testing.T) {
		if etag == "" {
			t.Fatal("no etag captured from the first response")
		}
		resp, body := get(bulkURL, map[string]string{"If-None-Match": etag})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status = %d, want 304 (body %q)", resp.StatusCode, body)
		}
		if len(body) != 0 {
			t.Fatalf("304 body must be empty, got %d bytes", len(body))
		}
		if got := resp.Header.Get("ETag"); got != etag {
			t.Fatalf("304 etag = %q, want %q", got, etag)
		}

		// A stale validator revalidates to 200.
		resp, body = get(bulkURL, map[string]string{"If-None-Match": `W/"stale"`})
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("stale validator: status = %d, body %d bytes", resp.StatusCode, len(body))
		}
	})

	t.Run("404 path", func(t *testing.T) {
		resp, body := get(srv.URL+"/test/data/nope", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (body %q)", resp.StatusCode, body)
		}
		var decoded struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil || decoded.Error == "" {
			t.Fatalf("404 body should carry a JSON error: %v (%q)", err, body)
		}
	})

	// Every path above — flatbuffer, json, 304, 404 — ran without a single
	// storage.* hostcall: query submission is fully in-wasm (loop C.7).
	if calls := storageHostcalls.Load(); calls != 0 {
		t.Fatalf("linked flow issued %d storage.* hostcalls across the request matrix", calls)
	}
}

// TestFlowMountRejectsUnsatisfiedCapability: loading must fail when the host
// cannot satisfy a capability the flow manifest declares.
func TestFlowMountRejectsUnsatisfiedCapability(t *testing.T) {
	dist := dataRetrievalFlowDist(t)

	// The LINKED artifact imports the store engine: mounting without an
	// explicit EngineLink is a hard error (first-party grant, never
	// inferred) — the untrusted-module security boundary.
	_, err := LoadMountedFlow(dist, FlowMountDeps{
		CapRegistry:    modulert.NewCapabilityRegistry(),
		NodeCtx:        &modulert.NodeContext{},
		MaxMemoryPages: 2048,
	})
	if err == nil {
		t.Fatal("LoadMountedFlow succeeded without an EngineLink for a linked artifact")
	}
	if !strings.Contains(err.Error(), "EngineLink") {
		t.Fatalf("error should name the missing engine link: %v", err)
	}

	// With the engine link but WITHOUT the storage_query capability handler
	// the load must still reject on the unsatisfied capability.
	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	store := newSeededMountStore(t, epoch1, epoch1+2*86400)
	_, err = LoadMountedFlow(dist, FlowMountDeps{
		CapRegistry:    modulert.NewCapabilityRegistry(), // no storage_query
		NodeCtx:        &modulert.NodeContext{},
		MaxMemoryPages: 2048,
		EngineLink:     store,
	})
	if err == nil {
		t.Fatal("LoadMountedFlow succeeded without the storage_query capability")
	}
	if !strings.Contains(err.Error(), "storage_query") {
		t.Fatalf("error should name the unsatisfied capability: %v", err)
	}
}

// TestFlowMountPoolServesConcurrentClients (loop C.4): a mount is served by a
// pool of instances; concurrent clients each get complete, correct responses.
func TestFlowMountPoolServesConcurrentClients(t *testing.T) {
	dist := dataRetrievalFlowDist(t)

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	store := newSeededMountStore(t, epoch1, epoch2)

	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", caps.NewStorageCapFactory(store))

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/test/data/", Flow: dist, Pool: 4}},
		FlowMountDeps{
			CapRegistry:    reg,
			NodeCtx:        &modulert.NodeContext{},
			MaxMemoryPages: 2048,
			EngineLink:     store,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()
	if got := mounted[0].PoolSize(); got != 4 {
		t.Fatalf("pool size = %d, want 4", got)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	target := epoch1 + 36*3600
	bulkURL := fmt.Sprintf("%s/test/data/omm/bulk?epoch=%d&limit=100&profile=nearest", srv.URL, target)
	expected, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(target), 100)
	if err != nil {
		t.Fatalf("QueryEpochRawStream: %v", err)
	}

	const clients = 8
	const perClient = 5
	errs := make(chan error, clients)
	for c := 0; c < clients; c++ {
		go func() {
			for i := 0; i < perClient; i++ {
				resp, err := http.Get(bulkURL)
				if err != nil {
					errs <- err
					return
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					errs <- err
					return
				}
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("status %d: %s", resp.StatusCode, body)
					return
				}
				if string(body) != string(expected.Bytes) {
					errs <- fmt.Errorf("response bytes diverge (%d vs %d)", len(body), len(expected.Bytes))
					return
				}
			}
			errs <- nil
		}()
	}
	for c := 0; c < clients; c++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent client: %v", err)
		}
	}
}

// TestFlowMountSkipsUninstalledFlow: a config mount whose flow artifact is
// not installed is skipped (error-logged) instead of failing registration —
// default configs ship the /api/v1/data/ mount before the module is
// delivered.
func TestFlowMountSkipsUninstalledFlow(t *testing.T) {
	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/test/data/", Flow: "com.example.not-installed"}},
		FlowMountDeps{
			CapRegistry: modulert.NewCapabilityRegistry(),
			NodeCtx:     &modulert.NodeContext{},
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts should skip uninstalled flows, got: %v", err)
	}
	if len(mounted) != 0 {
		t.Fatalf("mounted %d flows, want 0", len(mounted))
	}
}
