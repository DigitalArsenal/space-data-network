package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsSpaceAwareUIPath(t *testing.T) {
	// Route skeleton per SDN_SPACEAWARE_UI_LOOP.md U0.1 — must stay in sync
	// with sdn-js/ui/src/spaceaware/router.ts SPACEAWARE_ROUTES.
	served := []string{
		"/login",
		"/console",
		"/console/node",
		"/console/peers",
		"/console/groups",
		"/console/data",
		"/console/channels",
		"/console/conjunction",
		"/orbital",
		"/gantt",
		"/bmc2",
		"/bmc2/f1",
		"/bmc2/f2",
		"/bmc2/f3",
		"/bmc2/f4",
		"/bmc2/f5",
		"/bmc2/f6",
	}
	for _, p := range served {
		if !isSpaceAwareUIPath(p) {
			t.Errorf("isSpaceAwareUIPath(%q) = false, want true", p)
		}
		if !isSpaceAwareUIPath(p + "/") {
			t.Errorf("isSpaceAwareUIPath(%q) = false, want true", p+"/")
		}
	}

	// Legacy app / other daemon surfaces must NOT be claimed.
	notServed := []string{
		"/",
		"/index.html",
		"/node",
		"/peers",
		"/data",
		"/channels",
		"/conjunction",
		"/admin",
		"/admin/",
		"/webui/",
		"/api/v1/peers",
		"/console/unknown",
		"/bmc2/f7",
		"/bmc2/index",
		"/loginx",
		"/consoles",
		"/assets/app.js",
	}
	for _, p := range notServed {
		if isSpaceAwareUIPath(p) {
			t.Errorf("isSpaceAwareUIPath(%q) = true, want false", p)
		}
	}
}

func TestServeSpaceAwareUIEmbeddedArtifact(t *testing.T) {
	if len(spaceAwareAppHTML) == 0 {
		t.Fatal("embedded spaceaware_app.html is empty")
	}
	if !strings.Contains(string(spaceAwareAppHTML), "</head>") {
		t.Fatal("embedded artifact missing </head> (config injection point)")
	}
	// Packaging hard rule: single-file artifact — no external file references.
	for _, forbidden := range []string{"<script src=", `<link rel="stylesheet"`, "fonts.googleapis.com", "fonts.gstatic.com", "cdn.jsdelivr.net", "unpkg.com"} {
		if strings.Contains(string(spaceAwareAppHTML), forbidden) {
			t.Errorf("embedded artifact contains forbidden reference %q", forbidden)
		}
	}

	rec := httptest.NewRecorder()
	serveSpaceAwareUI(rec, httptest.NewRequest(http.MethodGet, "/console/node", nil))
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("COOP = %q, want same-origin", got)
	}
	if got := res.Header.Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
		t.Errorf("COEP = %q, want require-corp", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "window.__SDN_CONFIG__") {
		t.Error("served body missing injected window.__SDN_CONFIG__")
	}
	if !strings.Contains(body, "sa-root") {
		t.Error("served body missing SpaceAware root element")
	}

	// Method restriction.
	rec = httptest.NewRecorder()
	serveSpaceAwareUI(rec, httptest.NewRequest(http.MethodPost, "/console/node", nil))
	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Result().StatusCode)
	}
}
