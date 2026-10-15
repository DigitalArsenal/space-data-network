package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

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
