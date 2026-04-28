package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/plugins"
	"golang.org/x/crypto/curve25519"
)

type fakePlugin struct {
	id string
}

func (p fakePlugin) ID() string { return p.id }

func (p fakePlugin) Start(context.Context, plugins.RuntimeContext) error { return nil }

func (p fakePlugin) RegisterRoutes(*http.ServeMux) {}

func (p fakePlugin) Close() error { return nil }

func TestHasCatalogLicensingModuleRequiresRegisteredUnifiedModule(t *testing.T) {
	t.Parallel()

	reg := writeTestPluginRegistry(t, license.PluginCatalogEntry{
		ID:            licensingModuleID,
		Version:       "0.1.0",
		RequiredScope: "orbpro:base",
		EncryptedPath: "licensing.wasm.enc",
		KeyPath:       "licensing.key",
		ContentType:   "application/wasm+encrypted",
	})

	n := &Node{plugins: plugins.New()}
	if n.hasCatalogLicensingModule(reg) {
		t.Fatal("expected catalog-only licensing entry to require a registered runtime module")
	}

	if err := n.plugins.Register(fakePlugin{id: licensingModuleID}); err != nil {
		t.Fatalf("Register(fake licensing plugin) failed: %v", err)
	}

	if !n.hasCatalogLicensingModule(reg) {
		t.Fatal("expected registered unified licensing module to satisfy catalog startup")
	}
}

func TestShouldLoadLicensingFromCatalogSkipsCatalogWhenExplicitRuntimeWasmConfigured(t *testing.T) {
	reg := writeTestPluginRegistry(t, license.PluginCatalogEntry{
		ID:            licensingModuleID,
		Version:       "0.1.0",
		RequiredScope: "orbpro:runtime",
		EncryptedPath: "licensing.wasm.enc",
		KeyPath:       "licensing.key",
		ContentType:   "application/wasm+encrypted",
	})

	explicitPath := filepath.Join(t.TempDir(), "licensing-runtime.wasm")
	if err := os.WriteFile(explicitPath, []byte("runtime"), 0o600); err != nil {
		t.Fatalf("WriteFile(explicitPath) failed: %v", err)
	}
	t.Setenv("ORBPRO_LICENSING_WASM_PATH", explicitPath)

	n := &Node{}
	if n.shouldLoadLicensingFromCatalog(reg) {
		t.Fatal("expected explicit runtime wasm path to bypass catalog licensing load")
	}
}

func TestFindKeyBrokerWasmPathPrefersLicensingEnvVar(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy.wasm")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacyPath) failed: %v", err)
	}
	licensingPath := filepath.Join(t.TempDir(), "licensing.wasm")
	if err := os.WriteFile(licensingPath, []byte("licensing"), 0o600); err != nil {
		t.Fatalf("WriteFile(licensingPath) failed: %v", err)
	}

	t.Setenv("ORBPRO_KEY_BROKER_WASM_PATH", legacyPath)
	t.Setenv("ORBPRO_LICENSING_WASM_PATH", licensingPath)

	n := &Node{}
	if got := n.findKeyBrokerWasmPath(); got != licensingPath {
		t.Fatalf("expected ORBPRO_LICENSING_WASM_PATH to win, got %q", got)
	}
}

func TestFindKeyBrokerWasmPathFallsBackToLegacyEnvVar(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy.wasm")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("WriteFile(legacyPath) failed: %v", err)
	}

	t.Setenv("ORBPRO_KEY_BROKER_WASM_PATH", legacyPath)

	n := &Node{}
	if got := n.findKeyBrokerWasmPath(); got != legacyPath {
		t.Fatalf("expected ORBPRO_KEY_BROKER_WASM_PATH fallback, got %q", got)
	}
}

func writeTestPluginRegistry(t *testing.T, entries ...license.PluginCatalogEntry) *license.PluginRegistry {
	t.Helper()

	root := t.TempDir()
	for i := range entries {
		entry := &entries[i]
		encryptedBundle := []byte("encrypted-bundle")
		if encryptedPath := filepath.Join(root, entry.EncryptedPath); entry.EncryptedPath != "" {
			if err := os.WriteFile(encryptedPath, encryptedBundle, 0o600); err != nil {
				t.Fatalf("WriteFile(encrypted bundle) failed: %v", err)
			}
		}
		if keyPath := filepath.Join(root, entry.KeyPath); entry.KeyPath != "" {
			if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
				t.Fatalf("WriteFile(bundle key) failed: %v", err)
			}
		}
		if keyEnvelopePath := filepath.Join(root, entry.KeyEnvelopePath); entry.KeyEnvelopePath != "" {
			if entry.SignerPubKeyHex == "" {
				entry.SignerPubKeyHex = strings.Repeat("a", 64)
			}
			bundleHash := sha256.Sum256(encryptedBundle)
			envelope, err := license.WrapProviderContentKey(
				make([]byte, 32),
				licensingTestProviderWrappingPublicKey(t),
				license.ProviderContentKeyAAD{
					ModuleID:           entry.ID,
					Version:            entry.Version,
					BundleSHA256:       hex.EncodeToString(bundleHash[:]),
					SignerPublicKeyHex: entry.SignerPubKeyHex,
					ProviderPeerID:     licensingTestProviderPeerID,
				},
			)
			if err != nil {
				t.Fatalf("WrapProviderContentKey failed: %v", err)
			}
			rawEnvelope, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("json.Marshal(key envelope) failed: %v", err)
			}
			if err := os.WriteFile(keyEnvelopePath, rawEnvelope, 0o600); err != nil {
				t.Fatalf("WriteFile(key envelope) failed: %v", err)
			}
		}
	}

	catalog := license.PluginCatalogFile{Plugins: entries}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("json.Marshal(catalog) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), rawCatalog, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog) failed: %v", err)
	}

	reg, err := license.LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	return reg
}

const licensingTestProviderPeerID = "provider.orbpro.test"

func licensingTestProviderWrappingKey() []byte {
	return []byte{
		32, 31, 30, 29, 28, 27, 26, 25,
		24, 23, 22, 21, 20, 19, 18, 17,
		16, 15, 14, 13, 12, 11, 10, 9,
		8, 7, 6, 5, 4, 3, 2, 1,
	}
}

func licensingTestProviderWrappingPublicKey(t *testing.T) []byte {
	t.Helper()

	pub, err := curve25519.X25519(licensingTestProviderWrappingKey(), curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive provider wrapping public key: %v", err)
	}
	return pub
}
