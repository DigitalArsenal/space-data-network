package pmm

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// End-to-end over the REAL catalog produced from space-data-network-modules:
// load it, re-hash every artifact from disk, build, sign, verify, and encode
// both projections. This is the check that would have caught a catalog whose
// declared hashes drifted from the artifacts it names.
//
// Skips when the artifact tree is absent so the suite stays runnable anywhere.
func TestRealCatalogRoundTrip(t *testing.T) {
	const (
		catalogPath  = "/private/tmp/claude-501/-Users-tj-software-spacedatanetwork-stack/9fa4dc72-147f-47ee-8180-79e64571c503/scratchpad/modules-catalog.json"
		artifactRoot = "/Users/tj/software/spacedatanetwork-stack/.worktrees/hermes-mod-loadout"
	)
	if _, err := os.Stat(catalogPath); err != nil {
		t.Skip("real catalog not present")
	}
	if _, err := os.Stat(artifactRoot); err != nil {
		t.Skip("module artifact tree not present")
	}

	cf, err := LoadCatalog(catalogPath, artifactRoot)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if len(cf.Entries) != 67 {
		t.Fatalf("census drift: expected 67 entries, got %d", len(cf.Entries))
	}

	pub, priv, _ := ed25519.GenerateKey(nil)
	trust := TrustAnchor{
		ProviderDomain:     "sdn.spaceaware.io",
		NodePeerID:         "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",
		SigningPublicKey:   hex.EncodeToString(pub),
		SigningKeyPath:     "m/44'/0'/0'/0'/0'",
		SignatureAlgorithm: "ed25519",
	}
	m, err := BuildManifest(cf, trust, 1,
		"https://sdn.spaceaware.io"+Path, time.Now(), 30*24*time.Hour, edSigner{priv})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}

	sig, err := hex.DecodeString(m.Signature)
	if err != nil {
		t.Fatalf("signature hex: %v", err)
	}
	if !ed25519.Verify(pub, []byte(m.SignedStatement), sig) {
		t.Fatal("real-catalog manifest signature does not verify")
	}
	if err := VerifyStatement(m); err != nil {
		t.Fatalf("statement rebuild mismatch: %v", err)
	}

	bin, err := MarshalBinary(m)
	if err != nil {
		t.Fatalf("binary encode: %v", err)
	}
	if string(bin[8:12]) != "$PMM" {
		t.Fatal("binary projection lost the $PMM file identifier")
	}
	if _, err := MarshalJSON(m, cf.Browse); err != nil {
		t.Fatalf("json encode: %v", err)
	}

	// Every closed entry must be metadata-only; no anonymous byte path.
	closed := 0
	for _, e := range m.Modules {
		if e.AccessPolicy == "ENTITLED" {
			closed++
			if e.ArtifactPath != "" {
				t.Fatalf("closed module %s leaks an anonymous ARTIFACT_PATH", e.ModuleID)
			}
		}
	}
	if closed != 15 {
		t.Fatalf("expected 15 closed entries, got %d", closed)
	}

	// The catalog is INPUT: no local disk path may survive into any projection.
	jsonBytes, err := MarshalJSON(m, cf.Browse)
	if err != nil {
		t.Fatalf("json encode: %v", err)
	}
	if strings.Contains(string(jsonBytes), "source_artifact") ||
		strings.Contains(string(jsonBytes), artifactRoot) {
		t.Fatal("served JSON leaks the provider's local artifact layout")
	}

	// Emit the fixture the storefront consumes, straight from the real encoder,
	// so the UI is never developed against a hand-written shape that drifts.
	if out := os.Getenv("PMM_FIXTURE_OUT"); out != "" {
		if err := os.WriteFile(out, jsonBytes, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("wrote fixture to %s (%d bytes)", out, len(jsonBytes))
	}
	t.Logf("real catalog: %d entries, %d closed, %d binary bytes", len(m.Modules), closed, len(bin))
}
