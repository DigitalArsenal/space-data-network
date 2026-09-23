package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func delegationFixture(t *testing.T) (*http.ServeMux, *Handler, ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	users, err := NewUserStore(filepath.Join(dir, "auth.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { users.Close() })
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := users.AddUser("ed25519:admin", "Admin", peers.Admin, hex.EncodeToString(pub)); err != nil {
		t.Fatal(err)
	}
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closer() })
	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(users, sessions, time.Hour, "", "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, h, priv
}

func postJSON(mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.RemoteAddr = "203.0.113.4:1000"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func challengeFor(t *testing.T, mux *http.ServeMux, wallet ed25519.PublicKey) (string, []byte) {
	t.Helper()
	rec := postJSON(mux, "/api/auth/challenge", map[string]any{"client_pubkey_hex": hex.EncodeToString(wallet), "ts": time.Now().Unix()})
	var c struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawStdEncoding.DecodeString(c.Challenge)
	return c.ChallengeID, raw
}

func TestSessionKeyDelegation(t *testing.T) {
	mux, h, wallet := delegationFixture(t)
	walletPub := wallet.Public().(ed25519.PublicKey)
	session, _, _ := ed25519.GenerateKey(rand.Reader)
	expires := uint64(time.Now().Add(time.Hour).UnixMilli())

	delegate := func(id string, sessionKey ed25519.PublicKey, signedFor ed25519.PublicKey, challenge []byte, expiry uint64) int {
		sig := ed25519.Sign(wallet, DelegationDigest(challenge, signedFor, expiry))
		return postJSON(mux, "/api/auth/delegate", map[string]any{
			"challenge_id": id, "client_pubkey_hex": hex.EncodeToString(walletPub),
			"session_pubkey_hex": hex.EncodeToString(sessionKey), "expires_at_ms": expiry,
			"signature_hex": hex.EncodeToString(sig),
		}).Code
	}

	// A substituted session key (what a party in the middle would send) fails:
	// the wallet signed the page's own key.
	attacker, _, _ := ed25519.GenerateKey(rand.Reader)
	id, challenge := challengeFor(t, mux, walletPub)
	if code := delegate(id, attacker, session, challenge, expires); code == http.StatusOK {
		t.Fatal("delegated a key the wallet did not sign for")
	}
	if _, _, ok := h.DelegatedWallet(attacker); ok {
		t.Fatal("attacker key recorded")
	}

	// More than 12 hours is refused.
	id, challenge = challengeFor(t, mux, walletPub)
	if code := delegate(id, session, session, challenge, uint64(time.Now().Add(13*time.Hour).UnixMilli())); code != http.StatusBadRequest {
		t.Fatalf("13h delegation = %d, want 400", code)
	}

	// The real one.
	id, challenge = challengeFor(t, mux, walletPub)
	if code := delegate(id, session, session, challenge, expires); code != http.StatusOK {
		t.Fatalf("delegate = %d", code)
	}
	got, root, ok := h.DelegatedWallet(session)
	if !ok || root || !bytes.Equal(got, walletPub) {
		t.Fatalf("DelegatedWallet = %x %v %v", got, root, ok)
	}
	// The challenge was single-use.
	if code := delegate(id, session, session, challenge, expires); code == http.StatusOK {
		t.Fatal("a challenge delegated twice")
	}
	h.RevokeDelegation(session)
	if _, _, ok := h.DelegatedWallet(session); ok {
		t.Fatal("revoked delegation still live")
	}
}
