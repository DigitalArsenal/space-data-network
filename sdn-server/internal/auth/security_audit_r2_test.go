package auth

// Round-2 regression tests for the 2026-10-15 security audit (verifier
// follow-ups on SEC-028 and SEC-062). They use only APIs that exist on
// origin/main, so this file fails there on behaviour, not on compilation.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// r2BootstrapAttempt drives challenge+verify for xpub from remote, with extra
// request headers, and reports whether the node gained an admin.
func r2BootstrapAttempt(t *testing.T, h *Handler, xpub, remote string, headers map[string]string) bool {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)
	do := func(path, body string, handle func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.RemoteAddr = remote
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handle(w, r)
		return w
	}
	rec := do("/api/auth/challenge", fmt.Sprintf(`{"xpub":%q,"client_pubkey_hex":%q,"ts":%d}`, xpub, pubHex, time.Now().Unix()), h.handleChallenge)
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body.String())
	}
	var ch struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	raw, _ := base64.RawStdEncoding.DecodeString(ch.Challenge)
	do("/api/auth/verify", fmt.Sprintf(`{"challenge_id":%q,"xpub":%q,"client_pubkey_hex":%q,"challenge":%q,"signature_hex":%q}`,
		ch.ChallengeID, xpub, pubHex, ch.Challenge, hex.EncodeToString(ed25519.Sign(priv, raw))), h.handleVerify)
	return h.userStore.HasAdmin()
}

// On a node whose root identity did not load, the first wallet ANYWHERE to
// reach /api/auth/verify became "Initial Admin" (verifier note, adjacent to
// SEC-028). Only the operator on the node's own machine may bootstrap.
func TestR2_FirstAdminBootstrapOnlyFromThisMachine(t *testing.T) {
	h := newBootstrapHandler(t)
	for name, c := range map[string]struct {
		remote  string
		headers map[string]string
	}{
		"internet client":               {remote: "203.0.113.9:4000"},
		"through a local proxy":         {remote: "127.0.0.1:4000", headers: map[string]string{"X-Forwarded-For": "203.0.113.9"}},
		"through a proxy that says Via": {remote: "127.0.0.1:4000", headers: map[string]string{"Via": "1.1 edge"}},
		"tor onion client":              {remote: "[100::]:0"},
	} {
		if r2BootstrapAttempt(t, h, makeXPub(t, 3), c.remote, c.headers) {
			t.Fatalf("%s minted the first admin", name)
		}
	}
	if !r2BootstrapAttempt(t, h, makeXPub(t, 3), "127.0.0.1:4000", nil) {
		t.Fatalf("the operator on the node's own machine could not bootstrap the first admin")
	}
}

// One IPv6 subscriber holds a /64; per-/128 buckets gave it endless fresh
// challenge budget (verifier note on SEC-062).
func TestR2_AuthRateLimitBucketsIPv6By64(t *testing.T) {
	h := newBootstrapHandler(t)
	limited := false
	for i := 0; i < maxChallengePerMinutePerIP+5; i++ {
		pub, _, _ := ed25519.GenerateKey(nil)
		body := fmt.Sprintf(`{"client_pubkey_hex":%q,"ts":%d}`, hex.EncodeToString(pub), time.Now().Unix())
		r := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", strings.NewReader(body))
		r.RemoteAddr = fmt.Sprintf("[2001:db8:1:2::%x]:443", i+1)
		w := httptest.NewRecorder()
		h.handleChallenge(w, r)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("%d challenges from one /64 were never rate limited", maxChallengePerMinutePerIP+5)
	}
}
