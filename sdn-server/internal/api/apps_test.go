package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// appsFeed serves one request against a handler wired to a real ledger and a
// caller-supplied runtime snapshot, returning the decoded feed.
func appsFeed(t *testing.T, h *AppsHandler) map[string]interface{} {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/apps")
	if err != nil {
		t.Fatalf("GET /api/apps: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var feed map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	return feed
}

// A retrieval app's metrics reach the feed keyed by SOURCE ID, attributed to
// the app that produced them, with the debounce window and the next eligible
// pull spelled out. This is the contract the $APPS widget renders.
func TestAppsFeedReportsRetrievalMetricsPerSource(t *testing.T) {
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()

	const appID = "com.digitalarsenal.flows.celestrak-gp-ingest"
	const url = "https://celestrak.org/NORAD/elements/gp.php?SPECIAL=full-catalog&FORMAT=csv"

	ledger.RecordFetch(sourcemetrics.Fetch{URL: url, Status: 200, Bytes: 4_127_233, DurationMs: 5321})
	ledger.RecordIngest(sourcemetrics.Ingest{
		AppID: appID, ProviderID: "space-data-network-02", SourceName: "celestrak-gp",
		SourceURL: url, Schema: "OMM.fbs", BatchID: "d657df0b", Records: 10847, Inserted: 10847,
	})
	ledger.RecordPNM("space-data-network-02", "celestrak-gp", sourcemetrics.PNM{
		ID: "d657df0b", CID: "bafyreiexamplecid", Schema: "OMM.fbs",
		RecordCount: 10847, PublishedAt: time.Unix(1785000000, 0).UTC(),
	})

	runtime := func() plugins.RuntimeSnapshot {
		return plugins.RuntimeSnapshot{
			Modules: []plugins.RuntimeModuleEntry{{
				ID:      appID,
				Version: "0.1.0",
				Status:  "running",
				Stats:   plugins.RuntimeModuleStats{TimerRunCount: 3, UptimeMs: 900000, LastTimerStatus: "ok"},
				Manifest: &plugins.RuntimeModuleManifest{
					PluginID:     appID,
					Name:         "CelesTrak GP Ingest Flow",
					PluginFamily: "FLOW",
					Timers: []plugins.RuntimeModuleTimer{{
						TimerID: "timer-gp", DefaultIntervalMs: 10_800_000,
					}},
				},
			}},
		}
	}

	feed := appsFeed(t, NewAppsHandler(runtime, ledger.Sources, nil, ledger.RecordPNM))

	apps, _ := feed["apps"].([]interface{})
	if len(apps) != 1 {
		t.Fatalf("apps = %d, want 1: %v", len(apps), feed)
	}
	app := apps[0].(map[string]interface{})
	if app["id"] != appID {
		t.Fatalf("app id = %v", app["id"])
	}
	if app["kind"] != "flow" {
		t.Fatalf("app kind = %v, want flow (read from the FLOW registry)", app["kind"])
	}
	if app["name"] != "CelesTrak GP Ingest Flow" {
		t.Fatalf("app name = %v", app["name"])
	}
	if app["run_count"] != float64(3) {
		t.Fatalf("run_count = %v, want 3", app["run_count"])
	}

	timers, _ := app["timers"].([]interface{})
	if len(timers) != 1 {
		t.Fatalf("timers = %v", app["timers"])
	}
	timer := timers[0].(map[string]interface{})
	if timer["trigger_id"] != "timer-gp" || timer["interval_hours"] != float64(3) {
		t.Fatalf("timer = %v, want timer-gp at 3 h", timer)
	}

	sources, _ := app["sources"].([]interface{})
	if len(sources) != 1 {
		t.Fatalf("app sources = %v (metrics must be attributed to the producing app)", app["sources"])
	}
	src := sources[0].(map[string]interface{})
	if src["source_id"] != "space-data-network-02/celestrak-gp" {
		t.Fatalf("source_id = %v", src["source_id"])
	}
	if src["origin"] != "retrieved" {
		t.Fatalf("origin = %v, want retrieved (never derived)", src["origin"])
	}
	if src["debounce_hours"] != float64(3) {
		t.Fatalf("debounce_hours = %v, want 3", src["debounce_hours"])
	}
	if src["last_pull_size_bytes"] != float64(4_127_233) {
		t.Fatalf("last_pull_size_bytes = %v", src["last_pull_size_bytes"])
	}
	retrieved, _ := src["last_retrieved_at"].(string)
	nextDue, _ := src["next_eligible_at"].(string)
	if retrieved == "" || nextDue == "" {
		t.Fatalf("last_retrieved_at=%q next_eligible_at=%q, both required", retrieved, nextDue)
	}
	rt, err := time.Parse(time.RFC3339, retrieved)
	if err != nil {
		t.Fatalf("last_retrieved_at is not RFC3339: %v", err)
	}
	nt, err := time.Parse(time.RFC3339, nextDue)
	if err != nil {
		t.Fatalf("next_eligible_at is not RFC3339: %v", err)
	}
	if got := nt.Sub(rt); got != 3*time.Hour {
		t.Fatalf("next_eligible_at - last_retrieved_at = %v, want the 3 h debounce window", got)
	}

	pnm, _ := src["last_pnm"].(map[string]interface{})
	if pnm == nil || pnm["cid"] != "bafyreiexamplecid" {
		t.Fatalf("last_pnm = %v", src["last_pnm"])
	}
	if pnm["record_count"] != float64(10847) {
		t.Fatalf("last_pnm record_count = %v", pnm["record_count"])
	}

	// The top-level list is the full ledger, so a source whose app is not
	// currently loaded is still visible rather than silently disappearing.
	if all, _ := feed["sources"].([]interface{}); len(all) != 1 {
		t.Fatalf("top-level sources = %v", feed["sources"])
	}
}

// The feed must never leak an operator secret or a local path. It is served
// anonymously, so its field set is a security surface.
func TestAppsFeedFieldsAreAnonymousSafe(t *testing.T) {
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()
	ledger.RecordIngest(sourcemetrics.Ingest{
		AppID: "app", ProviderID: "p", SourceName: "s",
		SourceURL: "https://celestrak.org/pub/satcat.csv", Schema: "CAT.fbs", BatchID: "b",
	})

	feed := appsFeed(t, NewAppsHandler(nil, ledger.Sources, nil, nil))
	raw, _ := json.Marshal(feed)
	for _, forbidden := range []string{"/var/", "/home/", "/etc/", "password", "secret", "token", "xpub"} {
		if containsFold(string(raw), forbidden) {
			t.Fatalf("anonymous $APPS feed leaked %q: %s", forbidden, raw)
		}
	}
}

func containsFold(haystack, needle string) bool {
	h := []rune(haystack)
	n := []rune(needle)
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// A node with no apps and no retrievals answers honestly rather than erroring.
func TestAppsFeedEmptyNode(t *testing.T) {
	feed := appsFeed(t, NewAppsHandler(nil, nil, nil, nil))
	if feed["count"] != float64(0) {
		t.Fatalf("count = %v, want 0", feed["count"])
	}
	if apps, _ := feed["apps"].([]interface{}); len(apps) != 0 {
		t.Fatalf("apps = %v, want empty", feed["apps"])
	}
	if _, ok := feed["generated_at"].(string); !ok {
		t.Fatalf("generated_at missing: %v", feed)
	}
}

// slowPublicationStore stands in for the record store while a large ingest
// holds it: every lookup takes far longer than the feed's budget.
type slowPublicationStore struct {
	delay time.Duration
	calls int32
}

func (s *slowPublicationStore) ListDatasetShardPublications(
	storage.DatasetShardPublicationQuery) ([]storage.DatasetShardPublication, error) {
	atomic.AddInt32(&s.calls, 1)
	time.Sleep(s.delay)
	return nil, nil
}

// The $APPS feed must answer even when the record store does not.
//
// Measured on host-01 during a full CelesTrak SATCAT ingest: /api/v1/id
// answered in 10 ms, /api/v1/stats in 2.4 s, and this feed — which queried the
// store once per source with no bound — did not answer at all. "Is the
// retriever working?" is asked PRECISELY while it is working, so the status
// surface may never be the thing that blocks.
func TestAppsFeedAnswersWhileTheStoreIsBusy(t *testing.T) {
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()

	for _, name := range []string{"celestrak-gp", "celestrak-satcat", "celestrak-satcat-csv"} {
		ledger.RecordIngest(sourcemetrics.Ingest{
			AppID: "app", ProviderID: "p", SourceName: name,
			SourceURL: "https://celestrak.org/" + name, Schema: "OMM.fbs",
			BatchID: "b-" + name, PullBytes: 4_750_985, Records: 13000, Inserted: 13000,
		})
		// A PNM already persisted in the ledger must still be served when the
		// authoritative refresh is abandoned.
		ledger.RecordPNM("p", name, sourcemetrics.PNM{CID: "bafy-" + name, Schema: "OMM.fbs"})
	}

	h := NewAppsHandler(nil, ledger.Sources, nil, ledger.RecordPNM)
	h.store = &slowPublicationStore{delay: 5 * time.Second}

	start := time.Now()
	feed := appsFeed(t, h)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("feed took %s with a blocked store; it must abandon the refresh at its budget", elapsed)
	}
	srcs, _ := feed["sources"].([]interface{})
	if len(srcs) != 3 {
		t.Fatalf("sources = %d, want 3 served from the ledger", len(srcs))
	}
	for _, raw := range srcs {
		src := raw.(map[string]interface{})
		if src["last_pull_size_bytes"] != float64(4_750_985) {
			t.Fatalf("ledger metrics lost under a slow store: %v", src["last_pull_size_bytes"])
		}
		pnm, _ := src["last_pnm"].(map[string]interface{})
		if pnm == nil || pnm["cid"] == "" {
			t.Fatalf("persisted last_pnm not served when the refresh was abandoned: %v", src["last_pnm"])
		}
	}
}
