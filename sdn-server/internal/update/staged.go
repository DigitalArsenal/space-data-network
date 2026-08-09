package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	updatesDirName   = "updates"
	stagedDirName    = "staged"
	rollbackDirName  = "rollback"
	failedDirName    = "failed"
	incomingDirName  = "incoming"
	stateFileName    = "state.json"
	trustDirName     = "trust"
	trustRootsName   = "update-roots.json"
	manifestFileName = "manifest.json"
	carrierFileName  = "update.wasm"
	phaseFileName    = "apply-phase.json"

	// TrustRootsEnv overrides the bundle trust store path, primarily for
	// tests and managed deployments.
	TrustRootsEnv = "SDN_UPDATE_TRUST_ROOTS"

	StateSchema = "org.spacedatanetwork.update.state.v1"

	// ApplyPhaseSchema is the schema for the durable two-phase-apply crash
	// marker (updates/apply-phase.json). See RecoverPendingApply.
	ApplyPhaseSchema = "org.spacedatanetwork.update.apply-phase.v1"
)

// Paths describes the update working tree inside a self-contained bundle.
type Paths struct {
	Root     string
	Updates  string
	Staged   string
	Rollback string
	Failed   string
	Incoming string
	State    string
	Trust    string
	// Phase is the durable two-phase-apply crash marker path
	// (updates/apply-phase.json). It lives under Updates, which the bundle
	// swap never touches, so it survives a crash mid-apply.
	Phase string
}

func PathsFor(bundleRoot string) Paths {
	updates := filepath.Join(bundleRoot, updatesDirName)
	return Paths{
		Root:     bundleRoot,
		Updates:  updates,
		Staged:   filepath.Join(updates, stagedDirName),
		Rollback: filepath.Join(updates, rollbackDirName),
		Failed:   filepath.Join(updates, failedDirName),
		Incoming: filepath.Join(updates, incomingDirName),
		State:    filepath.Join(updates, stateFileName),
		Trust:    filepath.Join(bundleRoot, trustDirName, trustRootsName),
		Phase:    filepath.Join(updates, phaseFileName),
	}
}

// State records the last applied update so sequence policy and rollback
// targets survive bundle swaps. It lives under updates/, which the swap
// never touches.
type State struct {
	Schema    string `json:"schema"`
	Sequence  int64  `json:"sequence"`
	UpdateID  string `json:"update_id,omitempty"`
	Version   string `json:"version,omitempty"`
	Channel   string `json:"channel,omitempty"`
	AppliedAt string `json:"applied_at,omitempty"`

	Previous *StatePrevious `json:"previous,omitempty"`

	// Slots is the rollback retention inventory, newest first, capped at
	// RollbackSlotLimit (owner ruling 2026-08-09: keep the last five builds).
	// Slots[0] duplicates Previous and is the default reverse target; older
	// entries are reachable only by naming them. See slots.go for why Previous
	// is kept alongside rather than replaced.
	Slots []StateSlot `json:"slots,omitempty"`
}

type StatePrevious struct {
	Sequence int64  `json:"sequence"`
	UpdateID string `json:"update_id,omitempty"`
	Version  string `json:"version,omitempty"`
	Channel  string `json:"channel,omitempty"`
	Rollback string `json:"rollback_path,omitempty"`
}

func LoadState(paths Paths) (*State, error) {
	data, err := os.ReadFile(paths.State)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Schema: StateSchema}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read update state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse update state: %w", err)
	}
	if state.Schema != StateSchema {
		return nil, fmt.Errorf("unsupported update state schema: %s", state.Schema)
	}
	return &state, nil
}

func SaveState(paths Paths, state *State) error {
	state.Schema = StateSchema
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
		return err
	}
	tmp := paths.State + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, paths.State)
}

// applyPhaseMarker is the durable, on-disk record of an in-progress
// two-phase apply (G3). It is written once the Kubo phase has committed
// (runtime/kubo/ swapped and health-checked) and cleared once the whole
// apply completes or is rolled back. If the process dies while the marker
// is present, the bundle root is left with a NEW Kubo runtime and an OLD
// SDN/remaining payload; RecoverPendingApply uses the recorded move lists
// to undo exactly the Kubo-phase renames and restore the original bytes.
type applyPhaseMarker struct {
	Schema        string   `json:"schema"`
	UpdateID      string   `json:"update_id"`
	RollbackDir   string   `json:"rollback_dir"`
	Phase         string   `json:"phase"` // "kubo-done"
	KuboMoved     []string `json:"kubo_moved,omitempty"`
	KuboInstalled []string `json:"kubo_installed,omitempty"`
	StartedAt     string   `json:"started_at,omitempty"`
}

func savePhaseMarker(paths Paths, marker *applyPhaseMarker) error {
	marker.Schema = ApplyPhaseSchema
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
		return err
	}
	tmp := paths.Phase + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, paths.Phase)
}

func loadPhaseMarker(paths Paths) (*applyPhaseMarker, error) {
	data, err := os.ReadFile(paths.Phase)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read apply phase marker: %w", err)
	}
	var marker applyPhaseMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("parse apply phase marker: %w", err)
	}
	if marker.Schema != ApplyPhaseSchema {
		return nil, fmt.Errorf("unsupported apply phase marker schema: %s", marker.Schema)
	}
	return &marker, nil
}

func clearPhaseMarker(paths Paths) error {
	if err := os.Remove(paths.Phase); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RecoverPendingApply detects a durable two-phase-apply crash marker left
// behind by a process that died between the Kubo phase committing and the
// SDN/main phase (or its own cleanup) completing, and rolls the Kubo phase
// back so the bundle root returns to its pre-apply state. It is safe to
// call unconditionally on every start/apply/verify: when no marker is
// present it is a no-op (recovered=false, err=nil). Apply calls this
// automatically as its first step.
func RecoverPendingApply(paths Paths) (recovered bool, err error) {
	marker, err := loadPhaseMarker(paths)
	if err != nil {
		return false, err
	}
	if marker == nil {
		return false, nil
	}
	undoKuboPhase(paths, marker.RollbackDir, marker.KuboMoved, marker.KuboInstalled)
	if err := clearPhaseMarker(paths); err != nil {
		return false, fmt.Errorf("clear apply phase marker after recovery: %w", err)
	}
	return true, nil
}

// LoadTrustRoots reads the bundle trust store (or the SDN_UPDATE_TRUST_ROOTS
// override). The store is a JSON object mapping signing key ids to encoded
// Ed25519 public keys and ships with the original bundle install, outside
// the swapped payload contents.
func LoadTrustRoots(paths Paths) (TrustedRoots, error) {
	path := strings.TrimSpace(os.Getenv(TrustRootsEnv))
	if path == "" {
		path = paths.Trust
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read update trust roots (%s): %w", path, err)
	}
	var roots TrustedRoots
	if err := json.Unmarshal(data, &roots); err != nil {
		return nil, fmt.Errorf("parse update trust roots: %w", err)
	}
	if len(roots) == 0 {
		return nil, errors.New("update trust roots are empty")
	}
	return roots, nil
}

// StagedUpdate is one verified or rejected candidate found under
// updates/staged/<update_id>/.
type StagedUpdate struct {
	UpdateID   string
	Dir        string
	Manifest   *Manifest
	BundleFile string
	Result     *VerifyResult
	Err        error
}

func bundleFileName(format string) (string, error) {
	switch format {
	case "tar.zst":
		return "bundle.tar.zst", nil
	case "tar.gz":
		return "bundle.tar.gz", nil
	case "zip":
		return "bundle.zip", nil
	default:
		return "", fmt.Errorf("unsupported update bundle format: %s", format)
	}
}

// HostVerifyOptions returns VerifyOptions for the current host and state.
func HostVerifyOptions(roots TrustedRoots, currentSequence int64, now time.Time) VerifyOptions {
	return VerifyOptions{
		Platform:        runtime.GOOS,
		Arch:            runtime.GOARCH,
		CurrentSequence: currentSequence,
		TrustedRoots:    roots,
		Now:             now,
	}
}

// ScanStaged verifies every staged candidate. Candidates that fail
// verification are returned with Err set so `update check` can report them.
func ScanStaged(paths Paths, opts VerifyOptions) ([]StagedUpdate, error) {
	entries, err := os.ReadDir(paths.Staged)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read staged updates: %w", err)
	}
	var staged []StagedUpdate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		staged = append(staged, verifyStagedDir(filepath.Join(paths.Staged, entry.Name()), entry.Name(), opts))
	}
	sort.Slice(staged, func(i, j int) bool {
		left, right := int64(-1), int64(-1)
		if staged[i].Result != nil {
			left = staged[i].Result.Sequence
		}
		if staged[j].Result != nil {
			right = staged[j].Result.Sequence
		}
		if left != right {
			return left > right
		}
		return staged[i].UpdateID < staged[j].UpdateID
	})
	return staged, nil
}

func verifyStagedDir(dir, updateID string, opts VerifyOptions) StagedUpdate {
	staged := StagedUpdate{UpdateID: updateID, Dir: dir}
	manifestBytes, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		staged.Err = fmt.Errorf("read staged manifest: %w", err)
		return staged
	}
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		staged.Err = err
		return staged
	}
	staged.Manifest = manifest
	if manifest.UpdateID != updateID {
		staged.Err = fmt.Errorf("staged directory %q does not match update id %q", updateID, manifest.UpdateID)
		return staged
	}
	wasmBytes, err := os.ReadFile(filepath.Join(dir, carrierFileName))
	if err != nil {
		staged.Err = fmt.Errorf("read staged update carrier: %w", err)
		return staged
	}
	bundleName, err := bundleFileName(manifest.Bundle.Format)
	if err != nil {
		staged.Err = err
		return staged
	}
	staged.BundleFile = filepath.Join(dir, bundleName)
	bundleBytes, err := os.ReadFile(staged.BundleFile)
	if err != nil {
		staged.Err = fmt.Errorf("read staged update bundle: %w", err)
		return staged
	}
	result, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts)
	if err != nil {
		staged.Err = err
		return staged
	}
	staged.Result = result
	return staged
}

// Stage writes a verified payload into updates/staged/<update_id>/ so a
// later `update apply` can install it. The payload is re-verified from the
// written files by the caller before any swap.
func Stage(paths Paths, manifestBytes, wasmBytes []byte, opts VerifyOptions) (*StagedUpdate, error) {
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	bundleBytes, err := ExtractBundleFromCarrier(wasmBytes)
	if err != nil {
		return nil, err
	}
	result, err := manifest.VerifyPayload(wasmBytes, bundleBytes, opts)
	if err != nil {
		return nil, err
	}
	bundleName, err := bundleFileName(manifest.Bundle.Format)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(paths.Staged, manifest.UpdateID)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), manifestBytes, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, carrierFileName), wasmBytes, 0o644); err != nil {
		return nil, err
	}
	bundlePath := filepath.Join(dir, bundleName)
	if err := os.WriteFile(bundlePath, bundleBytes, 0o644); err != nil {
		return nil, err
	}
	return &StagedUpdate{
		UpdateID:   manifest.UpdateID,
		Dir:        dir,
		Manifest:   manifest,
		BundleFile: bundlePath,
		Result:     result,
	}, nil
}
