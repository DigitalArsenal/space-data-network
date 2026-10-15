package auth

// Round-2 regression test for the 2026-10-15 security audit (verifier
// follow-up on SEC-029): it needs the console secret, which origin/main lacks.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// The cached dev_auto_admin session skipped SEC-029's user-store re-check: a
// removed dev admin stayed admitted from the cache for the session lifetime.
func TestR2_DevAutoAdminCacheIsReconciled(t *testing.T) {
	h, users, _, _ := newRegressionHandler(t, nil, 24*time.Hour)
	if err := users.AddUser("xpub-dev-admin", "Dev", peers.Admin, strings.Repeat("ab", 32)); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	// A second admin, so the first may be removed.
	if err := users.AddUser("xpub-zz-other-admin", "Other", peers.Admin, strings.Repeat("cd", 32)); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	h.EnableDevAutoAdmin()
	if err := h.SetConsoleListenAddr("127.0.0.1:5001"); err != nil {
		t.Fatalf("SetConsoleListenAddr: %v", err)
	}
	token, err := GenerateConsoleToken()
	if err != nil {
		t.Fatalf("GenerateConsoleToken: %v", err)
	}
	if err := h.SetConsoleToken(token); err != nil {
		t.Fatalf("SetConsoleToken: %v", err)
	}
	req := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Host = "127.0.0.1:5001"
		r.Header.Set(ConsoleTokenHeader, token)
		return r
	}
	if s, err := h.sessionFromRequest(req()); err != nil || s.XPub != "xpub-dev-admin" {
		t.Fatalf("dev console with the secret was not admitted: %v %v", s, err)
	}
	if err := users.RemoveUser("xpub-dev-admin"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	// The cache must not hand back the removed account; dev_auto_admin may
	// pick the remaining admin afresh.
	if s, err := h.sessionFromRequest(req()); err == nil && s.XPub == "xpub-dev-admin" {
		t.Fatalf("removed dev admin still admitted from the cached session (%v)", s.TrustLevel)
	}
}
