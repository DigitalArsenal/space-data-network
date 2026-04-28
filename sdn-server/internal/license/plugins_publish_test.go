package license

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAddEncryptedPluginReplacesCatalogEntry(t *testing.T) {
	root := t.TempDir()
	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}

	first, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:                 "orbpro-core",
		Version:            "1.0.0",
		RequiredScope:      "orbpro:premium",
		EncryptedBundle:    []byte{0x01, 0x02, 0x03},
		KeyMaterial:        []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
		ContentType:        "application/wasm",
		CacheControl:       "public, max-age=60",
		AllowedDomains:     []string{"orbpro.digitalarsenal.io"},
		MaxGrantTimeoutMs:  120000,
		SignatureHex:       "first-signature",
		SignerPubKeyHex:    "first-signer",
		UploadedAtOverride: "2026-04-28T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("AddEncryptedPlugin first failed: %v", err)
	}
	if first.ID != "orbpro-core" || first.Version != "1.0.0" {
		t.Fatalf("first asset = %#v", first)
	}

	second, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:                 "orbpro-core",
		Version:            "2.0.0",
		RequiredScope:      "orbpro:premium",
		EncryptedBundle:    []byte{0x04, 0x05, 0x06, 0x07},
		KeyMaterial:        []byte("1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100"),
		ContentType:        "application/wasm",
		CacheControl:       "public, max-age=60",
		AllowedDomains:     []string{"orbpro.digitalarsenal.io"},
		MaxGrantTimeoutMs:  180000,
		SignatureHex:       "second-signature",
		SignerPubKeyHex:    "second-signer",
		UploadedAtOverride: "2026-04-28T12:01:00Z",
	})
	if err != nil {
		t.Fatalf("AddEncryptedPlugin second failed: %v", err)
	}
	if second.Version != "2.0.0" {
		t.Fatalf("second asset version = %q, want 2.0.0", second.Version)
	}
	if second.SizeBytes != 4 {
		t.Fatalf("second asset size = %d, want 4", second.SizeBytes)
	}

	catalogBytes, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog failed: %v", err)
	}
	var catalog PluginCatalogFile
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		t.Fatalf("decode catalog failed: %v", err)
	}
	if len(catalog.Plugins) != 1 {
		t.Fatalf("catalog entry count = %d, want 1: %s", len(catalog.Plugins), string(catalogBytes))
	}
	entry := catalog.Plugins[0]
	if entry.ID != "orbpro-core" || entry.Version != "2.0.0" {
		t.Fatalf("catalog entry = %#v", entry)
	}
	if entry.PlainPath != "" {
		t.Fatalf("plain_path = %q, want empty", entry.PlainPath)
	}
	if entry.EncryptedPath == "" || entry.KeyPath == "" {
		t.Fatalf("encrypted_path/key_path must be present: %#v", entry)
	}
	if entry.AllowedDomains[0] != "orbpro.digitalarsenal.io" {
		t.Fatalf("allowed domain = %q", entry.AllowedDomains[0])
	}

	reloaded, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("reload registry failed: %v", err)
	}
	reloadedAsset, ok := reloaded.Get("orbpro-core")
	if !ok {
		t.Fatal("reloaded asset missing")
	}
	if reloadedAsset.Version != "2.0.0" || reloadedAsset.SizeBytes != 4 {
		t.Fatalf("reloaded asset = %#v", reloadedAsset)
	}
	key, err := reloaded.ReadBundleKey("orbpro-core")
	if err != nil {
		t.Fatalf("ReadBundleKey failed: %v", err)
	}
	if len(key) != 32 || key[0] != 0x1f || key[31] != 0x00 {
		t.Fatalf("decoded key = %x", key)
	}
}
