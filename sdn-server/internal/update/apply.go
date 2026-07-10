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
	// TwoPhase reports whether the apply went through the Kubo-first
	// two-phase path (runtime/kubo/ was separable) rather than the legacy
	// single-phase swap.
	TwoPhase bool
}

type RollbackOptions struct {
	Reason string
	Now    time.Time
}

type RollbackResult struct {
	RestoredSequence int64
	RestoredUpdateID string
	RestoredVersion  string
	RestoredChannel  string
	FailedPath       string
	Reason           string
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
	twoPhase := hasSeparableKuboSubtree(newRoot)
	var swapErr error
	if twoPhase {
		if err := os.RemoveAll(rollbackDir); err != nil {
			return nil, err
		}
		hook := opts.KuboPhaseHook
		if hook == nil {
			hook = NoopKuboPhaseHook
		}
		swapErr = applyTwoPhase(paths, newRoot, rollbackDir, candidate.UpdateID, hook, opts.testFault)
	} else {
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

	previous := &StatePrevious{
		Sequence: state.Sequence,
		UpdateID: state.UpdateID,
		Version:  state.Version,
		Channel:  state.Channel,
		Rollback: rollbackDir,
	}
	newState := &State{
		Sequence:  candidate.Result.Sequence,
		UpdateID:  candidate.UpdateID,
		Version:   candidate.Result.Version,
		Channel:   candidate.Result.Channel,
		AppliedAt: time.Now().UTC().Format(time.RFC3339),
		Previous:  previous,
	}
	if err := SaveState(paths, newState); err != nil {
		return nil, fmt.Errorf("update applied but state write failed: %w", err)
	}
	if err := os.RemoveAll(candidate.Dir); err != nil {
		return nil, fmt.Errorf("update applied but staged cleanup failed: %w", err)
	}
	return &ApplyResult{
		UpdateID:     candidate.UpdateID,
		Version:      candidate.Result.Version,
		Sequence:     candidate.Result.Sequence,
		Channel:      candidate.Result.Channel,
		RollbackPath: rollbackDir,
		TwoPhase:     twoPhase,
	}, nil
}

func RollbackLast(paths Paths, opts RollbackOptions) (*RollbackResult, error) {
	state, err := LoadState(paths)
	if err != nil {
		return nil, err
	}
	if state.Previous == nil || strings.TrimSpace(state.Previous.Rollback) == "" {
		return nil, errors.New("no previous update rollback is available")
	}
	previousRoot := state.Previous.Rollback
	if info, err := os.Stat(previousRoot); err != nil {
		return nil, fmt.Errorf("stat previous update rollback: %w", err)
	} else if !info.IsDir() {
		return nil, errors.New("previous update rollback is not a directory")
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

	version := strings.TrimSpace(state.Previous.Version)
	channel := strings.TrimSpace(state.Previous.Channel)
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

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	newState := &State{
		Sequence:  state.Previous.Sequence,
		UpdateID:  state.Previous.UpdateID,
		Version:   version,
		Channel:   channel,
		AppliedAt: now.UTC().Format(time.RFC3339),
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
	}, nil
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
