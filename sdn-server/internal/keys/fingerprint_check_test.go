package keys

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFingerprintCheckVerdicts: the three verdicts an installer branches on,
// from the same canonicaliser the derivation uses, and never a source value
// in what is reported.
func TestFingerprintCheckVerdicts(t *testing.T) {
	const uuidValue = "4C4C4544-SECRET-UUID-VALUE"
	const hostValue = "host-value-never-printed"

	_, okInputs, err := fingerprintV4From(hostValue, "svc", []sourceReading{{key: "platuuid", value: uuidValue}})
	if err != nil {
		t.Fatal(err)
	}
	ok := classifyFingerprint(okInputs, nil)
	if ok.Status != FingerprintOK || ok.Err != nil || ok.User != "svc" {
		t.Fatalf("readable source: want ok for svc, got %+v", ok)
	}

	_, absentInputs, err := fingerprintV4From(hostValue, "svc", []sourceReading{{key: "platuuid", absent: true}})
	if err != nil {
		t.Fatal(err)
	}
	absent := classifyFingerprint(absentInputs, nil)
	if absent.Status != FingerprintAbsent || absent.Err != nil || !strings.Contains(absent.Reason, "platuuid") {
		t.Fatalf("missing source: want absent naming platuuid, got %+v", absent)
	}

	refusal := fmt.Errorf("machine fingerprint (v4): /sys/class/dmi/id/product_uuid exists but cannot be read (%w)", fs.ErrPermission)
	refused := classifyFingerprint(DerivationInputs{}, refusal)
	if refused.Status != FingerprintRefused || !errors.Is(refused.Err, fs.ErrPermission) {
		t.Fatalf("unreadable source: want refused carrying the error, got %+v", refused)
	}
	if !strings.Contains(refused.Reason, "cannot be read") {
		t.Fatalf("refusal reason must say what was refused: %q", refused.Reason)
	}
	if runtime.GOOS == "linux" && !strings.Contains(refused.Why, "0400") {
		t.Fatalf("on Linux a permission refusal must explain the kernel's 0400: %q", refused.Why)
	}

	for _, c := range []FingerprintCheck{ok, absent, refused} {
		for _, secret := range []string{uuidValue, hostValue} {
			if strings.Contains(c.Reason, secret) || strings.Contains(c.Why, secret) {
				t.Fatalf("verdict %s leaked a source value %q: %+v", c.Status, secret, c)
			}
		}
	}
}

// TestFingerprintCheckAgreesWithDerivation: on whatever machine runs the test,
// the check says refused exactly when DeriveDefaultPassword refuses — the
// installers' decision and the node's behaviour can never disagree.
func TestFingerprintCheckAgreesWithDerivation(t *testing.T) {
	check := CheckMachineFingerprint()
	_, derr := DeriveDefaultPassword()
	if (check.Status == FingerprintRefused) != (derr != nil) {
		t.Fatalf("check says %s but DeriveDefaultPassword error = %v", check.Status, derr)
	}
	switch check.Status {
	case FingerprintOK, FingerprintAbsent, FingerprintRefused:
	default:
		t.Fatalf("unknown verdict %q", check.Status)
	}
}

// sealPlaceholderIdentity puts a sealed mnemonic in keyDir, under a throwaway
// password: the "a secret exists and did not open" premise of the
// DerivationFailureHint tests.
func sealPlaceholderIdentity(t *testing.T, keyDir string) {
	t.Helper()
	enc, err := EncryptMnemonic(testMnemonic, "throwaway-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "mnemonic"), enc, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFailureHintOnFreshNodeSaysNoIdentityYet: a node that refused to CREATE
// its identity must not be told a secret failed to open. It is told there is
// no identity yet, that this host's fingerprint is refused and why, and the
// two ways to proceed.
func TestFailureHintOnFreshNodeSaysNoIdentityYet(t *testing.T) {
	keyDir := t.TempDir()
	refusal := fmt.Errorf("machine fingerprint (v4): /sys/class/dmi/id/product_uuid exists but cannot be read (open: %w)", fs.ErrPermission)
	check := FingerprintCheck{
		Status: FingerprintRefused, User: "sdn", Reason: refusal.Error(),
		Why: "the Linux kernel creates /sys/class/dmi/id/product_uuid readable by root only (mode 0400)", Err: refusal,
	}
	hint := noIdentityHintFor(keyDir, check)
	for _, want := range []string{
		"no node identity exists in " + keyDir,
		"nothing failed to open",
		"REFUSED",
		`"sdn"`,
		"cannot be read",
		"why: the Linux kernel",
		"SDN_KEY_PASSWORD_FILE",
		"security.key_password_file",
		"chmod 0400",
		"back",
		"read the source named above",
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("fresh-node hint must contain %q:\n%s", want, hint)
		}
	}
	for _, never := range []string{"did not open this secret", "sealed under:", "RECOVERY"} {
		if strings.Contains(hint, never) {
			t.Fatalf("fresh-node hint must not contain %q (there is no secret):\n%s", never, hint)
		}
	}

	// A refusal that is NOT the machine's (the fingerprint is fine; e.g. a
	// configured password file is missing) is not blamed on the machine.
	elsewhere := noIdentityHintFor(keyDir, FingerprintCheck{Status: FingerprintOK, User: "sdn", Reason: `derivable for account "sdn"`})
	if !strings.Contains(elsewhere, "no node identity exists") || strings.Contains(elsewhere, "REFUSED") ||
		!strings.Contains(elsewhere, "not the machine's") {
		t.Fatalf("hint with an available fingerprint must point at the password source:\n%s", elsewhere)
	}
}

// TestFailureHintDispatchesOnSealedIdentity: DerivationFailureHint itself
// tells the two situations apart, for every caller.
func TestFailureHintDispatchesOnSealedIdentity(t *testing.T) {
	fresh := t.TempDir()
	if hint := DerivationFailureHint(fresh); strings.Contains(hint, "did not open this secret") ||
		!strings.Contains(hint, "no node identity exists") {
		t.Fatalf("empty key dir: the hint must say there is no identity yet:\n%s", hint)
	}
	if HasSealedIdentity(fresh) {
		t.Fatal("an empty key dir has no sealed identity")
	}

	sealed := t.TempDir()
	sealPlaceholderIdentity(t, sealed)
	if !HasSealedIdentity(sealed) {
		t.Fatal("a sealed mnemonic is a sealed identity")
	}
	if hint := DerivationFailureHint(sealed); !strings.Contains(hint, "did not open this secret") {
		t.Fatalf("a sealed mnemonic that did not open keeps the decrypt-failure hint:\n%s", hint)
	}

	nodeKeyOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(nodeKeyOnly, "node.key"), []byte("sdnkey1\x00x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasSealedIdentity(nodeKeyOnly) {
		t.Fatal("a node.key alone is a sealed identity")
	}
}
