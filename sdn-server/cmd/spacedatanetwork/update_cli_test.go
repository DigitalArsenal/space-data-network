package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadBundleManifestAcceptsSignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature",
		"update": {
			"feedBaseUrl": "https://updates.spacedatanetwork.org",
			"pubsubTopic": "/sdn/updates/v1/beta",
			"updaterModule": "org.spacedatanetwork.updater",
			"updaterWasm": "runtime/modules/org.spacedatanetwork.updater.wasm"
		}
	}`)

	manifest, err := loadBundleManifest(path)
	if err != nil {
		t.Fatalf("loadBundleManifest returned error: %v", err)
	}
	if manifest.Version != "1.2.3" {
		t.Fatalf("Version = %q, want 1.2.3", manifest.Version)
	}
	if manifest.Channel != "beta" {
		t.Fatalf("Channel = %q, want beta", manifest.Channel)
	}
	if manifest.Update.FeedBaseURL != "https://updates.spacedatanetwork.org" ||
		manifest.Update.PubsubTopic != "/sdn/updates/v1/beta" ||
		manifest.Update.UpdaterModule != "org.spacedatanetwork.updater" ||
		manifest.Update.UpdaterWASM != "runtime/modules/org.spacedatanetwork.updater.wasm" {
		t.Fatalf("Update metadata = %#v", manifest.Update)
	}
}

func TestLoadBundleManifestRejectsMissingUpdateMetadata(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without update metadata")
	}
}

func TestLoadBundleManifestRejectsUnsignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted an unsigned manifest")
	}
}

func TestLoadBundleManifestRejectsMissingVersion(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without version")
	}
}

func TestLoadBundleManifestRejectsWhitespaceRequiredFields(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": " ",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest with whitespace-only version")
	}
}

func TestProviderFeedIndexURLUsesCLIBundlePath(t *testing.T) {
	got, err := providerFeedIndexURL("https://updates.spacedatanetwork.org/", "beta", "linux", "amd64")
	if err != nil {
		t.Fatalf("providerFeedIndexURL returned error: %v", err)
	}
	want := "https://updates.spacedatanetwork.org/cli-bundle/beta/linux/amd64/index.json"
	if got != want {
		t.Fatalf("providerFeedIndexURL = %q, want %q", got, want)
	}
}

func TestProviderFeedIndexURLRejectsHTTP(t *testing.T) {
	_, err := providerFeedIndexURL("http://updates.spacedatanetwork.org", "beta", "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("providerFeedIndexURL error = %v, want HTTPS rejection", err)
	}
}

func TestFetchProviderUpdateCandidateSelectsCompatibleUpdate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-bundle/beta/"+runtime.GOOS+"/"+runtime.GOARCH+"/index.json" {
			t.Fatalf("request path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema": "org.spacedatanetwork.update.index.v1",
			"generated_at": "2026-06-22T00:00:00Z",
			"feed_base_url": "` + serverURLPlaceholder + `",
			"updates": [
				{
					"update_id": "cli-bundle-beta-older",
					"version": "1.0.4",
					"sequence": 104,
					"channel": "beta",
					"target": {"platform": "` + runtime.GOOS + `", "arch": "` + runtime.GOARCH + `", "kind": "cli-bundle"},
					"manifest_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.4/manifest.json",
					"carrier_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.4/update.wasm"
				},
				{
					"update_id": "cli-bundle-beta-newer",
					"version": "1.0.5",
					"sequence": 105,
					"channel": "beta",
					"target": {"platform": "` + runtime.GOOS + `", "arch": "` + runtime.GOARCH + `", "kind": "cli-bundle"},
					"manifest_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.5/manifest.json",
					"carrier_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.5/update.wasm"
				}
			]
		}`))
	}))
	defer server.Close()

	update, err := fetchProviderUpdateCandidate(server.Client(), bundleManifest{
		Version: "1.0.4",
		Channel: "beta",
		Update:  bundleUpdateMetadata{FeedBaseURL: server.URL},
	}, 104, providerUpdateFilter{})
	if err != nil {
		t.Fatalf("fetchProviderUpdateCandidate returned error: %v", err)
	}
	if update.UpdateID != "cli-bundle-beta-newer" {
		t.Fatalf("selected update = %s, want cli-bundle-beta-newer", update.UpdateID)
	}
}

func TestReadHTTPSURLRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()

	_, err := readHTTPSURL(server.Client(), server.URL, 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("readHTTPSURL error = %v, want size rejection", err)
	}
}

func writeBundleManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const serverURLPlaceholder = "https://127.0.0.1"
