package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// The admit point (graph/tasks/sdn-allowed-xpubs-not-enforced.md, P1).
//
// bootstrapLicensingModule is what hands the licensing runtime a module's
// content key. Once the key is in the guest, an empty ALLOWED_XPUBS means
// "grant to anyone" — which is how a throwaway anonymous identity was issued a
// grant for com.orbpro.hpop. These tests assert the ruling that stops it, and
// the one that must keep the owner's gallery link alive.

func writeTestPluginRoot(t *testing.T, ids []string, policyJSON string) string {
	t.Helper()
	root := t.TempDir()
	entries := make([]license.PluginCatalogEntry, 0, len(ids))
	for _, id := range ids {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"bundle.wasm.enc", "bundle.key"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		entries = append(entries, license.PluginCatalogEntry{
			ID:            id,
			Version:       "1.0.0",
			EncryptedPath: filepath.Join(id, "bundle.wasm.enc"),
			KeyPath:       filepath.Join(id, "bundle.key"),
		})
	}
	catalog, err := json.Marshal(license.PluginCatalogFile{Plugins: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), catalog, 0o600); err != nil {
		t.Fatal(err)
	}
	if policyJSON != "" {
		if err := os.WriteFile(filepath.Join(root, license.GrantPolicyFileName), []byte(policyJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func planFor(t *testing.T, root string) catalogPublicationPlan {
	t.Helper()
	reg, err := license.LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	return planCatalogPublication(reg, nil)
}

func admitted(plan catalogPublicationPlan, id string) bool {
	for _, asset := range plan.Admitted {
		if asset.ID == id {
			return true
		}
	}
	return false
}

// The P1, verbatim: an unentitled paid module must not reach the key server.
func TestPlanRefusesAPaidModuleWithNoEntitlement(t *testing.T) {
	root := writeTestPluginRoot(t, []string{"com.orbpro.hpop"}, `{
      "modules": [{"match": "com.orbpro.hpop", "policy": "allowlist"}]
    }`)

	plan := planFor(t, root)

	if admitted(plan, "com.orbpro.hpop") {
		t.Fatal("com.orbpro.hpop was admitted with an empty allowlist — its content key would reach the key server, " +
			"which grants freely on an empty allowlist. This is the reported defect.")
	}
	if len(plan.Refused) != 1 || plan.Refused[0] != "com.orbpro.hpop" {
		t.Fatalf("Refused = %v, want [com.orbpro.hpop]", plan.Refused)
	}
}

// The countervailing owner directive of 2026-08-07: the private sandcastle
// gallery link derives a fresh identity from the URL UUID, so the RF modules
// must still be granted to an xpub nobody has ever seen.
func TestPlanKeepsTheLinkKeyGalleryLaneOpen(t *testing.T) {
	root := writeTestPluginRoot(t,
		[]string{"com.orbpro.rf-fspl", "com.orbpro.rf-rain", "com.orbpro.hpop"},
		`{
          "modules": [
            {"match": "com.orbpro.hpop", "policy": "allowlist"},
            {"match": "com.orbpro.rf-*", "policy": "link-key", "note": "owner directive 2026-08-07"}
          ]
        }`)

	plan := planFor(t, root)

	for _, id := range []string{"com.orbpro.rf-fspl", "com.orbpro.rf-rain"} {
		if !admitted(plan, id) {
			t.Fatalf("%s was refused — that breaks the owner's sandcastle gallery link", id)
		}
	}
	if admitted(plan, "com.orbpro.hpop") {
		t.Fatal("com.orbpro.hpop was admitted alongside the open RF set; the two lanes must be ruled apart")
	}
	if len(plan.Open) != 2 {
		t.Fatalf("Open = %v, want both RF modules listed in the boot ledger", plan.Open)
	}
}

// A module entitled to a real xpub is admitted; the key server then enforces
// membership and the cross-curve EPM binding at proof time.
func TestPlanAdmitsAnEntitledModule(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "com.orbpro.hpop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bundle.wasm.enc", "bundle.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := json.Marshal(license.PluginCatalogFile{Plugins: []license.PluginCatalogEntry{{
		ID:            "com.orbpro.hpop",
		Version:       "1.0.0",
		EncryptedPath: filepath.Join("com.orbpro.hpop", "bundle.wasm.enc"),
		KeyPath:       filepath.Join("com.orbpro.hpop", "bundle.key"),
		AllowedXpubs:  []string{"xpubENTITLEDCUSTOMER"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), catalog, 0o600); err != nil {
		t.Fatal(err)
	}

	plan := planFor(t, root)

	if !admitted(plan, "com.orbpro.hpop") {
		t.Fatal("an entitled module was refused; the allowlist lane must still deliver to paying customers")
	}
	if len(plan.Open) != 0 {
		t.Fatalf("Open = %v, want empty — an allowlisted module is not open", plan.Open)
	}
}

// Fail-closed by default: a provider that never writes a policy file publishes
// nothing it cannot enforce. This is the state every existing node is in.
func TestPlanIsFailClosedWithNoPolicyFile(t *testing.T) {
	root := writeTestPluginRoot(t, []string{"com.orbpro.hpop", "com.orbpro.rf-fspl"}, "")

	plan := planFor(t, root)

	if len(plan.Admitted) != 0 {
		t.Fatalf("Admitted = %d module(s) with no policy file and no allowlists; want 0", len(plan.Admitted))
	}
	if len(plan.Refused) != 2 {
		t.Fatalf("Refused = %v, want both modules", plan.Refused)
	}
}

// The owner's one-line way to end the "for now": every module snaps back to
// allowlist and the unentitled ones stop being served.
func TestPlanLockdownSwitchClosesEveryOpenLane(t *testing.T) {
	root := writeTestPluginRoot(t, []string{"com.orbpro.rf-fspl"}, `{
      "enforce_allowlist_only": true,
      "modules": [{"match": "com.orbpro.rf-*", "policy": "link-key"}]
    }`)

	plan := planFor(t, root)

	if len(plan.Admitted) != 0 {
		t.Fatal("enforce_allowlist_only did not close the open lane")
	}
}
