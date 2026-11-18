package update

// HOME UPDATES WITHOUT A HELPER: install, restart through the installed path,
// and a trial start.
//
// A node that updates a home bundle (home.go) runs as an account that may not
// be able to start anything outside its own service: a daemon run by a system
// systemd unit as an ordinary account cannot create a transient unit, and a
// helper started as its child dies with the unit when the daemon stops. So in
// a home bundle there is no helper:
//
//  1. INSTALL, while the daemon serves (InstallHomeUpdate): the staged update
//     is verified, extracted and renamed into the home bundle. Nothing the
//     running process needs is touched: the installed bundle never changes,
//     and the files of a home build that is running stay open after they are
//     renamed into its rollback slot. The new build is marked as a TRIAL
//     (updates/trial.json), and its files are verified once more against its
//     signed archive.
//  2. RESTART THROUGH THE INSTALLED PATH: the daemon stops its services the
//     way it does for any shutdown and then replaces itself (exec, the same
//     process id) with the installed launcher and its own arguments. No
//     supervisor is involved, so this is the same with systemd
//     (Restart=always, Restart=on-failure or none), launchd (KeepAlive) and a
//     daemon started by hand.
//  3. THE TRIAL START (ChooseHomeBuild, in the installed build): the home
//     build is verified in full, the trial's start is counted, and the trial
//     build runs with TrialEnv set. It must answer ready within the node's
//     update.health_timeout_seconds; it then records health-passed and the
//     trial is over. If it does not, it records why and replaces itself with
//     the installed launcher again.
//  4. A TRIAL THAT ENDED WITHOUT PASSING: the next start through the installed
//     path (after that exec, or after a crash restarted by the supervisor)
//     finds a trial that was started and not passed. It records
//     health-failed, rolls the home bundle back to the previous slot (which
//     quarantines the build under updates/failed/), records health-reverted,
//     and runs the previous build.
//
// The deploy ledger, the rollback slots and the quarantine are those of the
// helper path, so `update slots`, `update rollback`, the update_failed alert
// and the feed's retry rules read them unchanged.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
)

const (
	trialFileName   = "trial.json"
	verdictFileName = "verdict.json"

	// TrialEnv names the update a daemon runs as a trial. The installed
	// build's start check sets it; nothing else does.
	TrialEnv = "SDN_UPDATE_TRIAL"

	// TrialSchema is the schema of updates/trial.json.
	TrialSchema = "org.spacedatanetwork.update.trial.v1"
	// VerdictSchema is the schema of updates/verdict.json.
	VerdictSchema = "org.spacedatanetwork.update.verdict.v1"
)

// Trial is updates/trial.json: a home build installed and not yet proven.
type Trial struct {
	Schema      string `json:"schema"`
	UpdateID    string `json:"update_id"`
	Version     string `json:"version"`
	Sequence    int64  `json:"sequence"`
	Trigger     string `json:"trigger,omitempty"`
	SignalKeyID string `json:"signal_key_id,omitempty"`
	InstalledAt string `json:"installed_at"`
	// Starts counts the trial starts the installed build made. A trial that
	// is started once and not passed is reverted at the next start.
	Starts int `json:"starts"`
	// FailReason is why the trial build gave up, when it did.
	FailReason string `json:"fail_reason,omitempty"`
}

func trialPath(paths Paths) string { return filepath.Join(paths.Updates, trialFileName) }

// ReadTrial returns the pending trial of a home bundle, or nil.
func ReadTrial(paths Paths) (*Trial, error) {
	data, err := os.ReadFile(trialPath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var trial Trial
	if err := json.Unmarshal(data, &trial); err != nil || trial.Schema != TrialSchema {
		return nil, fmt.Errorf("%s is not a trial record", trialPath(paths))
	}
	return &trial, nil
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeTrial(paths Paths, trial *Trial) error {
	trial.Schema = TrialSchema
	return writeJSONAtomic(trialPath(paths), trial)
}

func clearTrial(paths Paths) error {
	if err := os.Remove(trialPath(paths)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PassTrial records that the trial build answered ready: health-passed in the
// ledger, and the trial is over.
func PassTrial(paths Paths, updateID string) error {
	trial, err := ReadTrial(paths)
	if err != nil {
		return err
	}
	if trial == nil || trial.UpdateID != updateID {
		return fmt.Errorf("no trial of %s is pending", updateID)
	}
	state, err := LoadState(paths)
	if err != nil {
		return err
	}
	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{
		Action: "health-passed", UpdateID: trial.UpdateID, Version: trial.Version, Sequence: state.Sequence,
		Reason:  "the trial build answered ready, and it is the build in the home bundle",
		Trigger: trial.Trigger, SignalKeyID: trial.SignalKeyID,
	}); err != nil {
		return err
	}
	return clearTrial(paths)
}

// FailTrial records why the trial build gives up. The installed build's next
// start reverts it.
func FailTrial(paths Paths, updateID, reason string) error {
	trial, err := ReadTrial(paths)
	if err != nil {
		return err
	}
	if trial == nil || trial.UpdateID != updateID {
		return fmt.Errorf("no trial of %s is pending", updateID)
	}
	trial.FailReason = reason
	return writeTrial(paths, trial)
}

// The restart hook: the daemon registers how it stops for an update and
// replaces itself with the installed launcher.
var (
	restartMu  sync.Mutex
	restartFor func()
)

// OnRestartForUpdate registers the daemon's restart for an update.
func OnRestartForUpdate(f func()) {
	restartMu.Lock()
	restartFor = f
	restartMu.Unlock()
}

// RequestRestartForUpdate asks the daemon to stop and start again through the
// installed path. It reports false when no daemon registered a restart.
func RequestRestartForUpdate() bool {
	restartMu.Lock()
	f := restartFor
	restartMu.Unlock()
	if f == nil {
		return false
	}
	f()
	return true
}

// InstallHomeOptions is one home install.
type InstallHomeOptions struct {
	UpdateID      string
	StoreRoot     string
	Trigger       string
	SignalKeyID   string
	AllowRollback bool
}

// InstallHomeUpdate installs a staged update into a home bundle as a trial,
// while the daemon serves, and verifies the installed files against the
// signed archive. A build that does not verify once installed is rolled back
// at once.
func InstallHomeUpdate(paths Paths, opts InstallHomeOptions) (*ApplyResult, error) {
	if !paths.IsHome() {
		return nil, errors.New("not a home bundle")
	}
	applyOpts := ApplyOptions{UpdateID: opts.UpdateID, StoreRoot: opts.StoreRoot, Trigger: opts.Trigger, SignalKeyID: opts.SignalKeyID, AllowRollback: opts.AllowRollback}
	prepared, err := Prepare(paths, applyOpts)
	if err != nil {
		return nil, err
	}
	defer prepared.Discard()
	result, err := ApplyPrepared(paths, prepared, applyOpts)
	if err != nil {
		return nil, err
	}
	if _, err := verifyAndRecord(paths); err != nil {
		reason := "the installed files do not verify against the signed archive: " + err.Error()
		if _, rbErr := revertTrial(paths, reason, opts.StoreRoot); rbErr != nil {
			return nil, fmt.Errorf("%s; and the rollback failed: %w", reason, rbErr)
		}
		return nil, errors.New(reason)
	}
	return result, nil
}

// launchHomeRestart is LaunchSelfUpgrade for a home bundle: install as a
// trial, then restart through the installed path. No helper.
func launchHomeRestart(paths Paths, opts SelfUpgradeOptions) (*SelfUpgradeLaunch, error) {
	if _, err := InstallHomeUpdate(paths, InstallHomeOptions{
		UpdateID: opts.UpdateID, StoreRoot: opts.StoreRoot, Trigger: opts.Trigger, SignalKeyID: opts.SignalKeyID,
	}); err != nil {
		return nil, err
	}
	if !RequestRestartForUpdate() {
		log.Warnf("update: %s is installed in %s and runs at the next start of this node", opts.UpdateID, paths.Root)
		return &SelfUpgradeLaunch{Mode: "next-start", PID: os.Getpid()}, nil
	}
	return &SelfUpgradeLaunch{Mode: "restart-through-installed-path", PID: os.Getpid()}, nil
}

// revertTrial ends a trial that did not pass: health-failed, a rollback to the
// previous slot (which quarantines the build), health-reverted. It reports
// the version restored.
func revertTrial(paths Paths, reason, storeRoot string) (string, error) {
	trial, err := ReadTrial(paths)
	if err != nil {
		return "", err
	}
	state, err := LoadState(paths)
	if err != nil {
		return "", err
	}
	id, version, sequence, trigger := state.UpdateID, state.Version, state.Sequence, ""
	if trial != nil {
		id, version, sequence, trigger = trial.UpdateID, trial.Version, trial.Sequence, trial.Trigger
	}
	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{Action: "health-failed", UpdateID: id, Version: version, Sequence: sequence, Reason: reason, Trigger: trigger}); err != nil {
		return "", err
	}
	restored, err := Rollback(paths, RollbackOptions{Reason: "trial failed: " + reason, StoreRoot: storeRoot})
	if err != nil {
		_ = RecordDeployLedgerEntry(paths, DeployLedgerEntry{Action: "revert-refused", UpdateID: id, Version: version, Sequence: sequence,
			Reason: "the trial failed (" + reason + ") and the rollback was refused: " + err.Error()})
		// Nothing was swapped: the build stays, and so does nobody's trial.
		_ = clearTrial(paths)
		return "", err
	}
	if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{Action: "health-reverted", UpdateID: id, Version: version, Sequence: sequence, Reason: reason, Trigger: trigger}); err != nil {
		log.Warnf("update: deploy ledger: %v", err)
	}
	_ = clearTrial(paths)
	_ = os.Remove(filepath.Join(paths.Updates, verdictFileName))
	return restored.RestoredVersion, nil
}

// HomeBuild is a verified home build, ready to run in place of the installed
// build.
type HomeBuild struct {
	Root       string
	Installed  string
	Version    string
	UpdateID   string
	Executable string
	// Trial is the update id this start runs as a trial; empty otherwise.
	Trial string
}

// StartCheck says what a process started from executable runs.
type StartCheck struct {
	// Build is the home build to run; nil runs this build.
	Build *HomeBuild
	// Notes are lines for the operator: refusals, reverts.
	Notes []string
}

// ChooseHomeBuild decides which build a process started from executable runs.
// daemon is true for `daemon`: the home build is then verified in full, and a
// pending trial is started, or reverted when its earlier start did not pass.
// Other commands accept a build whose files are unchanged since its last full
// verification (the recorded verdict) and verify in full otherwise.
func ChooseHomeBuild(executable string, daemon bool, storeRoot string) StartCheck {
	var check StartCheck
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	layout := bundle.ResolveFromExecutable(executable)
	if layout.Root == "" {
		return check
	}
	if record, _ := readHomeRecord(layout.Root); record != nil {
		return check // this process IS a home build
	}
	paths := PathsFor(layout.Root)
	if !paths.IsHome() {
		return check
	}
	if _, stale, err := homeCurrent(paths); err != nil || stale != "" {
		return check // nothing installed for this installed build
	}
	for range 3 {
		trial, err := ReadTrial(paths)
		if err != nil {
			check.Notes = append(check.Notes, err.Error())
			return check
		}
		if daemon && trial != nil && trial.Starts > 0 {
			reason := trial.FailReason
			if reason == "" {
				reason = "its trial start ended before it answered ready"
			}
			restored, err := revertTrial(paths, reason, storeRoot)
			if err != nil {
				check.Notes = append(check.Notes, fmt.Sprintf("update %s failed its trial (%s) and could not be reverted: %v", trial.UpdateID, reason, err))
			} else {
				check.Notes = append(check.Notes, fmt.Sprintf("update %s failed its trial (%s); rolled back to %s and quarantined", trial.UpdateID, reason, restored))
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(paths.Root, manifestFileName)); err != nil {
			return check // the home bundle holds no build: the installed one runs
		}
		var build *HomeBuild
		if daemon || !verdictHolds(paths) {
			build, err = verifyAndRecord(paths)
		} else {
			build, err = homeBuildFromState(paths)
		}
		if err != nil {
			if daemon && trial != nil {
				reason := "the installed build refused it: " + err.Error()
				restored, rbErr := revertTrial(paths, reason, storeRoot)
				if rbErr != nil {
					check.Notes = append(check.Notes, fmt.Sprintf("the build under %s is refused: %v; and it could not be reverted: %v; running the installed build", paths.Root, err, rbErr))
					return check
				}
				check.Notes = append(check.Notes, fmt.Sprintf("the build under %s is refused: %v; rolled back to %s and quarantined", paths.Root, err, restored))
				continue
			}
			check.Notes = append(check.Notes, fmt.Sprintf("the build under %s is refused: %v; running the installed build", paths.Root, err))
			return check
		}
		if daemon && trial != nil {
			trial.Starts++
			if err := writeTrial(paths, trial); err != nil {
				check.Notes = append(check.Notes, fmt.Sprintf("cannot count the trial start of %s (%v); running the installed build", trial.UpdateID, err))
				return check
			}
			build.Trial = trial.UpdateID
		}
		check.Build = build
		return check
	}
	return check
}

// homeBuildFromState names the build state.json records, without verifying it.
func homeBuildFromState(paths Paths) (*HomeBuild, error) {
	version, _, err := readBundleVersionAndChannel(paths.Root)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(paths)
	if err != nil {
		return nil, err
	}
	return &HomeBuild{Root: paths.Root, Installed: paths.Installed, Version: version, UpdateID: state.UpdateID,
		Executable: filepath.Join(paths.Root, "bin", executableFileName())}, nil
}

// verifyAndRecord verifies the home build in full and records the verdict.
func verifyAndRecord(paths Paths) (*HomeBuild, error) {
	_ = os.Remove(filepath.Join(paths.Updates, verdictFileName))
	build, err := VerifyHomeBuild(paths)
	if err != nil {
		return nil, err
	}
	if err := writeVerdict(paths, build.UpdateID); err != nil {
		log.Warnf("update: cannot record the verification of %s: %v", build.UpdateID, err)
	}
	return build, nil
}

// THE VERDICT. A full verification hashes the signed archive and every file of
// the build; that is done when a build is installed and at every daemon start.
// Every other command accepts the build when no file it depends on has
// changed since: the same set of files under the home bundle (outside
// updates/), its state, record and proof, and the installed manifest and
// update roots, each with the same size, mode, modification and change times,
// inode and device. A file written, replaced, renamed, added or removed
// changes one of them. The verdict guards against a damaged or replaced file;
// it is no defense against the account that owns the home folder, which can
// run any program it likes anyway.
type verdict struct {
	Schema   string               `json:"schema"`
	UpdateID string               `json:"update_id"`
	Files    map[string]fileStamp `json:"files"`
}

func verdictFiles(paths Paths, updateID string) (map[string]fileStamp, error) {
	files := map[string]fileStamp{}
	add := func(name string) error {
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		s, ok := stampOf(info)
		if !ok {
			return errors.New("file identity is not available on this platform")
		}
		files[name] = s
		return nil
	}
	err := filepath.WalkDir(paths.Root, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == paths.Updates {
			return filepath.SkipDir
		}
		return add(name)
	})
	if err != nil {
		return nil, err
	}
	for _, name := range []string{
		paths.State,
		filepath.Join(paths.Updates, homeRecordName),
		filepath.Join(paths.Installed, manifestFileName),
		filepath.Join(paths.Installed, trustDirName, trustRootsName),
	} {
		if err := add(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	proof := proofDir(paths, updateID)
	entries, err := os.ReadDir(proof)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := add(filepath.Join(proof, entry.Name())); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func writeVerdict(paths Paths, updateID string) error {
	files, err := verdictFiles(paths, updateID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(paths.Updates, verdictFileName), verdict{Schema: VerdictSchema, UpdateID: updateID, Files: files})
}

// verdictHolds reports whether the recorded verdict covers the build on disk
// exactly as it is now.
func verdictHolds(paths Paths) bool {
	if strings.TrimSpace(os.Getenv(TrustRootsEnv)) != "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(paths.Updates, verdictFileName))
	if err != nil {
		return false
	}
	var v verdict
	if json.Unmarshal(data, &v) != nil || v.Schema != VerdictSchema || v.UpdateID == "" {
		return false
	}
	state, err := LoadState(paths)
	if err != nil || state.UpdateID != v.UpdateID {
		return false
	}
	now, err := verdictFiles(paths, v.UpdateID)
	if err != nil || len(now) != len(v.Files) {
		return false
	}
	for name, s := range now {
		if v.Files[name] != s {
			return false
		}
	}
	return true
}
