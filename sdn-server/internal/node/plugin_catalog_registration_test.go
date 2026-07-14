package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// These tests pin the fix for the regression introduced by b2343a0e
// ("feat: route licensing bootstrap through wasm runtime"): that commit
// removed the only call to n.registerCatalogPlugins, so a node staged with
// on-disk plugin-catalog modules parsed the catalog (loadPluginRegistry) but
// never registered anything from it with the plugin manager — meaning the
// module scheduler (Manager.StartAll -> scheduleCronMethods) never saw them
// and their manifest `timers` blocks never ran.
//
// The fixture is the real spacex-starlink-source data-source adapter (a
// timers/cron module — exactly the class of module the owner directive wants
// running on the production node). It is staged as a PLAIN (plain_path)
// catalog bundle so DecryptBundle returns the bytes as-is and a nil/empty
// recipient key must work. Both tests SKIP when the closed-module artifact is
// not present in the checkout (public CI), which is the coverage limit.

// catalogModuleSensitiveCaps is the sensitive-capability set the real
// spacex-starlink-source manifest declares (mirrors the approvals in
// internal/modulert/starlink_source_integration_test.go). NewModule fails
// closed unless every one of these carries a recorded operator approval.
var catalogModuleSensitiveCaps = []string{"http", "pubsub", "storage_ingest", "wallet_sign"}

// stagePlainCatalogModule copies the starlink-source wasm into a fresh plugin
// root as a single plain_path catalog entry and returns the storage path (the
// node's config.Storage.Path), the catalog entry ID, and the module content
// hash used for capability approvals.
func stagePlainCatalogModule(t *testing.T) (storagePath, catalogID, moduleHash string) {
	t.Helper()

	wasmPath := testsupport.SkipIfNoStarlinkSourceWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	storagePath = filepath.Join(t.TempDir(), "data", "dev")
	pluginRoot := license.DefaultPluginRoot(storagePath)
	if err := os.MkdirAll(pluginRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(pluginRoot) failed: %v", err)
	}

	const bundleName = "catalog-source.wasm"
	if err := os.WriteFile(filepath.Join(pluginRoot, bundleName), wasmBytes, 0o600); err != nil {
		t.Fatalf("WriteFile(plain bundle) failed: %v", err)
	}

	catalogID = "com.orbpro.catalog-source"
	catalog := license.PluginCatalogFile{
		Plugins: []license.PluginCatalogEntry{
			{
				ID:            catalogID,
				Version:       "local-dev",
				RequiredScope: "orbpro:base",
				PlainPath:     bundleName,
				ContentType:   "application/wasm",
			},
		},
	}
	rawCatalog, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("json.Marshal(catalog) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "catalog.json"), rawCatalog, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog) failed: %v", err)
	}

	return storagePath, catalogID, modulert.ContentHashHex(wasmBytes)
}

func newCatalogTestNode(t *testing.T, storagePath string, policy *modulert.CapabilityPolicyStore) *Node {
	t.Helper()
	return &Node{
		config: &config.Config{
			Storage: config.StorageConfig{Path: storagePath},
		},
		plugins:          plugins.New(),
		capabilityPolicy: policy,
	}
}

// TestRegisterCatalogPluginsRegistersPlainModuleWithPluginManager is the
// regression guard: a plain catalog entry whose sensitive capabilities are
// operator-approved must be REGISTERED with the plugin manager (so StartAll
// schedules its cron/timers methods). A nil recipient key must work for the
// plain bundle.
func TestRegisterCatalogPluginsRegistersPlainModuleWithPluginManager(t *testing.T) {
	storagePath, catalogID, moduleHash := stagePlainCatalogModule(t)

	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	for _, capability := range catalogModuleSensitiveCaps {
		if _, err := policy.Approve(modulert.CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   catalogID,
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s) failed: %v", capability, err)
		}
	}

	n := newCatalogTestNode(t, storagePath, policy)

	reg, err := n.loadPluginRegistry()
	if err != nil {
		t.Fatalf("loadPluginRegistry failed: %v", err)
	}
	if reg == nil {
		t.Fatal("expected a non-nil plugin registry")
	}
	n.pluginRegistry = reg

	// No node identity is staged, so there is no plugin decryption key. This
	// is the plain_path path: DecryptBundle must return the bytes as-is and a
	// nil recipient key must be accepted.
	recipientKey, err := n.findPluginDecryptPrivateKey()
	if err != nil {
		t.Fatalf("findPluginDecryptPrivateKey failed: %v", err)
	}
	if recipientKey != nil {
		t.Fatalf("expected nil recipient key with no staged identity, got %d bytes", len(recipientKey))
	}

	if err := n.registerCatalogPlugins(reg, plugins.RuntimeContext{}, recipientKey); err != nil {
		t.Fatalf("registerCatalogPlugins returned an unexpected error: %v", err)
	}

	entries := n.plugins.Manifest()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 plugin registered with the manager, got %d: %+v", len(entries), entries)
	}

	// The catalog entry's runtime status must reflect successful registration.
	if status, _, ok := reg.RuntimeStatus(catalogID); !ok || status != "stopped" {
		t.Fatalf("expected registry runtime status %q for %q, got %q (ok=%v)", "stopped", catalogID, status, ok)
	}

	// The whole point of the fix: a timers/cron module, once registered, is
	// schedulable by StartAll -> scheduleCronMethods. Prove the registered
	// plugin exposes cron methods (the manifest declares timers).
	if len(entries[0].Cron) == 0 {
		t.Fatalf("expected registered data-source module %q to expose cron methods (timers), got none", entries[0].ID)
	}
}

// TestRegisterCatalogPluginsSkipsCapabilityDeniedModuleWithoutBlockingBoot
// proves the fail-closed capability gate is honored per-module and stays
// non-fatal to boot: with no operator approval the module is denied by
// NewModule, is NOT registered, is marked "error" in the registry, and the
// node's subsequent StartAll (the very next boot step) still proceeds.
func TestRegisterCatalogPluginsSkipsCapabilityDeniedModuleWithoutBlockingBoot(t *testing.T) {
	storagePath, catalogID, _ := stagePlainCatalogModule(t)

	// Empty policy: every sensitive capability the module declares is denied.
	policy, err := modulert.NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}

	n := newCatalogTestNode(t, storagePath, policy)

	reg, err := n.loadPluginRegistry()
	if err != nil {
		t.Fatalf("loadPluginRegistry failed: %v", err)
	}
	if reg == nil {
		t.Fatal("expected a non-nil plugin registry")
	}
	n.pluginRegistry = reg

	recipientKey, err := n.findPluginDecryptPrivateKey()
	if err != nil {
		t.Fatalf("findPluginDecryptPrivateKey failed: %v", err)
	}

	// registerCatalogPlugins surfaces the per-module denial as a joined error
	// (which the node init call site logs and continues past — non-fatal).
	err = n.registerCatalogPlugins(reg, plugins.RuntimeContext{}, recipientKey)
	if err == nil {
		t.Fatal("expected a non-nil joined error for the capability-denied module")
	}
	if !strings.Contains(err.Error(), "capabilit") {
		t.Fatalf("expected the error to describe a capability denial, got: %v", err)
	}

	// The denied module must NOT be registered with the plugin manager.
	if entries := n.plugins.Manifest(); len(entries) != 0 {
		t.Fatalf("expected no plugins registered after a capability denial, got %d: %+v", len(entries), entries)
	}

	// The registry runtime status for the denied module must be "error".
	if status, _, ok := reg.RuntimeStatus(catalogID); !ok || status != "error" {
		t.Fatalf("expected registry runtime status %q for denied %q, got %q (ok=%v)", "error", catalogID, status, ok)
	}

	// Boot must proceed: the next init step (Manager.StartAll) runs cleanly
	// with the denied module simply absent from the manager.
	if err := n.plugins.StartAll(context.Background(), plugins.RuntimeContext{}); err != nil {
		t.Fatalf("StartAll after a capability denial should proceed cleanly, got: %v", err)
	}
}
