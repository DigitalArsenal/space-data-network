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
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
	if record, err := readHomeRecord(bundleRoot); err != nil {
		// A home build whose record cannot be read is not treated as an
		// install: start the node through the installed path, which rebuilds
		// the record at the next update.
		p := pathsIn(bundleRoot)
		p.HomeErr = fmt.Errorf("the home bundle record in %s cannot be read (%w); start the node through its installed path", bundleRoot, err)
		return p
	} else if record != nil {
		p := pathsIn(bundleRoot)
		p.Installed = filepath.Clean(record.InstalledRoot)
		return p
	}
	// Windows has no home bundle (no exec), so the install folder is the
	// update root whether or not it is writable, and nothing probes it.
	if runtime.GOOS == "windows" {
		return pathsIn(bundleRoot)
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, manifestFileName)); err != nil || dirWritable(bundleRoot) {
		return pathsIn(bundleRoot)
	}
	// One spelling of the installed folder, however it was reached: the home
	// bundle is named after it and its record compares it.
	installed := filepath.Clean(bundleRoot)
	if resolved, err := filepath.EvalSymlinks(bundleRoot); err == nil {
		installed = resolved
	}
	home, err := HomeUpdateRoot(installed)
	if err != nil {
		p := pathsIn(bundleRoot)
		p.HomeErr = err
		return p
	}
	p := pathsIn(home)
	p.Installed = installed
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

// InstalledLauncher is the program a later start must name for a process
// started from executable: the installed launcher when executable belongs to
// a home build, executable itself otherwise. A service unit, a launchd agent
// or a restart command that named a home build's own path would start it with
// no check, and the path is gone after a rollback empties the home bundle.
func InstalledLauncher(executable string) string {
	resolved := executable
	if r, err := filepath.EvalSymlinks(executable); err == nil {
		resolved = r
	}
	layout := bundle.ResolveFromExecutable(resolved)
	if layout.Root == "" {
		return executable
	}
	record, err := readHomeRecord(layout.Root)
	if err != nil || record == nil {
		return executable
	}
	return filepath.Join(record.InstalledRoot, "bin", executableFileName())
}

func executableFileName() string {
	if runtime.GOOS == "windows" {
		return "spacedatanetwork.exe"
	}
	return "spacedatanetwork"
}

var homeFallbackLogged sync.Once

// homeBundleKey names the home bundle of one install: the first 16 hex
// characters of sha256 over the resolved installed folder. Two installs, or an
// install moved to another folder, get their own home bundles; an install
// whose files are replaced in place keeps its key and its record says the
// installed build changed. (A bundle whose mount path changes at every start
// would get a new home bundle each time; no bundle the node ships is run that
// way: the desktop app and its AppImage do not update themselves.)
func homeBundleKey(installed string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(installed)))
	return hex.EncodeToString(sum[:])[:16]
}

// homeBundleCandidate is where the home bundle of the install at installed
// would be under the account's home folder, or "".
func homeBundleCandidate(installed string) string {
	if resolved, err := filepath.EvalSymlinks(installed); err == nil {
		installed = resolved
	}
	home, err := accountHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".spacedatanetwork", homeBundleDirName, homeBundleKey(installed))
}

// accountHome is the effective user's home folder from its passwd entry.
// $HOME is used only when it is the same folder.
func accountHome() (string, error) {
	u, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || strings.TrimSpace(u.HomeDir) == "" {
		return "", fmt.Errorf("the account (uid %d) has no home folder in its passwd entry", os.Geteuid())
	}
	home := filepath.Clean(u.HomeDir)
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if env := strings.TrimSpace(os.Getenv("HOME")); env != "" {
		if resolved, err := filepath.EvalSymlinks(env); err == nil && resolved == home {
			return env, nil
		}
	}
	return home, nil
}

// HomeUpdateRoot is the home bundle of the install at installed:
// <home>/.spacedatanetwork/bundle/<key>, <home> from the account's passwd
// entry, or <storage.path>/update-bundle/<key> when there is no usable home.
// Root never gets one: a root process would run a build another account may
// have written.
func HomeUpdateRoot(installed string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", errors.New("on Windows a node updates only an install folder its account can write; this account cannot write it, so install the new release by hand or give the account write access to the install folder")
	}
	if os.Geteuid() == 0 {
		return "", fmt.Errorf("running as root, and root cannot write the install folder %s (a read-only mount?): root never runs a build from a home folder; make the install folder writable or install the new release by hand", installed)
	}
	key := homeBundleKey(installed)
	var reason string
	if home, err := accountHome(); err == nil {
		dir := filepath.Join(home, ".spacedatanetwork", homeBundleDirName, key)
		if err := checkOwnedChain(dir, home); isChainRefusal(err) {
			// Never a fallback: a bundle someone else can change is refused,
			// not replaced by another folder.
			return "", err
		} else if err != nil {
			// A home folder that is missing or cannot be reached (a service
			// with ProtectHome=, a passwd entry naming a folder that is not
			// there) is no usable home.
			reason = "the home folder " + home + " cannot be reached (" + err.Error() + ")"
		} else if canCreateUnder(dir) {
			return dir, nil
		} else {
			reason = dir + " cannot be written"
		}
	} else {
		reason = err.Error()
	}
	if HomeStorageFallback != nil {
		if storage := strings.TrimSpace(HomeStorageFallback()); storage != "" {
			dir := filepath.Join(storage, storageHomeDirName, key)
			if err := checkOwnedChain(dir, storage); isChainRefusal(err) {
				return "", err
			} else if err != nil {
				reason += "; " + err.Error()
			} else if canCreateUnder(dir) {
				homeFallbackLogged.Do(func() {
					log.Warnf("update: %s; updates go to %s, under the node's storage.path", reason, dir)
				})
				return dir, nil
			} else {
				reason += "; " + dir + " cannot be written either"
			}
		}
	}
	return "", fmt.Errorf("this account cannot write the install folder, and it has no folder to update into: %s", reason)
}

// chainRefusal is a folder another account can change: the build under it is
// refused, and no other folder is used in its place.
type chainRefusal struct{ err error }

func (r *chainRefusal) Error() string { return r.err.Error() }
func (r *chainRefusal) Unwrap() error { return r.err }

func isChainRefusal(err error) bool {
	var r *chainRefusal
	return errors.As(err, &r)
}

// checkOwnedChain requires every existing folder from dir up to and including
// top to be owned by this account and not writable by its group or by others,
// and every folder and symlink on the way from / to dir, as the system
// resolves it, to be one only this account or root can change (resolvedSafe):
// a build another account can change is never run. Such a folder is a
// chainRefusal; any other error (a folder that cannot be reached) is not.
func checkOwnedChain(dir, top string) error {
	top = filepath.Clean(top)
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	current := filepath.Clean(dir)
	// A folder that cannot be reached is remembered and the walk goes on up:
	// an existing folder above it that another account owns is a refusal.
	var unreachable error
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return &chainRefusal{fmt.Errorf("%s is not a folder", current)}
			}
			if err := ownedNotShared(current, info); err != nil {
				return &chainRefusal{err}
			}
			if resolved, rerr := filepath.EvalSymlinks(current); rerr == nil && resolved == top {
				break
			}
		} else if !errors.Is(err, os.ErrNotExist) && unreachable == nil {
			unreachable = err
		}
		if current == top {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return &chainRefusal{fmt.Errorf("%s is not under %s", dir, top)}
		}
		current = parent
	}
	if err := resolvedSafe(dir); isChainRefusal(err) || unreachable == nil {
		return err
	}
	return unreachable
}

// resolvedSafe walks name from / the way the system resolves it, following
// each symlink, and requires every folder and symlink it passes through to be
// owned by this account or by root, and every folder writable by its group or
// by others to carry the sticky bit (/tmp), where only an entry's owner can
// rename or remove it. Folders of name that do not exist are not checked: the
// node creates them, under a folder this checked.
func resolvedSafe(name string) error {
	if !filepath.IsAbs(name) {
		abs, err := filepath.Abs(name)
		if err != nil {
			return err
		}
		name = abs
	}
	pending := strings.Split(strings.TrimPrefix(filepath.Clean(name), "/"), "/")
	current := "/"
	if err := safeEntry(current); err != nil {
		return err
	}
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := safeEntry(next); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		links++
		if links > 40 {
			return fmt.Errorf("%s: too many symlinks", name)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return err
		}
		if filepath.IsAbs(target) {
			current = "/"
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	return nil
}

// safeEntry applies resolvedSafe's rule to one folder or symlink.
func safeEntry(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if err := ownedByUserOrRoot(name, info); err != nil {
		return &chainRefusal{err}
	}
	if info.IsDir() && info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return &chainRefusal{fmt.Errorf("%s is writable by its group or by others (mode %04o) without the sticky bit", name, info.Mode().Perm())}
	}
	return nil
}

// checkBundleTree requires every folder and file of the build in a home
// bundle (outside updates/) to be owned by this account and not writable by
// its group or by others, and every symlink to be owned by this account: the
// launcher, the daemon and the runtime it loads all live there.
func checkBundleTree(paths Paths) error {
	return filepath.WalkDir(paths.Root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == paths.Updates {
			return filepath.SkipDir
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ownedByUser(name, info)
		}
		return ownedNotShared(name, info)
	})
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

// Locks of a home bundle live beside it, so removing a stale bundle does not
// remove them: <key>.install.lock is held while an update is staged or
// installed into it (a typed `update install` and the daemon's own install
// never run at once), <key>.inuse.lock is held shared by every process that
// runs a build from it (a bundle a live process runs from is never removed),
// and <key>.daemon.lock is held by the one daemon it serves.
func homeLock(paths Paths, kind string, exclusive bool) (*fileLock, bool, error) {
	return lockFile(filepath.Dir(paths.Root), filepath.Base(paths.Root)+"."+kind+".lock", exclusive)
}

// LockHomeInstall takes the install lock of a home bundle, or says who holds it.
func LockHomeInstall(paths Paths) (*fileLock, error) {
	if !paths.IsHome() {
		return nil, nil
	}
	lock, ok, err := homeLock(paths, "install", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("another update is being installed in %s right now; try again when it is done", paths.Root)
	}
	return lock, nil
}

// HoldHomeInUse marks this process as running a build from the home bundle at
// root, for its whole life. A failure is not fatal: the bundle is then only
// unprotected against removal.
func HoldHomeInUse(root string) {
	lock, ok, err := lockFile(filepath.Dir(root), filepath.Base(root)+".inuse.lock", false)
	if err == nil && ok {
		heldInUse = lock
	}
}

var heldInUse *fileLock

// LockHomeDaemon takes the daemon lock of a home bundle for this daemon's life.
// One home bundle serves one daemon: a second node run from the same install
// by the same account is refused (give each node its own install), because a
// trial and its rollback restart one daemon and would judge the other's build.
//
// The lock is held for the life of the process and passed across each exec
// between the installed build and the home build (DaemonLockEnv), so no
// second daemon can take it between the start check and the daemon it starts.
func LockHomeDaemon(paths Paths) (*fileLock, error) {
	if !paths.IsHome() {
		return nil, nil
	}
	name := filepath.Join(filepath.Dir(paths.Root), filepath.Base(paths.Root)+".daemon.lock")
	if heldDaemon != nil {
		if heldDaemon.sameFile(name) {
			return heldDaemon, nil
		}
		heldDaemon.Release()
		heldDaemon = nil
	}
	lock, ok, err := homeLock(paths, "daemon", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errSecondDaemon(paths)
	}
	heldDaemon = lock
	return lock, nil
}

func errSecondDaemon(paths Paths) error {
	return fmt.Errorf("another daemon already runs from the install %s and updates through %s; one install serves one node per account, so give this node its own install", paths.Installed, paths.Root)
}

// daemonAlive reports whether a daemon holds the daemon lock of a home bundle.
// When none does, release takes the lock away again; the caller holds it
// until then.
func daemonAlive(paths Paths) (alive bool, release func()) {
	lock, ok, err := homeLock(paths, "daemon", true)
	if err != nil || !ok {
		return true, func() {}
	}
	return false, lock.Release
}

// lockTrial serializes every change of a home bundle's trial record: the
// trial build's pass and fail, a signal stop, the start check, a revert.
func lockTrial(paths Paths) (*fileLock, error) {
	return lockFileWait(filepath.Dir(paths.Root), filepath.Base(paths.Root)+".trial.lock")
}

// EnsureHome makes the home bundle ready to stage into: its record names the
// installed build as it is now. A home bundle made for another installed
// build, or whose record cannot be read, is removed first, unless a process
// still runs a build from it.
func EnsureHome(paths Paths) error {
	if paths.HomeErr != nil {
		return paths.HomeErr
	}
	if !paths.IsHome() {
		return nil
	}
	record, stale, err := homeCurrent(paths)
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(paths.Updates, homeRecordName)); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return err
		}
		if _, ierr := installedRecord(paths.Installed); ierr != nil {
			return ierr
		}
		// The record itself is unreadable: start the bundle again.
		stale = "its record cannot be read (" + err.Error() + ")"
		record = &HomeRecord{}
	}
	if record != nil && stale == "" {
		return nil
	}
	if record != nil {
		inUse, ok, lerr := homeLock(paths, "inuse", true)
		if lerr != nil {
			return lerr
		}
		if !ok {
			return fmt.Errorf("the home bundle %s is stale (%s), and a process still runs a build from it: it is replaced once that process stops (restart the node)", paths.Root, stale)
		}
		log.Warnf("update: %s; removing the home bundle %s and updating from the installed build", stale, paths.Root)
		err := os.RemoveAll(paths.Root)
		inUse.Release()
		if err != nil {
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
	return writeJSONAtomic(filepath.Join(paths.Updates, homeRecordName), now)
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
	// The anchor is the installed build alone: its roots through the kept
	// rotations. SDN_UPDATE_TRUST_ROOTS does not apply here.
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
	if err := checkBundleTree(paths); err != nil {
		return nil, err
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

// InstalledEnvVar carries, from the installed build's start check to the home
// build, the environment entries the check removed, so a restart through the
// installed path gives the installed launcher back the environment it had.
const InstalledEnvVar = "SDN_UPDATE_INSTALLED_ENV"

// pathInside reports whether p is root or under it, comparing p as written
// and resolved through symlinks against root as written and resolved.
func pathInside(p, root string) bool {
	roots := []string{filepath.Clean(root)}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, r)
	}
	values := []string{filepath.Clean(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		values = append(values, r)
	}
	for _, v := range values {
		for _, r := range roots {
			if v == r || strings.HasPrefix(v, r+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// scrubRuntimeEnv removes from environ the WASMEDGE_DIR and library-path
// entries that point into root, and returns what it removed.
func scrubRuntimeEnv(environ []string, root string) (kept, removed []string) {
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			kept = append(kept, kv)
			continue
		}
		switch name {
		case "WASMEDGE_DIR":
			if pathInside(value, root) {
				removed = append(removed, kv)
				continue
			}
		case "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH":
			var stay []string
			for _, part := range filepath.SplitList(value) {
				if part != "" && !pathInside(part, root) {
					stay = append(stay, part)
				}
			}
			if strings.Join(stay, string(filepath.ListSeparator)) != value {
				removed = append(removed, kv)
				if len(stay) == 0 {
					continue
				}
				kv = name + "=" + strings.Join(stay, string(filepath.ListSeparator))
			}
		}
		kept = append(kept, kv)
	}
	return kept, removed
}

// HomeBuildEnv is environ for a home build started by the installed build:
// WASMEDGE_DIR and library-path entries that point into the installed bundle
// (by either spelling of its folder) are removed, so the home build's
// launcher sets up its own runtime files, and the removed entries travel in
// InstalledEnvVar.
func HomeBuildEnv(environ []string, installedRoot string) []string {
	var base []string
	for _, kv := range environ {
		if !strings.HasPrefix(kv, InstalledEnvVar+"=") {
			base = append(base, kv)
		}
	}
	kept, removed := scrubRuntimeEnv(base, installedRoot)
	if len(removed) > 0 {
		if data, err := json.Marshal(removed); err == nil {
			kept = append(kept, InstalledEnvVar+"="+string(data))
		}
	}
	return kept
}

// InstalledEnv is environ for a restart through the installed path from a
// home build at homeRoot: entries that point into the home bundle are
// removed, and the ones the installed build's start check removed are given
// back, so the installed build runs on its own runtime files.
func InstalledEnv(environ []string, homeRoot string) []string {
	var restore []string
	var base []string
	for _, kv := range environ {
		if value, ok := strings.CutPrefix(kv, InstalledEnvVar+"="); ok {
			_ = json.Unmarshal([]byte(value), &restore)
			continue
		}
		base = append(base, kv)
	}
	kept, _ := scrubRuntimeEnv(base, homeRoot)
	names := map[string]bool{}
	for _, kv := range restore {
		name, _, _ := strings.Cut(kv, "=")
		names[name] = true
	}
	out := kept[:0:0]
	for _, kv := range kept {
		name, _, _ := strings.Cut(kv, "=")
		if !names[name] {
			out = append(out, kv)
		}
	}
	return append(out, restore...)
}

// HomeBuildRoot is the home bundle executable runs from, or "" when it is not
// a home build.
func HomeBuildRoot(executable string) string {
	if r, err := filepath.EvalSymlinks(executable); err == nil {
		executable = r
	}
	layout := bundle.ResolveFromExecutable(executable)
	if layout.Root == "" {
		return ""
	}
	if record, err := readHomeRecord(layout.Root); err == nil && record != nil {
		return layout.Root
	}
	return ""
}
