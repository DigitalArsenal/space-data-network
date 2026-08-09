package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// FORCED BAD BOOT -> AUTOMATIC SELF-ROLLBACK, through the REAL rollback.
//
// The existing helperPostApplyRestart tests stub the rollback function, so they
// prove the CONTROL FLOW reaches it. This one wires the real update.Rollback to
// a real bundle root, because the failure mode that matters is not "was
// rollback called" — it is "did the box actually end up running the previous
// binary, and did it say so". An unattended reversal that leaves no record is
// the unattributable change this whole lane exists to end.

func writeBundleFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// selfRollbackBundle builds a bundle root that looks like a box which has just
// applied a bad update and still holds its predecessor as a rollback slot.
func selfRollbackBundle(t *testing.T) update.Paths {
	t.Helper()
	root := t.TempDir()
	paths := update.PathsFor(root)
	for _, dir := range []string{paths.Updates, paths.Staged, paths.Rollback, paths.Failed, paths.Incoming} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// The (bad) build that is live.
	writeBundleFile(t, filepath.Join(root, "bin", "spacedatanetwork"), "binary-BAD")
	writeBundleFile(t, filepath.Join(root, "manifest.json"),
		`{"schema":"org.spacedatanetwork.bundle.v1","version":"5.0.2","channel":"beta"}`)

	// The good predecessor, sitting in its slot.
	slotDir := filepath.Join(paths.Rollback, "cli-beta-bad")
	writeBundleFile(t, filepath.Join(slotDir, "bin", "spacedatanetwork"), "binary-GOOD")
	writeBundleFile(t, filepath.Join(slotDir, "manifest.json"),
		`{"schema":"org.spacedatanetwork.bundle.v1","version":"5.0.1","channel":"beta"}`)

	slot := update.StateSlot{Sequence: 500, UpdateID: "cli-beta-good", Version: "5.0.1", Channel: "beta", Path: slotDir}
	if err := update.SaveState(paths, &update.State{
		Schema:   update.StateSchema,
		Sequence: 501,
		UpdateID: "cli-beta-bad",
		Version:  "5.0.2",
		Channel:  "beta",
		Slots:    []update.StateSlot{slot},
		Previous: &update.StatePrevious{
			Sequence: slot.Sequence, UpdateID: slot.UpdateID, Version: slot.Version,
			Channel: slot.Channel, Rollback: slot.Path,
		},
	}); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return paths
}

type deadProcess struct{}

func (deadProcess) PID() int    { return 4242 }
func (deadProcess) Kill() error { return nil }

func TestUnhealthyDaemonSelfRollsBackToThePreviousSlotAndSaysSo(t *testing.T) {
	paths := selfRollbackBundle(t)

	var out, errOut bytes.Buffer
	healthCalls := 0
	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:       paths,
		RestartArgv: []string{"/nonexistent/spacedatanetwork", "daemon"},
		AdminURL:    "https://127.0.0.1:5001/",
		Out:         &out,
		Err:         &errOut,
		Client:      &http.Client{},
		StartDaemon: func([]string, io.Writer, io.Writer) (helperStartedProcess, error) {
			return deadProcess{}, nil
		},
		WaitHealth: func(context.Context, *http.Client, string, time.Duration) error {
			healthCalls++
			// FORCED BAD BOOT: the new binary never becomes healthy. The
			// restored one does, so the second probe succeeds.
			if healthCalls == 1 {
				return errors.New("simulated bad boot: daemon never became healthy")
			}
			return nil
		},
		// No Rollback stub: the REAL update.RollbackLast runs.
	})
	if err == nil {
		t.Fatal("a failed health gate must surface as an error even after a successful rollback")
	}
	if !strings.Contains(err.Error(), "rolled back to 5.0.1") {
		t.Fatalf("error = %v, want it to name the restored version", err)
	}

	// The box is really running the previous binary, not merely claiming to.
	binary, err := os.ReadFile(filepath.Join(paths.Root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatalf("read binary: %v", err)
	}
	if string(binary) != "binary-GOOD" {
		t.Fatalf("installed binary = %q, want binary-GOOD — the rollback did not actually swap the bytes", binary)
	}

	state, err := update.LoadState(paths)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Version != "5.0.1" || state.UpdateID != "cli-beta-good" {
		t.Fatalf("state after rollback = %s/%s, want 5.0.1/cli-beta-good", state.Version, state.UpdateID)
	}

	// The bad build is quarantined, so a replayed signal cannot reinstall it.
	if !update.HasFailedUpdate(paths, "cli-beta-bad") {
		t.Fatal("the reversed update must be quarantined under updates/failed/")
	}

	// And it is ON THE RECORD. An unattended reversal with no ledger line is
	// exactly the unattributable change this lane exists to end.
	raw, err := os.ReadFile(filepath.Join(paths.Root, "deploy-ledger.jsonl"))
	if err != nil {
		t.Fatalf("the self-rollback wrote no ledger line: %v", err)
	}
	var entry update.DeployLedgerEntry
	last := strings.TrimSpace(string(raw))
	if i := strings.LastIndex(last, "\n"); i >= 0 {
		last = last[i+1:]
	}
	if err := json.Unmarshal([]byte(last), &entry); err != nil {
		t.Fatalf("ledger line is not JSON: %v", err)
	}
	if entry.Action != "rollback" || !entry.Rollback {
		t.Fatalf("ledger entry = %+v, want a rollback", entry)
	}
	if entry.Version != "5.0.1" || entry.FromVersion != "5.0.2" {
		t.Fatalf("ledger entry = %s <- %s, want 5.0.1 <- 5.0.2", entry.Version, entry.FromVersion)
	}
	if !strings.Contains(entry.Reason, "health") {
		t.Fatalf("ledger reason = %q, want it to name the health failure that triggered it", entry.Reason)
	}
	if !strings.Contains(out.String(), "rollback=applied") {
		t.Fatalf("helper output must report the rollback: %s", out.String())
	}
}

// A box with NO reverse target must fail loudly rather than pretend it reversed.
func TestUnhealthyDaemonWithNoSlotReportsThatItCannotReverse(t *testing.T) {
	root := t.TempDir()
	paths := update.PathsFor(root)
	if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeBundleFile(t, filepath.Join(root, "bin", "spacedatanetwork"), "binary-BAD")

	var out, errOut bytes.Buffer
	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:       paths,
		RestartArgv: []string{"/nonexistent/spacedatanetwork", "daemon"},
		AdminURL:    "https://127.0.0.1:5001/",
		Out:         &out,
		Err:         &errOut,
		Client:      &http.Client{},
		StartDaemon: func([]string, io.Writer, io.Writer) (helperStartedProcess, error) {
			return deadProcess{}, nil
		},
		WaitHealth: func(context.Context, *http.Client, string, time.Duration) error {
			return errors.New("simulated bad boot")
		},
	})
	if err == nil {
		t.Fatal("a bad boot with no reverse target must be an error")
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("error = %v, want it to say the rollback could not happen", err)
	}
}
