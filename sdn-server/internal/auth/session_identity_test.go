package auth

// Locks that /api/auth/verify and /api/auth/me report WHICH identity the caller
// is (graph task nst-node-admin-contract, integration report item 3). Without an
// identifier, a UI that reloads the page can prove it holds a session but cannot
// say whose — and a client that authenticated by signing key alone never learns
// the identity the node resolved for it.
//
// The identifier is a FINGERPRINT, never the xpub itself: TestAuth_Me_
// DoesNotExposeXPub and the two verify tests lock the raw extended public key
// out of these bodies, and that lock is deliberately preserved (see the
// authSessionUser doc comment for the threat model).

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

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const sessionIdentityXPub = "xpub-session-identity-test"

// newSessionIdentityHandler builds a handler over a real user + session store
// holding one admin whose signing key is the returned private key.
func newSessionIdentityHandler(t *testing.T) (*Handler, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{{
		XPub:             sessionIdentityXPub,
		Name:             "Session Identity Admin",
		TrustLevel:       "admin",
		SigningPubKeyHex: hex.EncodeToString(pub),
	}})
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

	return NewHandler(userStore, sessions, time.Hour, "", ""), priv
}

// TestVerifyAndMeReportTheAuthenticatedIdentity drives a full challenge/verify
// sign-in and then reads /api/auth/me with the issued cookie, asserting both
// bodies name the identity by fingerprint and that neither ever contains the
// raw xpub. The challenge carries NO xpub, so this also locks that a
// signing-key-only sign-in learns which identity it became.
func TestVerifyAndMeReportTheAuthenticatedIdentity(t *testing.T) {
	t.Parallel()

	handler, priv := newSessionIdentityHandler(t)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))

	// Step 1: challenge, resolved by signing key alone.
	challengeBody := `{"client_pubkey_hex":"` + pubHex + `","ts":` + itoa(time.Now().Unix()) + `}`
	challengeReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", strings.NewReader(challengeBody))
	challengeRec := httptest.NewRecorder()
	handler.handleChallenge(challengeRec, challengeReq)
	if challengeRec.Code != http.StatusOK {
		t.Fatalf("challenge status = %d: %s", challengeRec.Code, challengeRec.Body.String())
	}
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(challengeRec.Body.Bytes(), &challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	// Step 2: sign the raw challenge bytes.
	raw, err := base64.RawStdEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		t.Fatalf("challenge is not RawStdEncoding base64: %v", err)
	}
	sigHex := hex.EncodeToString(ed25519.Sign(priv, raw))

	// Step 3: verify.
	verifyBody := `{"challenge_id":"` + challenge.ChallengeID +
		`","client_pubkey_hex":"` + pubHex +
		`","challenge":"` + challenge.Challenge +
		`","signature_hex":"` + sigHex + `"}`
	verifyReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(verifyBody))
	verifyRec := httptest.NewRecorder()
	handler.handleVerify(verifyRec, verifyReq)
	if verifyRec.Code != http.StatusOK {
		t.Fatalf("verify status = %d: %s", verifyRec.Code, verifyRec.Body.String())
	}

	wantFingerprint := XPubFingerprint(sessionIdentityXPub)
	if len(wantFingerprint) != 16 {
		t.Fatalf("fingerprint length = %d, want 16 hex chars", len(wantFingerprint))
	}

	var verified struct {
		User struct {
			XPubFingerprint string `json:"xpub_fingerprint"`
			Name            string `json:"name"`
			TrustLevel      string `json:"trust_level"`
		} `json:"user"`
	}
	if err := json.Unmarshal(verifyRec.Body.Bytes(), &verified); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	if verified.User.XPubFingerprint != wantFingerprint {
		t.Fatalf("verify user.xpub_fingerprint = %q, want %q", verified.User.XPubFingerprint, wantFingerprint)
	}
	if strings.Contains(verifyRec.Body.String(), sessionIdentityXPub) {
		t.Fatalf("verify body leaked the raw xpub: %s", verifyRec.Body.String())
	}
	if verified.User.TrustLevel != "admin" {
		t.Fatalf("verify user.trust_level = %q, want %q", verified.User.TrustLevel, "admin")
	}

	// Step 4: /api/auth/me with the issued session cookie.
	var sessionCookie *http.Cookie
	for _, c := range verifyRec.Result().Cookies() {
		if c.Name == "sdn_wallet_session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("verify did not issue a session cookie")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meReq.AddCookie(sessionCookie)
	meRec := httptest.NewRecorder()
	handler.handleMe(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me status = %d: %s", meRec.Code, meRec.Body.String())
	}

	var me struct {
		XPubFingerprint string `json:"xpub_fingerprint"`
		Name            string `json:"name"`
		TrustLevel      string `json:"trust_level"`
	}
	if err := json.Unmarshal(meRec.Body.Bytes(), &me); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if me.XPubFingerprint != wantFingerprint {
		t.Fatalf("me xpub_fingerprint = %q, want %q", me.XPubFingerprint, wantFingerprint)
	}
	if strings.Contains(meRec.Body.String(), sessionIdentityXPub) {
		t.Fatalf("me body leaked the raw xpub: %s", meRec.Body.String())
	}
	if me.Name != "Session Identity Admin" {
		t.Fatalf("me name = %q", me.Name)
	}
	if me.TrustLevel != "admin" {
		t.Fatalf("me trust_level = %q, want %q", me.TrustLevel, "admin")
	}
}

// TestMeReportsTheFingerprintAtEveryTrustTier locks that the identity field is
// present regardless of tier — the low-tier "signed in, insufficient
// permissions" state is exactly the one a UI must be able to name.
func TestMeReportsTheFingerprintAtEveryTrustTier(t *testing.T) {
	t.Parallel()

	for _, trust := range []peers.TrustLevel{peers.Unknown, peers.Marginal, peers.Standard, peers.Trusted, peers.Admin} {
		trust := trust
		t.Run(trust.String(), func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			xpub := "xpub-tier-" + trust.String()
			userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{{
				XPub:       xpub,
				Name:       "Tier User",
				TrustLevel: trust.String(),
			}})
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
			token, err := sessions.CreateSession(xpub, trust, "127.0.0.1", "test-agent", time.Hour)
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			handler := NewHandler(userStore, sessions, time.Hour, "", "")
			req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
			rec := httptest.NewRecorder()
			handler.handleMe(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var me struct {
				XPubFingerprint string `json:"xpub_fingerprint"`
				TrustLevel      string `json:"trust_level"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if me.XPubFingerprint != XPubFingerprint(xpub) {
				t.Fatalf("xpub_fingerprint = %q, want %q", me.XPubFingerprint, XPubFingerprint(xpub))
			}
			if strings.Contains(rec.Body.String(), xpub) {
				t.Fatalf("me body leaked the raw xpub: %s", rec.Body.String())
			}
			if me.TrustLevel != trust.String() {
				t.Fatalf("trust_level = %q, want %q", me.TrustLevel, trust.String())
			}
		})
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
