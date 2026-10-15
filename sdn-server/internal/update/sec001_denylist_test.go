package update

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// denyThrowawaySigner puts a throwaway signer's key on the SEC-001 denylist,
// standing in for the published root (whose private half no test has).
func denyThrowawaySigner(t *testing.T, signer *testSigner) {
	t.Helper()
	base := trustanchors.Load()
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	poisoned, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	poisoned.Denylist.Ed25519PublicKeys = append(poisoned.Denylist.Ed25519PublicKeys, hex.EncodeToString(signer.publicKey))
	poisoned.Denylist.UpdateRootKeyIDs = append(poisoned.Denylist.UpdateRootKeyIDs, RootKeyID(signer.publicKey))
	reindexed, err := json.Marshal(poisoned)
	if err != nil {
		t.Fatal(err)
	}
	final, err := trustanchors.Parse(reindexed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(trustanchors.Use(final))
}

// A manifest signed by the published root no longer verifies, even when a
// node's trust file still lists that root.
func TestManifestSignedByADenylistedRootIsRefused(t *testing.T) {
	signer := newTestSigner(t)
	signer.keyID = RootKeyID(signer.publicKey)
	bundle, wasm := []byte("bundle"), []byte("wasm")
	raw := signer.signedManifest(t, nil, bundle, wasm)

	manifest, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature(signer.roots(t)); err != nil {
		t.Fatalf("control: the manifest must verify before its root is denied: %v", err)
	}

	denyThrowawaySigner(t, signer)
	manifest, err = ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature(signer.roots(t)); !errors.Is(err, trustanchors.ErrDenied) {
		t.Fatalf("VerifySignature by a denylisted root = %v, want ErrDenied", err)
	}
	// Listed under another key id it is still refused: the key, not the label, is denied.
	relabelled := TrustedRoots{"some-other-id": signer.publicKeyBase64SPKI(t)}
	if _, err := decodeTrustedPublicKey(relabelled["some-other-id"]); !errors.Is(err, trustanchors.ErrDenied) {
		t.Fatalf("decodeTrustedPublicKey of the denied key = %v, want ErrDenied", err)
	}
}

// The trust file shipped to every installed node (fleet-trust-roots.json
// before SEC-001) named only the published root. A node loading it now trusts
// nothing, rather than the published key; a mixed file keeps the new root.
func TestLoadTrustRootsDropsThePublishedRoot(t *testing.T) {
	anchors := trustanchors.Load()
	oldRoot := anchors.Denylist.Ed25519PublicKeys[0]
	raw, err := hex.DecodeString(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	spki := base64.StdEncoding.EncodeToString(append([]byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}, raw...))
	oldID := anchors.Denylist.UpdateRootKeyIDs[0]

	dir := t.TempDir()
	trustFile := filepath.Join(dir, "update-roots.json")
	t.Setenv(TrustRootsEnv, trustFile)

	write := func(roots TrustedRoots) {
		data, _ := json.Marshal(roots)
		if err := os.WriteFile(trustFile, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(TrustedRoots{oldID: spki})
	if _, err := LoadTrustRoots(Paths{}); err == nil || !strings.Contains(err.Error(), "SEC-001") {
		t.Fatalf("LoadTrustRoots with only the published root = %v, want an empty-roots refusal", err)
	}

	fresh := newTestSigner(t)
	freshID := RootKeyID(fresh.publicKey)
	write(TrustedRoots{oldID: spki, "relabelled": spki, freshID: fresh.publicKeyBase64SPKI(t)})
	roots, err := LoadTrustRoots(Paths{})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[freshID] == "" {
		t.Fatalf("LoadTrustRoots kept %v, want only the fresh root", roots)
	}
}

// A release may not move the fleet's roots back to the published key.
func TestTrustRootsReleaseNamingThePublishedRootIsRefused(t *testing.T) {
	anchors := trustanchors.Load()
	raw, _ := hex.DecodeString(anchors.Denylist.Ed25519PublicKeys[0])
	spki := base64.StdEncoding.EncodeToString(append([]byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}, raw...))
	m := &Manifest{TrustRoots: TrustedRoots{anchors.Denylist.UpdateRootKeyIDs[0]: spki}}
	if err := m.assertTrustRoots(); !errors.Is(err, trustanchors.ErrDenied) {
		t.Fatalf("assertTrustRoots naming the published root = %v, want ErrDenied", err)
	}
}
