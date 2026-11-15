package keys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailureHintKnowsAMnemonicOutsideTheKeyDir: the "no identity exists"
// hint looked only at keyDir/{mnemonic,node.key}. A node reading its mnemonic
// from SDN_MNEMONIC_FILE, whose decrypt had just FAILED, was then told that
// nothing failed to open and to create a new password file. An unknown key
// directory ("", a config that did not load) is not "nothing here" either.
func TestFailureHintKnowsAMnemonicOutsideTheKeyDir(t *testing.T) {
	keyDir := t.TempDir() // no mnemonic, no node.key
	elsewhere := filepath.Join(t.TempDir(), "secrets", "mnemonic")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o700); err != nil {
		t.Fatal(err)
	}
	enc, err := EncryptMnemonic(testMnemonic, "throwaway-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere, enc, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SDN_MNEMONIC_FILE", elsewhere)
	if !HasSealedIdentity(keyDir) {
		t.Fatal("SDN_MNEMONIC_FILE names an existing mnemonic: the node has an identity")
	}
	if hint := DerivationFailureHint(keyDir); strings.Contains(hint, "no node identity exists") ||
		strings.Contains(hint, "nothing failed to open") {
		t.Fatalf("a mnemonic at SDN_MNEMONIC_FILE failed to open; the hint must not say none exists:\n%s", hint)
	}

	t.Setenv("SDN_MNEMONIC_FILE", "")
	if hint := DerivationFailureHint(""); strings.Contains(hint, "no node identity exists") {
		t.Fatalf("an unknown key directory must not be reported as empty:\n%s", hint)
	}
	if !HasSealedIdentity("") {
		t.Fatal("an unknown key directory must count as possibly holding an identity")
	}
	if HasSealedIdentity(keyDir) {
		t.Fatal("an empty key directory with no SDN_MNEMONIC_FILE has no identity")
	}
}

// TestFailureHintNamesTheGeneratedKeyPasswordFile: the container entrypoint
// and the .deb/.rpm unit export SDN_KEY_PASSWORD_FILE to the daemon only. A
// command run beside them (docker exec, sudo -u sdn) is refused the machine
// key or decrypts with the wrong one; the hint must name the file and how to
// pass it, not leave the operator guessing.
func TestFailureHintNamesTheGeneratedKeyPasswordFile(t *testing.T) {
	t.Setenv("SDN_MNEMONIC_FILE", "")
	data := t.TempDir()
	keyDir := filepath.Join(data, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sealPlaceholderIdentity(t, keyDir)

	if hint := DerivationFailureHint(keyDir); strings.Contains(hint, "generated key password file") {
		t.Fatalf("no file beside the key dir: nothing to name:\n%s", hint)
	}
	if got := GeneratedKeyPasswordFile(keyDir); got != "" {
		t.Fatalf("GeneratedKeyPasswordFile = %q with no file", got)
	}

	generated := filepath.Join(data, "key-password")
	if err := os.WriteFile(generated, []byte(strings.Repeat("ab", 32)), 0o400); err != nil {
		t.Fatal(err)
	}
	if got := GeneratedKeyPasswordFile(keyDir); got != generated {
		t.Fatalf("GeneratedKeyPasswordFile = %q, want %q", got, generated)
	}
	hint := DerivationFailureHint(keyDir)
	for _, want := range []string{
		"generated key password file: " + generated,
		"SDN_KEY_PASSWORD_FILE=" + generated,
		"/app/entrypoint.sh",
		"sudo -u sdn env SDN_KEY_PASSWORD_FILE=" + generated,
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint must contain %q:\n%s", want, hint)
		}
	}
	if strings.Contains(hint, strings.Repeat("ab", 32)) {
		t.Fatal("the hint must never print the password")
	}
}

// TestFreshNodeRecipeSetsTheUmaskFirst: the recipe in the fresh-node hint
// created the file under the default umask and chmod-ed it afterwards, so it
// was world-readable for a moment (and a descriptor opened then keeps
// reading it).
func TestFreshNodeRecipeSetsTheUmaskFirst(t *testing.T) {
	hint := noIdentityHintFor(t.TempDir(), FingerprintCheck{Status: FingerprintRefused, User: "sdn", Reason: "refused"})
	umask := strings.Index(hint, "umask 077")
	create := strings.Index(hint, "> key-password")
	if umask < 0 || create < 0 || umask > create {
		t.Fatalf("the recipe must set umask 077 before it creates the file:\n%s", hint)
	}
}
