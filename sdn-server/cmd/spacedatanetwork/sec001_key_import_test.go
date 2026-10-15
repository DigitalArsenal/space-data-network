package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// rotationVectorPeerID is the PeerID the node derives from the PUBLIC BIP-39
// test vector, as committed by internal/wasm's rotation vector test.
func rotationVectorPeerID(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../../deployment/identity/testdata/derivation-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		PeerID string `json:"peerId"`
	}
	if err := json.Unmarshal(data, &v); err != nil || v.PeerID == "" {
		t.Fatalf("derivation vector: %v", err)
	}
	return v.PeerID
}

func useHDWalletWASM(t *testing.T) {
	t.Helper()
	wasmPath := strings.TrimSpace(os.Getenv("HD_WALLET_WASM_PATH"))
	if wasmPath == "" {
		wasmPath = filepath.Clean("../../internal/wasm/hd-wallet-wasi.wasm")
	}
	if _, err := os.Stat(wasmPath); err != nil {
		t.Skipf("hd-wallet WASI module unavailable: %v", err)
	}
	t.Setenv("HD_WALLET_WASM_PATH", wasmPath)
}

// SEC-001: `key import` must refuse to install a phrase whose identity is on
// the denylist. The published seed itself is never available to a test, so the
// PUBLIC test vector's identity stands in for it on a test denylist.
func TestKeyImportRefusesADenylistedIdentity(t *testing.T) {
	useHDWalletWASM(t)
	t.Setenv(config.EnvKeyPassword, "portable-explicit-test-password")
	t.Setenv(config.EnvKeyPasswordFile, "")
	t.Setenv(config.EnvMnemonicFile, "")

	phrase := strings.TrimSpace(strings.Repeat("abandon ", 11) + "about")
	cmd := &cobra.Command{}

	// Control: not denylisted, the import succeeds.
	ok := config.Default()
	ok.Storage.Path = filepath.Join(t.TempDir(), "data")
	if err := importIdentityMnemonic(cmd, ok, phrase); err != nil {
		t.Fatalf("control import: %v", err)
	}

	data, err := json.Marshal(trustanchors.Load())
	if err != nil {
		t.Fatal(err)
	}
	poisoned, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	poisoned.Denylist.PeerIDs = append(poisoned.Denylist.PeerIDs, rotationVectorPeerID(t))
	data, _ = json.Marshal(poisoned)
	poisoned, err = trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(trustanchors.Use(poisoned))

	refused := config.Default()
	refused.Storage.Path = filepath.Join(t.TempDir(), "data")
	err = importIdentityMnemonic(cmd, refused, phrase)
	if !errors.Is(err, trustanchors.ErrDenied) {
		t.Fatalf("import of a denylisted identity = %v, want ErrDenied", err)
	}
	if _, statErr := os.Stat(config.MnemonicPathResolved(refused)); statErr == nil {
		t.Fatal("a refused import still wrote the mnemonic file")
	}
}

// The offline generator seals its backup under a transport password when the
// host's at-rest password is machine-derived; --backup-password-file opens it.
func TestBackupPasswordFileIsReadLikeTheKeyPasswordFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transport-password")
	if err := os.WriteFile(path, []byte("correct horse battery staple\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readBackupPasswordFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "correct horse battery staple" {
		t.Fatalf("password = %q", got)
	}
	sealed, err := keys.EncryptMnemonic("throwaway test words", got)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := keys.DecryptMnemonic(sealed, got); err != nil || opened != "throwaway test words" {
		t.Fatalf("round trip = %q, %v", opened, err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBackupPasswordFile(path); err == nil {
		t.Fatal("a group/world-readable transport password file was accepted")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBackupPasswordFile(empty); err == nil {
		t.Fatal("an empty transport password file was accepted")
	}
}
