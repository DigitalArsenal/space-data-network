package credstore

import (
	"testing"
)

// Fixed, representative inputs. The node key here is arbitrary bytes — the
// derivation only requires non-empty material, not a real libp2p key.
const (
	fpA   = "hardware-fingerprint-A"
	fpB   = "hardware-fingerprint-B"
	hostA = "node-01.example.com"
	hostB = "node-02.example.com"
)

var (
	nodeKeyA = []byte("node-identity-private-key-material-AAAAAAAA")
	nodeKeyB = []byte("node-identity-private-key-material-BBBBBBBB")
)

func cp(b []byte) []byte { return append([]byte(nil), b...) }

// DETERMINISM: the same machine fingerprint + node key + hostname yields the
// identical root on a simulated restart, and a credentials.enc written by the
// first boot decrypts under the root re-derived on the second.
func TestRootDerivationDeterministicAcrossRestart(t *testing.T) {
	root1, err := DeriveRootKey(fpA, cp(nodeKeyA), hostA)
	if err != nil {
		t.Fatalf("boot 1 derive: %v", err)
	}
	root2, err := DeriveRootKey(fpA, cp(nodeKeyA), hostA)
	if err != nil {
		t.Fatalf("boot 2 derive: %v", err)
	}
	if root1 != root2 {
		t.Fatal("root derivation is not deterministic for identical inputs")
	}

	dir := t.TempDir()

	// Boot 1 writes.
	st1, err := NewStore(dir, root1)
	if err != nil {
		t.Fatalf("boot 1 store: %v", err)
	}
	if err := st1.Put(IDSpaceTrack, "operator@example.com", "the-space-track-secret"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Boot 2: a fresh Store over the SAME dir, root re-derived from the SAME
	// three inputs, must decrypt what boot 1 wrote — no env var, no prompt.
	st2, err := NewStore(dir, root2)
	if err != nil {
		t.Fatalf("boot 2 store: %v", err)
	}
	got, err := st2.Reveal(IDSpaceTrack)
	if err != nil {
		t.Fatalf("restart could not decrypt the keystore: %v", err)
	}
	if got.Secret.Reveal() != "the-space-track-secret" {
		t.Fatal("secret did not survive the simulated restart")
	}
}

// ResolveRoot (the production entry, using the REAL machine fingerprint and REAL
// hostname of this test host) is deterministic for the same node key: two calls
// yield the same root, and a keystore written under the first decrypts under the
// second. This exercises the actual os.Hostname()/deriveDefaultPassword() path.
func TestResolveRootDeterministicForSameNode(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "") // force the default derivation path

	r1, err := ResolveRoot(cp(nodeKeyA))
	if err != nil {
		t.Fatalf("resolve 1: %v", err)
	}
	r2, err := ResolveRoot(cp(nodeKeyA))
	if err != nil {
		t.Fatalf("resolve 2: %v", err)
	}
	if r1 != r2 {
		t.Fatal("ResolveRoot is not deterministic for the same machine + node key + hostname")
	}

	dir := t.TempDir()
	a, err := NewStore(dir, r1)
	if err != nil {
		t.Fatalf("store 1: %v", err)
	}
	if err := a.Put(IDSpaceTrack, "operator@example.com", "s3cr3t"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	b, err := NewStore(dir, r2)
	if err != nil {
		t.Fatalf("store 2: %v", err)
	}
	if _, err := b.Reveal(IDSpaceTrack); err != nil {
		t.Fatalf("restart decrypt under re-resolved root failed: %v", err)
	}
}

// MACHINE-BINDING: changing ANY of the three inputs — hostname, node keypair, or
// hardware fingerprint — produces a different root that CANNOT decrypt a
// keystore written under the baseline. Copying credentials.enc to another host
// or identity does not work.
func TestRootBindsToAllThreeInputs(t *testing.T) {
	baseline, err := DeriveRootKey(fpA, cp(nodeKeyA), hostA)
	if err != nil {
		t.Fatalf("baseline derive: %v", err)
	}

	dir := t.TempDir()
	st, err := NewStore(dir, baseline)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Put(IDSpaceTrack, "operator@example.com", "s3cr3t"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rootDiffHost, err := DeriveRootKey(fpA, cp(nodeKeyA), hostB)
	if err != nil {
		t.Fatal(err)
	}
	rootDiffKey, err := DeriveRootKey(fpA, cp(nodeKeyB), hostA)
	if err != nil {
		t.Fatal(err)
	}
	rootDiffFP, err := DeriveRootKey(fpB, cp(nodeKeyA), hostA)
	if err != nil {
		t.Fatal(err)
	}

	for name, altRoot := range map[string]string{
		"different hostname":             rootDiffHost,
		"different node keypair":         rootDiffKey,
		"different hardware fingerprint": rootDiffFP,
	} {
		if altRoot == baseline {
			t.Fatalf("SECURITY: %s produced the SAME root — the input is not bound in", name)
		}
		alt, err := NewStore(dir, altRoot)
		if err != nil {
			t.Fatalf("%s: store: %v", name, err)
		}
		if _, err := alt.Reveal(IDSpaceTrack); err == nil {
			t.Fatalf("SECURITY: %s still decrypted the keystore — not machine-bound", name)
		}
	}

	// Sanity: the exact original triple still decrypts (so the failures above
	// are genuine binding, not a broken keystore).
	same, err := DeriveRootKey(fpA, cp(nodeKeyA), hostA)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := NewStore(dir, same)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := ok.Reveal(IDSpaceTrack); err != nil {
		t.Fatalf("the original triple failed to decrypt: %v", err)
	}
}

// FAIL CLOSED: a missing input yields an error, never a weaker key.
func TestDeriveRootFailsClosedOnMissingInput(t *testing.T) {
	cases := map[string]struct {
		machine string
		key     []byte
		host    string
	}{
		"empty machine state": {"", nodeKeyA, hostA},
		"nil node key":        {fpA, nil, hostA},
		"empty node key":      {fpA, []byte{}, hostA},
		"empty hostname":      {fpA, nodeKeyA, ""},
		"blank hostname":      {fpA, nodeKeyA, "   "},
	}
	for name, tc := range cases {
		if _, err := DeriveRootKey(tc.machine, tc.key, tc.host); err == nil {
			t.Errorf("SECURITY: %s was accepted — a weaker key would be derived", name)
		}
	}
}

// FAIL CLOSED at the production entry: no node identity key => no root, no store.
func TestResolveAndOpenFailClosedWithoutNodeKey(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "") // ensure the default (deriving) path

	if _, err := ResolveRoot(nil); err == nil {
		t.Fatal("SECURITY: ResolveRoot returned a root with no node identity key")
	}
	if _, err := ResolveRoot([]byte{}); err == nil {
		t.Fatal("SECURITY: ResolveRoot returned a root with an empty node identity key")
	}
	if _, err := OpenStore(t.TempDir(), nil); err == nil {
		t.Fatal("SECURITY: OpenStore opened a keystore with no node identity key (weak-key fallback)")
	}
}

// OVERRIDE: SDN_KEY_PASSWORD takes precedence and does not require a node key.
func TestSDNKeyPasswordOverridesDerivation(t *testing.T) {
	t.Setenv("SDN_KEY_PASSWORD", "explicit-operator-chosen-secret")

	// The override is used verbatim and short-circuits the node-key requirement.
	root, err := ResolveRoot(nil)
	if err != nil {
		t.Fatalf("override path should not require a node key: %v", err)
	}
	if root != "explicit-operator-chosen-secret" {
		t.Fatalf("override not honored: got %q", root)
	}

	// It also differs from the derived root for the same node — proving it truly
	// overrides rather than coincidentally matching.
	derived, err := DeriveRootKey(fpA, cp(nodeKeyA), hostA)
	if err != nil {
		t.Fatal(err)
	}
	if root == derived {
		t.Fatal("override root coincided with the derived root")
	}

	// OpenStore works under the override with no node key.
	st, err := OpenStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("OpenStore under override: %v", err)
	}
	if err := st.Put(IDSpaceTrack, "operator@example.com", "s"); err != nil {
		t.Fatalf("Put under override: %v", err)
	}
}

// The length-prefixed IKM must be unambiguous: a boundary shift between two
// inputs must not collide with a different triple. ("ab","c") vs ("a","bc").
func TestRootIKMIsUnambiguous(t *testing.T) {
	// Same total bytes, different split between machine-state and hostname.
	r1, err := DeriveRootKey("ab", nodeKeyA, "c")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := DeriveRootKey("a", nodeKeyA, "bc")
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r2 {
		t.Fatal("SECURITY: IKM is ambiguous — a field-boundary shift collided")
	}
}
