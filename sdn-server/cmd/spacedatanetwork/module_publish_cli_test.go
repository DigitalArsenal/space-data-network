package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildModulePublishRequestFromPluginRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sgp4.wasm.enc"), []byte{0x01, 0x02}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sgp4.key"), []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := map[string]any{
		"plugins": []map[string]any{
			{
				"id":                   "sgp4",
				"version":              "2026.04.28",
				"required_scope":       "orbpro:premium",
				"encrypted_path":       "sgp4.wasm.enc",
				"key_path":             "sgp4.key",
				"content_type":         "application/wasm",
				"max_grant_timeout_ms": 120000,
			},
		},
	}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), rawCatalog, 0600); err != nil {
		t.Fatal(err)
	}

	req, err := buildModulePublishRequestFromPluginRoot(root, []string{"sgp4"}, 1777392000000, "nonce")
	if err != nil {
		t.Fatalf("buildModulePublishRequestFromPluginRoot failed: %v", err)
	}
	if req.Type != "module-publish.v1" || req.IssuedAtMs != 1777392000000 || req.Nonce != "nonce" {
		t.Fatalf("request header = %#v", req)
	}
	if len(req.Modules) != 1 {
		t.Fatalf("module count = %d, want 1", len(req.Modules))
	}
	module := req.Modules[0]
	if module.ID != "sgp4" || module.Version != "2026.04.28" {
		t.Fatalf("module = %#v", module)
	}
	if string(module.KeyMaterial) != "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("key material = %q", string(module.KeyMaterial))
	}
	if len(module.EncryptedBundle) != 2 || module.EncryptedBundle[0] != 0x01 || module.EncryptedBundle[1] != 0x02 {
		t.Fatalf("encrypted bundle = %x", module.EncryptedBundle)
	}
}

// OWNER RULING 2026-08-07: a module publication signs with THIS NODE'S root key
// by default — no separate .txt key ceremony — and uses a separate key only
// when one is deliberately configured. These pin that precedence, because the
// failure mode (silently signing with the wrong identity) is invisible at the
// call site and would be rejected only by the provider, long after the fact.

func TestResolveModulePublishMnemonicDefaultsToNodeRootKey(t *testing.T) {
	dir := t.TempDir()
	mnemonicPath := filepath.Join(dir, "mnemonic")
	const nodeRoot = "node root mnemonic words for the test only"
	if err := os.WriteFile(mnemonicPath, []byte(nodeRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDN_MNEMONIC_FILE", mnemonicPath)
	t.Setenv("SDN_MODULE_PUBLISH_WALLET_ENV", "")

	got, source, err := resolveModulePublishMnemonic("")
	if err != nil {
		t.Fatalf("expected the node root key to be usable with no wallet env: %v", err)
	}
	if got != nodeRoot {
		t.Fatalf("did not sign with the node root key: got %q", got)
	}
	if !strings.Contains(source, "node root key") {
		t.Fatalf("source must name the node root key for the operator log, got %q", source)
	}
}

func TestResolveModulePublishMnemonicPrefersDeliberateWalletEnv(t *testing.T) {
	dir := t.TempDir()
	mnemonicPath := filepath.Join(dir, "mnemonic")
	if err := os.WriteFile(mnemonicPath, []byte("node root mnemonic words\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDN_MNEMONIC_FILE", mnemonicPath)

	walletEnv := filepath.Join(dir, "publication.env")
	const separate = "deliberately configured publication key words"
	if err := os.WriteFile(walletEnv, []byte("SDN_TRACKED_DEV_ADMIN_MNEMONIC="+separate+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, source, err := resolveModulePublishMnemonic(walletEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got != separate {
		t.Fatalf("a deliberately configured key must outrank the node root, got %q", got)
	}
	if !strings.Contains(source, "configured publication key") {
		t.Fatalf("source should name the configured key, got %q", source)
	}
}

// A stray dev-wallet.env in the working directory must never silently outrank
// the node root key — that is how a publication gets signed by a forgotten
// test identity.
func TestResolveModulePublishMnemonicNodeRootOutranksImplicitDevWallet(t *testing.T) {
	dir := t.TempDir()
	mnemonicPath := filepath.Join(dir, "mnemonic")
	const nodeRoot = "node root mnemonic words for precedence"
	if err := os.WriteFile(mnemonicPath, []byte(nodeRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SDN_MNEMONIC_FILE", mnemonicPath)
	t.Setenv("SDN_MODULE_PUBLISH_WALLET_ENV", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "config", "dev-wallet.env"),
		[]byte("SDN_TRACKED_DEV_ADMIN_MNEMONIC=stray dev wallet words\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	got, _, err := resolveModulePublishMnemonic("")
	if err != nil {
		t.Fatal(err)
	}
	if got != nodeRoot {
		t.Fatalf("stray config/dev-wallet.env outranked the node root key: got %q", got)
	}
}
