package sigdomain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestStatementShape(t *testing.T) {
	sum := sha256.Sum256([]byte("portable payload"))
	stmt, err := Statement(DomainModulePublicationV1, sum[:])
	if err != nil {
		t.Fatalf("Statement() error = %v", err)
	}
	want := append(append([]byte(DomainModulePublicationV1), 0), sum[:]...)
	if !bytes.Equal(stmt, want) {
		t.Fatalf("Statement() = %x, want %x", stmt, want)
	}
	if len(stmt) != len(DomainModulePublicationV1)+1+ContentHashSize {
		t.Fatalf("Statement() length = %d, want %d", len(stmt), len(DomainModulePublicationV1)+1+ContentHashSize)
	}
}

func TestStatementRefusesUnregisteredDomain(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	for _, domain := range []string{"", "SDN-MODULE-PUBLICATION-V2", "sdn-module-publication-v1", "SDN-DPM-PNM"} {
		if _, err := Statement(domain, sum[:]); !errors.Is(err, ErrUnregisteredDomain) {
			t.Fatalf("Statement(%q) error = %v, want ErrUnregisteredDomain", domain, err)
		}
	}
}

func TestStatementRefusesWrongWidthHash(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	hexText := []byte("2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881")
	for name, hash := range map[string][]byte{
		"empty":     nil,
		"short":     sum[:16],
		"long":      append(append([]byte(nil), sum[:]...), 0),
		"hex-text":  hexText, // 64 bytes of ASCII: the classic raw-vs-hex mixup
		"truncated": sum[:31],
	} {
		if _, err := Statement(DomainModulePublicationV1, hash); err == nil {
			t.Fatalf("Statement(%s) error = nil, want a width error", name)
		}
	}
}

// TestStatementSpacesAreDisjoint is the whole point of the package: the SAME
// content hash under two registered domains must produce two different
// preimages, so a signature minted for one domain never verifies for the other.
func TestStatementSpacesAreDisjoint(t *testing.T) {
	sum := sha256.Sum256([]byte("same bytes, two meanings"))

	moduleStmt, err := Statement(DomainModulePublicationV1, sum[:])
	if err != nil {
		t.Fatalf("Statement(module) error = %v", err)
	}
	updateStmt, err := Statement(DomainUpdateManifestV1, sum[:])
	if err != nil {
		t.Fatalf("Statement(update) error = %v", err)
	}
	if bytes.Equal(moduleStmt, updateStmt) {
		t.Fatal("module and update statements over the same hash are identical — domain separation is not working")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	moduleSig := ed25519.Sign(priv, moduleStmt)
	if !ed25519.Verify(pub, moduleStmt, moduleSig) {
		t.Fatal("module signature does not verify over its own statement")
	}
	if ed25519.Verify(pub, updateStmt, moduleSig) {
		t.Fatal("a module signature verified as an update-manifest signature — cross-domain replay is possible")
	}
	// And the case that motivated the package: the bare digest form used by
	// internal/storage/manifest.go:815 must not accept a domain-separated
	// signature either.
	if ed25519.Verify(pub, sum[:], moduleSig) {
		t.Fatal("a module signature verified over the bare digest — the dataset-publication space is reachable")
	}
}

func TestRegistryIsClosed(t *testing.T) {
	got := Domains()
	want := []string{DomainModulePublicationV1, DomainUpdateManifestV1, DomainUpdateSignalV1}
	if len(got) != len(want) {
		t.Fatalf("Domains() = %v, want exactly %v — a new signed statement kind must be a reviewed change to sigdomain.go", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Domains()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, d := range got {
		if Describe(d) == "" {
			t.Fatalf("registered domain %q has no description", d)
		}
	}
	if Registered("SDN-NOT-A-DOMAIN") {
		t.Fatal("Registered() accepted an unregistered domain")
	}
}
