package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/accessgate"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sealed"
)

// The whole server is locked (owner order 2026-09-22): only the access gate's
// public set is served anonymously, on every path, not only /api/; a remote
// admin must use the sealed transport; loopback keeps working.
func TestWholeServerLock(t *testing.T) {
	authHandler, token := newAdminSession(t, peers.Admin)
	gate, err := accessgate.New(accessgate.DefaultPublic, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	serve := func(method, path, remote string, withSession bool, mutate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = remote
		req.Header.Set("Accept", "application/json")
		if withSession {
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
		}
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		serveAdminMuxRequest(rec, req, mux, true, false, authHandler, gate.Public, nil, true)
		return rec
	}
	const remote, local = "203.0.113.9:5000", "127.0.0.1:5000"

	// Public set: served to anyone.
	for _, path := range []string{"/", "/ipfs/bafy", "/api/node/info", "/updates/feed.json", "/wallet-ui/x.js"} {
		if rec := serve("GET", path, remote, false, nil); rec.Code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d, want 200", path, rec.Code)
		}
	}
	// Everything else is locked, API or not.
	for _, path := range []string{"/docs/", "/api/v1/stats", "/embedding/x", "/identity/", "/api/v0/cat"} {
		if rec := serve("GET", path, remote, false, nil); rec.Code == http.StatusOK {
			t.Errorf("anonymous GET %s = 200, want locked", path)
		}
	}
	// An admin's cookie from another machine is refused: sealed transport only.
	rec := serve("GET", "/api/v1/stats", remote, true, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "sealed_required") {
		t.Fatalf("remote cookie admin = %d %s, want 403 sealed_required", rec.Code, rec.Body.String())
	}
	// ...and a loopback request that was relayed by a proxy counts as remote.
	rec = serve("GET", "/api/v1/stats", local, true, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "198.51.100.1") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("proxied cookie admin = %d, want 403", rec.Code)
	}
	// This box's own admin (CLI, desktop app) is unaffected.
	if rec := serve("GET", "/api/v1/stats", local, true, nil); rec.Code != http.StatusOK {
		t.Fatalf("loopback admin = %d, want 200", rec.Code)
	}
	// Session introspection answers a remote cookie session (any tier).
	if rec := serve("GET", "/api/auth/me", remote, true, nil); rec.Code == http.StatusForbidden {
		t.Fatalf("remote /api/auth/me = %d", rec.Code)
	}
	// A sealed command's unwrapped request runs as its admin.
	sealedReq := func(r *http.Request) {
		*r = *r.WithContext(sealed.AdmittedContext(r.Context(), &auth.Session{XPub: "xpub-a", TrustLevel: peers.Admin}))
	}
	if rec := serve("GET", "/api/v1/stats", remote, false, sealedReq); rec.Code != http.StatusOK {
		t.Fatalf("sealed admin = %d %s, want 200", rec.Code, rec.Body.String())
	}
	// The sealed route itself answers 503 when the transport is not wired.
	if rec := serve("POST", "/api/rpc", remote, false, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api/rpc without a handler = %d, want 503", rec.Code)
	}
}
