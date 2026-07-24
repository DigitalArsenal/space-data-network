package sdnmodules

import (
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/modulert"
	"github.com/ipfs/kubo/sdn/sdncron"
	"github.com/ipfs/kubo/sdn/sdnservices"
)

// Boot check (task sdn-licensing-module-load): a persisted module that cannot
// be re-registered at boot is a unit of the node that is silently NOT running.
// Boot must tolerate the failure (skip, keep booting) AND record it in the
// BootFailures ledger so the runtime plugin can surface it loudly.
func TestBootRecordsFailureForUnresolvableInstalledModule(t *testing.T) {
	registry, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(InstalledEntry{
		ID:          "licensing",
		ContentHash: strings.Repeat("a", 64),
		Enabled:     true,
		Source:      "admin",
	}); err != nil {
		t.Fatal(err)
	}

	installer, err := New(Config{
		Services: &sdnservices.Services{
			Scheduler: sdncron.NewScheduler(nil, nil),
			CapReg:    modulert.NewCapabilityRegistry(),
		},
		Registry: registry,
		// Blockstore deliberately nil: the persisted bytes cannot resolve.
	})
	if err != nil {
		t.Fatal(err)
	}

	count, err := installer.Boot(t.Context())
	if err != nil {
		t.Fatalf("Boot() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("Boot() registered = %d, want 0", count)
	}

	failures := installer.BootFailures()
	if len(failures) != 1 {
		t.Fatalf("BootFailures() = %d entries, want 1: %+v", len(failures), failures)
	}
	if failures[0].ID != "licensing" || failures[0].Source != "admin" {
		t.Fatalf("BootFailures()[0] = %+v, want id=licensing source=admin", failures[0])
	}
	if !strings.Contains(failures[0].Error, "no blockstore") {
		t.Fatalf("BootFailures()[0].Error = %q, want no-blockstore error", failures[0].Error)
	}
	if failures[0].At == "" {
		t.Fatal("BootFailures()[0].At is empty")
	}

	// A subsequent clean Boot resets the ledger.
	if err := registry.SetEnabled("licensing", false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if _, err := installer.Boot(t.Context()); err != nil {
		t.Fatalf("second Boot() error = %v", err)
	}
	if failures := installer.BootFailures(); len(failures) != 0 {
		t.Fatalf("BootFailures() after clean boot = %+v, want empty", failures)
	}
}
