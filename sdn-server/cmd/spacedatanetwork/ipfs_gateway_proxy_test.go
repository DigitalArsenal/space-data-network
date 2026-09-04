package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestIPFSGatewayProxyServesOnlyHeldContent: the proxy asks Kubo for cached
// content only, and a CID this node does not hold answers 404 instead of a
// network fetch on behalf of the caller.
func TestIPFSGatewayProxyServesOnlyHeldContent(t *testing.T) {
	var sawCacheControl, sawOrigin string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCacheControl = r.Header.Get("Cache-Control")
		sawOrigin = r.Header.Get("Origin")
		if r.Header.Get("Cache-Control") != onlyIfCached {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/ipfs/bafyheld":
			_, _ = w.Write([]byte("held bytes"))
		default:
			w.WriteHeader(http.StatusPreconditionFailed)
		}
	}))
	defer upstream.Close()

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	proxy := newIPFSGatewayProxy(target)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ipfs/bafyheld", nil)
	req.Header.Set("Origin", "https://evil.example")
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "held bytes" {
		t.Fatalf("held content: status %d body %q", rec.Code, rec.Body.String())
	}
	if sawCacheControl != onlyIfCached {
		t.Fatalf("upstream saw Cache-Control %q, want %q", sawCacheControl, onlyIfCached)
	}
	if sawOrigin != "" {
		t.Fatalf("upstream saw Origin %q, want it stripped", sawOrigin)
	}

	rec = httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ipfs/bafynotheld/some/path.png", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("content this node does not hold: status %d, want 404", rec.Code)
	}
	if rec.Header().Get("X-SDN-IPFS") != "not-held" {
		t.Fatalf("missing not-held marker on 404")
	}
}
