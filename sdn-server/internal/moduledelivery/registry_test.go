package moduledelivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

func TestRegistryEnsurePublicationCIDPinsEncryptedBundleOnce(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	pluginRoot := filepath.Join(baseDir, "license", "plugins")
	if err := os.MkdirAll(pluginRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	bundle := []byte("encrypted-bundle")
	if err := os.WriteFile(filepath.Join(pluginRoot, "bundle.wasm.enc"), bundle, 0o600); err != nil {
		t.Fatalf("WriteFile(bundle) failed: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(pluginRoot, "bundle.key"),
		[]byte(base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(key) failed: %v", err)
	}
	catalogBytes, err := json.Marshal(license.PluginCatalogFile{
		Plugins: []license.PluginCatalogEntry{{
			ID:            "module-id",
			Version:       "1.0.0",
			RequiredScope: "orbpro:base",
			EncryptedPath: "bundle.wasm.enc",
			KeyPath:       "bundle.key",
			ContentType:   "application/wasm+encrypted",
		}},
	})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "catalog.json"), catalogBytes, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog) failed: %v", err)
	}

	var requestCount atomic.Int32
	ipfsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Hash":"bafy-module-cid","Size":"16"}`))
	}))
	defer ipfsServer.Close()

	reg, err := NewRegistry(baseDir, ipfsServer.URL)
	if err != nil {
		t.Fatalf("NewRegistry failed: %v", err)
	}

	firstCID, asset, err := reg.EnsurePublicationCID(context.Background(), "module-id")
	if err != nil {
		t.Fatalf("EnsurePublicationCID(first) failed: %v", err)
	}
	secondCID, _, err := reg.EnsurePublicationCID(context.Background(), "module-id")
	if err != nil {
		t.Fatalf("EnsurePublicationCID(second) failed: %v", err)
	}

	if firstCID != "bafy-module-cid" || secondCID != "bafy-module-cid" {
		t.Fatalf("unexpected CID values: %q / %q", firstCID, secondCID)
	}
	if asset.ID != "module-id" {
		t.Fatalf("asset.ID = %q", asset.ID)
	}
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("IPFS add request count = %d, want 1", got)
	}
}
