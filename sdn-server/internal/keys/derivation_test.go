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

func TestDeriveDefaultPasswordIsV4AndDeterministic(t *testing.T) {
	a, inputsA, err := DeriveDefaultPasswordWithSources()
	if err != nil {
		t.Fatalf("v4 derivation must succeed on a normal host: %v", err)
	}
	b, inputsB, err := DeriveDefaultPasswordWithSources()
	if err != nil {
		t.Fatalf("second derivation: %v", err)
	}
	if a != b {
		t.Fatal("v4 derivation must be deterministic on the same machine+user")
	}
	if len(a) != 32 {
		t.Fatalf("expected a 32-byte key, got %d", len(a))
	}
	if inputsA.Scheme != SchemeV4 {
		t.Fatalf("expected scheme v4, got %q", inputsA.Scheme)
	}
	if inputsA.User == "" || inputsA.User != inputsB.User {
		t.Fatalf("v4 inputs must carry a stable, non-empty user: %q vs %q", inputsA.User, inputsB.User)
	}
	if len(inputsA.States) != len(inputsB.States) {
		t.Fatal("source states must be deterministic")
	}
	if a == DeriveV3Password() {
		t.Fatal("v4 and v3 passwords must differ (distinct salts and inputs)")
	}
	if a == DeriveV2Password() {
		t.Fatal("v4 and v2 passwords must differ")
	}
	if a == DeriveLegacyPassword() {
		t.Fatal("v4 and legacy passwords must differ")
	}
}

// TestV4FingerprintBindsUserAndEncodesAbsence pins the owner ruling (the key
// is bound to (machine, user)) and design requirement 3 (absent is an
// explicit, encoded input — never a silent skip).
func TestV4FingerprintBindsUserAndEncodesAbsence(t *testing.T) {
	fp, inputs, err := machineFingerprintV4()
	if err != nil {
		t.Fatalf("v4 fingerprint on a normal host: %v", err)
	}
	if !strings.HasPrefix(fp, "v4|") {
		t.Fatalf("v4 fingerprint must be scheme-tagged: %q", fp)
	}
	if !strings.Contains(fp, "user="+inputs.User) {
		t.Fatalf("v4 fingerprint must carry the user %q: %q", inputs.User, fp)
	}

	// The canonical form: an absent source changes the fingerprint relative
	// to a present one, and both differ from the source not being listed.
	withValue, _, err := fingerprintV4From("box", "svc", []sourceReading{{key: "platuuid", value: "uuid-1234"}})
	if err != nil {
		t.Fatalf("present reading: %v", err)
	}
	withAbsent, absentInputs, err := fingerprintV4From("box", "svc", []sourceReading{{key: "platuuid", absent: true}})
	if err != nil {
		t.Fatalf("absent reading: %v", err)
	}
	if withValue == withAbsent {
		t.Fatal("present and absent sources must produce different fingerprints")
	}
	if !strings.Contains(withAbsent, "platuuid="+fingerprintAbsentMarker) {
		t.Fatalf("absence must be ENCODED, not skipped: %q", withAbsent)
	}
	found := false
	for _, s := range absentInputs.States {
		if s.Key == "platuuid" && s.State == SourceStateAbsent {
			found = true
		}
	}
	if !found {
		t.Fatalf("absent source must be recorded as explicitly absent: %+v", absentInputs.States)
	}
}

// TestV4RefusesDegradedInputs: empty/unknown inputs are ERRORS, never a
// silently weaker key (the precise v3 defect).
func TestV4RefusesDegradedInputs(t *testing.T) {
	if _, _, err := fingerprintV4From("box", "", nil); err == nil {
		t.Fatal("an empty username must refuse derivation")
	}
	if _, _, err := fingerprintV4From("", "svc", nil); err == nil {
		t.Fatal("an empty hostname must refuse derivation")
	}
}

// TestRoundTripUnderV4 is the base case: seal and open under the default
// machine+user-derived key.
func TestRoundTripUnderV4(t *testing.T) {
	pw, err := DeriveDefaultPassword()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	enc, err := EncryptMnemonic(testMnemonic, pw)
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
	if scheme != SchemeV4 {
		t.Fatalf("expected the current scheme, got %q", scheme)
	}
}

// TestMigrationFromV3 is the fleet-upgrade case for THIS change: every node
// still on the machine-only v3 derivation must open via the ladder (and report
// v3 so the caller re-seals under v4). Without this, shipping v4 orphans the
// fleet — the exact failure class this task exists to end.
func TestMigrationFromV3(t *testing.T) {
	enc, err := EncryptMnemonic(testMnemonic, DeriveV3Password())
	if err != nil {
		t.Fatalf("seal under v3: %v", err)
	}
	pw, err := DeriveDefaultPassword()
	if err != nil {
		t.Fatalf("derive v4: %v", err)
	}
	if _, err := DecryptMnemonic(enc, pw); err == nil {
		t.Fatal("v3 ciphertext must not open under v4 directly (else migration never triggers)")
	}
	got, scheme, err := DecryptMnemonicAnyScheme(enc)
	if err != nil {
		t.Fatalf("v3 mnemonic must open via the candidate ladder: %v", err)
	}
	if got != testMnemonic || scheme != SchemeV3 {
		t.Fatalf("expected v3 recovery of the original mnemonic, got %q/%q", got, scheme)
	}
}

// TestMigrationFromV3Degraded covers the vm-orbit-det-01 privilege fork: a
// mnemonic sealed by a v3 process that could NOT read the platform UUID must
// open via the ladder even when THIS process can (and vice versa is diagnosed,
// not recoverable — the UUID is simply unavailable then).
func TestMigrationFromV3Degraded(t *testing.T) {
	enc, err := EncryptMnemonic(testMnemonic, DeriveV3DegradedPassword())
	if err != nil {
		t.Fatalf("seal under degraded v3: %v", err)
	}
	got, scheme, err := DecryptMnemonicAnyScheme(enc)
	if err != nil {
		t.Fatalf("privilege-degraded v3 material must open via the ladder: %v", err)
	}
	if got != testMnemonic || scheme != SchemeV3 {
		t.Fatalf("expected v3 recovery, got %q/%q", got, scheme)
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
	pw, err := DeriveDefaultPassword()
	if err != nil {
		t.Fatalf("derive current: %v", err)
	}
	if _, err := DecryptMnemonic(enc, pw); err == nil {
		t.Fatal("v2 ciphertext must not open under the current scheme directly (else migration never triggers)")
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

	pw, inputs, err := DeriveDefaultPasswordWithSources()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if err := WriteDerivationRecord(keyDir, inputs); err != nil {
		t.Fatalf("write record: %v", err)
	}

	rec, ok := ReadDerivationRecord(keyDir)
	if !ok {
		t.Fatal("record must read back")
	}
	if rec.Scheme != string(SchemeV4) || len(rec.SourceStates) != len(inputs.States) {
		t.Fatalf("record round-trip mismatch: %+v", rec)
	}
	if rec.User != inputs.User {
		t.Fatalf("record must carry the sealing user (design requirement 5): %+v", rec)
	}
	if len(rec.Sources) == 0 {
		t.Fatalf("legacy present-source list must still be written: %+v", rec)
	}

	blob, err := os.ReadFile(filepath.Join(keyDir, derivationRecordFile))
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	// The record is a diagnostic aid, not a secret leak. (The hostname stays
	// hashed; the USER is recorded verbatim by design — see derivation.go —
	// and on a dev box may coincide with the hostname, so only exclude the
	// hostname when it is not also the username.)
	if host := hostnameForFingerprint(); host != "" && host != strings.ToLower(inputs.User) && strings.Contains(string(blob), host) {
		t.Fatalf("derivation record must not contain the raw hostname value: %s", blob)
	}
	if strings.Contains(string(blob), pw) {
		t.Fatal("derivation record must never contain key material")
	}

	hint := DerivationFailureHint(keyDir)
	for _, want := range []string{"SDN_KEY_PASSWORD_FILE", "escrow", "will NOT mint"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("failure hint must mention %q: %s", want, hint)
		}
	}
}

// TestFailureHintNamesTheUserDiff is design requirement 4 verbatim: "host
// matched, user differs (sealed root, running tjkoury)" would have cost
// nothing — so the hint must say exactly that class of thing.
func TestFailureHintNamesTheUserDiff(t *testing.T) {
	keyDir := t.TempDir()

	_, inputs, err := DeriveDefaultPasswordWithSources()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	sealedAs := inputs
	sealedAs.User = inputs.User + "-someone-else" // sealed under a different account
	if err := WriteDerivationRecord(keyDir, sealedAs); err != nil {
		t.Fatalf("write record: %v", err)
	}

	hint := DerivationFailureHint(keyDir)
	if !strings.Contains(hint, "USER DIFFERS") {
		t.Fatalf("hint must flag the user difference: %s", hint)
	}
	if !strings.Contains(hint, sealedAs.User) || !strings.Contains(hint, inputs.User) {
		t.Fatalf("hint must name BOTH the sealing and the running user: %s", hint)
	}
	if !strings.Contains(hint, "reseal") {
		t.Fatalf("hint must name the deliberate escape (key reseal): %s", hint)
	}
}

// TestFailureHintNamesSourceStateDiff: a source recorded present at seal time
// but absent now (or vice versa) is named, not folded into "cannot decrypt".
func TestFailureHintNamesSourceStateDiff(t *testing.T) {
	keyDir := t.TempDir()

	_, inputs, err := DeriveDefaultPasswordWithSources()
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	flipped := inputs
	flipped.States = append([]SourceState(nil), inputs.States...)
	for i := range flipped.States {
		if flipped.States[i].Key == "platuuid" {
			if flipped.States[i].State == SourceStatePresent {
				flipped.States[i].State = SourceStateAbsent
			} else {
				flipped.States[i].State = SourceStatePresent
			}
		}
	}
	if err := WriteDerivationRecord(keyDir, flipped); err != nil {
		t.Fatalf("write record: %v", err)
	}

	hint := DerivationFailureHint(keyDir)
	if !strings.Contains(hint, `SOURCE "platuuid"`) {
		t.Fatalf("hint must name the flipped source state: %s", hint)
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
