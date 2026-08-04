package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/apps"
)

func testRegistry(t *testing.T) *apps.Registry {
	t.Helper()
	r := apps.New(AppsRecordPrefix)
	record, err := apps.BuildInlinePageRecord(
		apps.AppIdentity{ID: "sdn-dashboard", Name: "SDN Node Dashboard", Version: "1.0.4"},
		[]apps.InlinePage{{ID: "dashboard", HTML: []byte("<!doctype html><title>dash</title>"), Entry: true}},
	)
	if err != nil {
		t.Fatalf("BuildInlinePageRecord: %v", err)
	}
	if _, err := r.InstallRecord(apps.RuntimeServer, "/", record); err != nil {
		t.Fatalf("InstallRecord: %v", err)
	}
	if _, err := r.Declare(apps.Declaration{
		ID: "spaceaware-orbital-console", Name: "Orbital Console",
		RuntimeClass: apps.RuntimeBrowser, URL: "https://spaceaware.io/beta/",
	}); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	return r
}

func serveDefaults(t *testing.T, h *DefaultAppsHandler, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The document a browser client reads before it has any identity: both
// defaults, each linking to the other, plus where to fetch the record.
func TestDefaultAppsDocumentCarriesBothFacesAndTheirLinks(t *testing.T) {
	h := NewDefaultAppsHandler(testRegistry(t)).WithNodePeerID("16Uiu2HAmTest")
	rec := serveDefaults(t, h, AppsDefaultPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc struct {
		NodePeerID     string   `json:"node_peer_id"`
		RuntimeClasses []string `json:"runtime_classes"`
		Defaults       map[string]struct {
			ID          string `json:"ID"`
			Name        string `json:"NAME"`
			Version     string `json:"VERSION"`
			State       string `json:"state"`
			URL         string `json:"url"`
			RecordURL   string `json:"record_url"`
			Default     bool   `json:"default"`
			RuntimeName string `json:"runtime_class"`
			CrossLink   *struct {
				RuntimeClass string `json:"runtime_class"`
				AppID        string `json:"app_id"`
				URL          string `json:"url"`
			} `json:"cross_link"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	if doc.NodePeerID != "16Uiu2HAmTest" {
		t.Fatalf("node_peer_id = %q", doc.NodePeerID)
	}
	if len(doc.RuntimeClasses) != 2 || doc.RuntimeClasses[0] != "server" || doc.RuntimeClasses[1] != "browser" {
		t.Fatalf("runtime_classes = %v", doc.RuntimeClasses)
	}
	server, ok := doc.Defaults["server"]
	if !ok {
		t.Fatalf("no server default in %s", rec.Body.String())
	}
	if server.ID != "sdn-dashboard" || server.URL != "/" || server.State != "installed" || !server.Default {
		t.Fatalf("server default = %+v", server)
	}
	if server.RecordURL != AppsRecordPrefix+"sdn-dashboard" {
		t.Fatalf("server record_url = %q", server.RecordURL)
	}
	browser, ok := doc.Defaults["browser"]
	if !ok {
		t.Fatalf("no browser default in %s", rec.Body.String())
	}
	if browser.ID != "spaceaware-orbital-console" || browser.URL != "https://spaceaware.io/beta/" ||
		browser.State != "declared" || browser.RecordURL != "" {
		t.Fatalf("browser default = %+v", browser)
	}
	if server.CrossLink == nil || server.CrossLink.AppID != "spaceaware-orbital-console" {
		t.Fatalf("server cross_link = %+v", server.CrossLink)
	}
	if browser.CrossLink == nil || browser.CrossLink.URL != "/" {
		t.Fatalf("browser cross_link = %+v", browser.CrossLink)
	}
}

// The record route hands back the $APP FlatBuffer itself, so a client can
// verify the app it is about to run against the record's own hashes.
func TestRecordRouteServesTheAPPFlatBuffer(t *testing.T) {
	h := NewDefaultAppsHandler(testRegistry(t))
	rec := serveDefaults(t, h, AppsRecordPrefix+"sdn-dashboard")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != apps.ContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, apps.ContentType)
	}
	decoded, err := apps.DecodeAPP(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("served bytes are not an $APP record: %v", err)
	}
	if decoded.ID != "sdn-dashboard" {
		t.Fatalf("served record ID = %q", decoded.ID)
	}
}

func TestRecordRouteRefusesUnknownAndNestedPaths(t *testing.T) {
	h := NewDefaultAppsHandler(testRegistry(t))
	for _, path := range []string{
		AppsRecordPrefix,
		AppsRecordPrefix + "nope",
		AppsRecordPrefix + "spaceaware-orbital-console", // declared: no record
		AppsRecordPrefix + "a/b",
	} {
		if rec := serveDefaults(t, h, path); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}

// A node with no registry still ANSWERS. "This node has no default app" and
// "this node is too old to have the route" must be distinguishable by a
// client, and only a 200 with empty defaults says the first.
func TestDefaultAppsAnswersWithoutARegistry(t *testing.T) {
	rec := serveDefaults(t, NewDefaultAppsHandler(nil), AppsDefaultPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc struct {
		Defaults map[string]json.RawMessage `json:"defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc.Defaults) != 0 {
		t.Fatalf("defaults = %v, want empty", doc.Defaults)
	}
}

func TestDefaultAppsRejectsWrites(t *testing.T) {
	h := NewDefaultAppsHandler(testRegistry(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	for _, path := range []string{AppsDefaultPath, AppsRecordPrefix + "sdn-dashboard"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, rec.Code)
		}
	}
}
