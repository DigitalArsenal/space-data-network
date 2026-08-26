package flowrt

// Gateway loop G.5 integration: the REAL compiled public-query flow bundle
// (space-data-network-modules/flows/public-query/dist/query, bridge
// linkage) is mounted at its PRODUCTION path (/api/v1/query) over the real
// storage capability handler and a REAL FlatSQL store + engine — so the
// injection/abuse suite below exercises the ACTUAL in-engine sandbox
// (authorizer / single-statement / stmt_readonly / timeout / row+byte
// caps), not a stub. The fb path exercises the whole C.5c body-reference
// chain: cap PutBodyRef -> wasm $sdnbodyref descriptor -> $HTR BODY_REF ->
// httpmount TakeBodyRef -> HTTP body bytes.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func publicQueryFlowDist(t *testing.T) string {
	t.Helper()
	root := os.Getenv("SDN_PUBLIC_QUERY_FLOW_DIST")
	if root == "" {
		root = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "public-query", "dist")
	}
	dist := filepath.Join(root, "query")
	if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err != nil {
		t.Skipf("public-query flow bundle not found at %s (set SDN_PUBLIC_QUERY_FLOW_DIST): %v", dist, err)
	}
	return dist
}

func buildQueryTestOMM(t *testing.T, norad uint32, name string, epochUnix int64) []byte {
	t.Helper()
	epoch := time.Unix(epochUnix, 0).UTC().Format("2006-01-02T15:04:05Z")
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(epochUnix)).
		WithMeanMotion(15.1).
		WithEccentricity(0.001).
		WithInclination(51.6).
		Build()
	return data[4:] // stored form: bare buffer
}

func queryPOST(t *testing.T, url, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, buf.Bytes()
}

func TestHTTPMountedPublicQueryFlow(t *testing.T) {
	dist := publicQueryFlowDist(t)

	// REAL store + engine with three OMM records under a real source tag.
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	records := [][]byte{
		buildQueryTestOMM(t, 25544, "ISS (ZARYA)", 1783300000),
		buildQueryTestOMM(t, 33591, "NOAA 19", 1783300100),
		buildQueryTestOMM(t, 20580, "HST", 1783300200),
	}
	tags := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-q1"}
	if inserted, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-q-test", nil, tags); err != nil || inserted != 3 {
		t.Fatalf("StoreBatchWithSourceTags: %d, %v", inserted, err)
	}

	// The production cap wiring with TIGHT caps so the abuse suite can
	// prove bounded time/size without long waits.
	queryCaps := flatsqlrt.SandboxCaps{MaxRows: 100, MaxBytes: 1 << 20, Timeout: 1500 * time.Millisecond}
	reg := modulert.NewCapabilityRegistry()
	storageFac := caps.NewStorageCapFactoryWithOptions(store, caps.StorageCapOptions{QueryCaps: queryCaps})
	reg.RegisterBridgeAware("storage_query", storageFac)

	// loop B1-followup default-deny gate: record a test-scoped operator
	// approval for THIS bundle's real content hash (capability_approval_test.go).
	policy := approvedCapabilityPolicy(t, dist, "storage_query")

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/api/v1/query", Flow: dist, Pool: 1}},
		FlowMountDeps{
			CapRegistry:    reg,
			NodeCtx:        &modulert.NodeContext{CapabilityPolicy: policy},
			MaxMemoryPages: 4096,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()

	doc := mounted[0].APIDoc()
	if doc == nil || doc.BasePath != "/api/v1/query" || len(doc.Routes) != 2 ||
		!doc.Routes[0].Anonymous || !doc.Routes[1].Anonymous {
		t.Fatalf("public-query bundle api block wrong: %+v", doc)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()
	url := srv.URL + "/api/v1/query"

	// Reference: the engine's own stream for the same SELECT.
	reference, err := store.QuerySandboxedStream("SELECT _data FROM OMM ORDER BY NORAD_CAT_ID", queryCaps)
	if err != nil {
		t.Fatalf("reference stream: %v", err)
	}
	wantEtag := fmt.Sprintf("W/\"fnv1a64-%016x\"", reference.FNV1a64)

	t.Run("fb SELECT streams the engine bytes verbatim via BODY_REF", func(t *testing.T) {
		resp, body := queryPOST(t, url, `{"sql":"SELECT _data FROM OMM ORDER BY NORAD_CAT_ID"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("content-type = %q", ct)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "3" {
			t.Fatalf("x-sdn-record-count = %q, want 3", rc)
		}
		if etag := resp.Header.Get("ETag"); etag != wantEtag {
			t.Fatalf("etag = %q, want %q", etag, wantEtag)
		}
		if !bytes.Equal(body, reference.Bytes) {
			t.Fatalf("body != engine stream (%d vs %d bytes)", len(body), len(reference.Bytes))
		}
		frames := splitSizePrefixedFrames(t, body)
		if len(frames) != 3 {
			t.Fatalf("frames = %d, want 3", len(frames))
		}

		// Conditional POST: If-None-Match answers 304, empty body.
		resp304, body304 := queryPOST(t, url, `{"sql":"SELECT _data FROM OMM ORDER BY NORAD_CAT_ID"}`,
			map[string]string{"If-None-Match": wantEtag})
		if resp304.StatusCode != http.StatusNotModified || len(body304) != 0 {
			t.Fatalf("304 = %d (%d bytes)", resp304.StatusCode, len(body304))
		}
	})

	t.Run("format=json full-record result is the schema-exact bare array with the shared etag", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json", `{"sql":"SELECT _data FROM OMM ORDER BY NORAD_CAT_ID"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if etag := resp.Header.Get("ETag"); etag != wantEtag {
			t.Fatalf("json etag = %q, want the shared %q", etag, wantEtag)
		}
		var recs []map[string]interface{}
		if err := json.Unmarshal(body, &recs); err != nil {
			t.Fatalf("bare array: %v (%q)", err, body[:min(len(body), 120)])
		}
		if len(recs) != 3 {
			t.Fatalf("records = %d", len(recs))
		}
		// HARD RULE: schema-exact key capitalization.
		if recs[0]["NORAD_CAT_ID"] != float64(20580) || recs[0]["OBJECT_NAME"] != "HST" {
			t.Fatalf("first record: %v", recs[0])
		}
	})

	t.Run("projection results are engine-assembled rows JSON with verbatim column keys", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json",
			`{"sql":"SELECT NORAD_CAT_ID, OBJECT_NAME, MEAN_MOTION FROM OMM WHERE NORAD_CAT_ID = 25544"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		var rows []map[string]interface{}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("rows json: %v (%q)", err, body)
		}
		if len(rows) != 1 || rows[0]["NORAD_CAT_ID"] != float64(25544) ||
			rows[0]["OBJECT_NAME"] != "ISS (ZARYA)" {
			t.Fatalf("rows: %v", rows)
		}
	})

	t.Run("sort/limit params wrap the SQL; aggregate SELECT works", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json",
			`{"sql":"SELECT NORAD_CAT_ID FROM OMM","sort":"NORAD_CAT_ID DESC","limit":2}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		var rows []map[string]interface{}
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("rows: %v (%q)", err, body)
		}
		if len(rows) != 2 || rows[0]["NORAD_CAT_ID"] != float64(33591) {
			t.Fatalf("sorted rows: %v", rows)
		}

		resp, body = queryPOST(t, url+"?format=json", `{"sql":"SELECT count(*) AS n FROM OMM"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("aggregate status = %d body %q", resp.StatusCode, body)
		}
		if string(body) != `[{"n":3}]` {
			t.Fatalf("aggregate body = %q", body)
		}
	})

	t.Run("profile=nearest serves the per-object epoch query", func(t *testing.T) {
		resp, body := queryPOST(t, url+"?format=json",
			`{"profile":"nearest","epoch":1783300100,"source":"celestrak-gp"}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		var recs []map[string]interface{}
		if err := json.Unmarshal(body, &recs); err != nil {
			t.Fatalf("profile records: %v (%q)", err, body)
		}
		if len(recs) != 3 {
			t.Fatalf("profile records = %d, want 3 (one per object)", len(recs))
		}
	})

	t.Run("GET serves the queryable surface enumerated from the live engine", func(t *testing.T) {
		resp, body := discoveryGET(t, url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		var surface struct {
			Tables []struct {
				Name    string   `json:"name"`
				Kind    string   `json:"kind"`
				Source  string   `json:"source"`
				Columns []string `json:"columns"`
				Records int64    `json:"records"`
			} `json:"tables"`
			Caps struct {
				TimeoutMs int64 `json:"timeout_ms"`
				MaxRows   int64 `json:"max_rows"`
				MaxBytes  int64 `json:"max_bytes"`
			} `json:"caps"`
		}
		if err := json.Unmarshal(body, &surface); err != nil {
			t.Fatalf("surface json: %v (%q)", err, body)
		}
		// EVERY EMBEDDED STANDARD IS ROUTED, so the surface is no longer
		// "the two decorated standards": it lists every routed relation
		// (an empty answer is a valid answer, and a caller cannot discover
		// what it may query from a listing that hides it). Per-source
		// partitions are listed only for standards that actually hold
		// records — otherwise the public body repeats a full column list
		// once per standard per source.
		if len(surface.Tables) < 200 {
			t.Fatalf("surface lists %d relations — every routed standard should appear", len(surface.Tables))
		}
		type surfaceRel = struct {
			Name    string   `json:"name"`
			Kind    string   `json:"kind"`
			Source  string   `json:"source"`
			Columns []string `json:"columns"`
			Records int64    `json:"records"`
		}
		var ommView, ommShadow *surfaceRel
		var foreignPartitions []string
		for i := range surface.Tables {
			rel := &surface.Tables[i]
			base, source, isPartition := strings.Cut(rel.Name, "@")
			switch {
			case rel.Name == "OMM":
				ommView = rel
			case rel.Name == "OMM@celestrak-gp":
				ommShadow = rel
			case isPartition && base != "OMM":
				// Only $OMM has records here, and only a standard with
				// records resident gets its partitions listed.
				foreignPartitions = append(foreignPartitions, rel.Name)
			case isPartition && rel.Source != source:
				t.Fatalf("%s reports source %q", rel.Name, rel.Source)
			}
		}
		if ommView == nil || ommView.Kind != "view" {
			t.Fatalf("surface has no OMM view: %+v", surface.Tables)
		}
		if ommShadow == nil || ommShadow.Kind != "table" || ommShadow.Source != "celestrak-gp" {
			t.Fatalf("surface has no OMM@celestrak-gp shadow: %+v", ommShadow)
		}
		if len(foreignPartitions) != 0 {
			t.Fatalf("per-source partitions listed for standards with nothing resident: %v", foreignPartitions)
		}
		if ommView.Records != 3 {
			t.Fatalf("OMM records = %d, want 3", ommView.Records)
		}
		hasData := false
		for _, col := range ommView.Columns {
			if col == "_data" {
				hasData = true
			}
		}
		if !hasData {
			t.Fatalf("OMM columns missing _data: %v", ommView.Columns)
		}
		if surface.Caps.TimeoutMs != 1500 || surface.Caps.MaxRows != 100 {
			t.Fatalf("caps: %+v", surface.Caps)
		}

		// If-None-Match answers 304.
		etag := resp.Header.Get("ETag")
		resp304, body304 := discoveryGET(t, url, map[string]string{"If-None-Match": etag})
		if resp304.StatusCode != http.StatusNotModified || len(body304) != 0 {
			t.Fatalf("surface 304 = %d (%d bytes)", resp304.StatusCode, len(body304))
		}
	})

	// ---- Injection / abuse suite against the REAL engine sandbox ----

	t.Run("injection and abuse suite rejects with correct statuses in bounded time", func(t *testing.T) {
		type abuseCase struct {
			name       string
			body       string
			wantStatus int
			wantCode   string
		}
		cases := []abuseCase{
			{"write attempt (UPDATE)", `{"sql":"UPDATE sdn_record_index SET cid = 'x'"}`, 400, "not-authorized"},
			{"write attempt (DROP)", `{"sql":"DROP TABLE OMM"}`, 400, "not-authorized"},
			{"write attempt (INSERT)", `{"sql":"INSERT INTO sdn_record_index VALUES (1)"}`, 400, "not-authorized"},
			{"multi-statement", `{"sql":"SELECT _data FROM OMM; DROP TABLE OMM"}`, 400, "multi-statement"},
			{"pragma", `{"sql":"PRAGMA journal_mode = DELETE"}`, 400, "not-authorized"},
			{"pragma read", `{"sql":"PRAGMA table_info(OMM)"}`, 400, "not-authorized"},
			{"attach", `{"sql":"ATTACH DATABASE ':memory:' AS x"}`, 400, "not-authorized"},
			{"temp write", `{"sql":"CREATE TEMP TABLE evil (x)"}`, 400, "not-authorized"},
			{"transaction", `{"sql":"BEGIN"}`, 400, "not-authorized"},
			{"control-table read", `{"sql":"SELECT * FROM sdn_record_index"}`, 400, "not-authorized"},
			{"sqlite_master read", `{"sql":"SELECT * FROM sqlite_master"}`, 400, "not-authorized"},
			{"runaway recursive CTE", `{"sql":"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c"}`, 422, "timeout"},
			{"oversized result (row cap)", `{"sql":"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 5000) SELECT x FROM c","format":"json"}`, 422, "row-cap"},
			{"cartesian blowup", `{"sql":"SELECT a.NORAD_CAT_ID FROM OMM a, OMM b, OMM c, OMM d, OMM e, OMM f, OMM g, OMM h","format":"json"}`, 422, "row-cap"},
			{"projection as flatbuffer", `{"sql":"SELECT NORAD_CAT_ID FROM OMM"}`, 406, "not-a-record-stream"},
			{"bad SQL", `{"sql":"SELECT nope FROM OMM"}`, 400, ""},
			{"empty body", ``, 400, "missing-sql"},
			{"sort injection", `{"sql":"SELECT _data FROM OMM","sort":"NORAD_CAT_ID; DROP TABLE OMM"}`, 400, "invalid-sort"},
		}
		for _, tc := range cases {
			started := time.Now()
			resp, body := queryPOST(t, url, tc.body, nil)
			elapsed := time.Since(started)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("%s: status = %d body %q, want %d", tc.name, resp.StatusCode, body, tc.wantStatus)
			}
			var payload struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("%s: error body: %v (%q)", tc.name, err, body)
			}
			if tc.wantCode != "" && payload.Code != tc.wantCode {
				t.Fatalf("%s: code = %q (%q), want %q", tc.name, payload.Code, payload.Error, tc.wantCode)
			}
			if payload.Error == "" {
				t.Fatalf("%s: empty error message", tc.name)
			}
			// BOUNDED TIME: nothing may run past the 1.5 s statement
			// deadline plus slack.
			if elapsed > 5*time.Second {
				t.Fatalf("%s: took %s — not bounded", tc.name, elapsed)
			}
		}

		// The store is intact and the engine healthy after the whole suite.
		resp, body := queryPOST(t, url+"?format=json", `{"sql":"SELECT count(*) AS n FROM OMM"}`, nil)
		if resp.StatusCode != http.StatusOK || string(body) != `[{"n":3}]` {
			t.Fatalf("post-suite query: %d %q", resp.StatusCode, body)
		}
	})

	t.Run("non-POST non-GET degrades to 404", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("DELETE status = %d, want 404", resp.StatusCode)
		}
	})
}
