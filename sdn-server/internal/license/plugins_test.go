package license

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

func TestLoadPluginRegistryAndReadAssets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	encryptedPath := filepath.Join(dir, "orbpro-core.wasm.enc")
	keyPath := filepath.Join(dir, "orbpro-core.key")

	encrypted := []byte("encrypted-wasm-bundle")
	if err := os.WriteFile(encryptedPath, encrypted, 0600); err != nil {
		t.Fatalf("write encrypted file: %v", err)
	}
	key := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(key)), 0600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	catalog := PluginCatalogFile{
		Plugins: []PluginCatalogEntry{
			{
				ID:            "orbpro-core",
				Version:       "2026.02.11",
				RequiredScope: "orbpro:premium",
				EncryptedPath: "orbpro-core.wasm.enc",
				KeyPath:       "orbpro-core.key",
				ContentType:   "application/wasm",
			},
		},
	}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, defaultPluginCatalogFile), rawCatalog, 0600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	reg, err := LoadPluginRegistry(dir)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("registry count = %d, want 1", got)
	}

	list := reg.ListPublic()
	if len(list) != 1 {
		t.Fatalf("ListPublic count = %d, want 1", len(list))
	}
	if list[0].ID != "orbpro-core" {
		t.Fatalf("descriptor id = %q", list[0].ID)
	}
	if list[0].RequiredScope != "orbpro:premium" {
		t.Fatalf("descriptor scope = %q", list[0].RequiredScope)
	}
	sum := sha256.Sum256(encrypted)
	if got := list[0].BundleSHA256; got != hex.EncodeToString(sum[:]) {
		t.Fatalf("bundle sha256 = %q, want %q", got, hex.EncodeToString(sum[:]))
	}

	bundle, asset, err := reg.ReadEncryptedBundle("orbpro-core")
	if err != nil {
		t.Fatalf("ReadEncryptedBundle failed: %v", err)
	}
	if !bytes.Equal(bundle, encrypted) {
		t.Fatal("encrypted bundle mismatch")
	}
	if asset.Version != "2026.02.11" {
		t.Fatalf("asset version = %q", asset.Version)
	}

	gotKey, err := reg.ReadBundleKey("orbpro-core")
	if err != nil {
		t.Fatalf("ReadBundleKey failed: %v", err)
	}
	if !bytes.Equal(gotKey, key) {
		t.Fatal("bundle key mismatch")
	}
}

func TestBuildPluginKeyEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	asset := &PluginAsset{
		ID:            "orbpro-core",
		Version:       "2026.02.11",
		RequiredScope: "orbpro:premium",
		BundleSHA256:  "cafebabe",
	}
	pluginKey := bytes.Repeat([]byte{0x7A}, 32)
	claims := &CapabilityClaims{
		Sub:    "xpub-test",
		PeerID: "12D3KooWTestPeer",
		JTI:    "token-jti-1",
		Exp:    time.Now().Add(10 * time.Minute).Unix(),
	}

	clientPriv := bytes.Repeat([]byte{0x33}, 32)
	clampX25519PrivateKey(clientPriv)
	clientPub, err := curve25519.X25519(clientPriv, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive client public key: %v", err)
	}

	envelope, err := BuildPluginKeyEnvelope(asset, pluginKey, clientPub, claims, "spaceaware-license", time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildPluginKeyEnvelope failed: %v", err)
	}
	if envelope.PluginID != asset.ID {
		t.Fatalf("envelope plugin id = %q", envelope.PluginID)
	}
	if envelope.ExpiresAt > claims.Exp {
		t.Fatalf("envelope exp = %d exceeds claims exp %d", envelope.ExpiresAt, claims.Exp)
	}

	serverPub, err := base64.RawStdEncoding.DecodeString(envelope.ServerX25519PubKey)
	if err != nil {
		t.Fatalf("decode server pubkey: %v", err)
	}
	sharedSecret, err := curve25519.X25519(clientPriv, serverPub)
	if err != nil {
		t.Fatalf("derive shared secret: %v", err)
	}
	wrapKey := derivePluginWrapKey(sharedSecret, envelope.AssociatedData)
	block, err := aes.NewCipher(wrapKey[:])
	if err != nil {
		t.Fatalf("aes.NewCipher failed: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("cipher.NewGCM failed: %v", err)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		t.Fatalf("decode nonce: %v", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(envelope.AssociatedData))
	if err != nil {
		t.Fatalf("gcm.Open failed: %v", err)
	}

	var payload pluginKeyEnvelopePayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	if payload.PluginID != asset.ID {
		t.Fatalf("payload plugin id = %q", payload.PluginID)
	}
	if payload.PeerID != claims.PeerID {
		t.Fatalf("payload peer id = %q", payload.PeerID)
	}
	unwrappedKey, err := base64.RawStdEncoding.DecodeString(payload.Key)
	if err != nil {
		t.Fatalf("decode unwrapped key: %v", err)
	}
	if !bytes.Equal(unwrappedKey, pluginKey) {
		t.Fatal("unwrapped key mismatch")
	}
}

func TestParseX25519PublicKey(t *testing.T) {
	t.Parallel()

	raw := bytes.Repeat([]byte{0x11}, 32)
	asHex := hex.EncodeToString(raw)
	gotHex, err := ParseX25519PublicKey(asHex)
	if err != nil {
		t.Fatalf("ParseX25519PublicKey(hex) failed: %v", err)
	}
	if !bytes.Equal(gotHex, raw) {
		t.Fatal("hex parse mismatch")
	}

	asB64 := base64.RawStdEncoding.EncodeToString(raw)
	gotB64, err := ParseX25519PublicKey(asB64)
	if err != nil {
		t.Fatalf("ParseX25519PublicKey(base64) failed: %v", err)
	}
	if !bytes.Equal(gotB64, raw) {
		t.Fatal("base64 parse mismatch")
	}
}
