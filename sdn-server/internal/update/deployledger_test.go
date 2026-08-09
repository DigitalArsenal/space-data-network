package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance for ops-host01-unledgered-rolls-and-fleet-skew's structural
// half: a change that cannot be recorded must not reach the bundle.

func TestRecordDeployLedgerEntryWritesInBundleRecord(t *testing.T) {
	root := t.TempDir()
	paths := PathsFor(root)

	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{
		Action:       "apply",
		UpdateID:     "sdn-cli-bundle-1.0.6-updatelane.deadbeef",
		Version:      "1.0.6-updatelane.deadbeef",
		Sequence:     1786278355,
		Channel:      "beta",
		FromVersion:  "1.0.6-updatelane.cafebabe",
		FromSequence: 1786277316,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, deployLedgerName))
	if err != nil {
		t.Fatalf("in-bundle ledger not written: %v", err)
	}
	var got DeployLedgerEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &got); err != nil {
		t.Fatalf("ledger line is not valid JSON: %v (%q)", err, raw)
	}
	if got.Schema != deployLedgerSchema {
		t.Errorf("schema = %q, want %q", got.Schema, deployLedgerSchema)
	}
	// The three facts that make a roll attributable after the fact: what
	// landed, what it superseded, and who held the box.
	if got.UpdateID == "" || got.Version == "" || got.Sequence == 0 {
		t.Errorf("entry does not identify the artifact: %+v", got)
	}
	if got.FromVersion != "1.0.6-updatelane.cafebabe" || got.FromSequence != 1786277316 {
		t.Errorf("entry does not record what it superseded: %+v", got)
	}
	if got.LockHolder == "" {
		t.Error("entry does not record lock custody; an unlocked apply must still say so")
	}
	if got.RecordedAt == "" || got.PID == 0 || got.BundleRoot != root {
		t.Errorf("entry missing provenance: %+v", got)
	}
}

// An unlocked apply is recorded as unlocked rather than passing silently —
// the failure mode that made 2026-08-09's roll history unreconstructable.
func TestRecordDeployLedgerEntryNamesMissingLock(t *testing.T) {
	if _, err := os.Stat(deployLockPath); err == nil {
		t.Skip("a real cutover lock is present on this box; not asserting on it")
	}
	root := t.TempDir()
	if err := RecordDeployLedgerEntry(PathsFor(root), DeployLedgerEntry{Action: "apply", UpdateID: "u", Version: "v"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, deployLedgerName))
	if !strings.Contains(string(raw), "UNLOCKED") {
		t.Errorf("an apply with no lock held must say so verbatim; got %q", raw)
	}
}

// THE ACCEPTANCE: if the record cannot be written, the apply is refused —
// and the refusal explains itself rather than surfacing as a generic I/O error.
func TestRecordDeployLedgerEntryRefusesWhenUnwritable(t *testing.T) {
	root := t.TempDir()
	// Make the ledger path un-openable by putting a directory in its place.
	if err := os.Mkdir(filepath.Join(root, deployLedgerName), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	err := RecordDeployLedgerEntry(PathsFor(root), DeployLedgerEntry{
		Action: "apply", UpdateID: "u", Version: "v", Sequence: 1, Channel: "beta",
	})
	if err == nil {
		t.Fatal("an unrecordable apply was allowed to proceed — this is the whole defect")
	}
	var refusal *DeployLedgerRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want *DeployLedgerRefusal, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "REFUSING TO APPLY") {
		t.Errorf("refusal does not name itself as a refusal: %v", err)
	}
}

// An empty bundle root has nothing to record against, so it cannot be applied
// to either. Guards against a caller passing a zero Paths and silently
// writing the ledger to a relative path in the process's cwd.
func TestRecordDeployLedgerEntryRefusesEmptyRoot(t *testing.T) {
	err := RecordDeployLedgerEntry(Paths{}, DeployLedgerEntry{Action: "apply", UpdateID: "u"})
	if err == nil {
		t.Fatal("an apply with no bundle root was allowed to proceed")
	}
	var refusal *DeployLedgerRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want *DeployLedgerRefusal, got %T: %v", err, err)
	}
}
