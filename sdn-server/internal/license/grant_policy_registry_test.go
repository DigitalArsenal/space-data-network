package license

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writePluginRoot lays out a catalog the way the live provider carries one:
// encrypted artifacts plus a key file per module.
func writePluginRoot(t *testing.T, entries []PluginCatalogEntry, policy string) string {
	t.Helper()
	root := t.TempDir()
	for i := range entries {
		dir := filepath.Join(root, entries[i].ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		bundle := filepath.Join(dir, "bundle.wasm.enc")
		key := filepath.Join(dir, "bundle.key")
		if err := os.WriteFile(bundle, []byte("encrypted-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(key, []byte("key-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries[i].EncryptedPath = filepath.Join(entries[i].ID, "bundle.wasm.enc")
		entries[i].KeyPath = filepath.Join(entries[i].ID, "bundle.key")
	}
	catalog, err := json.Marshal(PluginCatalogFile{Plugins: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), catalog, 0o600); err != nil {
		t.Fatal(err)
	}
	if policy != "" {
		if err := os.WriteFile(filepath.Join(root, GrantPolicyFileName), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The live catalog on the provider (audited 2026-08-07): 43 entries, EVERY ONE
// of them with an empty allowed_xpubs. This is that shape — the paid module and
// the gallery family side by side, with only the policy file telling them
// apart.
func TestRegistryRulesTheLivePairApart(t *testing.T) {
	root := writePluginRoot(t, []PluginCatalogEntry{
		{ID: "com.orbpro.hpop", Version: "1.0.0"},
		{ID: "com.orbpro.rf-fspl", Version: "0.1.0"},
	}, `{
      "default_policy": "allowlist",
      "modules": [
        {"match": "com.orbpro.rf-*", "policy": "link-key", "note": "owner directive 2026-08-07: gallery link key, for now"}
      ]
    }`)

	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	hpop, ok := reg.PublicationDecision("com.orbpro.hpop")
	if !ok {
		t.Fatal("com.orbpro.hpop is missing from the registry")
	}
	if hpop.Publish {
		t.Fatal("com.orbpro.hpop was published with no entitlement — this is the P1 verbatim")
	}

	rf, ok := reg.PublicationDecision("com.orbpro.rf-fspl")
	if !ok {
		t.Fatal("com.orbpro.rf-fspl is missing from the registry")
	}
	if !rf.Publish {
		t.Fatalf("the RF gallery lane was closed, which breaks the owner's sandcastle link: %s", rf.Reason)
	}
	if rf.Policy != GrantPolicyLinkKey {
		t.Fatalf("rf policy = %q, want %q", rf.Policy, GrantPolicyLinkKey)
	}
	if got := reg.EffectiveGrantPolicy("com.orbpro.rf-fspl"); got != GrantPolicyLinkKey {
		t.Fatalf("EffectiveGrantPolicy = %q, want %q (this is what the audit line prints)", got, GrantPolicyLinkKey)
	}
}

func TestRegistryCarriesACatalogDeclaredPolicy(t *testing.T) {
	root := writePluginRoot(t, []PluginCatalogEntry{
		{ID: "com.orbpro.rf-rain", Version: "0.1.0", GrantPolicy: "open"},
	}, "")

	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	asset, ok := reg.Get("com.orbpro.rf-rain")
	if !ok {
		t.Fatal("module missing")
	}
	if asset.GrantPolicy != GrantPolicyOpen {
		t.Fatalf("asset.GrantPolicy = %q, want %q", asset.GrantPolicy, GrantPolicyOpen)
	}
	if got := asset.Descriptor().GrantPolicy; got != GrantPolicyOpen {
		t.Fatalf("public descriptor GrantPolicy = %q, want %q", got, GrantPolicyOpen)
	}
}

func TestRegistryRejectsAnUnknownCatalogPolicy(t *testing.T) {
	root := writePluginRoot(t, []PluginCatalogEntry{
		{ID: "com.orbpro.hpop", Version: "1.0.0", GrantPolicy: "public"},
	}, "")

	if _, err := LoadPluginRegistry(root); err == nil {
		t.Fatal("an unknown catalog grant_policy loaded silently")
	}
}

// A registry loaded with no policy file at all must still be fail-closed —
// this is the state every existing provider is in before the operator writes
// one.
func TestRegistryWithNoPolicyFileRefusesUnentitledModules(t *testing.T) {
	root := writePluginRoot(t, []PluginCatalogEntry{
		{ID: "com.orbpro.hpop", Version: "1.0.0"},
	}, "")

	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	decision, _ := reg.PublicationDecision("com.orbpro.hpop")
	if decision.Publish {
		t.Fatal("with no policy file the node still published an unentitled module")
	}
}

func TestGrantPolicySurvivesACatalogSaveRoundTrip(t *testing.T) {
	root := t.TempDir()
	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:              "com.orbpro.rf-fspl",
		Version:         "0.1.0",
		EncryptedBundle: []byte("encrypted"),
		KeyMaterial:     []byte("0123456789abcdef0123456789abcdef"),
		GrantPolicy:     "link-key",
	}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := reloaded.Get("com.orbpro.rf-fspl")
	if !ok {
		t.Fatal("module did not survive the catalog round trip")
	}
	if asset.GrantPolicy != GrantPolicyLinkKey {
		t.Fatalf("GrantPolicy = %q after reload, want %q", asset.GrantPolicy, GrantPolicyLinkKey)
	}
}

func TestAddEncryptedPluginRejectsAnUnknownPolicy(t *testing.T) {
	reg, err := LoadPluginRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:              "com.orbpro.hpop",
		Version:         "1.0.0",
		EncryptedBundle: []byte("encrypted"),
		KeyMaterial:     []byte("0123456789abcdef0123456789abcdef"),
		GrantPolicy:     "everyone",
	})
	if err == nil {
		t.Fatal("a publish with an unknown grant policy was accepted")
	}
}
