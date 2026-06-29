package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"testing"
)

func TestModulePublishRequestWalletSignatureRejectsTampering(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	req := ModulePublishRequest{
		Type:       "module-publish.v1",
		IssuedAtMs: 1777392000000,
		Nonce:      "nonce-1",
		Modules: []ModulePublishEntry{
			{
				ID:              "orbpro-core",
				Version:         "1.0.0",
				RequiredScope:   "orbpro:premium",
				EncryptedBundle: []byte{0x01, 0x02, 0x03},
				KeyMaterial:     []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
			},
		},
	}

	if err := SignModulePublishRequest(&req, "xpub-admin", pub, priv); err != nil {
		t.Fatalf("SignModulePublishRequest failed: %v", err)
	}
	if req.SignatureHex == "" {
		t.Fatal("signature was not set")
	}
	authorizer := testModulePublishAuthorizer("xpub-admin", pub, true)
	if err := VerifyModulePublishRequest(req, authorizer); err != nil {
		t.Fatalf("VerifyModulePublishRequest failed: %v", err)
	}

	req.Modules[0].Version = "1.0.1"
	if err := VerifyModulePublishRequest(req, authorizer); err == nil {
		t.Fatal("VerifyModulePublishRequest accepted tampered request")
	}
}

func TestApplyModulePublishRequestStoresEncryptedModules(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	req := ModulePublishRequest{
		Type:       "module-publish.v1",
		IssuedAtMs: 1777392000000,
		Nonce:      "nonce-2",
		Modules: []ModulePublishEntry{
			{
				ID:                "conjunction-assessment",
				Version:           "2026.04.28",
				RequiredScope:     "orbpro:premium",
				EncryptedBundle:   []byte{0xaa, 0xbb, 0xcc},
				KeyMaterial:       []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
				ContentType:       "application/wasm",
				MaxGrantTimeoutMs: 90000,
			},
		},
	}
	if err := SignModulePublishRequest(&req, "xpub-admin", pub, priv); err != nil {
		t.Fatalf("SignModulePublishRequest failed: %v", err)
	}

	resp := ApplyModulePublishRequest(reg, req, testModulePublishAuthorizer("xpub-admin", pub, true))
	if !resp.OK {
		t.Fatalf("ApplyModulePublishRequest failed: %s", resp.Error)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("result count = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].ID != "conjunction-assessment" || resp.Results[0].SizeBytes != 3 {
		t.Fatalf("result = %#v", resp.Results[0])
	}
	asset, ok := reg.Get("conjunction-assessment")
	if !ok {
		t.Fatal("published asset missing")
	}
	if asset.Version != "2026.04.28" || asset.RequiredScope != "orbpro:premium" {
		t.Fatalf("asset = %#v", asset)
	}
	if encrypted, err := reg.IsEncrypted("conjunction-assessment"); err != nil || !encrypted {
		t.Fatalf("IsEncrypted = %v, %v", encrypted, err)
	}
}

func TestServeModulePublishJSONWritesResponse(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	req := ModulePublishRequest{
		Type:       "module-publish.v1",
		IssuedAtMs: 1777392000000,
		Nonce:      "nonce-3",
		Modules: []ModulePublishEntry{
			{
				ID:              "sgp4",
				Version:         "2026.04.28",
				EncryptedBundle: []byte{0x10, 0x20},
				KeyMaterial:     []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
			},
		},
	}
	if err := SignModulePublishRequest(&req, "xpub-admin", pub, priv); err != nil {
		t.Fatalf("SignModulePublishRequest failed: %v", err)
	}
	requestBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}

	var responseBytes bytes.Buffer
	ServeModulePublishJSON(bytes.NewReader(requestBytes), &responseBytes, reg, testModulePublishAuthorizer("xpub-admin", pub, true))

	var response ModulePublishResponse
	if err := json.Unmarshal(responseBytes.Bytes(), &response); err != nil {
		t.Fatalf("decode response failed: %v; body=%q", err, responseBytes.String())
	}
	if !response.OK {
		t.Fatalf("response failed: %s", response.Error)
	}
	if _, ok := reg.Get("sgp4"); !ok {
		t.Fatal("published asset missing")
	}
}

func TestVerifyModulePublishRequestRejectsNonAdminSigner(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	req := ModulePublishRequest{
		Type:       "module-publish.v1",
		IssuedAtMs: 1777392000000,
		Nonce:      "nonce-non-admin",
		Modules: []ModulePublishEntry{
			{
				ID:              "sgp4",
				Version:         "2026.04.28",
				EncryptedBundle: []byte{0x10, 0x20},
				KeyMaterial:     []byte("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
			},
		},
	}
	if err := SignModulePublishRequest(&req, "xpub-user", pub, priv); err != nil {
		t.Fatalf("SignModulePublishRequest failed: %v", err)
	}
	if err := VerifyModulePublishRequest(req, testModulePublishAuthorizer("xpub-user", pub, false)); err == nil {
		t.Fatal("VerifyModulePublishRequest accepted non-admin signer")
	}
}

func testModulePublishAuthorizer(xpub string, pub ed25519.PublicKey, admin bool) ModulePublishAuthorizer {
	return func(got string) (ModulePublishPrincipal, error) {
		if got != xpub {
			return ModulePublishPrincipal{}, nil
		}
		return ModulePublishPrincipal{
			XPub:             xpub,
			SigningPubKeyHex: bytesToHex(pub),
			Admin:            admin,
		}, nil
	}
}

func bytesToHex(bytes []byte) string {
	const table = "0123456789abcdef"
	out := make([]byte, len(bytes)*2)
	for i, b := range bytes {
		out[i*2] = table[b>>4]
		out[i*2+1] = table[b&0x0f]
	}
	return string(out)
}
