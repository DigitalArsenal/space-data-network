package update

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// END-TO-END RETENTION, through the REAL Apply.
//
// The unit tests in slots_test.go pin the policy functions. This one drives
// seven consecutive signed updates through Stage + Apply against one bundle
// root — the same code path a box runs — and then reverses. It exists because
// the failures this lane keeps producing are not in the policy, they are in the
// seams: a rollback directory named after the wrong update, a state file whose
// `previous` and `slots` disagree, a prune that removes the directory the state
// still points at.

func stageSignedUpdateAs(t *testing.T, paths Paths, signer *testSigner, updateID, version string, sequence int64, currentSequence int64) *StagedUpdate {
	t.Helper()
	files := map[string]string{"bin/spacedatanetwork": "binary-" + version}
	bundleBytes := makeBundleTarGz(t, version, files)
	wasmBytes := BuildCarrier(bundleBytes)
	manifestBytes := signer.signedManifest(t, func(doc map[string]any) {
		doc["update_id"] = updateID
		doc["version"] = version
		doc["sequence"] = sequence
		doc["bundle"].(map[string]any)["hash"] = sha256Hex(bundleBytes)
		doc["bundle"].(map[string]any)["size"] = int64(len(bundleBytes))
		doc["wasm"].(map[string]any)["hash"] = sha256Hex(wasmBytes)
	}, bundleBytes, wasmBytes)
	staged, err := Stage(paths, manifestBytes, wasmBytes, HostVerifyOptions(signer.roots(t), currentSequence, time.Now()))
	if err != nil {
		t.Fatalf("stage %s: %v", updateID, err)
	}
	return staged
}

func readLedger(t *testing.T, paths Paths) []DeployLedgerEntry {
	t.Helper()
	file, err := os.Open(filepath.Join(paths.Root, deployLedgerName))
	if err != nil {
		t.Fatalf("open deploy ledger: %v", err)
	}
	defer file.Close()
	var entries []DeployLedgerEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry DeployLedgerEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("ledger line is not JSON: %v (%s)", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestSevenAppliesLeaveExactlyFiveReversibleSlots(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)

	const applies = 7
	var currentSequence int64
	for i := 1; i <= applies; i++ {
		updateID := fmt.Sprintf("cli-beta-%04d", i)
		version := fmt.Sprintf("2.0.%d", i)
		sequence := int64(1000 + i)
		staged := stageSignedUpdateAs(t, paths, signer, updateID, version, sequence, currentSequence)
		result, err := Apply(paths, ApplyOptions{UpdateID: staged.UpdateID, Trigger: "signal", SignalKeyID: "testkey"})
		if err != nil {
			t.Fatalf("apply %s: %v", updateID, err)
		}
		if len(result.PruneErrors) != 0 {
			t.Fatalf("apply %s reported prune errors: %v", updateID, result.PruneErrors)
		}
		currentSequence = sequence

		want := i
		if want > RollbackSlotLimit {
			want = RollbackSlotLimit
		}
		if len(result.Slots) != want {
			t.Fatalf("after apply %d the box holds %d slots, want %d", i, len(result.Slots), want)
		}
	}

	// The running build is the last one applied.
	state, err := LoadState(paths)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Version != "2.0.7" {
		t.Fatalf("running version = %s, want 2.0.7", state.Version)
	}
	// The bundle really carries those bytes, not just the state file's claim.
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(binary) != "binary-2.0.7" {
		t.Fatalf("installed binary = %q, want binary-2.0.7", binary)
	}

	// Previous must mirror Slots[0] exactly, or a box rolled back onto a
	// pre-retention binary reverses to a different build than this one would.
	if state.Previous == nil || state.Previous.Rollback != state.Slots[0].Path {
		t.Fatalf("state.previous (%+v) does not mirror slots[0] (%+v)", state.Previous, state.Slots[0])
	}

	inv, err := Inventory(paths)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(inv.Slots) != RollbackSlotLimit {
		t.Fatalf("inventory holds %d slots, want exactly %d", len(inv.Slots), RollbackSlotLimit)
	}
	if len(inv.Missing) != 0 {
		t.Fatalf("inventory reports missing reverse targets: %+v — retention deleted something the state still claims", inv.Missing)
	}
	if len(inv.Unmanaged) != 0 {
		t.Fatalf("inventory reports unmanaged rollback dirs: %v — retention did not prune", inv.Unmanaged)
	}
	// Newest first, and they are the five most recent DISPLACED builds:
	// applying 2.0.7 displaces 2.0.6, and so on down to 2.0.2.
	wantVersions := []string{"2.0.6", "2.0.5", "2.0.4", "2.0.3", "2.0.2"}
	for i, want := range wantVersions {
		if inv.Slots[i].Version != want {
			t.Fatalf("slot[%d] version = %s, want %s (slots must be newest-first)", i, inv.Slots[i].Version, want)
		}
		if _, err := os.Stat(inv.Slots[i].Path); err != nil {
			t.Fatalf("slot[%d] (%s) is recorded but not on disk: %v", i, inv.Slots[i].Version, err)
		}
	}

	// The ledger must show every apply AND every retention decision, with the
	// trigger recorded — an unattended self-upgrade and a hand-run install are
	// otherwise indistinguishable after the fact.
	entries := readLedger(t, paths)
	var applyCount, retainCount, signalTriggered int
	var prunedTotal int
	for _, entry := range entries {
		switch entry.Action {
		case "apply":
			applyCount++
			if entry.Trigger == "signal" {
				signalTriggered++
			}
		case "retain":
			retainCount++
			prunedTotal += len(entry.PrunedSlots)
		}
	}
	if applyCount != applies {
		t.Fatalf("ledger recorded %d applies, want %d", applyCount, applies)
	}
	if signalTriggered != applies {
		t.Fatalf("ledger recorded %d signal-triggered applies, want %d", signalTriggered, applies)
	}
	if retainCount != 2 {
		t.Fatalf("ledger recorded %d retention decisions, want 2 (applies 6 and 7 each prune one)", retainCount)
	}
	if prunedTotal != 2 {
		t.Fatalf("ledger recorded %d pruned slots, want 2", prunedTotal)
	}
}

func TestRollbackDefaultsToPreviousAndReachesOlderSlotsByName(t *testing.T) {
	signer := newTestSigner(t)
	paths, root := setupBundleRoot(t, signer)

	var currentSequence int64
	for i := 1; i <= 4; i++ {
		staged := stageSignedUpdateAs(t, paths, signer,
			fmt.Sprintf("cli-beta-%04d", i), fmt.Sprintf("3.0.%d", i), int64(2000+i), currentSequence)
		if _, err := Apply(paths, ApplyOptions{UpdateID: staged.UpdateID}); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
		currentSequence = int64(2000 + i)
	}

	// Undirected rollback restores the immediately-previous build (3.0.3).
	result, err := Rollback(paths, RollbackOptions{Reason: "health gate failed after update"})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if result.RestoredVersion != "3.0.3" {
		t.Fatalf("undirected rollback restored %s, want the immediately-previous build 3.0.3", result.RestoredVersion)
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(binary) != "binary-3.0.3" {
		t.Fatalf("installed binary = %q, want binary-3.0.3", binary)
	}

	// The rollback wrote its own ledger line — it was the one bundle mutation
	// that used to write none, and the one that runs unattended.
	entries := readLedger(t, paths)
	last := entries[len(entries)-1]
	if last.Action != "rollback" {
		t.Fatalf("last ledger action = %q, want rollback", last.Action)
	}
	if !last.Rollback || last.Reason == "" {
		t.Fatalf("rollback ledger line must be marked as a rollback and carry its reason: %+v", last)
	}
	if last.Version != "3.0.3" || last.FromVersion != "3.0.4" {
		t.Fatalf("rollback ledger line = %s <- %s, want 3.0.3 <- 3.0.4", last.Version, last.FromVersion)
	}

	// The displaced build is quarantined, so a replayed signal for it cannot
	// reinstall it in a loop.
	if !HasFailedUpdate(paths, "cli-beta-0004") {
		t.Fatal("the reversed update must be quarantined under updates/failed/")
	}

	// The consumed slot is gone from the inventory; the older ones remain and
	// are reachable BY NAME.
	inv, err := Inventory(paths)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	for _, slot := range inv.Slots {
		if slot.Version == "3.0.3" {
			t.Fatal("the consumed slot must be dropped from the inventory")
		}
	}
	named, err := Rollback(paths, RollbackOptions{Slot: "3.0.1", Reason: "named an older generation deliberately"})
	if err != nil {
		t.Fatalf("named rollback: %v", err)
	}
	if named.RestoredVersion != "3.0.1" {
		t.Fatalf("named rollback restored %s, want 3.0.1", named.RestoredVersion)
	}
}
