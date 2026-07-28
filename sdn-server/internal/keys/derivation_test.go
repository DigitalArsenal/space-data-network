package keys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testMnemonic stands in for a seed phrase. It is deliberately NOT a run of
// BIP-39 wordlist entries: the repo's mnemonic guard (scripts/check-no-mnemonics.sh)
// rejects those in committed files, and these tests only need an opaque string
// to seal and re-open.
const testMnemonic = "seed-phrase-placeholder-for-at-rest-derivation-tests"

// TestV3IncludesMachineNameExcludesVolatile pins the owner ruling ("a
// deterministic key created from the machine name and local machine metadata")
// AND the safety amendment: the resize-volatile attributes that orphaned v2
// secrets must never appear in the v3 fingerprint.
func TestV3IncludesMachineNameExcludesVolatile(t *testing.T) {
	fp, sources := machineFingerprintV3()

	if host := hostnameForFingerprint(); host != "" {
		if !strings.Contains(fp, "host="+host) {
			t.Fatalf("v3 fingerprint must carry the machine name %q: %q", host, fp)
		}
	}

	// A droplet resize rewrites RAM/CPU; an OS rebuild rewrites machine-id.
	// Any of these inside the key means a resize or rebuild bricks the node.
	for _, banned := range []string{"memtotal=", "ncpu=", "cpu=", "machineid=", "model="} {
		if strings.Contains(fp, banned) {
			t.Fatalf("v3 fingerprint must not include volatile attribute %q: %q", banned, fp)
		}
	}
	for _, banned := range []string{"memtotal", "ncpu", "cpu", "machineid", "model"} {
		for _, s := range sources {
			if s == banned {
				t.Fatalf("v3 sources must not include volatile source %q: %v", banned, sources)
			}
		}
	}
	if !strings.HasPrefix(fp, "v3|") {
		t.Fatalf("v3 fingerprint must be scheme-tagged: %q", fp)
	}
}

// TestV2FingerprintPreserved guards the migration ladder: v2 must keep
// producing the exact same string, or every v2 node in the fleet is orphaned.
func TestV2FingerprintPreserved(t *testing.T) {
	fp := hardwareFingerprint()
	if !strings.HasPrefix(fp, "v2|arch=") {
		t.Fatalf("v2 fingerprint layout changed — this orphans existing nodes: %q", fp)
	}
	if !strings.Contains(fp, "ncpu=") {
		t.Fatalf("v2 fingerprint must retain ncpu for byte-fidelity: %q", fp)
	}
	if a, b := hardwareFingerprint(), hardwareFingerprint(); a != b {
		t.Fatal("v2 fingerprint must be deterministic")
	}
}

func TestDeriveDefaultPasswordIsV3AndDeterministic(t *testing.T) {
	a, sourcesA := DeriveDefaultPasswordWithSources()
	b, sourcesB := DeriveDefaultPasswordWithSources()
	if a != b {
		t.Fatal("v3 derivation must be deterministic on the same machine")
	}
	if len(a) != 32 {
		t.Fatalf("expected a 32-byte key, got %d", len(a))
	}
	if len(sourcesA) != len(sourcesB) {
		t.Fatal("source list must be deterministic")
	}
	if a == DeriveV2Password() {
		t.Fatal("v3 and v2 passwords must differ (distinct salts and inputs)")
	}
	if a == DeriveLegacyPassword() {
		t.Fatal("v3 and legacy passwords must differ")
	}
}

// TestRoundTripUnderV3 is the base case: seal and open under the default
// machine-derived key.
func TestRoundTripUnderV3(t *testing.T) {
	enc, err := EncryptMnemonic(testMnemonic, DeriveDefaultPassword())
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !IsMnemonicEncrypted(enc) {
		t.Fatal("sealed mnemonic must not look like plaintext")
	}
	got, scheme, err := DecryptMnemonicAnyScheme(enc)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != testMnemonic {
		t.Fatalf("round-trip mismatch: %q", got)
	}
	if scheme != SchemeV3 {
		t.Fatalf("expected the current scheme, got %q", scheme)
	}
}

// TestMigrationFromV2 is the vm-orbit-det-01 case: a mnemonic sealed under the
// old hardware derivation must still open, and must report v2 so the caller
// re-seals it. This is what keeps the identity alive across the upgrade.
func TestMigrationFromV2(t *testing.T) {
	enc, err := EncryptMnemonic(testMnemonic, DeriveV2Password())
	if err != nil {
		t.Fatalf("seal under v2: %v", err)
	}
	if _, err := DecryptMnemonic(enc, DeriveDefaultPassword()); err == nil {
		t.Fatal("v2 ciphertext must not open under v3 directly (else migration never triggers)")
	}
	got, scheme, err := DecryptMnemonicAnyScheme(enc)
	if err != nil {
		t.Fatalf("v2 mnemonic must open via the candidate ladder: %v", err)
	}
	if got != testMnemonic || scheme != SchemeV2 {
		t.Fatalf("expected v2 recovery of the original mnemonic, got %q/%q", got, scheme)
	}
}

// TestMigrationFromLegacy covers the oldest generation.
func TestMigrationFromLegacy(t *testing.T) {
	enc, err := EncryptMnemonic(testMnemonic, DeriveLegacyPassword())
	if err != nil {
		t.Fatalf("seal under legacy: %v", err)
	}
	got, scheme, err := DecryptMnemonicAnyScheme(enc)
	if err != nil {
		t.Fatalf("legacy mnemonic must open via the candidate ladder: %v", err)
	}
	if got != testMnemonic || scheme != SchemeLegacy {
		t.Fatalf("expected legacy recovery, got %q/%q", got, scheme)
	}
}

// TestSecretMigrationLadder proves the node.key path (raw bytes) has the same
// migration behaviour as the mnemonic path.
func TestSecretMigrationLadder(t *testing.T) {
	secret := []byte{0x08, 0x02, 0x12, 0x40, 0xde, 0xad, 0xbe, 0xef}
	enc, err := EncryptSecret(secret, DeriveV2Password())
	if err != nil {
		t.Fatalf("seal secret under v2: %v", err)
	}
	got, scheme, err := DecryptSecretAnyScheme(enc)
	if err != nil {
		t.Fatalf("v2 secret must open via the ladder: %v", err)
	}
	if string(got) != string(secret) || scheme != SchemeV2 {
		t.Fatalf("secret ladder mismatch: %x/%q", got, scheme)
	}
}

// TestWrongMachineFailsClosed is the whole point of fail-closed: a secret
// sealed on a DIFFERENT machine must not open under any known generation, and
// must not be silently treated as absent.
func TestWrongMachineFailsClosed(t *testing.T) {
	enc, err := EncryptMnemonic(testMnemonic, "a-completely-different-machines-derived-key")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, _, err := DecryptMnemonicAnyScheme(enc); err == nil {
		t.Fatal("a foreign machine's ciphertext must NOT open — fail closed")
	}
	if _, _, err := DecryptSecretAnyScheme(enc); err == nil {
		t.Fatal("a foreign machine's secret must NOT open — fail closed")
	}
}

// TestPassphraseOverrideBeatsMachineDerivation proves the escape hatch: an
// operator passphrase seals and opens independently of every machine source,
// which is the only path that survives a destroy+recreate.
func TestPassphraseOverrideBeatsMachineDerivation(t *testing.T) {
	const passphrase = "operator-supplied-secret-from-the-0600-file"

	enc, err := EncryptMnemonic(testMnemonic, passphrase)
	if err != nil {
		t.Fatalf("seal under passphrase: %v", err)
	}
	if _, _, err := DecryptMnemonicAnyScheme(enc); err == nil {
		t.Fatal("passphrase-sealed material must not open under machine derivation")
	}
	got, err := DecryptMnemonic(enc, passphrase)
	if err != nil {
		t.Fatalf("open under passphrase: %v", err)
	}
	if got != testMnemonic {
		t.Fatalf("passphrase round-trip mismatch: %q", got)
	}
}

// TestDerivationRecordRoundTripAndHint proves a decrypt failure is
// DIAGNOSABLE: the record names the sources that sealed the file (and never
// their values or any key material), and the hint names the recovery routes.
func TestDerivationRecordRoundTripAndHint(t *testing.T) {
	keyDir := t.TempDir()

	_, sources := DeriveDefaultPasswordWithSources()
	if err := WriteDerivationRecord(keyDir, SchemeV3, sources); err != nil {
		t.Fatalf("write record: %v", err)
	}

	rec, ok := ReadDerivationRecord(keyDir)
	if !ok {
		t.Fatal("record must read back")
	}
	if rec.Scheme != string(SchemeV3) || len(rec.Sources) != len(sources) {
		t.Fatalf("record round-trip mismatch: %+v", rec)
	}

	blob, err := os.ReadFile(filepath.Join(keyDir, derivationRecordFile))
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	// The record is a diagnostic aid, not a secret leak.
	if host := hostnameForFingerprint(); host != "" && strings.Contains(string(blob), host) {
		t.Fatalf("derivation record must not contain the raw hostname value: %s", blob)
	}
	if strings.Contains(string(blob), DeriveDefaultPassword()) {
		t.Fatal("derivation record must never contain key material")
	}

	hint := DerivationFailureHint(keyDir)
	for _, want := range []string{"SDN_KEY_PASSWORD_FILE", "escrow", "will NOT mint"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("failure hint must mention %q: %s", want, hint)
		}
	}
}

// TestDerivationRecordAbsentIsNotFatal — a node sealed before records existed
// must still produce a usable hint.
func TestDerivationRecordAbsentIsNotFatal(t *testing.T) {
	keyDir := t.TempDir()
	if _, ok := ReadDerivationRecord(keyDir); ok {
		t.Fatal("no record should be reported when none was written")
	}
	if hint := DerivationFailureHint(keyDir); !strings.Contains(hint, "no derivation record") {
		t.Fatalf("hint must explain the missing record: %s", hint)
	}
}

// TestHostnameNormalization proves an FQDN/case/trailing-dot change does not
// re-key the node — only a genuine rename does.
func TestHostnameNormalization(t *testing.T) {
	host := hostnameForFingerprint()
	if host != strings.ToLower(host) {
		t.Fatalf("hostname must be lowercased: %q", host)
	}
	if strings.Contains(host, ".") {
		t.Fatalf("only the leftmost label may be used, got %q", host)
	}
}
