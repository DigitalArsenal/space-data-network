package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// protectedEntries are bundle-root entries the swap must never replace: the
// update working tree and the trust store live outside payload contents.
var protectedEntries = map[string]bool{
	updatesDirName: true,
	trustDirName:   true,
}

type ApplyOptions struct {
	// UpdateID selects a staged update. Empty picks the verified candidate
	// with the highest sequence.
	UpdateID string
	DryRun   bool
	Now      time.Time
	// KuboPhaseHook drives Kubo process control around the Kubo-first phase
	// of a two-phase apply (see KuboPhaseHook doc). Nil uses
	// NoopKuboPhaseHook.
	KuboPhaseHook KuboPhaseHook
	// InstalledKuboVersion, when set, is threaded into manifest.Validate via
	// VerifyOptions so a manifest's compatibility.min_kubo_version gate is
	// enforced. Left empty, the gate is skipped (back-compat).
	InstalledKuboVersion string

	// AllowRollback threads the operator's explicit acceptance of a declared
	// source-lineage rollback through to re-verification at apply time. Apply
	// re-verifies from scratch, so without this a staged rollback accepted at
	// `install` time would be refused a moment later at `apply` time.
	AllowRollback bool

	// Trigger and SignalKeyID travel into the deploy ledger line so an
	// unattended self-upgrade is distinguishable, after the fact, from an
	// operator who ran `update install` by hand. On a box where every agent
	// authenticates with one key from one IP, this is the only thing that can
	// tell them apart.
	Trigger     string
	SignalKeyID string

	// testFault is an in-package-only fault-injection seam used by this
	// package's own tests to exercise the phase-2-failure and crash-
	// recovery paths through the real Apply entry point. It is unexported,
	// so no caller outside internal/update can set it.
	testFault *applyFaultInjection
}

type ApplyResult struct {
	UpdateID     string
	Version      string
	Sequence     int64
	Channel      string
	RollbackPath string
	DryRun       bool
	// Slots is the box's rollback inventory after this apply, newest first,
	// capped at RollbackSlotLimit.
	Slots []StateSlot
	// PrunedSlots are the rollback directories retention removed in this apply.
	PrunedSlots []string
	// PruneErrors are non-fatal retention failures. The apply succeeded; the
	// box simply still holds a directory it meant to reap.
	PruneErrors []string
	// PrunedStaged are staged payloads this apply made unusable and removed.
	PrunedStaged []string
	// TwoPhase reports whether the apply went through the Kubo-first
	// two-phase path (runtime/kubo/ was separable) rather than the legacy
	// single-phase swap.
	TwoPhase bool
	// ModuleUpdate reports whether this apply went through the G4
	// module-targeted swap path (Manifest.IsModuleUpdate): only the
	// artifacts in the manifest's Modules[] were touched, not the whole
	// bundle. Mutually exclusive with TwoPhase — a module-targeted update
	// never runs the Kubo two-phase path.
	ModuleUpdate bool
	// AppliedModules lists the module ids installed by a module-targeted
	// apply, in manifest order. Empty for a full-bundle apply.
	AppliedModules []string
}

type RollbackOptions struct {
	Reason string
	Now    time.Time
	// Slot names which retained build to restore: an update id, a version, or
	// a rollback path. Empty selects the immediately-previous verified build
	// (Slots[0]) — see selectSlot for why older generations must be named.
	Slot string
}

type RollbackResult struct {
	RestoredSequence int64
	RestoredUpdateID string
	RestoredVersion  string
	RestoredChannel  string
	FailedPath       string
	Reason           string
	// RestoredFrom is the rollback directory that was consumed.
	RestoredFrom string
	// Slots is the remaining inventory after the consumed slot was dropped.
	Slots []StateSlot
}

// Apply verifies a staged update and atomically swaps the bundle contents,
// keeping the previous payload under updates/rollback/<update_id>/. On any
// swap failure the previous contents are restored and the staged payload is
// moved to updates/failed/<update_id>/.
func Apply(paths Paths, opts ApplyOptions) (*ApplyResult, error) {
	// Self-heal first: if a previous apply crashed between the Kubo phase
	// committing and the SDN/main phase (or its own cleanup) completing,
	// undo the Kubo phase before doing anything else so every subsequent
	// step sees a consistent, fully-rolled-back bundle root.
	if _, err := RecoverPendingApply(paths); err != nil {
		return nil, fmt.Errorf("recover pending update apply: %w", err)
	}

	roots, err := LoadTrustRoots(paths)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(paths)
	if err != nil {
		return nil, err
	}
	verifyOpts := HostVerifyOptions(roots, state.Sequence, opts.Now)
	verifyOpts.InstalledKuboVersion = opts.InstalledKuboVersion
	verifyOpts.AllowRollback = opts.AllowRollback
	staged, err := ScanStaged(paths, verifyOpts)
	if err != nil {
		return nil, err
	}
	candidate, err := selectCandidate(staged, opts.UpdateID)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		return &ApplyResult{
			UpdateID: candidate.UpdateID,
			Version:  candidate.Result.Version,
			Sequence: candidate.Result.Sequence,
			Channel:  candidate.Result.Channel,
			DryRun:   true,
		}, nil
	}

	// LEDGER BEFORE MUTATION. This is the last point at which nothing has been
	// changed, so it is where the record has to be written: an apply that
	// cannot be recorded must not happen at all. A failure here is fatal and
	// deliberately aborts the apply — see deployledger.go for why the ledger
	// lives inside the bundle root being modified, and for the measured
	// history that made this a precondition instead of a convention.
	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{
		Action:       "apply",
		UpdateID:     candidate.UpdateID,
		Version:      candidate.Result.Version,
		Sequence:     candidate.Result.Sequence,
		Channel:      candidate.Result.Channel,
		FromVersion:  state.Version,
		FromSequence: state.Sequence,
		Rollback:     opts.AllowRollback,
		Trigger:      strings.TrimSpace(opts.Trigger),
		SignalKeyID:  strings.TrimSpace(opts.SignalKeyID),
	}); err != nil {
		return nil, err
	}

	incomingDir := filepath.Join(paths.Incoming, candidate.UpdateID)
	if err := os.RemoveAll(incomingDir); err != nil {
		return nil, err
	}
	if err := extractBundleArchive(candidate.BundleFile, candidate.Manifest.Bundle.Format, incomingDir); err != nil {
		return nil, fmt.Errorf("extract update bundle: %w", err)
	}
	defer os.RemoveAll(incomingDir)

	newRoot, err := locateBundleRoot(incomingDir)
	if err != nil {
		return nil, err
	}
	if err := validateIncomingBundle(newRoot, candidate.Result.Version); err != nil {
		return nil, err
	}

	rollbackDir := filepath.Join(paths.Rollback, candidate.UpdateID)
	moduleUpdate := candidate.Manifest.IsModuleUpdate()
	var twoPhase bool
	var swapErr error
	switch {
	case moduleUpdate:
		// G4 targeted swap: install only the declared module artifacts.
		// Never runs the Kubo two-phase path — that path exists for
		// full-bundle updates that may ship a new runtime/kubo/ subtree.
		swapErr = applyModuleTargets(paths, newRoot, rollbackDir, candidate.Manifest.Modules)
	case hasSeparableKuboSubtree(newRoot):
		twoPhase = true
		if err := os.RemoveAll(rollbackDir); err != nil {
			return nil, err
		}
		hook := opts.KuboPhaseHook
		if hook == nil {
			hook = NoopKuboPhaseHook
		}
		swapErr = applyTwoPhase(paths, newRoot, rollbackDir, candidate.UpdateID, hook, opts.testFault)
	default:
		// Degraded/back-compat path: the incoming bundle has no separable
		// runtime/kubo/ subtree (older release, or a non-Kubo-bearing
		// target), so fall back to the original single-phase atomic swap.
		swapErr = swapBundleContents(paths, newRoot, rollbackDir)
	}
	if swapErr != nil {
		if errors.Is(swapErr, errSimulatedCrash) {
			// A real crash would never reach this cleanup step either; the
			// on-disk state (Kubo-new/SDN-old + phase marker) is left
			// exactly as applyTwoPhase produced it, for RecoverPendingApply
			// to find on the next start.
			return nil, swapErr
		}
		failedDir := filepath.Join(paths.Failed, candidate.UpdateID)
		_ = os.RemoveAll(failedDir)
		_ = os.MkdirAll(filepath.Dir(failedDir), 0o755)
		_ = os.Rename(candidate.Dir, failedDir)
		return nil, swapErr
	}

	appliedAt := nowOr(opts.Now).UTC().Format(time.RFC3339)
	previous := &StatePrevious{
		Sequence: state.Sequence,
		UpdateID: state.UpdateID,
		Version:  state.Version,
		Channel:  state.Channel,
		Rollback: rollbackDir,
	}
	// RETENTION (owner ruling 2026-08-09): the displaced build becomes the
	// newest of up to five retained reverse targets. Previous stays as the
	// back-compat mirror of Slots[0] — see slots.go.
	slots := recordRollbackSlot(migrateSlots(state), StateSlot{
		Sequence:   state.Sequence,
		UpdateID:   state.UpdateID,
		Version:    state.Version,
		Channel:    state.Channel,
		Path:       rollbackDir,
		RecordedAt: appliedAt,
	})
	newState := &State{
		Sequence:  candidate.Result.Sequence,
		UpdateID:  candidate.UpdateID,
		Version:   candidate.Result.Version,
		Channel:   candidate.Result.Channel,
		AppliedAt: appliedAt,
		Previous:  previous,
		Slots:     slots,
	}
	if err := SaveState(paths, newState); err != nil {
		return nil, fmt.Errorf("update applied but state write failed: %w", err)
	}
	if err := os.RemoveAll(candidate.Dir); err != nil {
		return nil, fmt.Errorf("update applied but staged cleanup failed: %w", err)
	}
	result := &ApplyResult{
		UpdateID:     candidate.UpdateID,
		Version:      candidate.Result.Version,
		Sequence:     candidate.Result.Sequence,
		Channel:      candidate.Result.Channel,
		RollbackPath: rollbackDir,
		TwoPhase:     twoPhase,
		ModuleUpdate: moduleUpdate,
		Slots:        slots,
	}
	// Abandoned staged payloads are the same unbounded-growth class as rollback
	// directories, at ~20 MB each, and they had the same cause: nothing ever
	// removed them. Apply only ever deleted the ONE payload it installed, so a
	// staging attempt that never applied — a helper that died before the swap,
	// a signal superseded by a newer one moments later — left its carrier on
	// disk for good. Anything at or below the sequence now installed can never
	// be applied again (assertSequence refuses it), so keeping it is pure cost.
	for _, dir := range supersededStagedDirs(paths, candidate.Result.Sequence) {
		if err := os.RemoveAll(dir); err == nil {
			result.PrunedStaged = append(result.PrunedStaged, dir)
		}
	}
	pruned, pruneErrs := pruneToRetention(paths, slots, appliedAt)
	result.PrunedSlots = pruned
	for _, err := range pruneErrs {
		result.PruneErrors = append(result.PruneErrors, err.Error())
	}
	if moduleUpdate {
		for _, module := range candidate.Manifest.Modules {
			result.AppliedModules = append(result.AppliedModules, module.ID)
		}
	}
	return result, nil
}

// RollbackLast restores the immediately-previous verified build. It is the
// unchanged name for the unchanged default; Rollback selects an older slot.
func RollbackLast(paths Paths, opts RollbackOptions) (*RollbackResult, error) {
	opts.Slot = ""
	return Rollback(paths, opts)
}

// Rollback restores a retained build and leaves the displaced one under
// updates/failed/<update-id>/.
//
// LIKE APPLY, IT LEDGERS BEFORE IT MUTATES. Rollback was the one bundle
// mutation that wrote no record at all — and it is the one that runs
// UNATTENDED, from the helper's post-restart health gate, on a box nobody is
// watching. A box that quietly reverted itself and told no one is exactly the
// unattributable change deployledger.go exists to make impossible, so the same
// precondition applies here: no line on disk, no rollback.
func Rollback(paths Paths, opts RollbackOptions) (*RollbackResult, error) {
	state, err := LoadState(paths)
	if err != nil {
		return nil, err
	}
	slots := migrateSlots(state)
	slot, err := selectSlot(slots, opts.Slot)
	if err != nil {
		return nil, err
	}
	previousRoot := slot.Path
	if info, err := os.Stat(previousRoot); err != nil {
		return nil, fmt.Errorf("stat rollback slot %s: %w", slot.UpdateID, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("rollback slot %s is not a directory: %s", slot.UpdateID, previousRoot)
	}

	reason := strings.TrimSpace(opts.Reason)
	if reason == "" {
		reason = "unspecified"
	}
	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{
		Action:       "rollback",
		UpdateID:     slot.UpdateID,
		Version:      slot.Version,
		Sequence:     slot.Sequence,
		Channel:      slot.Channel,
		FromVersion:  state.Version,
		FromSequence: state.Sequence,
		Rollback:     true,
		Reason:       reason,
	}); err != nil {
		return nil, err
	}

	failedID := strings.TrimSpace(state.UpdateID)
	if failedID == "" {
		failedID = "current"
	}
	failedDir := filepath.Join(paths.Failed, failedID)
	if err := swapBundleContents(paths, previousRoot, failedDir); err != nil {
		return nil, fmt.Errorf("restore previous bundle: %w", err)
	}
	if err := os.RemoveAll(previousRoot); err != nil {
		return nil, fmt.Errorf("cleanup consumed rollback bundle: %w", err)
	}

	version := strings.TrimSpace(slot.Version)
	channel := strings.TrimSpace(slot.Channel)
	if version == "" || channel == "" {
		manifestVersion, manifestChannel, err := readBundleVersionAndChannel(paths.Root)
		if err != nil {
			return nil, err
		}
		if version == "" {
			version = manifestVersion
		}
		if channel == "" {
			channel = manifestChannel
		}
	}

	remaining := dropSlot(slots, *slot)
	newState := &State{
		Sequence:  slot.Sequence,
		UpdateID:  slot.UpdateID,
		Version:   version,
		Channel:   channel,
		AppliedAt: nowOr(opts.Now).UTC().Format(time.RFC3339),
		Slots:     remaining,
	}
	// Previous mirrors Slots[0] so a box rolled back onto a pre-retention
	// binary still finds the reverse target in the shape it understands.
	if len(remaining) > 0 {
		newState.Previous = &StatePrevious{
			Sequence: remaining[0].Sequence,
			UpdateID: remaining[0].UpdateID,
			Version:  remaining[0].Version,
			Channel:  remaining[0].Channel,
			Rollback: remaining[0].Path,
		}
	}
	if err := SaveState(paths, newState); err != nil {
		return nil, fmt.Errorf("rollback restored bundle but state write failed: %w", err)
	}
	return &RollbackResult{
		RestoredSequence: newState.Sequence,
		RestoredUpdateID: newState.UpdateID,
		RestoredVersion:  newState.Version,
		RestoredChannel:  newState.Channel,
		FailedPath:       failedDir,
		Reason:           strings.TrimSpace(opts.Reason),
		RestoredFrom:     previousRoot,
		Slots:            remaining,
	}, nil
}

// pruneToRetention enforces RollbackSlotLimit against the on-disk rollback
// tree, and does it in the order the ledger discipline requires: PLAN, RECORD,
// then DELETE.
//
// The record is not decoration. Before this existed, nothing in the codebase
// ever deleted a rollback directory — they were swept by hand, unlogged, and
// the 2026-08-09 reconciliation had to reconstruct which reverse targets had
// been destroyed from four unrelated sources. A retention policy that deletes
// silently would recreate that archaeology on a schedule.
//
// A failure to WRITE the record cancels the deletion (nothing is lost — the box
// simply keeps more than five for now). A failure to delete is reported and the
// apply still succeeds: too many reverse targets is a disk problem, never a
// correctness one.
func pruneToRetention(paths Paths, keep []StateSlot, recordedAt string) ([]string, []error) {
	plan, err := planRollbackPrune(paths, keep)
	if err != nil {
		return nil, []error{err}
	}
	if len(plan) == 0 {
		return nil, nil
	}
	held := make([]string, 0, len(keep))
	for _, slot := range keep {
		held = append(held, slot.UpdateID)
	}
	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{
		Action:     "retain",
		RecordedAt: recordedAt,
		Reason: fmt.Sprintf("rollback retention limit %d (owner ruling 2026-08-09); holding [%s]; pruning [%s]",
			RollbackSlotLimit, strings.Join(held, " "), strings.Join(plan, " ")),
		PrunedSlots: plan,
		HeldSlots:   held,
	}); err != nil {
		return nil, []error{fmt.Errorf("rollback retention prune SKIPPED — it could not be recorded, and an unrecordable deletion does not happen: %w", err)}
	}
	return applyRollbackPrune(plan)
}

func readBundleVersionAndChannel(bundleRoot string) (string, string, error) {
	data, err := os.ReadFile(filepath.Join(bundleRoot, manifestFileName))
	if err != nil {
		return "", "", fmt.Errorf("read restored bundle manifest: %w", err)
	}
	var manifest incomingBundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", fmt.Errorf("parse restored bundle manifest: %w", err)
	}
	if manifest.Schema != "org.spacedatanetwork.bundle.v1" {
		return "", "", fmt.Errorf("unsupported restored bundle schema: %s", manifest.Schema)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return "", "", errors.New("restored bundle manifest missing version")
	}
	return strings.TrimSpace(manifest.Version), strings.TrimSpace(manifest.Channel), nil
}

func selectCandidate(staged []StagedUpdate, updateID string) (*StagedUpdate, error) {
	if len(staged) == 0 {
		return nil, errors.New("no staged update is available")
	}
	if updateID != "" {
		for i := range staged {
			if staged[i].UpdateID == updateID {
				if staged[i].Err != nil {
					return nil, fmt.Errorf("staged update %s failed verification: %w", updateID, staged[i].Err)
				}
				return &staged[i], nil
			}
		}
		return nil, fmt.Errorf("no staged update with id %s", updateID)
	}
	// ScanStaged sorts verified candidates by descending sequence.
	for i := range staged {
		if staged[i].Err == nil {
			return &staged[i], nil
		}
	}
	return nil, fmt.Errorf("no staged update passed verification: %w", staged[0].Err)
}

func extractBundleArchive(archivePath, format, destDir string) error {
	if format == "zip" {
		return extractZipArchive(archivePath, destDir)
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var reader io.Reader
	switch format {
	case "tar.gz":
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer gz.Close()
		reader = gz
	case "tar.zst":
		zr, err := zstd.NewReader(file)
		if err != nil {
			return err
		}
		defer zr.Close()
		reader = zr
	default:
		return fmt.Errorf("unsupported update bundle format: %s", format)
	}
	return extractTar(tar.NewReader(reader), destDir)
}

func extractTar(tr *tar.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode).Perm()|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if strings.HasPrefix(header.Linkname, "/") {
				return fmt.Errorf("update bundle contains absolute symlink: %s", header.Name)
			}
			if _, err := safeJoin(filepath.Dir(target), header.Linkname); err != nil {
				return fmt.Errorf("update bundle symlink escapes bundle: %s", header.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("update bundle contains unsupported entry type for %s", header.Name)
		}
	}
}

func extractZipArchive(archivePath, destDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		target, err := safeJoin(destDir, name)
		if err != nil {
			return err
		}
		info := file.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := readZipFile(file)
			if err != nil {
				return err
			}
			linkName := string(linkTarget)
			if strings.HasPrefix(linkName, "/") {
				return fmt.Errorf("update bundle contains absolute symlink: %s", file.Name)
			}
			if _, err := safeJoin(filepath.Dir(target), linkName); err != nil {
				return fmt.Errorf("update bundle symlink escapes bundle: %s", file.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(linkName, target); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("update bundle contains unsupported entry type for %s", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(out, source)
		closeErr := out.Close()
		source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func readZipFile(file *zip.File) ([]byte, error) {
	source, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return io.ReadAll(source)
}

func safeJoin(base, name string) (string, error) {
	target := filepath.Join(base, filepath.FromSlash(name))
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("update bundle entry escapes bundle root: %s", name)
	}
	return target, nil
}

// locateBundleRoot finds the directory containing manifest.json: either the
// extraction dir itself or a single wrapper directory (archives are created
// as tar of the bundle directory).
func locateBundleRoot(extractedDir string) (string, error) {
	if _, err := os.Stat(filepath.Join(extractedDir, manifestFileName)); err == nil {
		return extractedDir, nil
	}
	entries, err := os.ReadDir(extractedDir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}
	if len(dirs) == 1 {
		nested := filepath.Join(extractedDir, dirs[0])
		if _, err := os.Stat(filepath.Join(nested, manifestFileName)); err == nil {
			return nested, nil
		}
	}
	return "", errors.New("update bundle does not contain a bundle manifest")
}

// incomingBundleManifest is the org.spacedatanetwork.bundle.v1 manifest
// inside the new bundle payload, used for artifact checksum verification.
type incomingBundleManifest struct {
	Schema    string `json:"schema"`
	Version   string `json:"version"`
	Channel   string `json:"channel"`
	Artifacts []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

func validateIncomingBundle(newRoot, expectedVersion string) error {
	data, err := os.ReadFile(filepath.Join(newRoot, manifestFileName))
	if err != nil {
		return fmt.Errorf("read incoming bundle manifest: %w", err)
	}
	var manifest incomingBundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse incoming bundle manifest: %w", err)
	}
	if manifest.Schema != "org.spacedatanetwork.bundle.v1" {
		return fmt.Errorf("unsupported incoming bundle schema: %s", manifest.Schema)
	}
	if expectedVersion != "" && manifest.Version != expectedVersion {
		return fmt.Errorf("incoming bundle version %s does not match update manifest version %s", manifest.Version, expectedVersion)
	}
	entries, err := os.ReadDir(newRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if protectedEntries[entry.Name()] {
			return fmt.Errorf("update bundle must not contain protected entry %q", entry.Name())
		}
	}
	for _, artifact := range manifest.Artifacts {
		path, err := safeJoin(newRoot, artifact.Path)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("incoming bundle artifact missing: %s", artifact.Path)
		}
		if sha256Hex(data) != strings.ToLower(artifact.SHA256) {
			return fmt.Errorf("incoming bundle artifact checksum mismatch: %s", artifact.Path)
		}
	}
	return nil
}

// swapBundleContents moves the current bundle payload entries into
// rollbackDir and the new payload entries into the bundle root, as a single
// atomic-per-entry operation across the whole tree. On failure it restores
// everything it moved. This is the legacy single-phase path: Apply uses it
// as the back-compat/degraded fallback when the incoming bundle has no
// separable runtime/kubo/ subtree (see applyTwoPhase for the Kubo-first
// path). It shares its per-entry rename/restore mechanics with the
// two-phase swap via swapEntrySet/restoreEntries.
func swapBundleContents(paths Paths, newRoot, rollbackDir string) error {
	if err := os.RemoveAll(rollbackDir); err != nil {
		return err
	}
	if err := os.MkdirAll(rollbackDir, 0o755); err != nil {
		return err
	}

	var movedToRollback, installed []string
	skip := func(name string) bool { return protectedEntries[name] }
	if err := swapEntrySet(paths.Root, newRoot, rollbackDir, "", isBundlePayloadEntry, skip, &movedToRollback, &installed); err != nil {
		restoreEntries(paths, rollbackDir, movedToRollback, installed)
		return err
	}
	return nil
}

// isBundlePayloadEntry reports whether a bundle-root entry belongs to the
// shipped payload (and should be retired when a new bundle omits it) rather
// than locally created data.
func isBundlePayloadEntry(name string) bool {
	switch name {
	case "bin", "runtime", manifestFileName, "checksums.txt", "LICENSE", "README.md":
		return true
	}
	return false
}
