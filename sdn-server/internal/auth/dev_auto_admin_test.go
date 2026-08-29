package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func newDevAutoAdminHandler(t *testing.T, entries []config.UserEntry) *Handler {
	t.Helper()
	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), entries)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { userStore.Close() })

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { closer() })

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	return NewHandler(userStore, sessions, 24*time.Hour, "", "")
}

func adminEntry() []config.UserEntry {
	return []config.UserEntry{{
		XPub:       "xpub-dev-admin",
		TrustLevel: "admin",
		Name:       "Dev Admin",
	}}
}

func TestDevAutoAdmin_LoopbackResolvesAdminSession(t *testing.T) {
	h := newDevAutoAdminHandler(t, adminEntry())
	h.EnableDevAutoAdmin()

	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	session, err := h.sessionFromRequest(r)
	if err != nil {
		t.Fatalf("sessionFromRequest: %v", err)
	}
	if session.XPub != "xpub-dev-admin" {
		t.Fatalf("session xpub: got %q", session.XPub)
	}
	if session.TrustLevel < peers.Admin {
		t.Fatalf("session trust: got %v want >= Admin", session.TrustLevel)
	}

	// Second request reuses the minted session instead of minting again.
	r2 := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r2.RemoteAddr = "[::1]:54321"
	session2, err := h.sessionFromRequest(r2)
	if err != nil {
		t.Fatalf("second sessionFromRequest: %v", err)
	}
	if session2.Token != session.Token {
		t.Fatalf("expected cached dev session to be reused")
	}
}

func TestDevAutoAdmin_RemoteAddrStaysUnauthenticated(t *testing.T) {
	h := newDevAutoAdminHandler(t, adminEntry())
	h.EnableDevAutoAdmin()

	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "203.0.113.9:4444"
	if _, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("non-loopback caller must not be auto-admitted")
	}
}

func TestDevAutoAdmin_OffByDefault(t *testing.T) {
	h := newDevAutoAdminHandler(t, adminEntry())

	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	if _, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("flag off: loopback caller must not be auto-admitted")
	}
}

func TestDevAutoAdmin_NoAdminUserNoSession(t *testing.T) {
	h := newDevAutoAdminHandler(t, []config.UserEntry{{
		XPub:       "xpub-standard",
		TrustLevel: "standard",
		Name:       "Standard User",
	}})
	h.EnableDevAutoAdmin()

	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	if _, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("no admin user: loopback caller must not be auto-admitted")
	}
}
