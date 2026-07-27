package auth

// Locks the owner directive of 2026-07-27: "it needs to accept the root account
// that the node is based on as the admin sign-in, no error message".

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// newRootHandler builds a handler over an EMPTY user store with a registered
// root identity, and returns the root private key.
func newRootHandler(t *testing.T, rootXPub string) (*Handler, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()

	slipPub, slipPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	legacyPub, legacyPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
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

	h := NewHandler(userStore, sessions, time.Hour, "", "")
	h.SetNodeRootIdentity(&RootIdentity{
		XPub:        rootXPub,
		Name:        "Node Root",
		SigningKeys: []ed25519.PublicKey{slipPub, legacyPub},
	})
	return h, slipPriv, legacyPriv
}

// signIn drives challenge+verify with the given key, optionally sending an
// xpub, and returns the verify recorder.
func signIn(t *testing.T, h *Handler, priv ed25519.PrivateKey, sendXPub string) *httptest.ResponseRecorder {
	t.Helper()

	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	cbody := `{"client_pubkey_hex":"` + pubHex + `","ts":` + itoa(time.Now().Unix())
	if sendXPub != "" {
		cbody += `,"xpub":"` + sendXPub + `"`
	}
	cbody += `}`

	crec := httptest.NewRecorder()
	h.handleChallenge(crec, httptest.NewRequest(http.MethodPost, "/api/auth/challenge", strings.NewReader(cbody)))
	if crec.Code != http.StatusOK {
		t.Fatalf("challenge status = %d: %s", crec.Code, crec.Body.String())
	}
	var ch struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(crec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		t.Fatalf("decode challenge bytes: %v", err)
	}

	vbody := `{"challenge_id":"` + ch.ChallengeID + `","client_pubkey_hex":"` + pubHex +
		`","challenge":"` + ch.Challenge + `","signature_hex":"` +
		hex.EncodeToString(ed25519.Sign(priv, raw)) + `"}`
	vrec := httptest.NewRecorder()
	h.handleVerify(vrec, httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(vbody)))
	return vrec
}

// TestRootAccountSignsInAsAdminWithNoEnrolment is the owner directive verbatim:
// the root account is admitted as admin, with no error, against an EMPTY user
// store and with no config seeding.
func TestRootAccountSignsInAsAdminWithNoEnrolment(t *testing.T) {
	t.Parallel()

	const rootXPub = "xpub-node-root-account"

	for _, tc := range []struct {
		name      string
		useLegacy bool
	}{
		{"slip10 / v2 key", false},
		{"legacy raw-challenge key", true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, slipPriv, legacyPriv := newRootHandler(t, rootXPub)
			priv := slipPriv
			if tc.useLegacy {
				priv = legacyPriv
			}

			rec := signIn(t, h, priv, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("root sign-in status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var got struct {
				User struct {
					XPubFingerprint string `json:"xpub_fingerprint"`
					Name            string `json:"name"`
					TrustLevel      string `json:"trust_level"`
				} `json:"user"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode verify: %v", err)
			}
			if got.User.TrustLevel != peers.Admin.String() {
				t.Fatalf("trust_level = %q, want %q", got.User.TrustLevel, peers.Admin.String())
			}
			if got.User.XPubFingerprint != XPubFingerprint(rootXPub) {
				t.Fatalf("session identity is not the node's own xpub")
			}
			var sessionCookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == "sdn_wallet_session" {
					sessionCookie = c
				}
			}
			if sessionCookie == nil {
				t.Fatal("root sign-in issued no session cookie")
			}
		})
	}
}

// TestRootSignInIgnoresClientSuppliedXPub is the §13.1 guarantee carried
// forward: even when the wallet sends its MASTER xpub, the session is issued
// for the node's OWN xpub and the master never reaches the store.
func TestRootSignInIgnoresClientSuppliedXPub(t *testing.T) {
	t.Parallel()

	const rootXPub = "xpub-node-root-account"
	h, slipPriv, _ := newRootHandler(t, rootXPub)

	masterXPub := makeXPub(t, 0)
	rec := signIn(t, h, slipPriv, masterXPub)
	if rec.Code != http.StatusOK {
		t.Fatalf("root sign-in with a master xpub status = %d, want %d: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var got struct {
		User struct {
			XPubFingerprint string `json:"xpub_fingerprint"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.User.XPubFingerprint != XPubFingerprint(rootXPub) {
		t.Fatal("session was issued for the client-supplied xpub, not the node's own")
	}

	users, err := h.userStore.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range users {
		if u.XPub == masterXPub {
			t.Fatal("the client-supplied MASTER xpub was written into the trust store")
		}
	}
}

// TestRootSignInIsRecordedInTheTrustMatrix locks the owner's second sentence:
// sign-ins are stored the same way peers are, with PRR's CONNECTION_COUNT and
// LAST_CONNECTED advancing per sign-in.
func TestRootSignInIsRecordedInTheTrustMatrix(t *testing.T) {
	t.Parallel()

	const rootXPub = "xpub-node-root-account"
	h, slipPriv, _ := newRootHandler(t, rootXPub)

	for i := 1; i <= 2; i++ {
		if rec := signIn(t, h, slipPriv, ""); rec.Code != http.StatusOK {
			t.Fatalf("sign-in %d status = %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	entries, err := h.userStore.TrustMatrix()
	if err != nil {
		t.Fatalf("TrustMatrix: %v", err)
	}
	var found *TrustMatrixEntry
	for i := range entries {
		if entries[i].XPub == rootXPub {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("root sign-in was not recorded in the trust matrix: %+v", entries)
	}
	if found.TrustLevel != peers.Admin {
		t.Fatalf("recorded trust level = %s, want %s", found.TrustLevel, peers.Admin)
	}
	if found.ConnectionCount != 2 {
		t.Fatalf("connection_count = %d, want 2 (one per sign-in)", found.ConnectionCount)
	}
	if found.LastConnected == 0 {
		t.Fatal("last_connected was not recorded")
	}
}

// TestNonRootKeyIsUnaffected locks that the carve-out is exactly that: a key
// that is not the node's own still gets the opaque failure, and §13.1's
// master-xpub bootstrap refusal still applies.
func TestNonRootKeyIsUnaffected(t *testing.T) {
	t.Parallel()

	h, _, _ := newRootHandler(t, "xpub-node-root-account")

	_, strangerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Unknown key, no xpub: opaque failure, no session.
	if rec := signIn(t, h, strangerPriv, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger sign-in status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	// Unknown key presenting a master xpub: still refused (§13.1 intact).
	if rec := signIn(t, h, strangerPriv, makeXPub(t, 0)); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger master-xpub bootstrap status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if h.userStore.HasAdmin() {
		t.Fatal("a stranger created an admin entry")
	}
}

// TestRootIdentityRegistrationIsFailClosed locks that a malformed or empty
// registration disables root sign-in rather than widening the match set.
func TestRootIdentityRegistrationIsFailClosed(t *testing.T) {
	t.Parallel()

	h, _, _ := newRootHandler(t, "xpub-node-root-account")

	if h.NodeRootIdentity() == nil {
		t.Fatal("fixture did not register a root identity")
	}
	if h.isNodeRootSigningKey(make([]byte, ed25519.PublicKeySize)) {
		t.Fatal("an all-zero key matched the root identity")
	}
	if h.isNodeRootSigningKey(nil) || h.isNodeRootSigningKey([]byte{1, 2, 3}) {
		t.Fatal("a malformed key matched the root identity")
	}

	// No xpub -> disabled.
	h.SetNodeRootIdentity(&RootIdentity{XPub: "  "})
	if h.NodeRootIdentity() != nil {
		t.Fatal("an identity with no xpub was registered")
	}
	// No usable keys -> disabled.
	h.SetNodeRootIdentity(&RootIdentity{XPub: "xpub-x", SigningKeys: []ed25519.PublicKey{{1, 2, 3}}})
	if h.NodeRootIdentity() != nil {
		t.Fatal("an identity with no usable keys was registered")
	}
	// nil -> cleared.
	h.SetNodeRootIdentity(nil)
	if h.NodeRootIdentity() != nil {
		t.Fatal("nil did not clear the root identity")
	}
}

// TestAuthStatusReportsRootAdminAvailability locks the admin_configured change
// Iris depends on: with a root identity there is ALWAYS an admin sign-in path,
// even with an empty user store.
func TestAuthStatusReportsRootAdminAvailability(t *testing.T) {
	t.Parallel()

	h, _, _ := newRootHandler(t, "xpub-node-root-account")

	rec := httptest.NewRecorder()
	h.handleAuthStatus(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		AdminConfigured    bool `json:"admin_configured"`
		RootAdminAvailable bool `json:"root_admin_available"`
		UsersConfigured    bool `json:"users_configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.RootAdminAvailable {
		t.Fatal("root_admin_available = false with a registered root identity")
	}
	if !body.AdminConfigured {
		t.Fatal("admin_configured = false although root can sign in as admin")
	}
	if body.UsersConfigured {
		t.Fatal("users_configured = true with an empty user store")
	}

	// Without a root identity the old semantics hold.
	h.SetNodeRootIdentity(nil)
	rec2 := httptest.NewRecorder()
	h.handleAuthStatus(rec2, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	var body2 struct {
		AdminConfigured    bool `json:"admin_configured"`
		RootAdminAvailable bool `json:"root_admin_available"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body2.RootAdminAvailable || body2.AdminConfigured {
		t.Fatal("root availability leaked after the identity was cleared")
	}
}
