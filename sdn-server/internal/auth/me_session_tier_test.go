package auth

// Locks graph task sdn-auth-me-tier-mismatch: /api/auth/me must report the
// SESSION's trust level — the value every RequireTrust gate enforces — never
// the user-store row's, and a valid session whose xpub has no store row is
// still authenticated (the root ceremony mints exactly such sessions).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func newMeTierHandler(t *testing.T, users []config.UserEntry) *Handler {
	t.Helper()

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), users)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	return NewHandler(userStore, sessions, time.Hour, "", "")
}

func meWithSession(t *testing.T, h *Handler, xpub string, trust peers.TrustLevel) *httptest.ResponseRecorder {
	t.Helper()

	token, err := h.sessions.CreateSession(xpub, trust, "127.0.0.1", "test", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)
	return rec
}

func TestMeReportsSessionTierNotRowTier(t *testing.T) {
	t.Parallel()

	const xpub = "xpub-me-tier-mismatch-test"
	h := newMeTierHandler(t, []config.UserEntry{{
		XPub:       xpub,
		Name:       "Config Row Standard",
		TrustLevel: "standard",
	}})

	rec := meWithSession(t, h, xpub, peers.Admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status = %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		XPubFingerprint string `json:"xpub_fingerprint"`
		Name            string `json:"name"`
		TrustLevel      string `json:"trust_level"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if body.TrustLevel != peers.Admin.String() {
		t.Fatalf("me trust_level = %q, want the session's %q (row says standard; gates enforce the session)", body.TrustLevel, peers.Admin.String())
	}
	if body.Name != "Config Row Standard" {
		t.Fatalf("me name = %q, want the row's display name", body.Name)
	}
	if body.XPubFingerprint != XPubFingerprint(xpub) {
		t.Fatalf("me fingerprint = %q, want %q", body.XPubFingerprint, XPubFingerprint(xpub))
	}
}

func TestMeAcceptsSessionWithoutStoreRow(t *testing.T) {
	t.Parallel()

	const xpub = "xpub-me-rowless-session-test"
	h := newMeTierHandler(t, nil)

	rec := meWithSession(t, h, xpub, peers.Admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status = %d, want 200 for a valid rowless session (root ceremony): %s", rec.Code, rec.Body.String())
	}

	var body struct {
		XPubFingerprint string `json:"xpub_fingerprint"`
		Name            string `json:"name"`
		TrustLevel      string `json:"trust_level"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if body.TrustLevel != peers.Admin.String() {
		t.Fatalf("me trust_level = %q, want %q", body.TrustLevel, peers.Admin.String())
	}
	if body.XPubFingerprint != XPubFingerprint(xpub) {
		t.Fatalf("me fingerprint = %q, want %q", body.XPubFingerprint, XPubFingerprint(xpub))
	}
	if body.Name != "" {
		t.Fatalf("me name = %q, want empty for a rowless session", body.Name)
	}
}
