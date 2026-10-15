package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// SEC-001 (verifier r2): a persisted account whose signing key (or xpub)
// belongs to a denylisted identity — the old root's own row, an enrolled old
// root key — is never resolved for sign-in, by xpub or by key. A throwaway
// key stands in for the leaked one.
func TestPersistedAccountsOfADenylistedIdentityAreNotSignInable(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(pub)
	store, err := NewUserStore(filepath.Join(t.TempDir(), "auth.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const xpub = "xpub-sec001-old-root-test"
	if err := store.RecordRootSignIn(xpub, "old root", peers.Admin, keyHex); err != nil {
		t.Fatal(err)
	}
	if u, err := store.GetUser(xpub); err != nil || u == nil {
		t.Fatalf("baseline GetUser = %v, %v", u, err)
	}

	var raw map[string]any
	if err := json.Unmarshal(trustanchors.EmbeddedJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	deny := raw["denylist"].(map[string]any)
	deny["ed25519PublicKeys"] = append(deny["ed25519PublicKeys"].([]any), keyHex)
	data, _ := json.Marshal(raw)
	anchors, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	restore := trustanchors.Use(anchors)
	defer restore()

	if u, err := store.GetUser(xpub); err != nil || u != nil {
		t.Fatalf("GetUser(denylisted account) = %+v, %v; want nil", u, err)
	}
	if u, err := store.GetUserBySigningPubKey(keyHex); err != nil || u != nil {
		t.Fatalf("GetUserBySigningPubKey(denylisted key) = %+v, %v; want nil", u, err)
	}
	users, err := store.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range users {
		found = found || u.XPub == xpub
	}
	if !found {
		t.Fatal("the row must stay listed so an operator can remove it")
	}
}

// useTestDenylist installs anchors whose denylist also names the given
// ed25519 keys and xpubs (throwaway values standing in for the leaked ones).
func useTestDenylist(t *testing.T, keys []ed25519.PublicKey, xpubs []string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(trustanchors.EmbeddedJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	deny := raw["denylist"].(map[string]any)
	for _, k := range keys {
		deny["ed25519PublicKeys"] = append(deny["ed25519PublicKeys"].([]any), hex.EncodeToString(k))
	}
	for _, x := range xpubs {
		deny["xpubs"] = append(deny["xpubs"].([]any), x)
	}
	data, _ := json.Marshal(raw)
	anchors, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(trustanchors.Use(anchors))
}

// Verifier r3: after the re-key, RevokeRootGrants revokes the NEW root's
// grants; the OLD (denylisted) root's sessions and delegations must go at
// startup, be refused on validation, and its key must be refused as
// presented, from any address.
func TestDenylistedIdentityGrantsAreRevokedAndRefused(t *testing.T) {
	h, _, _ := newRootHandler(t, "xpub-new-root-after-rekey")
	oldPub, oldPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const oldXPub = "xpub-old-root-sec001-test"
	const enrolledXPub = "xpub-operator-who-enrolled-the-old-key"
	// What a node carried over from before the re-key.
	oldSession, err := h.sessions.CreateSession(oldXPub, peers.Admin, "203.0.113.7", "old", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.userStore.RecordRootSignIn(enrolledXPub, "enrolled old key", peers.Admin, hex.EncodeToString(oldPub)); err != nil {
		t.Fatal(err)
	}
	enrolledSession, err := h.sessions.CreateSession(enrolledXPub, peers.Admin, "203.0.113.7", "old", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sessPub, _, _ := ed25519.GenerateKey(rand.Reader)
	h.recordDelegation(sessPub, oldPub, time.Now().Add(time.Hour), time.Now())

	useTestDenylist(t, []ed25519.PublicKey{oldPub}, []string{oldXPub})

	h.RevokeDeniedGrants()
	for name, token := range map[string]string{"old root": oldSession, "enrolled old key": enrolledSession} {
		if s, err := h.sessions.ValidateSession(token); err == nil && s != nil {
			t.Fatalf("%s session survived RevokeDeniedGrants", name)
		}
	}
	if _, _, ok := h.DelegatedWallet(sessPub); ok {
		t.Fatal("a delegation from the denylisted wallet survived")
	}
	h.loadDelegations()
	if _, _, ok := h.DelegatedWallet(sessPub); ok {
		t.Fatal("the denylisted wallet's delegation came back from auth.db")
	}

	// On validation, whatever minted it.
	late, err := h.sessions.CreateSession(oldXPub, peers.Admin, "127.0.0.1", "late", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := h.sessions.ValidateSession(late); err == nil && s != nil {
		t.Fatal("ValidateSession accepted a session of a denylisted xpub")
	}
	sessPub2, _, _ := ed25519.GenerateKey(rand.Reader)
	h.recordDelegation(sessPub2, oldPub, time.Now().Add(time.Hour), time.Now())
	if _, _, ok := h.DelegatedWallet(sessPub2); ok {
		t.Fatal("DelegatedWallet admitted a denylisted wallet")
	}

	// As presented: even from loopback, with no stored account involved.
	for _, remote := range []string{"127.0.0.1:40000", "203.0.113.7:443"} {
		if rec := rootChallengeFrom(h, oldPriv, remote, nil); rec.Code == http.StatusOK {
			t.Fatalf("a challenge presenting the denylisted key from %s was answered: %s", remote, rec.Body.String())
		}
	}
}
