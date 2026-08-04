package main

import (
	"net/http"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/apps"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// The node's own homepage becomes a real $APP record built from the SAME bytes
// it serves at "/". Same bytes, two envelopes — the record and the page cannot
// drift because there is only one copy of the content.
func TestDashboardIsRegisteredAsTheServerDefaultAPP(t *testing.T) {
	page := []byte("<!doctype html><title>SDN</title><body>dashboard</body>")
	registry, err := buildAppRegistry(config.AppsConfig{}, page)
	if err != nil {
		t.Fatalf("buildAppRegistry: %v", err)
	}
	entry, ok := registry.Default(apps.RuntimeServer)
	if !ok {
		t.Fatal("no server default")
	}
	if entry.ID != dashboardAppID || entry.URL != "/" || entry.State != apps.StateInstalled {
		t.Fatalf("server default = %+v", entry)
	}
	record, ok := registry.Record(dashboardAppID)
	if !ok {
		t.Fatal("dashboard record not served")
	}
	decoded, err := apps.DecodeAPP(record)
	if err != nil {
		t.Fatalf("decode dashboard record: %v", err)
	}
	if len(decoded.UI) != 1 || decoded.UI[0].ContentBytes != len(page) {
		t.Fatalf("dashboard UI = %+v, want one page of %d bytes", decoded.UI, len(page))
	}
}

// No dashboard artifact ⇒ NO server-class app. The node serves the wordmark
// placeholder at "/", and advertising an app whose page is not there would be
// a record describing something that does not exist.
func TestNoDashboardArtifactMeansNoServerDefault(t *testing.T) {
	registry, err := buildAppRegistry(config.AppsConfig{}, nil)
	if err != nil {
		t.Fatalf("buildAppRegistry: %v", err)
	}
	if entry, ok := registry.Default(apps.RuntimeServer); ok {
		t.Fatalf("server default resolved to %q with no dashboard artifact", entry.ID)
	}
	if len(registry.IDs()) != 0 {
		t.Fatalf("IDs = %v, want none", registry.IDs())
	}
}

// The browser default is deployed DATA: the operator declares the console the
// browser client should open, and the two faces link to each other.
func TestDeclaredBrowserAppBecomesTheBrowserDefault(t *testing.T) {
	registry, err := buildAppRegistry(config.AppsConfig{
		Declared: []config.DeclaredAppConfig{{
			ID:           "spaceaware-orbital-console",
			Name:         "Orbital Console",
			RuntimeClass: "browser",
			URL:          "https://spaceaware.io/beta/",
		}},
	}, []byte("<!doctype html><title>SDN</title>"))
	if err != nil {
		t.Fatalf("buildAppRegistry: %v", err)
	}
	defaults := registry.Defaults()
	server, ok := defaults[apps.RuntimeServer]
	if !ok {
		t.Fatal("no server default")
	}
	browser, ok := defaults[apps.RuntimeBrowser]
	if !ok {
		t.Fatal("no browser default")
	}
	if browser.ID != "spaceaware-orbital-console" || browser.State != apps.StateDeclared {
		t.Fatalf("browser default = %+v", browser)
	}
	if server.CrossLink == nil || server.CrossLink.URL != "https://spaceaware.io/beta/" {
		t.Fatalf("server cross_link = %+v", server.CrossLink)
	}
	if browser.CrossLink == nil || browser.CrossLink.URL != "/" {
		t.Fatalf("browser cross_link = %+v", browser.CrossLink)
	}
}

// A bad apps.* declaration is a loud error the operator sees, never a silent
// no-op that leaves a client opening the wrong thing.
func TestBadAppDeclarationsAreErrors(t *testing.T) {
	cases := map[string]config.AppsConfig{
		"unknown runtime class": {Declared: []config.DeclaredAppConfig{
			{ID: "x", RuntimeClass: "desktop", URL: "https://example.org/"}}},
		"declared app with no url": {Declared: []config.DeclaredAppConfig{
			{ID: "x", RuntimeClass: "browser"}}},
		"default names an unregistered app": {DefaultBrowserApp: "not-installed"},
		"default names the wrong class":     {DefaultBrowserApp: dashboardAppID},
	}
	for name, cfg := range cases {
		if _, err := buildAppRegistry(cfg, []byte("<!doctype html><title>SDN</title>")); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// Both default-$APP routes are on the ANONYMOUS read surface. A browser client
// asking "what do I open?" has no session yet — gating this read would make
// the default app undiscoverable to exactly the client it exists for.
func TestDefaultAppRoutesAreAnonymousReads(t *testing.T) {
	for _, path := range []string{
		"/api/v1/apps/default",
		"/api/v1/apps/records/" + dashboardAppID,
	} {
		if !isPublicReadAPIPath(path) {
			t.Fatalf("%s is not on the anonymous read surface", path)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if !isPublicAPIRequest(method, path) {
				t.Fatalf("%s %s is not anonymous", method, path)
			}
		}
		// Read-only: no write method may ride the same allowance.
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if isPublicAPIRequest(method, path) {
				t.Fatalf("%s %s is anonymous — the surface is read-only", method, path)
			}
		}
	}
}
