package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/plugins"
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
	t.Setenv("ORBPRO_LICENSING_WASM_PATH", "")

	n := &Node{}
	if got := n.findKeyBrokerWasmPath(); got != legacyPath {
		t.Fatalf("expected ORBPRO_KEY_BROKER_WASM_PATH fallback, got %q", got)
	}
}

func writeTestPluginRegistry(t *testing.T, entries ...license.PluginCatalogEntry) *license.PluginRegistry {
	t.Helper()
	return writeTestPluginRegistryWithGrantPolicy(t, nil, entries...)
}

// writeTestPluginRegistryWithGrantPolicy writes the same fixture with an
// operator grant-policy.json beside the catalog. A nil policy writes no file,
// which is the fail-closed built-in default (allowlist over an empty list) and
// therefore publishes NOTHING to the key server (23680234). Tests that need the
// key server to actually answer a challenge for a module must pass a policy;
// tests about the admit point itself want the nil case.
func writeTestPluginRegistryWithGrantPolicy(
	t *testing.T,
	grantPolicy *license.GrantPolicyConfig,
	entries ...license.PluginCatalogEntry,
) *license.PluginRegistry {
	t.Helper()

	root := t.TempDir()
	for _, entry := range entries {
		if encryptedPath := filepath.Join(root, entry.EncryptedPath); entry.EncryptedPath != "" {
			if err := os.WriteFile(encryptedPath, []byte("encrypted-bundle"), 0o600); err != nil {
				t.Fatalf("WriteFile(encrypted bundle) failed: %v", err)
			}
		}
		if keyPath := filepath.Join(root, entry.KeyPath); entry.KeyPath != "" {
			if err := os.WriteFile(keyPath, []byte(base64.RawStdEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
				t.Fatalf("WriteFile(bundle key) failed: %v", err)
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

	if grantPolicy != nil {
		rawPolicy, err := json.Marshal(grantPolicy)
		if err != nil {
			t.Fatalf("json.Marshal(grant policy) failed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, license.GrantPolicyFileName), rawPolicy, 0o600); err != nil {
			t.Fatalf("WriteFile(grant policy) failed: %v", err)
		}
	}

	reg, err := license.LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	return reg
}
