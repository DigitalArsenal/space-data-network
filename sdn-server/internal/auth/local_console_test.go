package auth

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func newLocalConsoleHandler(t *testing.T, withRoot bool) *Handler {
	t.Helper()
	h := newDevAutoAdminHandler(t, nil)
	if withRoot {
		pub, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		h.SetNodeRootIdentity(&RootIdentity{XPub: "xpub-node-root", SigningKeys: []ed25519.PublicKey{pub}})
	}
	return h
}

func loopbackRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	return r
}

func TestLocalConsole_LoopbackIsNodeRoot(t *testing.T) {
	h := newLocalConsoleHandler(t, true)
	h.EnableLocalConsole()

	session, err := h.sessionFromRequest(loopbackRequest())
	if err != nil {
		t.Fatalf("sessionFromRequest: %v", err)
	}
	if session.XPub != h.rootSessionXPub() {
		t.Fatalf("session xpub: got %q want the node root %q", session.XPub, h.rootSessionXPub())
	}
	if session.TrustLevel < peers.Admin {
		t.Fatalf("session trust: got %v want >= Admin", session.TrustLevel)
	}
	again, err := h.sessionFromRequest(loopbackRequest())
	if err != nil || again.Token != session.Token {
		t.Fatalf("expected the standing local-console session to be reused")
	}
}

func TestLocalConsole_OffByDefault(t *testing.T) {
	h := newLocalConsoleHandler(t, true)
	if _, err := h.sessionFromRequest(loopbackRequest()); err == nil {
		t.Fatalf("admin.local_console off: a loopback caller must not be admitted")
	}
}

func TestLocalConsole_ProxyHopRefused(t *testing.T) {
	h := newLocalConsoleHandler(t, true)
	h.EnableLocalConsole()
	for _, header := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		r := loopbackRequest()
		r.Header.Set(header, "203.0.113.9")
		if _, err := h.sessionFromRequest(r); err == nil {
			t.Fatalf("%s present: a proxied request must not be admitted", header)
		}
	}
}

func TestLocalConsole_RemoteRefused(t *testing.T) {
	h := newLocalConsoleHandler(t, true)
	h.EnableLocalConsole()
	r := loopbackRequest()
	r.RemoteAddr = "203.0.113.9:4444"
	if _, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("a non-loopback caller must not be admitted")
	}
}

func TestLocalConsole_NoRootIdentityNoSession(t *testing.T) {
	h := newLocalConsoleHandler(t, false)
	h.EnableLocalConsole()
	if _, err := h.sessionFromRequest(loopbackRequest()); err == nil {
		t.Fatalf("no root identity: nothing to admit")
	}
}
