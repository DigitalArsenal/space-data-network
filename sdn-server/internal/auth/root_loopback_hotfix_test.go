package auth

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func rootChallengeFrom(h *Handler, priv ed25519.PrivateKey, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	body := `{"client_pubkey_hex":"` + pubHex + `","ts":` + itoa(time.Now().Unix()) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", strings.NewReader(body))
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handleChallenge(rec, req)
	return rec
}

// TestRootKeyRefusedOffTheBox: the SEC-001 hotfix. A root key presented from a
// public address, or from loopback through a proxy hop, gets no challenge and
// does not fall through to the user store.
func TestRootKeyRefusedOffTheBox(t *testing.T) {
	h, slip, legacy := newRootHandler(t, "xpub-node-root-account")
	for _, priv := range []ed25519.PrivateKey{slip, legacy} {
		for _, c := range []struct {
			remote string
			hdr    map[string]string
		}{
			{"203.0.113.7:443", nil},
			{"[2001:db8::1]:443", nil},
			{"172.70.114.9:51000", map[string]string{"CF-Connecting-IP": "127.0.0.1"}},
			{"127.0.0.1:40000", map[string]string{"X-Forwarded-For": "203.0.113.7"}},
		} {
			if rec := rootChallengeFrom(h, priv, c.remote, c.hdr); rec.Code == http.StatusOK {
				t.Fatalf("root challenge from %s %v admitted: %s", c.remote, c.hdr, rec.Body.String())
			}
		}
	}
	// Loopback with no proxy hop still works (ops over an SSH tunnel).
	if rec := signIn(t, h, slip, ""); rec.Code != http.StatusOK {
		t.Fatalf("loopback root sign-in = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRootGrantsRevokedAtRegistration: sessions and delegations the root
// account held before the hotfix binary started do not survive it.
func TestRootGrantsRevokedAtRegistration(t *testing.T) {
	xpub := "xpub-node-root-account"
	h, slip, _ := newRootHandler(t, xpub)
	token, err := h.sessions.CreateSession(xpub, peers.Admin, "203.0.113.7", "attacker", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sessPub, _, _ := ed25519.GenerateKey(nil)
	h.recordDelegation(sessPub, slip.Public().(ed25519.PublicKey), time.Now().Add(time.Hour), time.Now())
	if _, _, ok := h.DelegatedWallet(sessPub); !ok {
		t.Fatal("delegation not recorded")
	}

	h.RevokeRootGrants()

	if s, err := h.sessions.ValidateSession(token); err == nil && s != nil {
		t.Fatal("root session survived RevokeRootGrants")
	}
	if _, _, ok := h.DelegatedWallet(sessPub); ok {
		t.Fatal("root-wallet delegation survived RevokeRootGrants")
	}
	h.loadDelegations()
	if _, _, ok := h.DelegatedWallet(sessPub); ok {
		t.Fatal("root-wallet delegation came back from auth.db")
	}
}
