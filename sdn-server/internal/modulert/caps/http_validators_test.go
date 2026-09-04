package caps

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ETag / Last-Modified gating: the second GET for a URL presents the
// validators the first 2xx carried, and a 304 comes back as status 304 with an
// empty body rather than as an error.

type memoryValidators struct {
	mu   sync.Mutex
	rows map[string][2]string
}

func (m *memoryValidators) source(url string) (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[url]
	return row[0], row[1]
}

func (m *memoryValidators) observe(url, etag, lastModified string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string][2]string{}
	}
	m.rows[url] = [2]string{etag, lastModified}
}

func TestFetchValidatorsArePresentedAndA304IsNotAnError(t *testing.T) {
	ledger := &memoryValidators{}
	SetFetchValidatorSource(ledger.source)
	SetFetchValidatorObserver(ledger.observe)
	t.Cleanup(func() {
		SetFetchValidatorSource(nil)
		SetFetchValidatorObserver(nil)
	})

	const etag = `"sw-all-20260903"`
	const lastModified = "Wed, 03 Sep 2026 09:00:00 GMT"
	var seenIfNoneMatch, seenIfModifiedSince []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenIfNoneMatch = append(seenIfNoneMatch, r.Header.Get("If-None-Match"))
		seenIfModifiedSince = append(seenIfModifiedSince, r.Header.Get("If-Modified-Since"))
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", lastModified)
		w.Write([]byte("DATE,BSRN\n2026-09-03,2600\n")) //nolint:errcheck
	}))
	defer server.Close()
	url := server.URL + "/SpaceData/SW-All.csv"

	// First pull: no validators to present, full body, validators recorded.
	first := httpCapCall(t, map[string]interface{}{"url": url})
	if ok, _ := first["ok"].(bool); !ok {
		t.Fatalf("first fetch failed: %v", first)
	}
	result := first["result"].(map[string]interface{})
	if status, _ := result["status"].(float64); int(status) != http.StatusOK {
		t.Fatalf("first status = %v", result["status"])
	}
	if body, _ := result["body"].(string); body == "" {
		t.Fatal("first fetch delivered no body")
	}
	if seenIfNoneMatch[0] != "" || seenIfModifiedSince[0] != "" {
		t.Fatalf("first request presented validators it could not have: %q %q", seenIfNoneMatch[0], seenIfModifiedSince[0])
	}
	if gotEtag, gotLM := ledger.source(url); gotEtag != etag || gotLM != lastModified {
		t.Fatalf("observer recorded %q %q", gotEtag, gotLM)
	}

	// Second pull: validators presented, publisher answers 304, no error.
	second := httpCapCall(t, map[string]interface{}{"url": url})
	if ok, _ := second["ok"].(bool); !ok {
		t.Fatalf("304 was surfaced as an error: %v", second)
	}
	result = second["result"].(map[string]interface{})
	if status, _ := result["status"].(float64); int(status) != http.StatusNotModified {
		t.Fatalf("second status = %v, want 304", result["status"])
	}
	if body, _ := result["body"].(string); body != "" {
		t.Fatalf("304 delivered a body: %q", body)
	}
	if enc, _ := result["body_encoding"].(string); enc != "utf8" {
		t.Fatalf("304 body_encoding = %q", enc)
	}
	if seenIfNoneMatch[1] != etag || seenIfModifiedSince[1] != lastModified {
		t.Fatalf("second request presented %q %q", seenIfNoneMatch[1], seenIfModifiedSince[1])
	}
	// The 304 must not erase the validators.
	if gotEtag, _ := ledger.source(url); gotEtag != etag {
		t.Fatalf("304 erased the ETag: %q", gotEtag)
	}

	// A module that sets its own validator keeps it verbatim.
	third := httpCapCall(t, map[string]interface{}{
		"url":     url,
		"headers": map[string]string{"If-None-Match": `"module-owned"`},
	})
	if ok, _ := third["ok"].(bool); !ok {
		t.Fatalf("third fetch failed: %v", third)
	}
	if seenIfNoneMatch[2] != `"module-owned"` {
		t.Fatalf("host overrode the module's If-None-Match: %q", seenIfNoneMatch[2])
	}
	if seenIfModifiedSince[2] != "" {
		t.Fatalf("host added If-Modified-Since beside a module-owned validator: %q", seenIfModifiedSince[2])
	}
}

func TestFetchValidatorsIgnoreNonGetAndUnknownURLs(t *testing.T) {
	ledger := &memoryValidators{}
	ledger.observe("http://example.invalid/x", `"e"`, "")
	SetFetchValidatorSource(ledger.source)
	t.Cleanup(func() { SetFetchValidatorSource(nil) })

	req, _ := http.NewRequest(http.MethodPost, "http://example.invalid/x", nil)
	applyFetchValidators(req)
	if req.Header.Get("If-None-Match") != "" {
		t.Fatal("validators applied to a POST")
	}
	req, _ = http.NewRequest(http.MethodGet, "http://example.invalid/never-fetched", nil)
	applyFetchValidators(req)
	if req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" {
		t.Fatal("validators invented for an unknown URL")
	}
	req, _ = http.NewRequest(http.MethodGet, "http://example.invalid/x", nil)
	applyFetchValidators(req)
	if req.Header.Get("If-None-Match") != `"e"` || req.Header.Get("If-Modified-Since") != "" {
		t.Fatalf("GET validators = %q %q", req.Header.Get("If-None-Match"), req.Header.Get("If-Modified-Since"))
	}
}
