package main

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/update"
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

// And no download of `update install` or `update check` follows a redirect
// off the feed: not the index, the manifest or the carrier, whether the hop
// goes to another host or port, to plain HTTP, or out of the feed's path. The
// refused hop is never contacted. A redirect that stays inside the feed is
// followed. (The node's signal lane has the same rule:
// TestUpdateSignalFetchDoesNotFollowARedirectOffTheBundleFeed.)
func TestUpdateDownloadsDoNotFollowARedirectOffTheBundleFeed(t *testing.T) {
	const lane = "/updates/cli-bundle/beta/" + runtime.GOOS + "/" + runtime.GOARCH
	var feed *httptest.Server
	index := func() []byte {
		base := feed.URL + "/updates"
		return []byte(`{
			"schema": "org.spacedatanetwork.update.index.v1",
			"generated_at": "2026-11-15T00:00:00Z",
			"feed_base_url": "` + base + `",
			"updates": [{
				"update_id": "cli-bundle-beta-2.0.0",
				"version": "2.0.0",
				"sequence": 200,
				"channel": "beta",
				"target": {"platform": "` + runtime.GOOS + `", "arch": "` + runtime.GOARCH + `", "kind": "cli-bundle"},
				"manifest_url": "` + base + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/2.0.0/manifest.json",
				"carrier_url": "` + base + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/2.0.0/update.wasm"
			}]
		}`)
	}
	var elsewhereHits, plainHits, offPathHits atomic.Int64
	// Each would serve a valid index, so following the hop would succeed.
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereHits.Add(1)
		_, _ = w.Write(index())
	}))
	defer elsewhere.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write(index())
	}))
	defer plain.Close()
	var redirects sync.Map // request path on the feed -> Location
	redirectTo := func(path, location string) { redirects.Store(path, location) }
	feed = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target, ok := redirects.Load(r.URL.Path); ok {
			http.Redirect(w, r, target.(string), http.StatusFound)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/updates/") {
			offPathHits.Add(1)
		}
		_, _ = w.Write(index())
	}))
	defer feed.Close()
	manifest := bundleManifest{Version: "1.0.0", Channel: "beta", Update: bundleUpdateMetadata{FeedBaseURL: feed.URL + "/updates"}}
	base, err := update.ParseFeedBase(manifest.Update.FeedBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// One client trusts every httptest TLS server (they share a certificate),
	// so only the redirect policy can stop a hop.
	client := feed.Client()
	hits := func() int64 { return elsewhereHits.Load() + plainHits.Load() + offPathHits.Load() }

	for name, location := range map[string]string{
		"another port":     elsewhere.URL + lane + "/index.json",
		"plain HTTP":       plain.URL + lane + "/index.json",
		"out of the path":  "/admin/index.json",
		"a sibling prefix": "/updates-elsewhere/index.json",
	} {
		redirectTo(lane+"/index.json", location)
		before := hits()
		if candidate, err := fetchProviderUpdateCandidate(client, manifest, 100, providerUpdateFilter{}); err == nil {
			t.Errorf("index redirected to %s (%s): followed, candidate %s", name, location, candidate.UpdateID)
		} else if !strings.Contains(err.Error(), "redirect") {
			t.Errorf("index redirected to %s: %v, want the redirect refused", name, err)
		}
		if hits() != before {
			t.Errorf("index redirected to %s: the hop off the feed was contacted", name)
		}

		redirectTo(lane+"/2.0.0/manifest.json", location)
		before = hits()
		if _, err := readFeedURL(client, base, feed.URL+lane+"/2.0.0/manifest.json", 1<<20); err == nil {
			t.Errorf("manifest redirected to %s: followed", name)
		}
		if hits() != before {
			t.Errorf("manifest redirected to %s: the hop off the feed was contacted", name)
		}
	}

	// A hop that stays inside the feed is followed.
	redirectTo(lane+"/index.json", lane+"/moved/index.json")
	if candidate, err := fetchProviderUpdateCandidate(client, manifest, 100, providerUpdateFilter{}); err != nil || candidate.UpdateID != "cli-bundle-beta-2.0.0" {
		t.Fatalf("index redirected inside the feed: candidate %+v, err %v", candidate, err)
	}
	// A download that starts off the feed is refused before it is made.
	before := hits()
	if _, err := readFeedURL(client, base, elsewhere.URL+lane+"/2.0.0/update.wasm", 1<<20); err == nil || hits() != before {
		t.Fatalf("a carrier off the feed: err %v, contacted %t", err, hits() != before)
	}
	// An operator-named URL (update stage --manifest) is not bound to the feed,
	// but no hop of it may leave HTTPS.
	redirectTo("/operator/manifest.json", plain.URL+"/m.json")
	if _, err := readHTTPSURL(client, feed.URL+"/operator/manifest.json", 1<<20); err == nil || !strings.Contains(err.Error(), "away from HTTPS") {
		t.Fatalf("an operator URL redirected to plain HTTP: %v, want refused", err)
	}
	redirectTo("/operator/manifest.json", elsewhere.URL+"/m.json")
	if _, err := readHTTPSURL(client, feed.URL+"/operator/manifest.json", 1<<20); err != nil {
		t.Fatalf("an operator URL redirected to another HTTPS host: %v, want followed", err)
	}
}
