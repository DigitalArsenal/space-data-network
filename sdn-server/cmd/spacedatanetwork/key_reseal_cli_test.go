package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spf13/cobra"
)

// The CLI reads the daemon's sealed-node-key marker. If the daemon ever changes
// it, this fails here rather than silently reporting "not encrypted at rest"
// about a file that very much is.
func TestNodeKeyEncPrefixMatchesTheDaemonMarker(t *testing.T) {
	if nodeKeyEncPrefix != "sdnkey1\x00" {
		t.Fatalf("nodeKeyEncPrefix = %q; it must match internal/node's nodeKeyEncMagic", nodeKeyEncPrefix)
	}
}

func TestKeyResealRegisteredUnderKey(t *testing.T) {
	requireCommand(t, []string{"key", "reseal"}, "reseal")
	if !strings.Contains(keyResealCmd.UsageString(), "--confirm") {
		t.Fatalf("reseal help must document --confirm:\n%s", keyResealCmd.UsageString())
	}
}

// Re-sealing is the supported way to make an identity portable, so the round
// trip has to be exactly that: the material opens under the NEW password
// afterwards, and the bytes inside are unchanged. A re-seal that altered the
// mnemonic would change the PeerID and lose the node.
func TestKeyResealMovesMaterialToTheConfiguredPassword(t *testing.T) {
	// Not a BIP-39 wordlist run (the repo mnemonic guard rejects those in
	// committed files); the test only needs an opaque string to seal.
	const phrase = "seed-phrase-placeholder-for-reseal-cli-tests"
	const destination = "portable-file-fed-password"

	machinePassword, err := keys.DeriveDefaultPassword()
	if err != nil {
		t.Skipf("machine-derived password unavailable on this host: %v", err)
	}

	dir := t.TempDir()
	mnemonicPath := filepath.Join(dir, "mnemonic")

	// Sealed under the machine-derived password, exactly like a node that has
	// never been told about a password file.
	sealed, err := keys.EncryptMnemonic(phrase, machinePassword)
	if err != nil {
		t.Fatalf("seal mnemonic: %v", err)
	}
	if err := os.WriteFile(mnemonicPath, sealed, 0o600); err != nil {
		t.Fatalf("write mnemonic: %v", err)
	}

	// The operator action: recover with the machine password, re-seal under the
	// configured one.
	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	recovered, _, err := keys.DecryptMnemonicAnyScheme(data)
	if err != nil {
		t.Fatalf("machine-derived recovery failed: %v", err)
	}
	resealed, err := keys.EncryptMnemonic(strings.TrimSpace(recovered), destination)
	if err != nil {
		t.Fatalf("re-seal: %v", err)
	}
	if err := os.WriteFile(mnemonicPath, resealed, 0o600); err != nil {
		t.Fatalf("write re-sealed mnemonic: %v", err)
	}

	// It now opens with the destination password — the property that lets the
	// identity move to another machine.
	reopened, err := keys.DecryptMnemonic(resealed, destination)
	if err != nil {
		t.Fatalf("re-sealed mnemonic does not open with the configured password: %v", err)
	}
	if strings.TrimSpace(reopened) != phrase {
		t.Fatal("re-seal changed the mnemonic — the identity would be lost")
	}
	// And no longer with the old machine-derived one, which is the point of
	// moving custody rather than adding to it.
	if _, err := keys.DecryptMnemonic(resealed, machinePassword); err == nil {
		t.Fatal("re-sealed mnemonic still opens with the machine-derived password")
	}
}

// Without --confirm nothing is written: this rewrites identity files, and a
// typo must not be enough to do that.
func TestKeyResealRefusesWithoutConfirm(t *testing.T) {
	original := keyResealConfirm
	t.Cleanup(func() { keyResealConfirm = original })
	keyResealConfirm = false

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	err := runKeyReseal(cmd, nil)
	if err == nil {
		t.Fatal("reseal without --confirm must refuse")
	}
	// The refusal is only useful if it says what it would have touched, or why
	// it could not tell.
	msg := err.Error()
	if !strings.Contains(msg, "--confirm") && !strings.Contains(msg, "key password") && !strings.Contains(msg, "config") {
		t.Fatalf("refusal is not actionable: %v", err)
	}
}
