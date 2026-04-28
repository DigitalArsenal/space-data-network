package license

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleUploadHandlerRequiresAuthenticatedSession(t *testing.T) {
	t.Parallel()

	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	handler := NewModuleUploadHandler(
		reg,
		func(string) (string, error) { return "", nil },
		func(*http.Request) (string, error) { return "", errors.New("no session") },
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin-modules/upload", strings.NewReader(""))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

func TestModuleUploadHandlerStoresEncryptedCatalogEntry(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	xpub := "xpub-upload-test"
	pubHex := hex.EncodeToString(pub)
	encryptedBundle := []byte("encrypted wasm bundle bytes")
	contentKey := bytes.Repeat([]byte{0x5a}, 32)
	bundleHash := sha256.Sum256(encryptedBundle)
	signatureHex := hex.EncodeToString(ed25519.Sign(priv, bundleHash[:]))
	providerPrivateKey, providerPublicKey := testX25519KeyPair(t)
	providerPeerID := "provider.orbpro.test"
	metadata := moduleUploadMetadata{
		ID:                "com.spaceaware.test-protocol",
		Version:           "0.0.1",
		RequiredScope:     "spaceaware:test",
		AllowedDomains:    []string{"SpaceAware.io", "www.spaceaware.io"},
		MaxGrantTimeoutMs: 120_000,
	}
	contentKeyEnvelope := testProviderContentKeyEnvelopeForUpload(t, contentKey, providerPublicKey, metadata, encryptedBundle, pubHex, providerPeerID)
	body, contentType := moduleUploadMultipart(t, metadata, encryptedBundle, contentKeyEnvelope, signatureHex)
	handler := NewModuleUploadHandler(
		reg,
		func(gotXPub string) (string, error) {
			if gotXPub != xpub {
				t.Fatalf("key lookup xpub = %q, want %q", gotXPub, xpub)
			}
			return pubHex, nil
		},
		func(*http.Request) (string, error) { return xpub, nil },
	)
	var publishedID string
	handler.SetAfterUpload(func(asset *PluginAsset) error {
		publishedID = asset.ID
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin-modules/upload", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if publishedID != "com.spaceaware.test-protocol" {
		t.Fatalf("after-upload publish id = %q", publishedID)
	}

	asset, ok := reg.Get("com.spaceaware.test-protocol")
	if !ok {
		t.Fatal("expected uploaded asset in registry")
	}
	if asset.BundleSHA256 != hex.EncodeToString(bundleHash[:]) {
		t.Fatalf("BundleSHA256 = %q, want %q", asset.BundleSHA256, hex.EncodeToString(bundleHash[:]))
	}
	if !asset.AllowsDomain("api.spaceaware.io") {
		t.Fatal("expected normalized allowed domain policy to include spaceaware.io subdomains")
	}
	encrypted, err := reg.IsEncrypted("com.spaceaware.test-protocol")
	if err != nil {
		t.Fatalf("IsEncrypted failed: %v", err)
	}
	if !encrypted {
		t.Fatal("expected encrypted registry entry")
	}
	if got, _, err := reg.ReadEncryptedBundle("com.spaceaware.test-protocol"); err != nil {
		t.Fatalf("ReadEncryptedBundle failed: %v", err)
	} else if !bytes.Equal(got, encryptedBundle) {
		t.Fatalf("encrypted bundle bytes changed")
	}
	if got, err := reg.ReadBundleKeyWithProviderKey("com.spaceaware.test-protocol", providerPrivateKey, providerPeerID); err != nil {
		t.Fatalf("ReadBundleKeyWithProviderKey failed: %v", err)
	} else if !bytes.Equal(got, contentKey) {
		t.Fatalf("content key bytes changed")
	}

	rawCatalog, err := os.ReadFile(filepath.Join(root, defaultPluginCatalogFile))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if strings.Contains(string(rawCatalog), "plain_path") {
		t.Fatalf("catalog should not contain plain_path: %s", rawCatalog)
	}
	if strings.Contains(string(rawCatalog), "key_path") {
		t.Fatalf("catalog should not contain key_path: %s", rawCatalog)
	}
	if !strings.Contains(string(rawCatalog), "encrypted_path") || !strings.Contains(string(rawCatalog), "key_envelope_path") {
		t.Fatalf("catalog should contain encrypted_path and key_envelope_path: %s", rawCatalog)
	}
	if _, err := os.Stat(filepath.Join(root, "com.spaceaware.test-protocol", "bundle.key")); !os.IsNotExist(err) {
		t.Fatalf("bundle.key must not exist")
	}
	if strings.Contains(string(rawCatalog), hex.EncodeToString(contentKey)) {
		t.Fatalf("catalog leaked content key: %s", rawCatalog)
	}
}

func TestPluginRegistryReadBundleKeyUnwrapsProviderEnvelope(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	providerPrivateKey, providerPublicKey := testX25519KeyPair(t)
	providerPeerID := "provider.orbpro.test"
	encryptedBundle := []byte("encrypted wasm bundle bytes")
	contentKey := bytes.Repeat([]byte{0x6c}, 32)
	signerPubKeyHex := strings.Repeat("d", 64)
	metadata := moduleUploadMetadata{
		ID:            "com.spaceaware.test-protocol",
		Version:       "0.0.1",
		RequiredScope: "spaceaware:test",
	}
	contentKeyEnvelope := testProviderContentKeyEnvelopeForUpload(t, contentKey, providerPublicKey, metadata, encryptedBundle, signerPubKeyHex, providerPeerID)

	if _, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:                 metadata.ID,
		Version:            metadata.Version,
		RequiredScope:      metadata.RequiredScope,
		EncryptedBundle:    encryptedBundle,
		ContentKeyEnvelope: contentKeyEnvelope,
		ProviderPeerID:     providerPeerID,
		SignerPubKeyHex:    signerPubKeyHex,
	}); err != nil {
		t.Fatalf("AddEncryptedPlugin failed: %v", err)
	}

	got, err := reg.ReadBundleKeyWithProviderKey(metadata.ID, providerPrivateKey, providerPeerID)
	if err != nil {
		t.Fatalf("ReadBundleKeyWithProviderKey failed: %v", err)
	}
	if !bytes.Equal(got, contentKey) {
		t.Fatalf("content key bytes changed")
	}
}

func TestModuleUploadHandlerListsPublicDescriptorsWithoutKeyMaterial(t *testing.T) {
	t.Parallel()

	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	providerPeerID := "provider.orbpro.test"
	_, providerPublicKey := testX25519KeyPair(t)
	encryptedBundle := []byte("encrypted")
	contentKeyEnvelope := testProviderContentKeyEnvelopeForUpload(
		t,
		bytes.Repeat([]byte{0x11}, 32),
		providerPublicKey,
		moduleUploadMetadata{ID: "com.spaceaware.test-protocol", Version: "0.0.1"},
		encryptedBundle,
		strings.Repeat("e", 64),
		providerPeerID,
	)
	if _, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:                 "com.spaceaware.test-protocol",
		Version:            "0.0.1",
		EncryptedBundle:    encryptedBundle,
		ContentKeyEnvelope: contentKeyEnvelope,
		ProviderPeerID:     providerPeerID,
		RequiredScope:      "spaceaware:test",
		AllowedDomains:     []string{"spaceaware.io"},
		MaxGrantTimeoutMs:  120_000,
		SignerPubKeyHex:    strings.Repeat("e", 64),
	}); err != nil {
		t.Fatalf("AddEncryptedPlugin failed: %v", err)
	}
	handler := NewModuleUploadHandler(
		reg,
		func(string) (string, error) { return "", nil },
		func(*http.Request) (string, error) { return "", nil },
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugin-modules", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Modules []PluginDescriptor `json:"modules"`
		Count   int                `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if payload.Count != 1 || len(payload.Modules) != 1 {
		t.Fatalf("unexpected list response: %+v", payload)
	}
	if payload.Modules[0].ID != "com.spaceaware.test-protocol" {
		t.Fatalf("module id = %q", payload.Modules[0].ID)
	}
	if got := payload.Modules[0].AllowedDomains; len(got) != 1 || got[0] != "spaceaware.io" {
		t.Fatalf("allowed domains = %v", got)
	}
	if payload.Modules[0].MaxGrantTimeoutMs != 120_000 {
		t.Fatalf("max grant timeout = %d", payload.Modules[0].MaxGrantTimeoutMs)
	}
	if strings.Contains(rec.Body.String(), "bundle.key") || strings.Contains(rec.Body.String(), "key_path") {
		t.Fatalf("list response leaked key material: %s", rec.Body.String())
	}
}

func moduleUploadMultipart(
	t *testing.T,
	meta moduleUploadMetadata,
	encryptedBundle []byte,
	contentKeyEnvelope *ProviderContentKeyEnvelope,
	signatureHex string,
) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	bundleWriter, err := writer.CreateFormFile("bundle", "bundle.wasm.enc")
	if err != nil {
		t.Fatalf("CreateFormFile(bundle): %v", err)
	}
	if _, err := bundleWriter.Write(encryptedBundle); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := writer.WriteField("metadata", string(metaJSON)); err != nil {
		t.Fatalf("WriteField(metadata): %v", err)
	}
	envelopeJSON, err := json.Marshal(contentKeyEnvelope)
	if err != nil {
		t.Fatalf("marshal content key envelope: %v", err)
	}
	if err := writer.WriteField("content_key_envelope", string(envelopeJSON)); err != nil {
		t.Fatalf("WriteField(content_key_envelope): %v", err)
	}
	if err := writer.WriteField("signature_hex", signatureHex); err != nil {
		t.Fatalf("WriteField(signature_hex): %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	return body, writer.FormDataContentType()
}

func testProviderContentKeyEnvelopeForUpload(
	t *testing.T,
	contentKey []byte,
	providerPublicKey []byte,
	meta moduleUploadMetadata,
	encryptedBundle []byte,
	signerPubKeyHex string,
	providerPeerID string,
) *ProviderContentKeyEnvelope {
	t.Helper()

	bundleHash := sha256.Sum256(encryptedBundle)
	envelope, err := WrapProviderContentKey(contentKey, providerPublicKey, ProviderContentKeyAAD{
		ModuleID:           meta.ID,
		Version:            meta.Version,
		BundleSHA256:       hex.EncodeToString(bundleHash[:]),
		SignerPublicKeyHex: signerPubKeyHex,
		ProviderPeerID:     providerPeerID,
	})
	if err != nil {
		t.Fatalf("WrapProviderContentKey failed: %v", err)
	}
	return envelope
}
