package keys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveDefaultPasswordDeterministic(t *testing.T) {
	a := DeriveDefaultPassword()
	b := DeriveDefaultPassword()
	if a != b {
		t.Fatal("DeriveDefaultPassword must be deterministic across calls on the same machine")
	}
	if len(a) != 32 {
		t.Fatalf("expected 32-byte derived key, got %d", len(a))
	}
}

func TestHardwareFingerprintExcludesHostname(t *testing.T) {
	fp := hardwareFingerprint()
	host, _ := os.Hostname()
	if host != "" && contains(fp, host) {
		t.Fatalf("hardware fingerprint must not contain the hostname %q: %q", host, fp)
	}
	if !contains(fp, "arch=") || !contains(fp, "os=") {
		t.Fatalf("fingerprint missing baseline attributes: %q", fp)
	}
}

func TestDefaultAndLegacyDiffer(t *testing.T) {
	if DeriveDefaultPassword() == DeriveLegacyPassword() {
		t.Fatal("hardware-derived and legacy hostname-derived passwords must differ")
	}
}

// TestLegacyMigrationRoundTrip proves the migration path: a mnemonic encrypted
// under the legacy hostname-based key can be decrypted with the legacy password
// and re-encrypted under the hardware-derived key, and cannot be decrypted with
// the new key beforehand (which is exactly what triggers migration).
func TestLegacyMigrationRoundTrip(t *testing.T) {
	const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

	legacyEnc, err := EncryptMnemonic(mnemonic, DeriveLegacyPassword())
	if err != nil {
		t.Fatalf("encrypt with legacy password: %v", err)
	}

	if _, err := DecryptMnemonic(legacyEnc, DeriveDefaultPassword()); err == nil {
		t.Fatal("legacy ciphertext must NOT decrypt under the hardware key (else migration never triggers)")
	}

	got, err := DecryptMnemonic(legacyEnc, DeriveLegacyPassword())
	if err != nil {
		t.Fatalf("decrypt with legacy password: %v", err)
	}
	if got != mnemonic {
		t.Fatalf("legacy decrypt mismatch: %q", got)
	}

	newEnc, err := EncryptMnemonic(got, DeriveDefaultPassword())
	if err != nil {
		t.Fatalf("re-encrypt with hardware key: %v", err)
	}
	back, err := DecryptMnemonic(newEnc, DeriveDefaultPassword())
	if err != nil {
		t.Fatalf("decrypt re-encrypted with hardware key: %v", err)
	}
	if back != mnemonic {
		t.Fatalf("post-migration mismatch: %q", back)
	}
}

func TestHostnameCanary(t *testing.T) {
	dir := t.TempDir()

	first, err := CheckAndUpdateHostnameCanary(dir)
	if err != nil {
		t.Fatalf("first canary check: %v", err)
	}
	if !first.FirstRun || first.Changed {
		t.Fatalf("expected first run, no change; got %+v", first)
	}

	second, err := CheckAndUpdateHostnameCanary(dir)
	if err != nil {
		t.Fatalf("second canary check: %v", err)
	}
	if second.FirstRun || second.Changed {
		t.Fatalf("expected unchanged on second run; got %+v", second)
	}

	// Simulate a rename by planting a different stored hash.
	if err := os.WriteFile(filepath.Join(dir, hostnameCanaryFile), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatalf("plant canary: %v", err)
	}
	changed, err := CheckAndUpdateHostnameCanary(dir)
	if err != nil {
		t.Fatalf("changed canary check: %v", err)
	}
	if !changed.Changed || changed.PreviousHash != "deadbeef" {
		t.Fatalf("expected detected change from deadbeef; got %+v", changed)
	}
}

func contains(haystack, needle string) bool {
	return needle != "" && len(needle) <= len(haystack) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
