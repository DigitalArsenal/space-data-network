package flowrt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFlowAPIDoc(t *testing.T) {
	doc, version := parseFlowAPIDoc([]byte(`{
		"programId": "com.example.flow",
		"version": "1.2.3",
		"api": {
			"basePath": "/api/v1/example",
			"tag": "example",
			"routes": [
				{
					"path": "things",
					"summary": "List things",
					"anonymous": true,
					"responses": {
						"200": {"recordStream": true, "content": {"application/json": {"schema": {"type": "array"}}}}
					}
				},
				{"path": "things/{id}", "method": "post"}
			]
		}
	}`))
	if version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", version)
	}
	if doc == nil {
		t.Fatal("api doc not parsed")
	}
	if len(doc.Routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(doc.Routes))
	}
	if doc.Routes[0].Method != "GET" {
		t.Fatalf("default method = %q, want GET", doc.Routes[0].Method)
	}
	if doc.Routes[1].Method != "POST" {
		t.Fatalf("method normalization = %q, want POST", doc.Routes[1].Method)
	}
	if !doc.Routes[0].Anonymous {
		t.Fatal("anonymous flag lost")
	}
	resp, ok := doc.Routes[0].Responses["200"]
	if !ok || !resp.RecordStream {
		t.Fatal("recordStream response lost")
	}
	if _, ok := resp.Content["application/json"]; !ok {
		t.Fatal("content media type lost")
	}
}

func TestParseFlowAPIDocAbsent(t *testing.T) {
	doc, version := parseFlowAPIDoc([]byte(`{"programId": "x", "version": "0.1.0"}`))
	if doc != nil {
		t.Fatalf("expected nil api doc, got %+v", doc)
	}
	if version != "0.1.0" {
		t.Fatalf("version = %q", version)
	}
}

// The REAL data-retrieval bundle (loop G.1 acceptance input) must carry a
// parseable api extension: the OpenAPI generator reads exactly these bytes
// from the mounted flow at runtime.
func TestDataRetrievalBundleCarriesAPIDoc(t *testing.T) {
	dist := os.Getenv("SDN_DATA_RETRIEVAL_FLOW_DIST")
	if dist == "" {
		dist = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "data-retrieval", "dist")
	}
	data, err := os.ReadFile(filepath.Join(dist, "flow.json"))
	if err != nil {
		t.Skipf("data-retrieval flow bundle not found (set SDN_DATA_RETRIEVAL_FLOW_DIST): %v", err)
	}
	doc, version := parseFlowAPIDoc(data)
	if doc == nil {
		t.Fatal("data-retrieval dist flow.json has no api extension")
	}
	if version == "" {
		t.Fatal("data-retrieval dist flow.json has no version")
	}
	if doc.BasePath != "/api/v1/data" {
		t.Fatalf("basePath = %q, want /api/v1/data", doc.BasePath)
	}
	var sawBulk, sawQuery bool
	for _, route := range doc.Routes {
		switch route.Path {
		case "omm/bulk":
			sawBulk = true
			if route.Method != "GET" || !route.Anonymous {
				t.Fatalf("omm/bulk decl unexpected: method=%q anonymous=%v", route.Method, route.Anonymous)
			}
			resp, ok := route.Responses["200"]
			if !ok || !resp.RecordStream {
				t.Fatal("omm/bulk 200 must be a recordStream response")
			}
			if _, ok := resp.Content["application/vnd.sdn.flatbuffers.stream"]; !ok {
				t.Fatal("omm/bulk must declare the flatbuffer stream media type")
			}
			if _, ok := resp.Content["application/json"]; !ok {
				t.Fatal("omm/bulk must declare the bare-array json media type")
			}
		case "query":
			sawQuery = true
			if route.Method != "POST" {
				t.Fatalf("query method = %q, want POST", route.Method)
			}
		}
	}
	if !sawBulk || !sawQuery {
		t.Fatalf("expected omm/bulk + query routes, got %+v", doc.Routes)
	}
}
