package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// A stored session is re-resolved against its account on every request
// (reconcileSession, SEC-029): a session whose account row is gone is refused,
// and its trust is capped at the row's. Tests that mint sessions directly
// therefore give each session the account a real sign-in always has.

// sessionAccountStore returns a user store holding one row per entry.
func sessionAccountStore(t *testing.T, accounts map[string]peers.TrustLevel) *UserStore {
	t.Helper()
	store, err := NewUserStore(filepath.Join(t.TempDir(), "accounts.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for xpub, trust := range accounts {
		enrollSessionAccount(t, store, xpub, trust)
	}
	return store
}

// enrollSessionAccount adds the account row a session for xpub stands for.
func enrollSessionAccount(t *testing.T, store *UserStore, xpub string, trust peers.TrustLevel) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.AddUser(xpub, "session account", trust, hex.EncodeToString(pub)); err != nil {
		t.Fatalf("AddUser(%s): %v", xpub, err)
	}
}

// registerRootAccount makes xpub the node's own root account: root sessions
// (the root ceremony, the local console) have no account row by design.
func registerRootAccount(t *testing.T, h *Handler, xpub string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	h.SetNodeRootIdentity(&RootIdentity{XPub: xpub, Name: "Node Root", SigningKeys: []ed25519.PublicKey{pub}})
}
