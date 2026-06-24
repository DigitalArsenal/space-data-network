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

	// TrustRootsEnv overrides the bundle trust store path, primarily for
	// tests and managed deployments.
	TrustRootsEnv = "SDN_UPDATE_TRUST_ROOTS"

	StateSchema = "org.spacedatanetwork.update.state.v1"
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
}

type StatePrevious struct {
	Sequence int64  `json:"sequence"`
	UpdateID string `json:"update_id,omitempty"`
	Version  string `json:"version,omitempty"`
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
