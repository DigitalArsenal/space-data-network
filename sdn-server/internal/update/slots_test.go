package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The retention rule is a policy with an exact number in it (owner ruling
// 2026-08-09: five). These tests pin the number, the ORDER, the fact that the
// pruning is ledgered before it deletes, and the two properties that made the
// old one-slot rule dangerous: that a reverse target is never silently lost,
// and that the default reverse target is the immediately-previous build.

func slotPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	paths := PathsFor(root)
	for _, dir := range []string{paths.Updates, paths.Staged, paths.Rollback, paths.Failed, paths.Incoming} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return paths
}

func makeSlotDir(t *testing.T, paths Paths, id string) string {
	t.Helper()
	dir := filepath.Join(paths.Rollback, id)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir slot %s: %v", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "spacedatanetwork"), []byte(id), 0o755); err != nil {
		t.Fatalf("write slot binary %s: %v", id, err)
	}
	return dir
}

func TestRetentionKeepsFiveNewestAndPrunesTheRest(t *testing.T) {
	paths := slotPaths(t)

	var slots []StateSlot
	// Eight applies, newest last.
	for i := 1; i <= 8; i++ {
		id := "u" + string(rune('0'+i))
		dir := makeSlotDir(t, paths, id)
		slots = recordRollbackSlot(slots, StateSlot{Sequence: int64(i), UpdateID: id, Version: "v" + id, Path: dir})
	}
	if len(slots) != RollbackSlotLimit {
		t.Fatalf("retained %d slots, want %d", len(slots), RollbackSlotLimit)
	}
	if slots[0].UpdateID != "u8" {
		t.Fatalf("slots[0] = %s, want the newest (u8) — the default reverse target must be the immediately-previous build", slots[0].UpdateID)
	}
	if slots[len(slots)-1].UpdateID != "u4" {
		t.Fatalf("oldest retained = %s, want u4", slots[len(slots)-1].UpdateID)
	}

	pruned, errs := pruneToRetention(paths, slots, "2026-08-09T00:00:00Z")
	if len(errs) != 0 {
		t.Fatalf("prune errors: %v", errs)
	}
	if len(pruned) != 3 {
		t.Fatalf("pruned %d directories (%v), want 3 (u1..u3)", len(pruned), pruned)
	}
	for _, id := range []string{"u1", "u2", "u3"} {
		if _, err := os.Stat(filepath.Join(paths.Rollback, id)); !os.IsNotExist(err) {
			t.Fatalf("slot %s should have been pruned", id)
		}
	}
	for _, id := range []string{"u4", "u5", "u6", "u7", "u8"} {
		if _, err := os.Stat(filepath.Join(paths.Rollback, id)); err != nil {
			t.Fatalf("slot %s must be retained: %v", id, err)
		}
	}
}

// The prune must be RECORDED before it deletes. This is the same discipline as
// the apply's ledger precondition: if the record cannot be written, the
// destructive step does not happen at all.
func TestRetentionPruneIsLedgeredBeforeItDeletes(t *testing.T) {
	paths := slotPaths(t)
	keepDir := makeSlotDir(t, paths, "keep")
	doomedDir := makeSlotDir(t, paths, "doomed")

	pruned, errs := pruneToRetention(paths, []StateSlot{{Sequence: 2, UpdateID: "keep", Path: keepDir}}, "2026-08-09T00:00:00Z")
	if len(errs) != 0 {
		t.Fatalf("prune errors: %v", errs)
	}
	if len(pruned) != 1 || pruned[0] != doomedDir {
		t.Fatalf("pruned = %v, want [%s]", pruned, doomedDir)
	}

	ledger, err := os.ReadFile(filepath.Join(paths.Root, deployLedgerName))
	if err != nil {
		t.Fatalf("the retention prune wrote no ledger line: %v", err)
	}
	var entry DeployLedgerEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(ledger))), &entry); err != nil {
		t.Fatalf("ledger line is not a JSON entry: %v", err)
	}
	if entry.Action != "retain" {
		t.Fatalf("ledger action = %q, want retain", entry.Action)
	}
	if len(entry.PrunedSlots) != 1 || entry.PrunedSlots[0] != doomedDir {
		t.Fatalf("ledger pruned_slots = %v, want [%s] — a deletion nobody can name is the defect this closes", entry.PrunedSlots, doomedDir)
	}
	if len(entry.HeldSlots) != 1 || entry.HeldSlots[0] != "keep" {
		t.Fatalf("ledger held_slots = %v, want [keep]", entry.HeldSlots)
	}
}

// A record that cannot be written must cancel the deletion, not proceed with
// it. Simulated by making the bundle root unwritable, which is the only thing
// that can stop the in-bundle ledger write.
func TestRetentionPruneSkippedWhenUnrecordable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permission this test relies on")
	}
	paths := slotPaths(t)
	doomedDir := makeSlotDir(t, paths, "doomed")

	if err := os.Chmod(paths.Root, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(paths.Root, 0o755) })

	pruned, errs := pruneToRetention(paths, nil, "2026-08-09T00:00:00Z")
	if len(pruned) != 0 {
		t.Fatalf("pruned %v despite an unrecordable retention decision", pruned)
	}
	if len(errs) == 0 {
		t.Fatal("an unrecordable prune must report why it was skipped")
	}
	if _, err := os.Stat(doomedDir); err != nil {
		t.Fatalf("the doomed slot must survive an unrecordable prune: %v", err)
	}
}

// The first apply on a never-updated bundle displaces a payload with no
// recorded identity. It is still a reverse target, and discarding it would
// leave the riskiest install with nothing to fall back to.
func TestFirstApplyRetainsTheUnidentifiedShippedPayload(t *testing.T) {
	paths := slotPaths(t)
	dir := makeSlotDir(t, paths, "first")
	slots := recordRollbackSlot(nil, StateSlot{Sequence: 0, UpdateID: "", Path: dir})
	if len(slots) != 1 {
		t.Fatalf("retained %d slots, want 1 — the shipped payload is a valid reverse target", len(slots))
	}
	if !strings.Contains(slots[0].Describe(), "no recorded update id") {
		t.Fatalf("Describe() = %q, want it to say the identity is unrecorded", slots[0].Describe())
	}
}

// The default is the immediately-previous build; an older generation must be
// NAMED. This asymmetry is the lesson of the 2026-08-09 reconciliation, where
// restoring a two-generation-old slot would have silently reverted four landed
// lanes.
func TestSelectSlotDefaultsToPreviousAndRequiresNamingOlder(t *testing.T) {
	slots := []StateSlot{
		{Sequence: 3, UpdateID: "c", Version: "v3", Path: "/rb/c"},
		{Sequence: 2, UpdateID: "b", Version: "v2", Path: "/rb/b"},
		{Sequence: 1, UpdateID: "a", Version: "v1", Path: "/rb/a"},
	}
	got, err := selectSlot(slots, "")
	if err != nil {
		t.Fatalf("default select: %v", err)
	}
	if got.UpdateID != "c" {
		t.Fatalf("default slot = %s, want c (the immediately-previous build)", got.UpdateID)
	}
	for _, selector := range []string{"a", "v1", "/rb/a"} {
		got, err := selectSlot(slots, selector)
		if err != nil {
			t.Fatalf("select %q: %v", selector, err)
		}
		if got.UpdateID != "a" {
			t.Fatalf("select %q = %s, want a", selector, got.UpdateID)
		}
	}
	if _, err := selectSlot(slots, "nope"); err == nil {
		t.Fatal("an unmatched selector must be refused, and the refusal must list what the box holds")
	} else if !strings.Contains(err.Error(), "c (v3)") {
		t.Fatalf("refusal %q does not list the available slots", err.Error())
	}
	if _, err := selectSlot(nil, ""); err == nil {
		t.Fatal("a box with no slots must refuse a rollback rather than pretend one is possible")
	}
}

// A prune must never reach outside the rollback root, and must never touch a
// directory a slot record still claims — that would recreate the "previous that
// can no longer be fetched" defect.
func TestPrunePlanNeverTouchesClaimedOrOutsideDirectories(t *testing.T) {
	paths := slotPaths(t)
	keep := makeSlotDir(t, paths, "keep")
	drop := makeSlotDir(t, paths, "drop")
	// A sibling directory outside the rollback root must be invisible to the plan.
	outside := filepath.Join(paths.Updates, "not-a-rollback")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	plan, err := planRollbackPrune(paths, []StateSlot{{UpdateID: "keep", Path: keep}})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan) != 1 || plan[0] != drop {
		t.Fatalf("plan = %v, want exactly [%s]", plan, drop)
	}
}

func TestInventoryReportsMissingAndUnmanagedSlots(t *testing.T) {
	paths := slotPaths(t)
	live := makeSlotDir(t, paths, "live")
	stray := makeSlotDir(t, paths, "stray")
	state := &State{
		Schema:   StateSchema,
		Sequence: 9,
		UpdateID: "current",
		Version:  "v9",
		Slots: []StateSlot{
			{Sequence: 8, UpdateID: "live", Version: "v8", Path: live},
			{Sequence: 7, UpdateID: "gone", Version: "v7", Path: filepath.Join(paths.Rollback, "gone")},
		},
	}
	if err := SaveState(paths, state); err != nil {
		t.Fatalf("save state: %v", err)
	}

	inv, err := Inventory(paths)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if inv.Limit != RollbackSlotLimit {
		t.Fatalf("inventory limit = %d, want %d", inv.Limit, RollbackSlotLimit)
	}
	if len(inv.Slots) != 1 || inv.Slots[0].UpdateID != "live" {
		t.Fatalf("slots = %+v, want just the on-disk one", inv.Slots)
	}
	if len(inv.Missing) != 1 || inv.Missing[0].UpdateID != "gone" {
		t.Fatalf("missing = %+v, want the recorded-but-absent slot reported, never silently dropped", inv.Missing)
	}
	if len(inv.Unmanaged) != 1 || inv.Unmanaged[0] != stray {
		t.Fatalf("unmanaged = %v, want [%s]", inv.Unmanaged, stray)
	}
}
