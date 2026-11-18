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
	"time"

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
	// Deadline is when the trial must have answered ready: the start check
	// sets it, before it starts the trial build, to the start time plus
	// update.health_timeout_seconds.
	Deadline string `json:"deadline,omitempty"`
	// Interruptions counts the times the trial build was stopped by a signal
	// (a service restart, a reboot) before it answered ready. Each one runs
	// the trial again from the start with a new deadline, up to
	// MaxTrialInterruptions; the next one fails the trial.
	Interruptions int `json:"interruptions,omitempty"`
}

// MaxTrialInterruptions is how many signal stops a trial survives: a build
// stopped this many times during its trial runs it again each time, and the
// next stop fails it, so a build that is always restarted before it is ready
// cannot stay on trial for ever.
const MaxTrialInterruptions = 3

// TrialDeadline is the deadline of the pending trial of updateID.
func TrialDeadline(paths Paths, updateID string) (time.Time, error) {
	trial, err := ReadTrial(paths)
	if err != nil {
		return time.Time{}, err
	}
	if trial == nil || trial.UpdateID != updateID || trial.Deadline == "" {
		return time.Time{}, fmt.Errorf("no started trial of %s", updateID)
	}
	return time.Parse(time.RFC3339Nano, trial.Deadline)
}

// TrialPending reports whether the trial of updateID is still undecided.
func TrialPending(paths Paths, updateID string) bool {
	trial, err := ReadTrial(paths)
	return err == nil && trial != nil && trial.UpdateID == updateID && trial.FailReason == ""
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
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// The rename is durable only once the folder is.
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

func writeTrial(paths Paths, trial *Trial) error {
	trial.Schema = TrialSchema
	return writeJSONAtomic(trialPath(paths), trial)
}

func writeTrialLocked(paths Paths, trial *Trial) error {
	lock, err := lockTrial(paths)
	if err != nil {
		return err
	}
	defer lock.Release()
	return writeTrial(paths, trial)
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
	lock, err := lockTrial(paths)
	if err != nil {
		return err
	}
	defer lock.Release()
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
	// One durable state write means "passed": the trial mark is cleared and
	// the pass recorded together. A stop after it leaves trial.json behind,
	// which every start reads as passed (finishPassedTrial); a stop before it
	// leaves the trial running, as it was.
	if state.UpdateID == trial.UpdateID {
		state.TrialUpdateID = ""
		state.TrialPassed = trial.UpdateID
		if err := SaveState(paths, state); err != nil {
			return err
		}
	}
	if crashAfterPassStateWrite != nil {
		crashAfterPassStateWrite()
	}
	return finishPass(paths, trial, state.Sequence)
}

// finishPass removes the trial record of a passed trial and writes
// health-passed to the ledger (once). The caller holds the trial lock.
func finishPass(paths Paths, trial *Trial, sequence int64) error {
	if err := clearTrial(paths); err != nil {
		return err
	}
	if ledgerHas(paths, trial.UpdateID, "health-passed") {
		return nil
	}
	return RecordDeployLedgerEntry(paths, DeployLedgerEntry{
		Action: "health-passed", UpdateID: trial.UpdateID, Version: trial.Version, Sequence: sequence,
		Reason:  "the trial build answered ready, and it is the build in the home bundle",
		Trigger: trial.Trigger, SignalKeyID: trial.SignalKeyID,
	})
}

// FailTrial records why the trial build gives up. The installed build's next
// start reverts it. It writes only while the trial record still names
// updateID: a trial another process already passed or reverted stays over.
func FailTrial(paths Paths, updateID, reason string) error {
	lock, err := lockTrial(paths)
	if err != nil {
		return err
	}
	defer lock.Release()
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

// InterruptTrial records that the trial build of updateID was stopped by a
// signal before it answered ready: the trial runs again from the start at
// the next start, with a new deadline, unless it has been stopped
// MaxTrialInterruptions times already, in which case the trial fails. It
// reports whether the trial runs again; false with no error when there is no
// pending trial of updateID (it passed, failed or was reverted).
func InterruptTrial(paths Paths, updateID string) (rerun bool, err error) {
	lock, err := lockTrial(paths)
	if err != nil {
		return false, err
	}
	defer lock.Release()
	trial, err := ReadTrial(paths)
	if err != nil || trial == nil || trial.UpdateID != updateID || trial.FailReason != "" {
		return false, err
	}
	if trial.Interruptions >= MaxTrialInterruptions {
		trial.FailReason = fmt.Sprintf("it was stopped %d times during its trial before it answered ready", trial.Interruptions+1)
		return false, writeTrial(paths, trial)
	}
	trial.Interruptions++
	trial.Starts = 0
	trial.Deadline = ""
	return true, writeTrial(paths, trial)
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
	lock, err := LockHomeInstall(paths)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
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
//
// It works under the trial lock, on the trial record as it is then, and only
// on the build state.json names: a trial record that names another update is
// what a revert stopped after its rollback leaves (the build is already
// quarantined), and is only cleared, so a second revert never takes away the
// build that was running before the trial.
func revertTrial(paths Paths, reason, storeRoot string) (string, error) {
	lock, err := lockTrial(paths)
	if err != nil {
		return "", err
	}
	defer lock.Release()
	// An unreadable trial record is reverted by what state.json names.
	trial, readErr := ReadTrial(paths)
	state, err := LoadState(paths)
	if err != nil {
		return "", err
	}
	if trial == nil && readErr == nil {
		return state.Version, nil // another process ended the trial first
	}
	if trial != nil && trial.UpdateID != state.UpdateID {
		finishInterruptedRevert(paths, trial)
		return state.Version, nil
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
		// Nothing was swapped. The build that failed its trial is the only
		// one the store-format guard lets open the store, so it keeps
		// running; the ledger line raises the update_failed alert on the
		// node, and an operator decides (sdn-signed-updater.md).
		_ = RecordDeployLedgerEntry(paths, DeployLedgerEntry{Action: "revert-refused", UpdateID: id, Version: version, Sequence: sequence,
			Reason: "the trial failed (" + reason + ") and the rollback was refused: " + err.Error() + "; the build that failed keeps running until an operator acts"})
		log.Errorf("update: %s failed its trial (%s) and was NOT rolled back: %v. It keeps running; see the update_failed alert", id, reason, err)
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

// finishInterruptedRevert ends a trial record that names an update other than
// the one state.json names: a revert stopped after its rollback, or a fail
// recorded after the trial was already reverted. When the update is in
// quarantine and the ledger has no health-reverted for it, that line is
// written (the update_failed alert reads it). Nothing is rolled back.
func finishInterruptedRevert(paths Paths, trial *Trial) {
	if HasFailedUpdate(paths, trial.UpdateID) && !ledgerHas(paths, trial.UpdateID, "health-reverted") {
		reason := trial.FailReason
		if reason == "" {
			reason = "its trial ended before it answered ready"
		}
		if err := RecordDeployLedgerEntry(paths, DeployLedgerEntry{Action: "health-reverted", UpdateID: trial.UpdateID, Version: trial.Version, Sequence: trial.Sequence,
			Reason: reason + "; the revert was interrupted after its rollback and is completed here", Trigger: trial.Trigger}); err != nil {
			log.Warnf("update: deploy ledger: %v", err)
		}
	}
	_ = clearTrial(paths)
	_ = os.Remove(filepath.Join(paths.Updates, verdictFileName))
}

// restoreMissingTrial writes the trial record of the build state.json marks
// as on trial (State.TrialUpdateID) when the record is missing and the ledger
// has not ended that trial: what a crash between the state write and the trial
// write of a home install leaves. It runs on the daemon start path only, and
// holds the trial lock from its first read to its write, so it cannot race a
// PassTrial (which clears the mark before it removes the record). It returns
// the record and a note, or nil.
func restoreMissingTrial(paths Paths) (*Trial, string) {
	lock, err := lockTrial(paths)
	if err != nil {
		return nil, ""
	}
	defer lock.Release()
	if existing, err := ReadTrial(paths); err != nil || existing != nil {
		return nil, ""
	}
	state, err := LoadState(paths)
	if err != nil || state.TrialUpdateID == "" || state.TrialUpdateID != state.UpdateID || state.TrialPassed == state.UpdateID {
		return nil, ""
	}
	for _, ended := range []string{"health-passed", "health-reverted", "revert-refused"} {
		if ledgerHas(paths, state.UpdateID, ended) {
			return nil, ""
		}
	}
	trial := &Trial{UpdateID: state.UpdateID, Version: state.Version, Sequence: state.Sequence, InstalledAt: state.AppliedAt}
	if err := writeTrial(paths, trial); err != nil {
		return nil, ""
	}
	if again, err := ReadTrial(paths); err != nil || again == nil || again.UpdateID != state.UpdateID {
		return nil, ""
	}
	return trial, fmt.Sprintf("update %s was installed and its trial record was not written (the install stopped after its state write); it runs as a trial", state.UpdateID)
}

// finishPassedTrial completes a pass that stopped after its state write
// (State.TrialPassed names the build it runs): the trial record is removed and
// health-passed written, so the passed build is never judged as a trial
// again. It returns a note when it did anything.
func finishPassedTrial(paths Paths) string {
	lock, err := lockTrial(paths)
	if err != nil {
		return ""
	}
	defer lock.Release()
	state, err := LoadState(paths)
	if err != nil || state.TrialPassed == "" || state.TrialPassed != state.UpdateID {
		return ""
	}
	trial, err := ReadTrial(paths)
	if err != nil {
		return ""
	}
	if trial != nil && trial.UpdateID != state.UpdateID {
		return ""
	}
	if trial == nil {
		if ledgerHas(paths, state.UpdateID, "health-passed") {
			return ""
		}
		trial = &Trial{UpdateID: state.UpdateID, Version: state.Version, Sequence: state.Sequence}
	}
	if err := finishPass(paths, trial, state.Sequence); err != nil {
		return fmt.Sprintf("update %s passed its trial, and finishing the pass failed: %v", state.UpdateID, err)
	}
	return fmt.Sprintf("update %s passed its trial (the pass stopped after its state write); the pass is finished, and it does not run as a trial again", state.UpdateID)
}

// ledgerHas reports whether the home bundle's deploy ledger has a line with
// action for updateID.
func ledgerHas(paths Paths, updateID, action string) bool {
	data, err := os.ReadFile(filepath.Join(paths.Root, deployLedgerName))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		var entry DeployLedgerEntry
		if json.Unmarshal([]byte(line), &entry) == nil && entry.UpdateID == updateID && entry.Action == action {
			return true
		}
	}
	return false
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
	// Deadline is when the trial must have answered ready.
	Deadline string
}

// StartCheck says what a process started from executable runs.
type StartCheck struct {
	// Build is the home build to run; nil runs this build.
	Build *HomeBuild
	// Notes are lines for the operator: refusals, reverts.
	Notes []string
	// Refuse is set when this process must not run at all: a second daemon
	// for one home bundle.
	Refuse error
}

// StartCheckOptions are what the start check reads lazily: only a daemon start
// with a trial to start reads the config.
type StartCheckOptions struct {
	// Daemon is true for `daemon`.
	Daemon bool
	// StoreRoot is the node's storage.path, for the store-format guard of a
	// revert.
	StoreRoot func() string
	// HealthTimeout is update.health_timeout_seconds.
	HealthTimeout func() time.Duration
	// Now replaces time.Now.
	Now func() time.Time
}

// ChooseHomeBuild decides which build a process started from executable runs.
// A daemon start verifies the home build in full, starts a pending trial with
// its deadline, and reverts a trial that was started and did not pass. Any
// start reverts a trial whose deadline has passed. Other commands accept a
// build whose files are unchanged since its last full verification (the
// recorded verdict) and verify in full otherwise.
func ChooseHomeBuild(executable string, opts StartCheckOptions) StartCheck {
	var check StartCheck
	if opts.Now == nil {
		opts.Now = time.Now
	}
	storeRoot := func() string {
		if opts.StoreRoot == nil {
			return ""
		}
		return opts.StoreRoot()
	}
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
	if os.Geteuid() == 0 {
		return check // root never runs a home build (HomeUpdateRoot)
	}
	paths := PathsFor(layout.Root)
	if paths.HomeErr != nil {
		// A home bundle this account may not use (owned by another account,
		// writable by others): say so when one is there.
		if candidate := homeBundleCandidate(layout.Root); candidate != "" {
			if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
				check.Notes = append(check.Notes, fmt.Sprintf("the build under %s is refused: %v; running the installed build", candidate, paths.HomeErr))
			}
		}
		return check
	}
	if !paths.IsHome() {
		return check
	}
	if _, err := os.Stat(paths.Root); errors.Is(err, os.ErrNotExist) {
		return check
	}
	if err := checkOwnedChain(paths.Updates, homeAnchor(paths.Root)); err != nil {
		check.Notes = append(check.Notes, fmt.Sprintf("the build under %s is refused: %v; running the installed build", paths.Root, err))
		return check
	}
	if _, stale, err := homeCurrent(paths); err != nil || stale != "" {
		if err != nil {
			check.Notes = append(check.Notes, fmt.Sprintf("the home bundle %s is set aside: %v; running the installed build", paths.Root, err))
		}
		return check // nothing installed for this installed build
	}
	if opts.Daemon {
		// Held for this process's life, and passed across the exec that
		// starts the home build (DaemonLockForExec): no other daemon can
		// start in between and judge this one's trial.
		if _, err := LockHomeDaemon(paths); err != nil {
			check.Refuse = err
			return check
		}
	}
	for range 4 {
		// A pass that stopped after its state write is finished first: the
		// build it passed is never judged as a trial again.
		if note := finishPassedTrial(paths); note != "" {
			check.Notes = append(check.Notes, note)
		}
		trial, err := ReadTrial(paths)
		if err != nil {
			// A trial whose record cannot be read is a failed trial.
			trial = &Trial{Starts: 1, FailReason: "its trial record cannot be read: " + err.Error()}
		}
		if trial == nil && opts.Daemon {
			if restored, note := restoreMissingTrial(paths); restored != nil {
				trial = restored
				check.Notes = append(check.Notes, note)
			}
		}
		if trial != nil && trial.UpdateID != "" {
			if state, serr := LoadState(paths); serr == nil && state.UpdateID != trial.UpdateID {
				// A trial of an update this bundle no longer runs: a revert
				// stopped after its rollback. The daemon start clears it; a
				// typed command leaves it alone.
				if !opts.Daemon {
					trial = nil
				} else {
					if _, err := revertTrial(paths, "", storeRoot()); err != nil {
						check.Notes = append(check.Notes, fmt.Sprintf("the trial record of update %s cannot be cleared: %v; running the installed build", trial.UpdateID, err))
						return check
					}
					check.Notes = append(check.Notes, fmt.Sprintf("update %s was already rolled back (its revert stopped after the rollback); its trial record is cleared", trial.UpdateID))
					continue
				}
			}
		}
		if trial != nil && trial.Starts > 0 {
			deadline, _ := time.Parse(time.RFC3339Nano, trial.Deadline)
			passed := trial.FailReason == "" && (trial.Deadline == "" || opts.Now().Before(deadline))
			if opts.Daemon || !passed {
				if !opts.Daemon {
					// Never under a live daemon: the trial build may be the
					// process running (stuck, or late after a suspend), and a
					// revert would move its files away under it.
					alive, release := daemonAlive(paths)
					if alive {
						check.Notes = append(check.Notes, fmt.Sprintf("update %s did not pass its trial, and the daemon running it is still alive: the next daemon start rolls it back; running the installed build", trial.UpdateID))
						return check
					}
					defer release()
				}
				reason := trial.FailReason
				switch {
				case reason != "":
				case !passed:
					reason = "it did not answer ready by its deadline " + trial.Deadline
				default:
					reason = "its trial start ended before it answered ready"
				}
				restored, err := revertTrial(paths, reason, storeRoot())
				if err != nil {
					check.Notes = append(check.Notes, fmt.Sprintf("update %s failed its trial (%s) and could NOT be rolled back: %v; it keeps running until an operator acts (the node raises update_failed)", trial.UpdateID, reason, err))
				} else {
					check.Notes = append(check.Notes, fmt.Sprintf("update %s failed its trial (%s); rolled back to %s and quarantined", trial.UpdateID, reason, restored))
				}
				continue
			}
		}
		if _, err := os.Stat(filepath.Join(paths.Root, manifestFileName)); err != nil {
			return check // the home bundle holds no build: the installed one runs
		}
		var build *HomeBuild
		if opts.Daemon || !verdictHolds(paths) {
			build, err = verifyAndRecord(paths)
		} else {
			build, err = homeBuildFromState(paths)
		}
		if err != nil {
			if opts.Daemon && trial != nil {
				reason := "the installed build refused it: " + err.Error()
				restored, rbErr := revertTrial(paths, reason, storeRoot())
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
		if opts.Daemon && trial != nil {
			timeout := 600 * time.Second
			if opts.HealthTimeout != nil {
				if d := opts.HealthTimeout(); d > 0 {
					timeout = d
				}
			}
			trial.Starts++
			trial.Deadline = opts.Now().Add(timeout).UTC().Format(time.RFC3339Nano)
			if err := writeTrialLocked(paths, trial); err != nil {
				check.Notes = append(check.Notes, fmt.Sprintf("cannot record the trial start of %s (%v); running the installed build", trial.UpdateID, err))
				return check
			}
			build.Trial = trial.UpdateID
			build.Deadline = trial.Deadline
		}
		check.Build = build
		return check
	}
	return check
}

// homeAnchor is the top of a home bundle's ownership chain: the account's home
// folder when the bundle is under it, else the storage folder the bundle
// falls back to (<storage.path>/update-bundle/<key>).
func homeAnchor(root string) string {
	if home, err := accountHome(); err == nil {
		resolvedHome := home
		if r, err := filepath.EvalSymlinks(home); err == nil {
			resolvedHome = r
		}
		resolvedRoot := root
		if r, err := filepath.EvalSymlinks(root); err == nil {
			resolvedRoot = r
		}
		if strings.HasPrefix(resolvedRoot, resolvedHome+string(filepath.Separator)) {
			return resolvedHome
		}
	}
	return filepath.Dir(filepath.Dir(root))
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
