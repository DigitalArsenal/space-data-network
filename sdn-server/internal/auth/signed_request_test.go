package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

const credentialRoute = "/api/v1/cellular/credentials/opencellid"

type signedRequestFixture struct {
	handler   *Handler
	userStore *UserStore
	xpub      string
	pub       ed25519.PublicKey
	priv      ed25519.PrivateKey
}

func newSignedRequestFixture(t *testing.T, trust peers.TrustLevel, bindSigningKey bool) *signedRequestFixture {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
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

	xpub := "xpub-operator-signed-request"
	signingHex := ""
	if bindSigningKey {
		signingHex = hex.EncodeToString(pub)
	}
	if err := userStore.AddUser(xpub, "Signed Request Operator", trust, signingHex); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	return &signedRequestFixture{
		handler:   NewHandler(userStore, sessions, time.Hour, "", ""),
		userStore: userStore,
		xpub:      xpub,
		pub:       pub,
		priv:      priv,
	}
}

// mintChallenge stores a pending challenge exactly as handleChallenge would for
// an enrolled operator, and returns its id and bytes.
func (f *signedRequestFixture) mintChallenge(t *testing.T) (string, []byte) {
	t.Helper()
	return f.mintChallengeWith(t, func(p *pendingChallenge) {})
}

func (f *signedRequestFixture) mintChallengeWith(t *testing.T, mutate func(*pendingChallenge)) (string, []byte) {
	t.Helper()

	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		t.Fatalf("rand: %v", err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	id := hex.EncodeToString(idBytes)

	now := time.Now().UTC()
	pending := pendingChallenge{
		id:        id,
		xpub:      f.xpub,
		pubKey:    append(ed25519.PublicKey(nil), f.pub...),
		challenge: challenge,
		createdAt: now,
		expiresAt: now.Add(time.Minute),
	}
	mutate(&pending)

	f.handler.mu.Lock()
	f.handler.challenges[id] = pending
	f.handler.mu.Unlock()
	return id, pending.challenge
}

// signRequest builds the Authorization header for one exact request.
func (f *signedRequestFixture) authorization(challengeID string, challenge []byte, method, requestURI string, body []byte) string {
	bodyDigest := sha256.Sum256(body)
	canonical := canonicalRequestString(method, requestURI, bodyDigest[:])
	signature := ed25519.Sign(f.priv, signedRequestMessage(challenge, canonical))
	return fmt.Sprintf(`%s challenge="%s", signature="%s"`, SignedRequestScheme, challengeID, hex.EncodeToString(signature))
}

func newSignedRequest(method, target string, body []byte, authorization string) *http.Request {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Origin", galleryOriginAuth)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return req
}

const galleryOriginAuth = "https://digitalarsenal.github.io"

// The acceptance the task names: a signed operator write reaches the handler.
func TestSignedRequestAdmitsAnEnrolledOperator(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{"slotId":"provider-wrapping","ciphertext":"..."}`)

	req := newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body))
	rec := httptest.NewRecorder()

	var seenBody []byte
	var seenSession *Session
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenSession = SessionFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if !bytes.Equal(seenBody, body) {
		t.Fatalf("handler saw body %q, want the client's bytes %q — the digest read must restore it", seenBody, body)
	}
	if seenSession == nil || seenSession.XPub != f.xpub {
		t.Fatalf("session = %+v, want the operator's xpub", seenSession)
	}
	if seenSession.Token != "" {
		t.Fatal("the ephemeral session carries a token; nothing here may be persisted or handed back")
	}
	if got := rec.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie = %q; signed-request admission must mint no cookie", got)
	}
}

// Single use, and burnt whether the signature verified or not — otherwise the
// header is an oracle to grind against.
func TestSignedRequestChallengeIsSingleUse(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{}`)
	authorization := f.authorization(id, challenge, http.MethodPut, credentialRoute, body)

	first := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(first, newSignedRequest(http.MethodPut, credentialRoute, body, authorization))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first attempt status = %d, want %d", first.Code, http.StatusNoContent)
	}

	replay := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a replayed signed request must not reach the handler")
	})(replay, newSignedRequest(http.MethodPut, credentialRoute, body, authorization))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want %d", replay.Code, http.StatusUnauthorized)
	}
}

func TestSignedRequestBurnsTheChallengeOnABadSignature(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{}`)

	bad := fmt.Sprintf(`%s challenge="%s", signature="%s"`, SignedRequestScheme, id, strings.Repeat("00", ed25519.SignatureSize))
	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
		t.Fatal("a bad signature must not reach the handler")
	})(rec, newSignedRequest(http.MethodPut, credentialRoute, body, bad))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	f.handler.mu.Lock()
	_, stillPending := f.handler.challenges[id]
	f.handler.mu.Unlock()
	if stillPending {
		t.Fatal("a failed signature left its challenge pending; the header becomes a grinding oracle")
	}

	// And the correct signature for that challenge is now worthless.
	rec2 := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
		t.Fatal("a burnt challenge must not admit anybody")
	})(rec2, newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body)))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec2.Code, http.StatusUnauthorized)
	}
}

// The signature authorises ONE method, ONE target and ONE body. Each of those
// is a separate way for a stolen header to be useless.
func TestSignedRequestSignatureIsBoundToTheRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		signedMethod string
		signedURI    string
		signedBody   []byte
		sentMethod   string
		sentURI      string
		sentBody     []byte
	}{
		{
			name:         "method swapped PUT to DELETE",
			signedMethod: http.MethodPut, signedURI: credentialRoute, signedBody: []byte(`{"a":1}`),
			sentMethod: http.MethodDelete, sentURI: credentialRoute, sentBody: []byte(`{"a":1}`),
		},
		{
			name:         "provider swapped",
			signedMethod: http.MethodPut, signedURI: credentialRoute, signedBody: []byte(`{"a":1}`),
			sentMethod: http.MethodPut, sentURI: "/api/v1/cellular/credentials/wigle", sentBody: []byte(`{"a":1}`),
		},
		{
			name:         "query string swapped",
			signedMethod: http.MethodPut, signedURI: credentialRoute + "?scope=one", signedBody: []byte(`{"a":1}`),
			sentMethod: http.MethodPut, sentURI: credentialRoute + "?scope=two", sentBody: []byte(`{"a":1}`),
		},
		{
			name:         "body swapped",
			signedMethod: http.MethodPut, signedURI: credentialRoute, signedBody: []byte(`{"a":1}`),
			sentMethod: http.MethodPut, sentURI: credentialRoute, sentBody: []byte(`{"a":2}`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSignedRequestFixture(t, peers.Standard, true)
			id, challenge := f.mintChallenge(t)
			authorization := f.authorization(id, challenge, tc.signedMethod, tc.signedURI, tc.signedBody)

			rec := httptest.NewRecorder()
			f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
				t.Fatalf("%s: a signature for a different request was accepted", tc.name)
			})(rec, newSignedRequest(tc.sentMethod, tc.sentURI, tc.sentBody, authorization))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// This mode is read-only with respect to identity: it can never enrol anybody
// and can never bind a signing key.
func TestSignedRequestRefusesUnboundAndBootstrapIdentities(t *testing.T) {
	t.Parallel()

	t.Run("operator with no bound signing key", func(t *testing.T) {
		f := newSignedRequestFixture(t, peers.Standard, false)
		id, challenge := f.mintChallenge(t)
		body := []byte(`{}`)
		rec := httptest.NewRecorder()
		f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
			t.Fatal("an operator with no bound signing key must not be admitted by a side channel")
		})(rec, newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}

		if user, err := f.userStore.GetUser(f.xpub); err != nil {
			t.Fatalf("GetUser: %v", err)
		} else if strings.TrimSpace(user.SigningPubKeyHex) != "" {
			t.Fatal("the refused attempt BOUND a signing key; this mode must never write identity")
		}
	})

	t.Run("first-admin bootstrap challenge", func(t *testing.T) {
		f := newSignedRequestFixture(t, peers.Standard, true)
		id, challenge := f.mintChallengeWith(t, func(p *pendingChallenge) { p.firstAdminBootstrap = true })
		body := []byte(`{}`)
		rec := httptest.NewRecorder()
		f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
			t.Fatal("a bootstrap challenge must go through the ordinary sign-in, never this side channel")
		})(rec, newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
}

// Trust tiers are unchanged: signed-request admission grants the operator's own
// level and not one step more.
func TestSignedRequestDoesNotEscalateTrust(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{}`)

	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Admin, func(http.ResponseWriter, *http.Request) {
		t.Fatal("a Standard operator must not clear an Admin gate")
	})(rec, newSignedRequest(http.MethodPut, "/api/v1/admin/thing", body, f.authorization(id, challenge, http.MethodPut, "/api/v1/admin/thing", body)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOriginAuth {
		t.Fatalf("Access-Control-Allow-Origin = %q — even the trust refusal must be readable", got)
	}
}

// An Authorization header belonging to some other scheme must not become an
// authentication FAILURE — it simply is not this mode.
func TestForeignAuthorizationSchemeIsNotThisMode(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
		t.Fatal("a Bearer header must not admit anybody")
	})(rec, newSignedRequest(http.MethodPut, credentialRoute, []byte(`{}`), "Bearer some-other-systems-token"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestParseSignedRequestAuthorization(t *testing.T) {
	t.Parallel()

	valid := fmt.Sprintf(`%s challenge="abc123", signature="%s"`, SignedRequestScheme, strings.Repeat("ab", ed25519.SignatureSize))
	creds, err := parseSignedRequestAuthorization(valid)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if creds.challengeID != "abc123" || len(creds.signature) != ed25519.SignatureSize {
		t.Fatalf("creds = %+v", creds)
	}

	for _, raw := range []string{"", "   ", "Bearer x", "SDN-Signed", `SDN-Signed challenge="abc"`, `SDN-Signed signature="00"`} {
		if _, err := parseSignedRequestAuthorization(raw); err == nil {
			t.Fatalf("parse(%q) succeeded, want an error", raw)
		}
	}
}

// The digest read happens before any auth check, so it must be bounded.
func TestSignedRequestRefusesAnOversizedBody(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := bytes.Repeat([]byte("x"), maxSignedRequestBody+1)

	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(http.ResponseWriter, *http.Request) {
		t.Fatal("an oversized signed request must not reach the handler")
	})(rec, newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// A gate composed underneath the wall must resolve the SAME session rather than
// re-evaluating the request — the single-use credential is spent by then.
func TestNestedTrustGateReusesTheAdmittedSession(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	id, challenge := f.mintChallenge(t)
	body := []byte(`{}`)

	rec := httptest.NewRecorder()
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		if !f.handler.RequireTrust(w, r, peers.Standard) {
			t.Fatal("the nested trust gate refused a caller the wall had already admitted")
		}
		w.WriteHeader(http.StatusNoContent)
	})(rec, newSignedRequest(http.MethodPut, credentialRoute, body, f.authorization(id, challenge, http.MethodPut, credentialRoute, body)))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// The cookie remains the transport for this node's own origin, unchanged.
func TestCookieSessionStillWins(t *testing.T) {
	t.Parallel()

	f := newSignedRequestFixture(t, peers.Standard, true)
	token, err := f.handler.sessions.CreateSession(f.xpub, peers.Standard, "127.0.0.1", "test-agent", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := newSignedRequest(http.MethodPut, credentialRoute, []byte(`{}`), "")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	admitted := false
	f.handler.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		admitted = true
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)

	if !admitted || rec.Code != http.StatusNoContent {
		t.Fatalf("cookie session no longer admits: status = %d", rec.Code)
	}
}
