package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// `update install` takes its manifest and carrier from the bundle's own feed
// and nowhere else. The index is unsigned and only points: an entry whose
// manifest_url or carrier_url leaves the bundle's update.feedBaseUrl is
// refused when it is selected, before either is fetched.
func TestFetchProviderUpdateCandidateRefusesAnEntryOffTheBundleFeed(t *testing.T) {
	const lane = "/updates/cli-bundle/beta/" + runtime.GOOS + "/" + runtime.GOARCH
	for name, urls := range map[string][2]string{
		"manifest on another host": {"https://attacker.example" + lane + "/2.0.0/manifest.json", "{base}" + lane + "/2.0.0/update.wasm"},
		"carrier on another host":  {"{base}" + lane + "/2.0.0/manifest.json", "https://attacker.example" + lane + "/2.0.0/update.wasm"},
		"outside the feed path":    {"{base}/admin/manifest.json", "{base}" + lane + "/2.0.0/update.wasm"},
	} {
		t.Run(name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != lane+"/index.json" {
					t.Errorf("fetched %s: only the index may be fetched", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				base := server.URL + "/updates"
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"schema": "org.spacedatanetwork.update.index.v1",
					"generated_at": "2026-11-15T00:00:00Z",
					"feed_base_url": "` + base + `",
					"updates": [{
						"update_id": "cli-bundle-beta-2.0.0",
						"version": "2.0.0",
						"sequence": 200,
						"channel": "beta",
						"target": {"platform": "` + runtime.GOOS + `", "arch": "` + runtime.GOARCH + `", "kind": "cli-bundle"},
						"manifest_url": "` + strings.ReplaceAll(urls[0], "{base}", server.URL) + `",
						"carrier_url": "` + strings.ReplaceAll(urls[1], "{base}", server.URL) + `"
					}]
				}`))
			}))
			defer server.Close()

			candidate, err := fetchProviderUpdateCandidate(server.Client(), bundleManifest{
				Version: "1.0.0",
				Channel: "beta",
				Update:  bundleUpdateMetadata{FeedBaseURL: server.URL + "/updates"},
			}, 100, providerUpdateFilter{})
			if err == nil || !strings.Contains(err.Error(), "is not under this bundle's update feed") {
				t.Fatalf("candidate = %+v, err = %v: want the entry refused as off the bundle's feed", candidate, err)
			}
		})
	}
}
