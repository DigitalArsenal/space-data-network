package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// kuboRuntimeRelPath is the bundle-relative path to the separable Kubo
// runtime subtree that G3's two-phase apply installs and rolls back
// independently of the rest of the payload.
const kuboRuntimeRelPath = "runtime/kubo"

// KuboPhaseHook lets a coordinator (e.g. node.go) drive the Kubo process
// lifecycle around the Kubo-first phase of a two-phase apply. Apply never
// stops or starts the Kubo subprocess itself — that is a node.go/lifecycle
// concern out of this package's scope — but it calls this hook at the two
// points where process control matters:
//
//	BeforeSwap: immediately before the runtime/kubo/ rename, so a live Kubo
//	            process holding those files open can be stopped first.
//	AfterSwap:  immediately after the new runtime/kubo/ payload is in place
//	            but BEFORE the phase is durably committed. A non-nil error
//	            here rolls the Kubo phase back and fails Apply before phase
//	            2 (the SDN/remaining payload) ever starts. A real hook
//	            should restart Kubo against the new binary and probe its
//	            API here.
//
// NoopKuboPhaseHook is the default and performs no process control.
//
// TODO(node-wiring): node.go should supply a KuboPhaseHook that actually
// stops the running kubo/ipfs subprocess in BeforeSwap and, in AfterSwap,
// restarts it against the newly-installed binary and health-checks its API
// (e.g. `ipfs id` or the RPC /api/v0/version endpoint) before returning nil.
type KuboPhaseHook interface {
	BeforeSwap(paths Paths, updateID string) error
	AfterSwap(paths Paths, updateID string) error
}

type noopKuboPhaseHook struct{}

func (noopKuboPhaseHook) BeforeSwap(Paths, string) error { return nil }
func (noopKuboPhaseHook) AfterSwap(Paths, string) error  { return nil }

// NoopKuboPhaseHook performs no Kubo process control and always reports the
// phase healthy. It is the default when ApplyOptions.KuboPhaseHook is nil.
var NoopKuboPhaseHook KuboPhaseHook = noopKuboPhaseHook{}

// errSimulatedCrash is returned by applyTwoPhase only when a test asks it to
// simulate the process dying immediately after the Kubo phase commits. It is
// unexported and only reachable via the unexported ApplyOptions.testFault
// field, which production code never sets.
var errSimulatedCrash = errors.New("update: simulated crash after kubo phase commit (test only)")

// applyFaultInjection is an in-package-only test seam (reached via the
// unexported ApplyOptions.testFault field) for exercising the phase-2-
// failure rollback path and the crash/recovery path through the real Apply
// entry point. Production callers, which all live outside this package,
// have no way to set it.
type applyFaultInjection struct {
	// beforeMainPhase, if non-nil, runs after the Kubo phase has committed
	// and before the SDN/main phase begins. A non-nil return simulates a
	// phase-2 failure: both phases are rolled back and Apply fails.
	beforeMainPhase func() error
	// crashAfterKuboPhase, if true, makes applyTwoPhase return
	// errSimulatedCrash immediately after the phase marker is persisted,
	// leaving the Kubo-new/SDN-old on-disk state and the marker exactly as
	// a real process crash at that instant would.
	crashAfterKuboPhase bool
}

// hasSeparableKuboSubtree reports whether the incoming bundle root ships a
// runtime/kubo/ subtree that can be swapped independently. Bundles without
// one (older releases, or non-Kubo-bearing targets) fall back to the
// original single-phase swapBundleContents path unchanged.
func hasSeparableKuboSubtree(newRoot string) bool {
	info, err := os.Stat(filepath.Join(newRoot, filepath.FromSlash(kuboRuntimeRelPath)))
	return err == nil && info.IsDir()
}

// joinRel joins a bundle-relative path prefix ("", or "runtime") with an
// entry name using "/" so recorded paths are stable across platforms.
func joinRel(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

// swapEntrySet retires currentDir entries that are either replaced by a
// same-named entry in newDir, or considered payload (isPayload) but absent
// from newDir, moving them into rollbackDir; then installs every entry from
// newDir into currentDir. Every moved/installed name is recorded (as a
// bundle-relative path, prefix-joined) into movedToRollback/installed so a
// caller can undo a partial run. This is the single building block shared
// by the legacy single-phase swap, the two-phase main/SDN phase, and the
// nested runtime/ subtree pass (which excludes "kubo" via skip).
func swapEntrySet(currentDir, newDir, rollbackDir, prefix string, isPayload func(name string) bool, skip func(name string) bool, movedToRollback, installed *[]string) error {
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return err
	}
	var currentEntries []os.DirEntry
	if entries, err := os.ReadDir(currentDir); err == nil {
		currentEntries = entries
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	newEntries, err := os.ReadDir(newDir)
	if err != nil {
		return err
	}
	newNames := make(map[string]bool, len(newEntries))
	for _, entry := range newEntries {
		newNames[entry.Name()] = true
	}
	for _, entry := range currentEntries {
		name := entry.Name()
		if skip != nil && skip(name) {
			continue
		}
		if !newNames[name] && !isPayload(name) {
			continue
		}
		if err := os.Rename(filepath.Join(currentDir, name), filepath.Join(rollbackDir, name)); err != nil {
			return fmt.Errorf("retire current bundle entry %s: %w", joinRel(prefix, name), err)
		}
		*movedToRollback = append(*movedToRollback, joinRel(prefix, name))
	}
	for _, entry := range newEntries {
		name := entry.Name()
		if skip != nil && skip(name) {
			continue
		}
		if err := os.Rename(filepath.Join(newDir, name), filepath.Join(currentDir, name)); err != nil {
			return fmt.Errorf("install bundle entry %s: %w", joinRel(prefix, name), err)
		}
		*installed = append(*installed, joinRel(prefix, name))
	}
	return nil
}

// restoreEntries is the inverse of a swapEntrySet run: it removes every
// installed entry and moves every retired entry back from rollbackDir to
// paths.Root, best-effort (used both for immediate in-process rollback and
// for crash recovery replaying a persisted marker).
func restoreEntries(paths Paths, rollbackDir string, movedToRollback, installed []string) {
	for _, name := range installed {
		_ = os.RemoveAll(filepath.Join(paths.Root, filepath.FromSlash(name)))
	}
	for _, name := range movedToRollback {
		target := filepath.Join(paths.Root, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		_ = os.Rename(filepath.Join(rollbackDir, filepath.FromSlash(name)), target)
	}
}

// undoKuboPhase reverses exactly the Kubo-phase renames described by moved/
// installed, restoring paths.Root to its pre-phase-1 state. Used both by
// the in-process phase-2-failure rollback and by RecoverPendingApply.
func undoKuboPhase(paths Paths, rollbackDir string, moved, installed []string) {
	restoreEntries(paths, rollbackDir, moved, installed)
}

// swapKuboPhase is phase 1 of the two-phase apply: it swaps only the
// runtime/kubo/ subtree into its own slot under rollbackDir/runtime/kubo,
// running the KuboPhaseHook immediately before and after the swap. On any
// failure it self-heals (restores whatever it had already moved) before
// returning, so a normal (non-crash) phase-1 failure never leaves partial
// state on disk.
func swapKuboPhase(paths Paths, newRoot, rollbackDir string, hook KuboPhaseHook, updateID string) (moved, installed []string, err error) {
	if hook == nil {
		hook = NoopKuboPhaseHook
	}
	newKubo := filepath.Join(newRoot, filepath.FromSlash(kuboRuntimeRelPath))
	if info, statErr := os.Stat(newKubo); statErr != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("kubo phase: incoming bundle has no %s subtree", kuboRuntimeRelPath)
	}

	if err := hook.BeforeSwap(paths, updateID); err != nil {
		return nil, nil, fmt.Errorf("kubo phase pre-swap hook: %w", err)
	}

	curRuntimeDir := filepath.Join(paths.Root, "runtime")
	rbRuntimeDir := filepath.Join(rollbackDir, "runtime")
	if err := os.MkdirAll(curRuntimeDir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(rbRuntimeDir, 0o755); err != nil {
		return nil, nil, err
	}

	curKubo := filepath.Join(curRuntimeDir, "kubo")
	if info, statErr := os.Stat(curKubo); statErr == nil && info.IsDir() {
		if err := os.Rename(curKubo, filepath.Join(rbRuntimeDir, "kubo")); err != nil {
			return nil, nil, fmt.Errorf("retire current kubo runtime: %w", err)
		}
		moved = append(moved, kuboRuntimeRelPath)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, nil, statErr
	}

	if err := os.Rename(newKubo, curKubo); err != nil {
		restoreEntries(paths, rollbackDir, moved, installed)
		return nil, nil, fmt.Errorf("install new kubo runtime: %w", err)
	}
	installed = append(installed, kuboRuntimeRelPath)

	if err := hook.AfterSwap(paths, updateID); err != nil {
		restoreEntries(paths, rollbackDir, moved, installed)
		return nil, nil, fmt.Errorf("kubo phase health check: %w", err)
	}
	return moved, installed, nil
}

// swapMainPhaseContents is phase 2 of the two-phase apply: it swaps every
// remaining bundle-root payload entry, treating the runtime/ subtree
// specially so the kubo/ child (already owned by phase 1) is left alone
// while every other runtime/ child (sdn/, wasmedge/, modules/, ui/, ...) is
// swapped like any other payload entry. On failure it restores everything
// phase 2 itself moved (but NOT phase 1 — the caller is responsible for
// also undoing phase 1 on a phase-2 failure).
func swapMainPhaseContents(paths Paths, newRoot, rollbackDir string) (moved, installed []string, err error) {
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return nil, nil, err
	}

	newRuntimeDir := filepath.Join(newRoot, "runtime")
	if info, statErr := os.Stat(newRuntimeDir); statErr == nil && info.IsDir() {
		curRuntimeDir := filepath.Join(paths.Root, "runtime")
		rbRuntimeDir := filepath.Join(rollbackDir, "runtime")
		skipKubo := func(name string) bool { return name == "kubo" }
		alwaysPayload := func(string) bool { return true }
		if err := swapEntrySet(curRuntimeDir, newRuntimeDir, rbRuntimeDir, "runtime", alwaysPayload, skipKubo, &moved, &installed); err != nil {
			restoreEntries(paths, rollbackDir, moved, installed)
			return moved, installed, err
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, nil, statErr
	}

	skipTop := func(name string) bool { return protectedEntries[name] || name == "runtime" }
	if err := swapEntrySet(paths.Root, newRoot, rollbackDir, "", isBundlePayloadEntry, skipTop, &moved, &installed); err != nil {
		restoreEntries(paths, rollbackDir, moved, installed)
		return moved, installed, err
	}
	return moved, installed, nil
}

// applyTwoPhase orchestrates the Kubo-first two-phase apply: swap
// runtime/kubo/ (phase 1, verified via hook), durably record that the phase
// committed (so a crash before phase 2 finishes is recoverable via
// RecoverPendingApply), then swap the remaining payload (phase 2). Any
// phase-2 failure rolls BOTH phases back and returns an error; phase-1
// failures self-heal inside swapKuboPhase and never reach this far.
func applyTwoPhase(paths Paths, newRoot, rollbackDir, updateID string, hook KuboPhaseHook, fault *applyFaultInjection) error {
	kuboMoved, kuboInstalled, err := swapKuboPhase(paths, newRoot, rollbackDir, hook, updateID)
	if err != nil {
		return fmt.Errorf("kubo phase: %w", err)
	}

	marker := &applyPhaseMarker{
		UpdateID:      updateID,
		RollbackDir:   rollbackDir,
		Phase:         "kubo-done",
		KuboMoved:     kuboMoved,
		KuboInstalled: kuboInstalled,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := savePhaseMarker(paths, marker); err != nil {
		undoKuboPhase(paths, rollbackDir, kuboMoved, kuboInstalled)
		return fmt.Errorf("persist apply phase marker: %w", err)
	}

	if fault != nil && fault.crashAfterKuboPhase {
		// Simulate the process dying right here: return without touching
		// the marker or the swapped files, exactly as a real crash would.
		return errSimulatedCrash
	}
	if fault != nil && fault.beforeMainPhase != nil {
		if err := fault.beforeMainPhase(); err != nil {
			undoKuboPhase(paths, rollbackDir, kuboMoved, kuboInstalled)
			_ = clearPhaseMarker(paths)
			return fmt.Errorf("sdn phase: %w", err)
		}
	}

	if _, _, err := swapMainPhaseContents(paths, newRoot, rollbackDir); err != nil {
		undoKuboPhase(paths, rollbackDir, kuboMoved, kuboInstalled)
		_ = clearPhaseMarker(paths)
		return fmt.Errorf("sdn phase: %w", err)
	}

	if err := clearPhaseMarker(paths); err != nil {
		return fmt.Errorf("update applied but phase marker cleanup failed: %w", err)
	}
	return nil
}
