package auth

// Regression tests for the 2026-10-15 security audit findings fixed in this
// package (SEC-012, SEC-028, SEC-029, SEC-062). Each reproduces the verified
// attack and uses only APIs that existed before the fix, so this file fails
// against the unfixed tree.

import (
	"bytes"
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

func newRegressionHandler(t *testing.T, entries []config.UserEntry, ttl time.Duration) (*Handler, *UserStore, *SessionStore, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	users, err := NewUserStore(filepath.Join(dir, "users.db"), entries)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = users.Close() })
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })
	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	h := NewHandler(users, sessions, ttl, "", "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, users, sessions, mux
}

func cookieRequest(mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Accept", "application/json")
	r.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// ---- SEC-012 -------------------------------------------------------------

// A loopback request with no session was the node's root account whenever
// admin.local_console was on: every web page and every local process.
func TestSEC012_LocalConsoleIsNotAmbientLoopbackAuthority(t *testing.T) {
	h, users, _, mux := newRegressionHandler(t, nil, 24*time.Hour)
	pub, _, _ := ed25519.GenerateKey(nil)
	h.SetNodeRootIdentity(&RootIdentity{XPub: "xpub-node-root", SigningKeys: []ed25519.PublicKey{pub}})
	h.EnableLocalConsole()

	for name, r := range map[string]*http.Request{
		"plain loopback": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/auth/users", nil)
			r.RemoteAddr = "127.0.0.1:50000"
			r.Host = "127.0.0.1:5001"
			return r
		}(),
		"rebinding host": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/auth/users", nil)
			r.RemoteAddr = "127.0.0.1:50000"
			r.Host = "rebind.evil.example:5001"
			return r
		}(),
	} {
		if s, err := h.sessionFromRequest(r); err == nil {
			t.Fatalf("%s: admitted as %s (%v)", name, s.XPub, s.TrustLevel)
		}
	}

	r := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{"xpub":"xpub-ATTACKER","name":"backdoor","trust_level":"admin"}`))
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:5001"
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if u, _ := users.GetUser("xpub-ATTACKER"); u != nil {
		t.Fatalf("an ambient loopback POST created %q (%d %s)", u.Name, w.Code, w.Body.String())
	}
}

// dev_auto_admin had the same ambient path.
func TestSEC012_DevAutoAdminIsNotAmbientLoopbackAuthority(t *testing.T) {
	h, _, _, _ := newRegressionHandler(t, []config.UserEntry{{XPub: "xpub-dev-admin", TrustLevel: "admin", Name: "Dev"}}, 24*time.Hour)
	h.EnableDevAutoAdmin()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Host = "127.0.0.1:5001"
	if s, err := h.sessionFromRequest(r); err == nil {
		t.Fatalf("dev_auto_admin admitted a bare loopback request as %s (%v)", s.XPub, s.TrustLevel)
	}
}

// ---- SEC-028 -------------------------------------------------------------

func challengeAndVerify(t *testing.T, h *Handler, xpub string, priv ed25519.PrivateKey, remote string) *httptest.ResponseRecorder {
	t.Helper()
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	body, _ := json.Marshal(map[string]any{"xpub": xpub, "client_pubkey_hex": pubHex, "ts": time.Now().Unix()})
	r := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.handleChallenge(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", w.Code, w.Body.String())
	}
	var ch struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &ch)
	raw, _ := base64.RawStdEncoding.DecodeString(ch.Challenge)
	body, _ = json.Marshal(map[string]any{
		"challenge_id":      ch.ChallengeID,
		"xpub":              xpub,
		"client_pubkey_hex": pubHex,
		"challenge":         ch.Challenge,
		"signature_hex":     hex.EncodeToString(ed25519.Sign(priv, raw)),
	})
	r = httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(body))
	r.RemoteAddr = remote
	w = httptest.NewRecorder()
	h.handleVerify(w, r)
	return w
}

// Anyone who knew an xpub-only admin row's xpub bound their own key to it and
// signed in as admin; the real operator was locked out.
func TestSEC028_ForeignKeyCannotClaimAnXpubOnlyAdminRow(t *testing.T) {
	const xpub = "xpub6CUGRUonZSQ4TWtTMmzXdrXDtypWKiKrhko4egpiMZbpiaQL2jkwSB1icqYh2cfDfVxdx4df189oLKnC5fSwqPfgyP3hooxujYzAu3fDVmz"
	h, users, _, _ := newRegressionHandler(t, []config.UserEntry{{XPub: xpub, TrustLevel: "admin", Name: "Ops"}}, 24*time.Hour)

	_, attacker, _ := ed25519.GenerateKey(nil)
	w := challengeAndVerify(t, h, xpub, attacker, "203.0.113.7:4444")
	if w.Code == http.StatusOK {
		t.Fatalf("a foreign key signed in to an xpub-only admin row: %s", w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "sdn_wallet_session" && c.Value != "" {
			t.Fatalf("a session cookie was issued to the foreign key")
		}
	}
	u, _ := users.GetUser(xpub)
	if u == nil || u.SigningPubKeyHex != "" {
		t.Fatalf("the row's signing key changed: %+v", u)
	}
	if got, _ := users.GetUserBySigningPubKey(hex.EncodeToString(attacker.Public().(ed25519.PublicKey))); got != nil {
		t.Fatalf("the attacker key now resolves to %q at %v", got.Name, got.TrustLevel)
	}
}

// ---- SEC-029 -------------------------------------------------------------

// A removed admin's cookie kept creating admins; a demoted admin kept Admin.
func TestSEC029_RemovedOrDemotedAdminLosesTheSession(t *testing.T) {
	ttl := time.Hour
	_, users, sessions, mux := newRegressionHandler(t, []config.UserEntry{{XPub: "xpub-operator", TrustLevel: "admin", Name: "Operator"}}, ttl)
	key := func() string {
		pub, _, _ := ed25519.GenerateKey(nil)
		return hex.EncodeToString(pub)
	}
	if err := users.AddUser("xpub-mallory", "Mallory", peers.Admin, key()); err != nil {
		t.Fatal(err)
	}
	if err := users.AddUser("xpub-eve", "Eve", peers.Admin, key()); err != nil {
		t.Fatal(err)
	}
	opTok, _ := sessions.CreateSession("xpub-operator", peers.Admin, "198.51.100.1", "ua", ttl)
	malTok, _ := sessions.CreateSession("xpub-mallory", peers.Admin, "198.51.100.2", "ua", ttl)
	eveTok, _ := sessions.CreateSession("xpub-eve", peers.Admin, "198.51.100.3", "ua", ttl)

	if w := cookieRequest(mux, http.MethodDelete, "/api/auth/users/xpub-mallory", opTok, ""); w.Code != http.StatusOK {
		t.Fatalf("operator DELETE mallory: %d %s", w.Code, w.Body.String())
	}
	w := cookieRequest(mux, http.MethodPost, "/api/auth/users", malTok, `{"xpub":"xpub-backdoor","name":"bd","trust_level":"admin"}`)
	if w.Code == http.StatusCreated || w.Code == http.StatusOK {
		t.Fatalf("removed admin created a user: %d %s", w.Code, w.Body.String())
	}
	if u, _ := users.GetUser("xpub-backdoor"); u != nil {
		t.Fatalf("removed admin planted %q", u.Name)
	}
	if s, err := sessions.ValidateSession(malTok); err == nil {
		t.Fatalf("removed admin's session is still valid at %v", s.TrustLevel)
	}

	if w := cookieRequest(mux, http.MethodPut, "/api/auth/users/xpub-eve", opTok, `{"trust_level":"standard"}`); w.Code != http.StatusOK {
		t.Fatalf("operator demote eve: %d %s", w.Code, w.Body.String())
	}
	if w := cookieRequest(mux, http.MethodGet, "/api/auth/users", eveTok, ""); w.Code == http.StatusOK {
		t.Fatalf("demoted admin still lists users")
	}
}

// Rotation copied the stored trust: an active session outlived its removal
// forever. The account row is removed here WITHOUT going through the API, so
// only the per-request re-resolution can stop it.
func TestSEC029_RotationNeverResurrectsARemovedAccount(t *testing.T) {
	ttl := time.Hour
	_, users, sessions, mux := newRegressionHandler(t, []config.UserEntry{{XPub: "xpub-operator", TrustLevel: "admin", Name: "Operator"}}, ttl)
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := users.AddUser("xpub-gone", "Gone", peers.Admin, hex.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	tok, _ := sessions.CreateSession("xpub-gone", peers.Admin, "198.51.100.9", "ua", ttl)
	if err := users.RemoveUser("xpub-gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.db.Exec("UPDATE sessions SET expires_at = ? WHERE token = ?", time.Now().Add(10*time.Minute).Unix(), hashToken(tok)); err != nil {
		t.Fatal(err)
	}
	w := cookieRequest(mux, http.MethodGet, "/api/auth/users", tok, "")
	if w.Code == http.StatusOK {
		t.Fatalf("a removed account's near-expiry session was served")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "sdn_wallet_session" && c.Value != "" {
			if s, err := sessions.ValidateSession(c.Value); err == nil {
				t.Fatalf("rotation issued a live session for a removed account at %v", s.TrustLevel)
			}
		}
	}
}

// ---- SEC-062 -------------------------------------------------------------

// Behind the local proxy, the leftmost X-Forwarded-For entry (client-written)
// keyed the auth rate limits and session/audit IPs.
func TestSEC062_ClientIPIsTheEntryTheProxyAppended(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", nil)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.50")
	r.Header.Set("X-Real-IP", "203.0.113.50")
	if got := clientIPForRequest(r); got != "203.0.113.50" {
		t.Fatalf("clientIPForRequest = %q, want the proxy-appended 203.0.113.50", got)
	}
	// A remote peer's forwarding headers are never read.
	r.RemoteAddr = "198.51.100.7:40000"
	if got := clientIPForRequest(r); got != "198.51.100.7" {
		t.Fatalf("clientIPForRequest from a remote peer = %q, want its own address", got)
	}
}

// Per-IP rate limits were walked past with a fresh X-Forwarded-For per request.
func TestSEC062_ForgedForwardedForDoesNotResetTheAuthRateLimit(t *testing.T) {
	h, _, _, _ := newRegressionHandler(t, nil, time.Hour)
	limited := false
	for i := 0; i < 200; i++ {
		// A different xpub and key each time, so only the per-IP bucket can stop it.
		body, _ := json.Marshal(map[string]any{"xpub": "xpub-nobody-" + itoa(int64(i)), "client_pubkey_hex": strings.Repeat("ab", 31) + hex.EncodeToString([]byte{byte(i)}), "ts": time.Now().Unix()})
		r := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("X-Forwarded-For", "10.0."+itoa(int64(i/250))+"."+itoa(int64(i%250))+", 203.0.113.50")
		w := httptest.NewRecorder()
		h.handleChallenge(w, r)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("200 challenges from one client behind the proxy were never rate limited")
	}
}
