package credstore

import (
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// TestCredentialsSurviveDerivationUpgrade is the regression guard for a silent
// data loss: the at-rest machine derivation (keys.DeriveDefaultPassword) feeds
// the credstore root, so changing it would orphan every node's stored
// credentials — on host-01 that is the Space-Track login.
//
// A keystore sealed under the OLD generation must still open after the
// upgrade, and must then be re-sealed under the new one.
func TestCredentialsSurviveDerivationUpgrade(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "")
	dir := t.TempDir()

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Skip("hostname unavailable")
	}
	nodeKey := []byte("node-identity-private-key-material-AAAAAAAA")

	// Seal a credential under the PREVIOUS (v2) machine derivation.
	oldRoot, err := DeriveRootKey(keys.DeriveV2Password(), cp(nodeKey), hostname)
	if err != nil {
		t.Fatalf("derive old root: %v", err)
	}
	oldStore, err := NewStore(dir, oldRoot)
	if err != nil {
		t.Fatalf("open old store: %v", err)
	}
	if err := oldStore.Put("spacetrack", "operator@example.com", "the-secret"); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Upgrade: open the way the node does now (current derivation primary,
	// older generations attached as fallbacks).
	newStore, err := OpenStore(dir, cp(nodeKey))
	if err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	got, err := newStore.Reveal("spacetrack")
	if err != nil {
		t.Fatalf("credentials must survive the derivation upgrade: %v", err)
	}
	if got.Secret != "the-secret" || got.Username != "operator@example.com" {
		t.Fatalf("credential mismatch after upgrade: %+v", got)
	}

	// The keystore must now be re-sealed under the CURRENT derivation, so a
	// store with no fallbacks at all can open it.
	currentRoot, err := ResolveRoot(cp(nodeKey))
	if err != nil {
		t.Fatalf("resolve current root: %v", err)
	}
	plainStore, err := NewStore(dir, currentRoot)
	if err != nil {
		t.Fatalf("open re-sealed store: %v", err)
	}
	if _, err := plainStore.Reveal("spacetrack"); err != nil {
		t.Fatalf("keystore must be re-sealed under the current derivation: %v", err)
	}
}

// TestForeignKeystoreStillFailsClosed proves the fallback ladder did not turn
// the credstore into something that opens under any key.
func TestForeignKeystoreStillFailsClosed(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "")
	dir := t.TempDir()

	foreign, err := NewStore(dir, "a-root-from-an-entirely-different-machine")
	if err != nil {
		t.Fatalf("open foreign store: %v", err)
	}
	if err := foreign.Put("spacetrack", "u", "s"); err != nil {
		t.Fatalf("put: %v", err)
	}

	store, err := OpenStore(dir, cp([]byte("node-identity-private-key-material-AAAAAAAA")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.Reveal("spacetrack"); err == nil {
		t.Fatal("a foreign keystore must NOT open — fail closed")
	}
}

// TestExplicitPassphraseAttachesNoFallbacks proves an operator passphrase is
// machine-independent: no machine-derived fallback is wired in, so the
// keystore cannot be opened by re-deriving machine state.
func TestExplicitPassphraseAttachesNoFallbacks(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "explicit-operator-secret")
	dir := t.TempDir()

	store, err := OpenStore(dir, cp([]byte("node-identity-private-key-material-AAAAAAAA")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(store.fallbacks) != 0 {
		t.Fatalf("explicit passphrase must attach no machine-derived fallbacks, got %d", len(store.fallbacks))
	}
}
