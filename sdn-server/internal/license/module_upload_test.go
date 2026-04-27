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
	body, contentType := moduleUploadMultipart(t, moduleUploadMetadata{
		ID:                "com.spaceaware.test-protocol",
		Version:           "0.0.1",
		RequiredScope:     "spaceaware:test",
		AllowedDomains:    []string{"SpaceAware.io", "www.spaceaware.io"},
		MaxGrantTimeoutMs: 120_000,
	}, encryptedBundle, contentKey, signatureHex)
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
	if got, err := reg.ReadBundleKey("com.spaceaware.test-protocol"); err != nil {
		t.Fatalf("ReadBundleKey failed: %v", err)
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
	if !strings.Contains(string(rawCatalog), "encrypted_path") || !strings.Contains(string(rawCatalog), "key_path") {
		t.Fatalf("catalog should contain encrypted_path and key_path: %s", rawCatalog)
	}
}

func TestModuleUploadHandlerListsPublicDescriptorsWithoutKeyMaterial(t *testing.T) {
	t.Parallel()

	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	if _, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:                "com.spaceaware.test-protocol",
		Version:           "0.0.1",
		EncryptedBundle:   []byte("encrypted"),
		ContentKey:        bytes.Repeat([]byte{0x11}, 32),
		RequiredScope:     "spaceaware:test",
		AllowedDomains:    []string{"spaceaware.io"},
		MaxGrantTimeoutMs: 120_000,
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
	contentKey []byte,
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
	if err := writer.WriteField("content_key_hex", hex.EncodeToString(contentKey)); err != nil {
		t.Fatalf("WriteField(content_key_hex): %v", err)
	}
	if err := writer.WriteField("signature_hex", signatureHex); err != nil {
		t.Fatalf("WriteField(signature_hex): %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	return body, writer.FormDataContentType()
}
