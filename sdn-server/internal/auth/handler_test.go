package auth

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestAuth_ChallengeVerify_SucceedsWithBoundKey(t *testing.T) {
	t.Parallel()

	// Generate an Ed25519 keypair for auth.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{
			XPub:             "xpub-test-admin",
			SigningPubKeyHex: pubHex,
			TrustLevel:       "admin",
			Name:             "Test Admin",
		},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(userStore, sessions, 24*time.Hour, "", "")

	// Step 1: request challenge
	chReqBody, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-test-admin",
		"client_pubkey_hex": pubHex,
		"ts":                time.Now().Unix(),
	})
	chReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(chReqBody))
	chReq.RemoteAddr = "127.0.0.1:12345"
	chRec := httptest.NewRecorder()
	h.handleChallenge(chRec, chReq)

	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge status: got %d want %d: %s", chRec.Code, http.StatusOK, chRec.Body.String())
	}

	var chResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(chRec.Body.Bytes(), &chResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}
	if chResp.ChallengeID == "" || chResp.Challenge == "" {
		t.Fatalf("challenge response missing fields: %#v", chResp)
	}
	challengeBytes, err := base64.RawStdEncoding.DecodeString(chResp.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	// Step 2: sign and verify
	sig := ed25519.Sign(priv, challengeBytes)
	verReqBody, _ := json.Marshal(map[string]any{
		"challenge_id":      chResp.ChallengeID,
		"xpub":              "xpub-test-admin",
		"client_pubkey_hex": pubHex,
		"challenge":         chResp.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	})
	verReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(verReqBody))
	verReq.RemoteAddr = "127.0.0.1:12345"
	verRec := httptest.NewRecorder()
	h.handleVerify(verRec, verReq)

	if verRec.Code != http.StatusOK {
		t.Fatalf("verify status: got %d want %d: %s", verRec.Code, http.StatusOK, verRec.Body.String())
	}
	if cookie := verRec.Header().Get("Set-Cookie"); cookie == "" {
		t.Fatalf("expected Set-Cookie to be set")
	}

	var verResp struct {
		User struct {
			XPub       string           `json:"xpub"`
			TrustLevel peers.TrustLevel `json:"trust_level"`
		} `json:"user"`
	}
	if err := json.Unmarshal(verRec.Body.Bytes(), &verResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}
	if verResp.User.XPub != "xpub-test-admin" {
		t.Fatalf("unexpected xpub: %q", verResp.User.XPub)
	}
	if verResp.User.TrustLevel < peers.Admin {
		t.Fatalf("unexpected trust level: %v", verResp.User.TrustLevel)
	}
}

func TestAuth_ChallengeVerify_FailsWithMismatchedKey(t *testing.T) {
	t.Parallel()

	// Configured key for the user.
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	// Attacker uses a different keypair.
	attPub, attPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey(attacker): %v", err)
	}
	attPubHex := hex.EncodeToString(attPub)

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{
			XPub:             "xpub-test-user",
			SigningPubKeyHex: pubHex,
			TrustLevel:       "standard",
			Name:             "Test User",
		},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(userStore, sessions, 24*time.Hour, "", "")

	// Request a challenge, but with the wrong pubkey.
	chReqBody, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-test-user",
		"client_pubkey_hex": attPubHex,
		"ts":                time.Now().Unix(),
	})
	chReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(chReqBody))
	chReq.RemoteAddr = "127.0.0.1:12345"
	chRec := httptest.NewRecorder()
	h.handleChallenge(chRec, chReq)

	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge status: got %d want %d: %s", chRec.Code, http.StatusOK, chRec.Body.String())
	}

	var chResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(chRec.Body.Bytes(), &chResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}

	challengeBytes, err := base64.RawStdEncoding.DecodeString(chResp.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	sig := ed25519.Sign(attPriv, challengeBytes)

	verReqBody, _ := json.Marshal(map[string]any{
		"challenge_id":      chResp.ChallengeID,
		"xpub":              "xpub-test-user",
		"client_pubkey_hex": attPubHex,
		"challenge":         chResp.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	})
	verReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(verReqBody))
	verReq.RemoteAddr = "127.0.0.1:12345"
	verRec := httptest.NewRecorder()
	h.handleVerify(verRec, verReq)

	if verRec.Code != http.StatusForbidden {
		t.Fatalf("verify status: got %d want %d: %s", verRec.Code, http.StatusForbidden, verRec.Body.String())
	}
}

func TestAuth_ChallengeVerify_FailsWhenUserMissingSigningKey(t *testing.T) {
	t.Parallel()

	// Client keypair (doesn't matter).
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{
			XPub:       "xpub-missing-key",
			TrustLevel: "admin",
			Name:       "Misconfigured Admin",
		},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(userStore, sessions, 24*time.Hour, "", "")

	chReqBody, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-missing-key",
		"client_pubkey_hex": pubHex,
		"ts":                time.Now().Unix(),
	})
	chReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(chReqBody))
	chReq.RemoteAddr = "127.0.0.1:12345"
	chRec := httptest.NewRecorder()
	h.handleChallenge(chRec, chReq)

	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge status: got %d want %d: %s", chRec.Code, http.StatusOK, chRec.Body.String())
	}

	var chResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(chRec.Body.Bytes(), &chResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}

	challengeBytes, err := base64.RawStdEncoding.DecodeString(chResp.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	sig := ed25519.Sign(priv, challengeBytes)

	verReqBody, _ := json.Marshal(map[string]any{
		"challenge_id":      chResp.ChallengeID,
		"xpub":              "xpub-missing-key",
		"client_pubkey_hex": pubHex,
		"challenge":         chResp.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	})
	verReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(verReqBody))
	verReq.RemoteAddr = "127.0.0.1:12345"
	verRec := httptest.NewRecorder()
	h.handleVerify(verRec, verReq)

	if verRec.Code != http.StatusForbidden {
		t.Fatalf("verify status: got %d want %d: %s", verRec.Code, http.StatusForbidden, verRec.Body.String())
	}
}
