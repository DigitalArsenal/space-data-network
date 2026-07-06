package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
)

type fakeFlowDocSource struct {
	programID string
	version   string
	mountPath string
	doc       *flowrt.FlowAPIDoc
}

func (f *fakeFlowDocSource) ProgramID() string          { return f.programID }
func (f *fakeFlowDocSource) FlowVersion() string        { return f.version }
func (f *fakeFlowDocSource) MountPath() string          { return f.mountPath }
func (f *fakeFlowDocSource) APIDoc() *flowrt.FlowAPIDoc { return f.doc }

func testFlow() *fakeFlowDocSource {
	return &fakeFlowDocSource{
		programID: "com.digitalarsenal.flows.data-retrieval",
		version:   "0.2.3",
		mountPath: "/api/v1/data/",
		doc: &flowrt.FlowAPIDoc{
			BasePath: "/api/v1/data",
			Tag:      "data",
			Routes: []flowrt.FlowAPIRoute{
				{
					Path:        "omm/bulk",
					Method:      "GET",
					OperationID: "getOmmBulk",
					Summary:     "Per-object OMM catalog stream",
					Anonymous:   true,
					Params: []map[string]interface{}{
						{"name": "epoch", "in": "query", "schema": map[string]interface{}{"type": "string"}},
					},
					Responses: map[string]flowrt.FlowAPIResponse{
						"200": {
							Description:  "stream",
							RecordStream: true,
							Content: map[string]flowrt.FlowAPIContent{
								ContentTypeFlatBufferStream: {
									Description: "aligned $OMM frames",
									SDS:         map[string]interface{}{"fileIdentifier": "$OMM"},
								},
								ContentTypeJSON: {
									Schema: map[string]interface{}{"type": "array"},
								},
							},
						},
						"304": {Description: "not modified"},
					},
				},
			},
		},
	}
}

func generateTestSpec(t *testing.T) map[string]interface{} {
	t.Helper()
	spec, err := GenerateOpenAPI(DocsHandlerOptions{
		Version: "test",
		Flows:   []FlowDocSource{testFlow()},
		EffectiveAnonymous: func(method, path string) bool {
			return method == http.MethodGet && path == "/api/v1/data/omm/bulk"
		},
	})
	if err != nil {
		t.Fatalf("GenerateOpenAPI: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}
	return doc
}

func opAt(t *testing.T, doc map[string]interface{}, path, method string) map[string]interface{} {
	t.Helper()
	paths, _ := doc["paths"].(map[string]interface{})
	item, _ := paths[path].(map[string]interface{})
	if item == nil {
		t.Fatalf("path %s missing from spec", path)
	}
	op, _ := item[method].(map[string]interface{})
	if op == nil {
		t.Fatalf("operation %s %s missing from spec", method, path)
	}
	return op
}

func TestGenerateOpenAPIFromMountedFlow(t *testing.T) {
	doc := generateTestSpec(t)

	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi version = %v", doc["openapi"])
	}

	op := opAt(t, doc, "/api/v1/data/omm/bulk", "get")
	if op["x-sdn-served-by"] != "flow" {
		t.Fatalf("x-sdn-served-by = %v, want flow", op["x-sdn-served-by"])
	}
	if op["x-sdn-flow"] != "com.digitalarsenal.flows.data-retrieval" {
		t.Fatalf("x-sdn-flow = %v", op["x-sdn-flow"])
	}
	if op["x-sdn-flow-version"] != "0.2.3" {
		t.Fatalf("x-sdn-flow-version = %v", op["x-sdn-flow-version"])
	}
	if op["x-sdn-anonymous"] != true {
		t.Fatalf("x-sdn-anonymous = %v, want true (effective allowlist admits it)", op["x-sdn-anonymous"])
	}
	if op["x-sdn-anonymous-requested"] != true {
		t.Fatalf("x-sdn-anonymous-requested = %v", op["x-sdn-anonymous-requested"])
	}

	// Record-stream conventions: both media types + both standard headers.
	responses := op["responses"].(map[string]interface{})
	ok200 := responses["200"].(map[string]interface{})
	headers := ok200["headers"].(map[string]interface{})
	if _, ok := headers["X-SDN-Record-Count"]; !ok {
		t.Fatal("200 must document X-SDN-Record-Count")
	}
	if _, ok := headers["ETag"]; !ok {
		t.Fatal("200 must document ETag")
	}
	content := ok200["content"].(map[string]interface{})
	fb, ok := content[ContentTypeFlatBufferStream].(map[string]interface{})
	if !ok {
		t.Fatal("200 must document the flatbuffer stream media type")
	}
	if _, ok := fb["x-sds-schema"]; !ok {
		t.Fatal("flatbuffer media type must carry x-sds-schema")
	}
	if _, ok := content[ContentTypeJSON]; !ok {
		t.Fatal("200 must document the bare-array json media type")
	}
	if _, ok := responses["304"]; !ok {
		t.Fatal("304 must be documented")
	}

	// components carry the shared header definitions.
	components := doc["components"].(map[string]interface{})
	headersDef := components["headers"].(map[string]interface{})
	if _, ok := headersDef["XSDNRecordCount"]; !ok {
		t.Fatal("components.headers.XSDNRecordCount missing")
	}
}

func TestGenerateOpenAPINativeAndPlanned(t *testing.T) {
	doc := generateTestSpec(t)

	// Native routes are present and clearly marked.
	health := opAt(t, doc, "/api/v1/data/health", "get")
	if health["x-sdn-served-by"] != "native" {
		t.Fatalf("health x-sdn-served-by = %v", health["x-sdn-served-by"])
	}
	nodeInfo := opAt(t, doc, "/api/node/info", "get")
	if nodeInfo["x-sdn-served-by"] != "native" {
		t.Fatalf("node/info x-sdn-served-by = %v", nodeInfo["x-sdn-served-by"])
	}
	docsOp := opAt(t, doc, "/api/v1/docs", "get")
	if docsOp["x-sdn-served-by"] != "native" {
		t.Fatalf("docs x-sdn-served-by = %v", docsOp["x-sdn-served-by"])
	}

	// Auth-gated native routes carry the effective stamp = false.
	if health["x-sdn-anonymous"] != false {
		t.Fatalf("health x-sdn-anonymous = %v (test allowlist admits only omm/bulk)", health["x-sdn-anonymous"])
	}

	// Planned Phase G routes are present, marked, and unmistakable.
	planned := []struct{ path, method, phase string }{
		{"/api/v1/peers", "get", "G.2"},
		{"/api/v1/peers/{peerId}", "get", "G.2"},
		{"/api/v1/standards", "get", "G.2"},
		{"/api/v1/peers/{peerId}/pnm", "get", "G.3"},
		{"/api/v1/peers/{peerId}/{standard}/latest", "get", "G.4"},
		{"/api/v1/query", "post", "G.5"},
	}
	for _, p := range planned {
		op := opAt(t, doc, p.path, p.method)
		if op["x-sdn-status"] != "planned" {
			t.Fatalf("%s %s x-sdn-status = %v, want planned", p.method, p.path, op["x-sdn-status"])
		}
		if op["x-sdn-planned-in"] != p.phase {
			t.Fatalf("%s x-sdn-planned-in = %v, want %s", p.path, op["x-sdn-planned-in"], p.phase)
		}
		summary, _ := op["summary"].(string)
		if !strings.HasPrefix(summary, "[PLANNED "+p.phase+"]") {
			t.Fatalf("%s summary %q lacks the [PLANNED %s] prefix", p.path, summary, p.phase)
		}
	}
}

// A mounted flow claiming a path shadows the planned declaration for it —
// the spec self-updates as Phase G lands.
func TestGenerateOpenAPIMountedFlowShadowsPlanned(t *testing.T) {
	peersFlow := &fakeFlowDocSource{
		programID: "com.digitalarsenal.flows.peers",
		version:   "0.1.0",
		mountPath: "/api/v1/peers",
		doc: &flowrt.FlowAPIDoc{
			Tag: "discovery",
			Routes: []flowrt.FlowAPIRoute{{
				Path: "", Method: "GET", Summary: "real peers flow",
			}},
		},
	}
	spec, err := GenerateOpenAPI(DocsHandlerOptions{
		Version: "test",
		Flows:   []FlowDocSource{peersFlow},
	})
	if err != nil {
		t.Fatalf("GenerateOpenAPI: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	op := opAt(t, doc, "/api/v1/peers", "get")
	if op["x-sdn-served-by"] != "flow" {
		t.Fatalf("mounted flow must shadow the planned decl, got %v", op["x-sdn-served-by"])
	}
	if _, ok := op["x-sdn-status"]; ok {
		t.Fatal("real mounted route must not be marked planned")
	}
}

func TestDocsHandlerServesSpecAndUI(t *testing.T) {
	h, err := NewDocsHandler(DocsHandlerOptions{
		Version: "test",
		Flows:   []FlowDocSource{testFlow()},
	})
	if err != nil {
		t.Fatalf("NewDocsHandler: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Spec: JSON + ETag + 304 revalidation.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi.json status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("openapi.json content-type = %q", ct)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("openapi.json must carry an ETag")
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("served spec is not JSON: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("openapi.json revalidation = %d, want 304", rec.Code)
	}

	// Docs page: HTML, strict same-origin CSP, references only local assets.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/docs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("docs status = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("docs CSP too loose: %q", csp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/v1/docs/scalar.js") || !strings.Contains(body, "/api/v1/openapi.json") {
		t.Fatal("docs page must reference the local bundle + local spec")
	}
	if strings.Contains(body, "https://cdn") || strings.Contains(body, "cdn.jsdelivr") {
		t.Fatal("docs page must not reference a CDN")
	}

	// Vendored Scalar bundle served locally.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/docs/scalar.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scalar.js status = %d", rec.Code)
	}
	if rec.Body.Len() < 1_000_000 {
		t.Fatalf("scalar.js suspiciously small (%d bytes) — vendored bundle missing?", rec.Body.Len())
	}
}
