package sdnmodules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ipfs/kubo/sdn/sdnmodules"
)

// TestRegistryRoundTrip covers the persisted registry: put (insert + replace
// preserving InstalledAt), enable/disable, remove, and the 0600 file contract.
func TestRegistryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r, err := sdnmodules.NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if err := r.Put(sdnmodules.InstalledEntry{ID: "a", ContentHash: "aa", Enabled: true, InstalledAt: "t0"}); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := r.Put(sdnmodules.InstalledEntry{ID: "b", ContentHash: "bb", Enabled: true}); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	list, _ := r.List()
	if len(list) != 2 {
		t.Fatalf("List = %d, want 2", len(list))
	}

	// Replace preserves the original InstalledAt when the new entry omits it.
	if err := r.Put(sdnmodules.InstalledEntry{ID: "a", ContentHash: "aa2", Enabled: true}); err != nil {
		t.Fatalf("Put a replace: %v", err)
	}
	ea, ok, _ := r.Get("a")
	if !ok || ea.ContentHash != "aa2" || ea.InstalledAt != "t0" {
		t.Fatalf("replace lost fields: %+v (ok=%v)", ea, ok)
	}

	// Disable, then remove.
	if err := r.SetEnabled("b", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	eb, _, _ := r.Get("b")
	if eb.Enabled {
		t.Fatalf("SetEnabled(false) did not stick: %+v", eb)
	}
	if err := r.Remove("a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := r.Get("a"); ok {
		t.Fatalf("Remove did not delete entry a")
	}

	// 0600 permission contract on the persisted file.
	info, err := os.Stat(filepath.Join(dir, "installed.json"))
	if err != nil {
		t.Fatalf("stat registry: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("registry perms = %v, want 0600", perm)
	}

	// A fresh Registry over the same dir sees the persisted state.
	r2, _ := sdnmodules.NewRegistry(dir)
	l2, _ := r2.List()
	if len(l2) != 1 || l2[0].ID != "b" {
		t.Fatalf("reopened registry = %+v, want [b]", l2)
	}
}

// TestRegistryNoPersistence: an empty dir is no-persistence mode — Put/Remove are
// no-ops, List is empty, and no file is written.
func TestRegistryNoPersistence(t *testing.T) {
	r, err := sdnmodules.NewRegistry("")
	if err != nil {
		t.Fatalf("NewRegistry(\"\"): %v", err)
	}
	if r.Path() != "" || r.Dir() != "" {
		t.Fatalf("no-persistence registry must report empty dir/path")
	}
	if err := r.Put(sdnmodules.InstalledEntry{ID: "x", ContentHash: "xx", Enabled: true}); err != nil {
		t.Fatalf("Put in no-persistence: %v", err)
	}
	list, _ := r.List()
	if len(list) != 0 {
		t.Fatalf("no-persistence List = %d, want 0", len(list))
	}
}
