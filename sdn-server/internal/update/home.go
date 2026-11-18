package update

// UPDATES WITHOUT WRITE ACCESS TO THE INSTALL FOLDER (stack issue 846).
//
// A node installed under a folder its account cannot write (a bundle under
// /opt owned by root, run by an ordinary account) could never update itself:
// every update is staged in, and swapped into, the folder it updates. Such a
// node now updates its HOME BUNDLE instead, a bundle root under the account's
// home directory that the same machinery (stage, prepare, the helper's stop,
// swap, restart, health gate and revert, rollback slots, the deploy ledger)
// works on unchanged. PathsFor picks the folder:
//
//   - the account can write the install folder: the install folder;
//   - it cannot: $HOME/.spacedatanetwork/bundle, or <storage.path>/update-bundle
//     when HOME is unset or cannot be written;
//   - a folder holding updates/home.json IS a home bundle.
//
// An account that can write the install folder can rewrite the installed
// binary, the trust anchor itself, so a copy under its home would protect
// nothing; that case keeps the in-place swap. The home case adds one thing:
// at every start, a process started from the installed bundle verifies the
// home build against the INSTALLED build's update roots and runs it only when
// it verifies (ChooseHomeBuild). Nothing under the home directory is trusted
// for being there.
//
// The home bundle holds a payload only after an update has been applied. Its
// first rollback slot is therefore empty, and an empty slot (or an empty home
// bundle) means "the installed build": rolling back to it leaves the home
// bundle without a payload and the installed build runs.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
)

const (
	homeRecordName     = "home.json"
	homeBundleDirName  = "bundle"
	storageHomeDirName = "update-bundle"
	proofsDirName      = "proofs"
	rotationsDirName   = "rotations"

	// HomeRecordSchema is the schema of updates/home.json in a home bundle.
	HomeRecordSchema = "org.spacedatanetwork.update.home.v1"
)

// HomeStorageFallback returns the node's storage.path. PathsFor uses it for
// the home bundle when HOME is unset or cannot be written. The command line
// sets it; nil means there is no fallback.
var HomeStorageFallback func() string

// HomeRecord is updates/home.json: which installed build the home bundle
// updates. A home bundle whose installed build has changed since (another
// build installed in its place, newer or older) is stale: the installed build
// runs, and the next update starts the home bundle again.
type HomeRecord struct {
	Schema                  string `json:"schema"`
	InstalledRoot           string `json:"installed_root"`
	InstalledVersion        string `json:"installed_version"`
	InstalledManifestSHA256 string `json:"installed_manifest_sha256"`
	InstalledSequence       int64  `json:"installed_sequence"`
}

// pathsIn is the update working tree inside root.
func pathsIn(bundleRoot string) Paths {
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

// PathsFor is the update root for the bundle at bundleRoot: the bundle itself
// when this account can write it, else its home bundle (see the file comment).
func PathsFor(bundleRoot string) Paths {
	if strings.TrimSpace(bundleRoot) == "" {
		return pathsIn(bundleRoot)
	}
	if record, err := readHomeRecord(bundleRoot); err == nil && record != nil {
		p := pathsIn(bundleRoot)
		p.Installed = filepath.Clean(record.InstalledRoot)
		return p
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, manifestFileName)); err != nil || dirWritable(bundleRoot) {
		return pathsIn(bundleRoot)
	}
	home, err := HomeUpdateRoot()
	if err != nil {
		p := pathsIn(bundleRoot)
		p.HomeErr = err
		return p
	}
	p := pathsIn(home)
	p.Installed = filepath.Clean(bundleRoot)
	return p
}

// IsHome reports an update root that is a home bundle.
func (p Paths) IsHome() bool { return strings.TrimSpace(p.Installed) != "" }

// BuildRoot is the bundle the update root runs: the home bundle once it holds
// a payload, else the installed bundle.
func (p Paths) BuildRoot() string {
	if !p.IsHome() {
		return p.Root
	}
	if _, err := os.Stat(filepath.Join(p.Root, manifestFileName)); err == nil {
		return p.Root
	}
	return p.Installed
}

// RestartArgv is argv with its program replaced by the installed launcher when
// the update root is a home bundle: a restart goes through the installed
// build's check of the home build (ChooseHomeBuild), and still works once a
// rollback has emptied the home bundle.
func (p Paths) RestartArgv(argv []string) []string {
	if !p.IsHome() || len(argv) == 0 {
		return argv
	}
	launcher := filepath.Join(p.Installed, "bin", executableFileName())
	if _, err := os.Stat(launcher); err != nil {
		return argv
	}
	out := append([]string{launcher}, argv[1:]...)
	return out
}

func executableFileName() string {
	if runtime.GOOS == "windows" {
		return "spacedatanetwork.exe"
	}
	return "spacedatanetwork"
}

var homeFallbackLogged sync.Once

// HomeUpdateRoot is this account's home bundle: $HOME/.spacedatanetwork/bundle,
// or <storage.path>/update-bundle when HOME is unset or cannot be written.
func HomeUpdateRoot() (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("on Windows a node updates only an install folder its account can write; this account cannot write it, so install the new release by hand or give the account write access to the install folder")
	}
	var reason string
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		dir := filepath.Join(home, ".spacedatanetwork", homeBundleDirName)
		if canCreateUnder(dir) {
			return dir, nil
		}
		reason = dir + " cannot be written"
	} else {
		reason = "HOME is not set"
	}
	if HomeStorageFallback != nil {
		if storage := strings.TrimSpace(HomeStorageFallback()); storage != "" {
			dir := filepath.Join(storage, storageHomeDirName)
			if canCreateUnder(dir) {
				homeFallbackLogged.Do(func() {
					log.Warnf("update: %s; updates go to %s, under the node's storage.path", reason, dir)
				})
				return dir, nil
			}
			reason += "; " + dir + " cannot be written either"
		}
	}
	return "", fmt.Errorf("this account cannot write the install folder, and it has no folder to update into: %s", reason)
}

// canCreateUnder reports whether dir exists and is writable, or its nearest
// existing parent is.
func canCreateUnder(dir string) bool {
	for current := filepath.Clean(dir); ; {
		info, err := os.Stat(current)
		if err == nil {
			return info.IsDir() && dirWritable(current)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func readHomeRecord(root string) (*HomeRecord, error) {
	data, err := os.ReadFile(filepath.Join(root, updatesDirName, homeRecordName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record HomeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse %s: %w", homeRecordName, err)
	}
	if record.Schema != HomeRecordSchema || strings.TrimSpace(record.InstalledRoot) == "" {
		return nil, fmt.Errorf("%s is not a home bundle record", homeRecordName)
	}
	return &record, nil
}

// installedRecord describes the installed build at root as it is now.
func installedRecord(root string) (*HomeRecord, error) {
	data, err := os.ReadFile(filepath.Join(root, manifestFileName))
	if err != nil {
		return nil, fmt.Errorf("read the installed bundle manifest: %w", err)
	}
	version, _, err := readBundleVersionAndChannel(root)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(pathsIn(root))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &HomeRecord{
		Schema:                  HomeRecordSchema,
		InstalledRoot:           filepath.Clean(root),
		InstalledVersion:        version,
		InstalledManifestSHA256: hex.EncodeToString(sum[:]),
		InstalledSequence:       state.Sequence,
	}, nil
}

// homeCurrent reads the home record and checks it against the installed build
// as it is now. stale explains a record made for another installed build.
func homeCurrent(paths Paths) (record *HomeRecord, stale string, err error) {
	record, err = readHomeRecord(paths.Root)
	if err != nil || record == nil {
		return record, "", err
	}
	now, err := installedRecord(paths.Installed)
	if err != nil {
		return record, "", err
	}
	if filepath.Clean(record.InstalledRoot) != now.InstalledRoot || record.InstalledManifestSHA256 != now.InstalledManifestSHA256 {
		return record, fmt.Sprintf("the installed build changed after the home bundle was made (installed now %s, then %s)", now.InstalledVersion, record.InstalledVersion), nil
	}
	return record, "", nil
}

// installedState is the update state of a home bundle that has none of its
// own yet (or a stale one): the installed build's.
func installedState(paths Paths) (*State, error) {
	installed, err := LoadState(pathsIn(paths.Installed))
	if err != nil {
		return nil, err
	}
	state := &State{
		Schema:         StateSchema,
		Sequence:       installed.Sequence,
		UpdateID:       installed.UpdateID,
		Version:        installed.Version,
		Channel:        installed.Channel,
		TrustRootsFrom: installed.TrustRootsFrom,
	}
	if state.Version == "" || state.Channel == "" {
		if version, channel, err := readBundleVersionAndChannel(paths.Installed); err == nil {
			if state.Version == "" {
				state.Version = version
			}
			if state.Channel == "" {
				state.Channel = channel
			}
		}
	}
	return state, nil
}

// EnsureHome makes the home bundle ready to stage into: its record names the
// installed build as it is now. A home bundle made for another installed build
// is removed first.
func EnsureHome(paths Paths) error {
	if paths.HomeErr != nil {
		return paths.HomeErr
	}
	if !paths.IsHome() {
		return nil
	}
	record, stale, err := homeCurrent(paths)
	if err != nil {
		return err
	}
	if record != nil && stale == "" {
		return nil
	}
	if record != nil {
		log.Warnf("update: %s; removing the home bundle %s and updating from the installed build", stale, paths.Root)
		if err := os.RemoveAll(paths.Root); err != nil {
			return fmt.Errorf("remove the stale home bundle %s: %w", paths.Root, err)
		}
	}
	now, err := installedRecord(paths.Installed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Updates, 0o700); err != nil {
		return fmt.Errorf("create the home bundle %s: %w", paths.Root, err)
	}
	data, err := json.MarshalIndent(now, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(paths.Updates, homeRecordName+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(paths.Updates, homeRecordName))
}

// proofDir holds the signed update manifest and the bundle archive of one home
// build: what ChooseHomeBuild verifies the build against at every start.
func proofDir(paths Paths, updateID string) string {
	return filepath.Join(paths.Updates, proofsDirName, updateID)
}

// keepProof moves a staged update, just applied, to the proofs of the home
// bundle (without its carrier, which the proof does not need).
func keepProof(paths Paths, candidate *StagedUpdate) error {
	dst := proofDir(paths, candidate.UpdateID)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(candidate.Dir, dst); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dst, carrierFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// pruneProofs removes the proofs of builds the home bundle no longer holds
// (neither current nor in a rollback slot).
func pruneProofs(paths Paths, state *State) {
	if !paths.IsHome() || state == nil {
		return
	}
	keep := map[string]bool{state.UpdateID: true}
	for _, slot := range state.Slots {
		keep[slot.UpdateID] = true
	}
	entries, err := os.ReadDir(filepath.Join(paths.Updates, proofsDirName))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !keep[entry.Name()] {
			_ = os.RemoveAll(filepath.Join(paths.Updates, proofsDirName, entry.Name()))
		}
	}
}

// recordRotation keeps a signed manifest that replaces the update roots in a
// home bundle. The roots a home bundle uses are always computed from the
// installed roots through these manifests (homeTrustRoots).
func recordRotation(paths Paths, m *Manifest) error {
	if len(m.raw) == 0 {
		return errors.New("the manifest that replaces the update roots has no signed bytes to keep")
	}
	dir := filepath.Join(paths.Root, trustDirName, rotationsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%s.json", *m.Sequence, m.UpdateID)
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, m.raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// homeTrustRoots are the INSTALLED build's update roots, advanced through each
// recorded rotation whose manifest verifies against the roots before it.
func homeTrustRoots(paths Paths) (TrustedRoots, error) {
	roots, err := LoadTrustRoots(pathsIn(paths.Installed))
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(paths.Root, trustDirName, rotationsDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return roots, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the update-root rotations of %s: %w", paths.Root, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		m, err := ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("update-root rotation %s: %w", name, err)
		}
		if err := m.VerifySignature(roots); err != nil {
			return nil, fmt.Errorf("update-root rotation %s does not verify against the roots before it: %w", name, err)
		}
		if err := m.assertTrustRoots(); err != nil || len(m.TrustRoots) == 0 {
			return nil, fmt.Errorf("update-root rotation %s names no valid roots: %v", name, err)
		}
		roots = m.TrustRoots
	}
	return roots, nil
}

// HomeBuild is a verified home build, ready to run in place of the installed
// build.
type HomeBuild struct {
	Root       string
	Installed  string
	Version    string
	UpdateID   string
	Executable string
}

// ChooseHomeBuild decides which build a process started from executable runs.
// nil, nil: run this build (it is not an installed bundle with a home bundle,
// or the home bundle holds no build). nil, err: the home bundle holds a build
// that is refused, err says why; run this build. Otherwise the returned home
// build verified against the installed build's update roots.
func ChooseHomeBuild(executable string) (*HomeBuild, error) {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	layout := bundle.ResolveFromExecutable(executable)
	if layout.Root == "" {
		return nil, nil
	}
	if record, _ := readHomeRecord(layout.Root); record != nil {
		return nil, nil // this process IS a home build
	}
	paths := PathsFor(layout.Root)
	if !paths.IsHome() {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(paths.Root, manifestFileName)); err != nil {
		return nil, nil
	}
	build, err := VerifyHomeBuild(paths)
	if err != nil {
		return nil, fmt.Errorf("the build under %s is refused: %w", paths.Root, err)
	}
	return build, nil
}

// VerifyHomeBuild checks the build in a home bundle against the installed
// build's update roots: the record names the installed build as it is now; the
// signed manifest of the build state.json names verifies, targets this
// platform and is newer than the installed build; its archive is the one it
// signs; and the payload on disk is exactly that archive's bundle.
func VerifyHomeBuild(paths Paths) (*HomeBuild, error) {
	if !paths.IsHome() {
		return nil, errors.New("not a home bundle")
	}
	record, stale, err := homeCurrent(paths)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("it has no home bundle record")
	}
	if stale != "" {
		return nil, errors.New(stale)
	}
	raw, err := os.ReadFile(paths.State)
	if err != nil {
		return nil, fmt.Errorf("read its update state: %w", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil || strings.TrimSpace(state.UpdateID) == "" {
		return nil, errors.New("its update state names no update")
	}
	proof := proofDir(paths, state.UpdateID)
	manifestBytes, err := os.ReadFile(filepath.Join(proof, manifestFileName))
	if err != nil {
		return nil, fmt.Errorf("no signed manifest for update %s: %w", state.UpdateID, err)
	}
	m, err := ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	roots, err := homeTrustRoots(paths)
	if err != nil {
		return nil, err
	}
	if err := m.VerifySignature(roots); err != nil {
		return nil, fmt.Errorf("update %s: %w", state.UpdateID, err)
	}
	if m.UpdateID != state.UpdateID {
		return nil, fmt.Errorf("the signed manifest is update %s, not %s", m.UpdateID, state.UpdateID)
	}
	if err := m.assertTarget(runtime.GOOS, runtime.GOARCH); err != nil {
		return nil, err
	}
	if m.IsModuleUpdate() || m.IsUIPackage() {
		return nil, fmt.Errorf("update %s is not a full bundle", m.UpdateID)
	}
	if *m.Sequence <= record.InstalledSequence {
		return nil, fmt.Errorf("update %s (sequence %d) is not newer than the installed build (sequence %d)", m.UpdateID, *m.Sequence, record.InstalledSequence)
	}
	if cmp, err := compareVersions(m.Version, record.InstalledVersion); err == nil && cmp < 0 {
		return nil, fmt.Errorf("update %s is version %s, older than the installed %s", m.UpdateID, m.Version, record.InstalledVersion)
	}
	bundleName, err := bundleFileName(m.Bundle.Format)
	if err != nil {
		return nil, err
	}
	archive := filepath.Join(proof, bundleName)
	sum, size, err := fileSHA256(archive)
	if err != nil {
		return nil, fmt.Errorf("read the bundle archive of update %s: %w", m.UpdateID, err)
	}
	if sum != m.Bundle.Hash || size != m.Bundle.Size {
		return nil, fmt.Errorf("the bundle archive of update %s is not the one its manifest signs", m.UpdateID)
	}
	want, err := archiveTree(archive, m.Bundle.Format)
	if err != nil {
		return nil, fmt.Errorf("read the bundle archive of update %s: %w", m.UpdateID, err)
	}
	if err := compareTree(paths.Root, want); err != nil {
		return nil, fmt.Errorf("its files differ from signed update %s: %w", m.UpdateID, err)
	}
	if version, _, err := readBundleVersionAndChannel(paths.Root); err != nil || version != m.Version {
		return nil, fmt.Errorf("its bundle manifest does not name version %s", m.Version)
	}
	exe := filepath.Join(paths.Root, "bin", executableFileName())
	return &HomeBuild{Root: paths.Root, Installed: paths.Installed, Version: m.Version, UpdateID: m.UpdateID, Executable: exe}, nil
}

// treeEntry is one file or symlink of a bundle.
type treeEntry struct {
	symlink bool
	target  string
	sha     string
	exec    bool
}

func fileSHA256(name string) (string, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// archiveTree lists the files and symlinks of the bundle in an archive,
// relative to its bundle root (the archive root or its single wrapper folder,
// as locateBundleRoot finds it after extraction).
func archiveTree(archive, format string) (map[string]treeEntry, error) {
	all := map[string]treeEntry{}
	add := func(name string, e treeEntry) error {
		clean := path.Clean(strings.TrimPrefix(strings.ReplaceAll(name, "\\", "/"), "./"))
		if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) {
			return fmt.Errorf("archive entry escapes the bundle: %s", name)
		}
		all[clean] = e
		return nil
	}
	switch format {
	case "zip":
		r, err := zip.OpenReader(archive)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		for _, file := range r.File {
			info := file.FileInfo()
			if info.IsDir() {
				continue
			}
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return nil, err
				}
				if err := add(file.Name, treeEntry{symlink: true, target: string(target)}); err != nil {
					return nil, err
				}
				continue
			}
			h := sha256.New()
			_, err = io.Copy(h, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			if err := add(file.Name, treeEntry{sha: hex.EncodeToString(h.Sum(nil)), exec: info.Mode().Perm()&0o100 != 0}); err != nil {
				return nil, err
			}
		}
	case "tar.gz", "tar.zst":
		f, err := os.Open(archive)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		var reader io.Reader
		if format == "tar.gz" {
			gz, err := gzip.NewReader(f)
			if err != nil {
				return nil, err
			}
			defer gz.Close()
			reader = gz
		} else {
			zr, err := zstd.NewReader(f)
			if err != nil {
				return nil, err
			}
			defer zr.Close()
			reader = zr
		}
		tr := tar.NewReader(reader)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			switch header.Typeflag {
			case tar.TypeDir:
			case tar.TypeSymlink:
				if err := add(header.Name, treeEntry{symlink: true, target: header.Linkname}); err != nil {
					return nil, err
				}
			case tar.TypeReg:
				h := sha256.New()
				if _, err := io.Copy(h, tr); err != nil {
					return nil, err
				}
				if err := add(header.Name, treeEntry{sha: hex.EncodeToString(h.Sum(nil)), exec: header.Mode&0o100 != 0}); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unsupported archive entry %s", header.Name)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported update bundle format: %s", format)
	}
	prefix := ""
	if _, ok := all[manifestFileName]; !ok {
		var tops []string
		for name := range all {
			if dir, rest, ok := strings.Cut(name, "/"); ok && rest == manifestFileName {
				tops = append(tops, dir)
			}
		}
		if len(tops) != 1 {
			return nil, errors.New("the archive does not hold one bundle manifest")
		}
		prefix = tops[0] + "/"
	}
	tree := make(map[string]treeEntry, len(all))
	for name, e := range all {
		if rel, ok := strings.CutPrefix(name, prefix); ok {
			tree[rel] = e
		}
	}
	return tree, nil
}

// compareTree requires the payload of root to be exactly want: under every
// top-level entry the bundle ships or that is a payload entry, the same files
// and symlinks, no more and no fewer, with the same bytes, executable bits and
// link targets. The update working tree and the trust folder are not payload.
func compareTree(root string, want map[string]treeEntry) error {
	tops := map[string]bool{}
	for name := range want {
		top, _, _ := strings.Cut(name, "/")
		tops[top] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if isBundlePayloadEntry(entry.Name()) {
			tops[entry.Name()] = true
		}
	}
	for top := range protectedEntries {
		delete(tops, top)
	}
	seen := 0
	for top := range tops {
		start := filepath.Join(root, top)
		if _, err := os.Lstat(start); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(start, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, name)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				return nil
			}
			expected, ok := want[rel]
			if !ok {
				return fmt.Errorf("%s is not in the signed bundle", rel)
			}
			seen++
			info, err := os.Lstat(name)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(name)
				if err != nil {
					return err
				}
				if !expected.symlink || target != expected.target {
					return fmt.Errorf("%s differs", rel)
				}
				return nil
			}
			if expected.symlink || !info.Mode().IsRegular() {
				return fmt.Errorf("%s differs", rel)
			}
			sum, _, err := fileSHA256(name)
			if err != nil {
				return err
			}
			if sum != expected.sha {
				return fmt.Errorf("%s differs", rel)
			}
			if runtime.GOOS != "windows" && (info.Mode().Perm()&0o100 != 0) != expected.exec {
				return fmt.Errorf("%s has other permissions", rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if seen != len(want) {
		for name := range want {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
				return fmt.Errorf("%s is missing", name)
			}
		}
		return errors.New("files of the signed bundle are missing")
	}
	return nil
}

// HomeBuildEnv is environ for a home build started by the installed build:
// WASMEDGE_DIR and library-path entries that point into the installed bundle
// are dropped, so the home build's launcher sets up its own runtime files.
func HomeBuildEnv(environ []string, installedRoot string) []string {
	installedRoot = filepath.Clean(installedRoot)
	inside := func(p string) bool {
		p = filepath.Clean(p)
		return p == installedRoot || strings.HasPrefix(p, installedRoot+string(filepath.Separator))
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		switch name {
		case "WASMEDGE_DIR":
			if inside(value) {
				continue
			}
		case "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH":
			var kept []string
			for _, part := range filepath.SplitList(value) {
				if part != "" && !inside(part) {
					kept = append(kept, part)
				}
			}
			if len(kept) == 0 {
				continue
			}
			kv = name + "=" + strings.Join(kept, string(filepath.ListSeparator))
		}
		out = append(out, kv)
	}
	return out
}
