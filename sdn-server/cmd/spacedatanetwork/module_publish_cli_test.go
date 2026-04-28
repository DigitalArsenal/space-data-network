package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
				"allowed_domains":      []string{"orbpro.digitalarsenal.io"},
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
	if module.AllowedDomains[0] != "orbpro.digitalarsenal.io" {
		t.Fatalf("allowed domain = %q", module.AllowedDomains[0])
	}
}
