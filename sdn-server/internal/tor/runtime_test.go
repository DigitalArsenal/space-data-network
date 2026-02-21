package tor

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestDeriveHiddenServiceSeedDeterministic(t *testing.T) {
	in := []byte("node-private-key-material")

	a := deriveHiddenServiceSeed(in)
	b := deriveHiddenServiceSeed(in)
	if a != b {
		t.Fatal("expected deterministic seed for identical input")
	}

	c := deriveHiddenServiceSeed([]byte("different"))
	if a == c {
		t.Fatal("expected different seed for different input")
	}
}

func TestWriteDeterministicHiddenServiceKeys(t *testing.T) {
	dir := t.TempDir()
	host, err := writeDeterministicHiddenServiceKeys(dir, []byte("node-key"))
	if err != nil {
		t.Fatalf("writeDeterministicHiddenServiceKeys failed: %v", err)
	}

	if ok, _ := regexp.MatchString(`^[a-z2-7]{56}\.onion$`, host); !ok {
		t.Fatalf("unexpected onion host format: %q", host)
	}

	secretPath := filepath.Join(dir, "hs_ed25519_secret_key")
	publicPath := filepath.Join(dir, "hs_ed25519_public_key")

	secretBytes := mustReadFile(t, secretPath)
	publicBytes := mustReadFile(t, publicPath)

	if got, want := len(secretBytes), 32+ed25519.PrivateKeySize; got != want {
		t.Fatalf("secret key file length = %d, want %d", got, want)
	}
	if got, want := len(publicBytes), 32+ed25519.PublicKeySize; got != want {
		t.Fatalf("public key file length = %d, want %d", got, want)
	}

	host2, err := writeDeterministicHiddenServiceKeys(dir, []byte("node-key"))
	if err != nil {
		t.Fatalf("writeDeterministicHiddenServiceKeys (second run) failed: %v", err)
	}
	if host != host2 {
		t.Fatalf("onion hostname changed across deterministic writes: %q != %q", host, host2)
	}
}

func TestOnionAddressFromPublicKey(t *testing.T) {
	seed := deriveHiddenServiceSeed([]byte("key-material"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)

	host, err := onionAddressFromPublicKey(pub)
	if err != nil {
		t.Fatalf("onionAddressFromPublicKey failed: %v", err)
	}
	if len(host) != 62 { // 56 chars + ".onion"
		t.Fatalf("unexpected onion hostname length: %d", len(host))
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
