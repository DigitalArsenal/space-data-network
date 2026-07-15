package sdnservices

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
)

// httpCall drives one http.request through the cap handler bound to a bridge
// with the given granted capabilities, returning the decoded cap envelope.
func httpCall(t *testing.T, cfg HTTPCapConfig, granted []string, req map[string]interface{}) map[string]interface{} {
	t.Helper()
	bridge := modulert.NewHostBridge(&modulert.NodeContext{}, granted)
	handler := NewHTTPCapFactory(cfg)(nil, bridge)
	payload, _ := json.Marshal(req)
	resp, err := handler("http.request", payload)
	if err != nil {
		t.Fatalf("http cap handler error: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(resp, &meta); err != nil {
		t.Fatalf("cap response not JSON: %v (%s)", err, resp)
	}
	return meta
}

// TestHTTPCapFetchesFromStub proves the happy path: a granted module fetches a
// stub URL and receives its body.
func TestHTTPCapFetchesFromStub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("DATE,F10.7\n2026-01-02,151.5\n"))
	}))
	defer srv.Close()

	meta := httpCall(t, HTTPCapConfig{}, []string{"http"}, map[string]interface{}{"url": srv.URL})
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("fetch failed: %v", meta)
	}
	result := meta["result"].(map[string]interface{})
	if status, _ := result["status"].(float64); status != 200 {
		t.Fatalf("status = %v, want 200", result["status"])
	}
	if body, _ := result["body"].(string); !strings.Contains(body, "F10.7") {
		t.Fatalf("body missing payload: %q", body)
	}
}

// TestHTTPCapFailClosedWithoutGrant proves the fail-closed gate: a bridge NOT
// granted "http" cannot fetch, even calling the handler directly.
func TestHTTPCapFailClosedWithoutGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("stub was contacted by a module without the http grant")
	}))
	defer srv.Close()

	meta := httpCall(t, HTTPCapConfig{}, nil /* no grant */, map[string]interface{}{"url": srv.URL})
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("ungranted module fetched: %v", meta)
	}
	msg, _ := meta["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "http capability grant") {
		t.Fatalf("refusal does not name the missing grant: %v", meta)
	}
}

// TestHTTPCapMaxBytesErrorsInsteadOfTruncating proves an oversized response is
// refused, never silently truncated (a truncated CelesTrak catalog would
// ingest a partial batch as complete).
func TestHTTPCapMaxBytesErrorsInsteadOfTruncating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64*1024))
	}))
	defer srv.Close()

	meta := httpCall(t, HTTPCapConfig{}, []string{"http"}, map[string]interface{}{"url": srv.URL, "max_bytes": 32 * 1024})
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("oversized response delivered instead of refused: %v", meta)
	}
	msg, _ := meta["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "exceeds") {
		t.Fatalf("error does not name the size violation: %v", meta)
	}
}

// ---------------------------------------------------------------------------
// CelesTrak / Space-Track fetch policy — deterministic unit tests.
// ---------------------------------------------------------------------------

// TestCelestrakPolicyLedgerSkipsWithin3h proves the 3h URL-keyed ledger: a
// second fetch of the SAME celestrak URL within the TTL is refused, and it is
// PERSISTED (survives a policy rebuild = a daemon restart).
func TestCelestrakPolicyLedgerSkipsWithin3h(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: base}
	cfg := HTTPCapConfig{LedgerDir: dir, now: clock.Now, sleep: func(time.Duration) {}}

	url := "https://celestrak.org/SpaceData/SW-All.csv"
	p := newCelestrakPolicy(cfg)
	if skip, _ := p.reserve(url); skip {
		t.Fatal("first fetch was skipped; the ledger must be empty initially")
	}

	// 2h later: still within the 3h window -> skip.
	clock.now = base.Add(2 * time.Hour)
	if skip, reason := p.reserve(url); !skip {
		t.Fatalf("re-fetch at +2h was NOT skipped (reason=%q); the 3h ledger must refuse it", reason)
	}

	// A FRESH policy over the same dir (a daemon restart) still refuses at +2h:
	// the ledger is persistent.
	clock2 := &fakeClock{now: base.Add(2 * time.Hour)}
	p2 := newCelestrakPolicy(HTTPCapConfig{LedgerDir: dir, now: clock2.Now, sleep: func(time.Duration) {}})
	if skip, _ := p2.reserve(url); !skip {
		t.Fatal("persistent ledger did not survive a policy rebuild (restart): re-fetch at +2h must still be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, celestrakLedgerFile)); err != nil {
		t.Fatalf("ledger file was not persisted under LedgerDir: %v", err)
	}

	// >3h later: the window has passed -> fetch allowed again.
	clock.now = base.Add(3*time.Hour + time.Second)
	if skip, _ := p.reserve(url); skip {
		t.Fatal("fetch at +3h1s was skipped; the 3h window has elapsed and it must be allowed")
	}
}

// TestCelestrakPolicySpacingEnforced proves >=2.5s SERIAL spacing between two
// gated-host requests: a second fetch immediately after a first sleeps out the
// remainder of the 2.5s gap.
func TestCelestrakPolicySpacingEnforced(t *testing.T) {
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: base}
	var slept []time.Duration
	var mu sync.Mutex
	sleep := func(d time.Duration) {
		mu.Lock()
		slept = append(slept, d)
		clock.now = clock.now.Add(d) // a real sleep advances the clock
		mu.Unlock()
	}
	p := newCelestrakPolicy(HTTPCapConfig{now: clock.Now, sleep: sleep})

	// Two DIFFERENT celestrak URLs back-to-back (no wall time between them):
	// the ledger does not skip either, but spacing must insert a ~2.5s wait
	// before the second.
	if skip, _ := p.reserve("https://celestrak.org/NORAD/elements/gp.php?GROUP=active"); skip {
		t.Fatal("first gated fetch skipped unexpectedly")
	}
	if skip, _ := p.reserve("https://celestrak.org/SpaceData/SW-All.csv"); skip {
		t.Fatal("second gated fetch skipped unexpectedly (different URL, not in ledger)")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 1 {
		t.Fatalf("spacing sleeps = %d (%v), want exactly 1 before the second gated fetch", len(slept), slept)
	}
	if slept[0] < 2500*time.Millisecond || slept[0] > 2500*time.Millisecond {
		t.Fatalf("spacing wait = %v, want exactly 2.5s (full gap, no time elapsed between calls)", slept[0])
	}
}

// TestCelestrakPolicyIgnoresNonGatedHosts proves a local test stub / arbitrary
// host is NOT subject to the spacing + ledger courtesy (so the e2e flow test is
// unaffected).
func TestCelestrakPolicyIgnoresNonGatedHosts(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)}
	sleep := func(time.Duration) { t.Fatal("a non-gated host must not incur a spacing sleep") }
	p := newCelestrakPolicy(HTTPCapConfig{now: clock.Now, sleep: sleep})

	url := "http://127.0.0.1:54321/sw-all.csv"
	for i := 0; i < 3; i++ {
		if skip, _ := p.reserve(url); skip {
			t.Fatalf("non-gated host fetch %d was skipped; only celestrak/space-track are gated", i)
		}
	}
}

func TestIsGatedHost(t *testing.T) {
	gated := []string{
		"https://celestrak.org/SpaceData/SW-All.csv",
		"https://www.celestrak.org/NORAD/elements/gp.php",
		"https://celestrak.com/x",
		"https://www.space-track.org/basicspacedata/query",
	}
	for _, u := range gated {
		if !isGatedHost(u) {
			t.Errorf("expected %q to be gated", u)
		}
	}
	notGated := []string{
		"http://127.0.0.1:8080/x",
		"https://example.com/celestrak.org",
		"https://mirror.example.org/sw-all.csv",
	}
	for _, u := range notGated {
		if isGatedHost(u) {
			t.Errorf("expected %q NOT to be gated", u)
		}
	}
}

// fakeClock is a deterministic clock for the policy tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
