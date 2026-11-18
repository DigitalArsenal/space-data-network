package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// trialUpdateID is the update this daemon runs as a trial, if any.
var trialUpdateID string

// homeRestart is the restart through the installed path that replaces this
// process once the daemon has stopped for an update of its home bundle.
type homeRestart struct {
	argv     []string
	homeRoot string
}

var execAfterDaemon *homeRestart

// daemonServing is set once the daemon waits for its stop signal: from then
// on a stop for an update goes through the ordinary shutdown.
var daemonServing atomic.Bool

// beforeExec stops the child processes this daemon owns before a forced exec
// through the installed path (the ordinary shutdown stops them itself). The
// daemon's one child process is tor (internal/tor); Kubo and the modules run
// inside the daemon.
var (
	beforeExecMu sync.Mutex
	beforeExec   []func()
)

func registerBeforeExec(f func()) {
	beforeExecMu.Lock()
	beforeExec = append(beforeExec, f)
	beforeExecMu.Unlock()
}

func runBeforeExec() {
	beforeExecMu.Lock()
	fns := append([]func(){}, beforeExec...)
	beforeExecMu.Unlock()
	for _, f := range fns {
		func() {
			defer func() { _ = recover() }()
			f()
		}()
	}
}

// trialTimerDisabled is set only by the sdn_e2e_no_trial_timer test build.
var trialTimerDisabled bool

// heldDaemonLock keeps this daemon's lock on its home bundle for its life.
var heldDaemonLock any

// runHomeBuildIfVerified runs, in place of this process, the build an update
// put in this account's home bundle (internal/update/home.go), when this
// process was started from an installed bundle the account cannot write and
// that build verifies against the installed build's update roots. Every
// command goes through it, so a command typed by hand runs the build the
// daemon runs, and a supervisor that restarts the installed path gets the
// updated build. A refused build is named on stderr and this build runs.
func runHomeBuildIfVerified() {
	update.HomeStorageFallback = storagePathFromArgs
	daemon := isDaemonCommand(os.Args[1:])
	// The daemon lock the process this one replaced held, if any.
	update.AdoptDaemonLock(daemon)
	// A trial is named only by the start check below, for one process: the
	// trial build reads it here and no child inherits it.
	trialUpdateID = strings.TrimSpace(os.Getenv(update.TrialEnv))
	_ = os.Unsetenv(update.TrialEnv)
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if root := update.HomeBuildRoot(exe); root != "" {
		if os.Geteuid() == 0 {
			fmt.Fprintf(os.Stderr, "update: refusing to run as root from the home bundle %s: root never runs a build from a home folder; run the installed program instead\n", root)
			os.Exit(1)
		}
		update.HoldHomeInUse(root)
		if trialUpdateID != "" {
			paths := update.PathsFor(root)
			watchTrialSignals(paths, trialUpdateID)
			if !trialTimerDisabled {
				startTrialTimer(paths, trialUpdateID)
			}
			fmt.Fprintf(os.Stderr, "update: this process runs update %s as a trial\n", trialUpdateID)
		}
		return
	}
	check := update.ChooseHomeBuild(exe, update.StartCheckOptions{
		Daemon:        daemon,
		StoreRoot:     storagePathFromArgs,
		HealthTimeout: healthTimeoutFromArgs,
	})
	for _, note := range check.Notes {
		fmt.Fprintf(os.Stderr, "update: %s\n", note)
	}
	if check.Refuse != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", check.Refuse)
		os.Exit(1)
	}
	build := check.Build
	if build == nil {
		return
	}
	env := update.DaemonLockForExec(update.HomeBuildEnv(os.Environ(), build.Installed))
	if build.Trial != "" {
		env = append(env, update.TrialEnv+"="+build.Trial)
		fmt.Fprintf(os.Stderr, "update: starting %s (update %s) as a trial: it must answer ready by %s\n", build.Version, build.Trial, build.Deadline)
	}
	if err := execHomeBuild(build.Executable, os.Args, env); err != nil {
		fmt.Fprintf(os.Stderr, "update: cannot start the build under %s: %v; running the installed build\n", build.Root, err)
	}
}

// startTrialTimer enforces a trial's deadline from the first instruction of
// the trial build: a build that never answers ready, wherever it is stuck,
// is stopped at the deadline and replaced by the installed path, which rolls
// it back. Once the daemon serves, the stop is the ordinary shutdown; if that
// has not replaced the process within the shutdown watchdog's bound, or the
// daemon never got that far, the timer stops the daemon's child processes and
// replaces the process itself.
func startTrialTimer(paths update.Paths, id string) {
	deadline, err := update.TrialDeadline(paths, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: the trial of %s has no deadline (%v); the installed path rolls it back at its next start\n", id, err)
		return
	}
	go func() {
		time.Sleep(time.Until(deadline))
		if trialInterrupted.Load() {
			return // stopped by a signal: the next start runs the trial again
		}
		trial, err := update.ReadTrial(paths)
		if err == nil && (trial == nil || trial.UpdateID != id) {
			return // passed, or already rolled back
		}
		reason := "it did not answer ready by its deadline " + deadline.UTC().Format(time.RFC3339)
		if err == nil && trial.FailReason == "" {
			_ = update.FailTrial(paths, id, reason)
		}
		fmt.Fprintf(os.Stderr, "update: update %s failed its trial (%s); stopping, and the installed path rolls it back\n", id, reason)
		if daemonServing.Load() && update.RequestRestartForUpdate() {
			time.Sleep(shutdownWatchdogTimeout + 15*time.Second)
		}
		runBeforeExec()
		restartThroughInstalledPath(&homeRestart{argv: paths.RestartArgv(os.Args), homeRoot: paths.Root})
	}()
}

// trialInterrupted is set once a stop signal reached the trial build.
var trialInterrupted atomic.Bool

// watchTrialSignals makes a stop signal (a service restart, a reboot) during
// a trial run the trial again at the next start, with a new deadline, rather
// than count as a failed trial (update.InterruptTrial; after
// update.MaxTrialInterruptions such stops the next one fails the trial). It
// watches from the first instruction of the trial build: before the daemon
// handles signals itself, the process then ends as the signal would have
// ended it; after, the daemon's ordinary shutdown runs.
func watchTrialSignals(paths update.Paths, id string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-ch
		trialInterrupted.Store(true)
		rerun, err := update.InterruptTrial(paths, id)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "update: stopped by %v during the trial of %s, and the stop could not be recorded (%v): the next start rolls it back\n", sig, id, err)
		case rerun:
			fmt.Fprintf(os.Stderr, "update: stopped by %v during the trial of %s: the next start runs its trial again\n", sig, id)
		default:
			fmt.Fprintf(os.Stderr, "update: stopped by %v during the trial of %s, which is over or was stopped %d times already: the next start rolls it back unless it passed\n", sig, id, update.MaxTrialInterruptions)
		}
		if daemonServing.Load() {
			signal.Stop(ch)
			return
		}
		runBeforeExec()
		signal.Reset(sig)
		reraise(sig)
		time.Sleep(5 * time.Second)
		os.Exit(1)
	}()
}

// healthTimeoutFromArgs is update.health_timeout_seconds of the config this
// command names. Only a daemon start that starts a trial reads it.
func healthTimeoutFromArgs() time.Duration {
	cfg, _, err := config.LoadResolved(configPathFromArgs())
	if err != nil || cfg == nil {
		return config.Default().Update.HealthTimeout()
	}
	return cfg.Update.HealthTimeout()
}

// isDaemonCommand reports whether args run the daemon: its first word that is
// not a flag (or a flag's value) is "daemon".
func isDaemonCommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return false
		case arg == "-c" || arg == "--config":
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			return arg == "daemon"
		}
	}
	return false
}

// configPathFromArgs is the -c / --config this command names, read before
// cobra parses flags.
func configPathFromArgs() string {
	path := ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				path = args[i+1]
			}
		case strings.HasPrefix(arg, "--config="):
			path = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c="):
			path = strings.TrimPrefix(arg, "-c=")
		}
	}
	return path
}

// storagePathFromArgs is storage.path of the config this command names. It is
// read only when it is needed: the home bundle's fallback when the account has
// no usable home folder, and the store-format guard of a revert.
func storagePathFromArgs() string {
	cfg, _, err := config.LoadResolved(configPathFromArgs())
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Storage.Path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(cfg.Storage.Path); err == nil {
		return abs
	}
	return cfg.Storage.Path
}

// watchTrial is the trial build's health gate (internal/update/trial.go): it
// must answer ready by the trial's deadline, set by the start check before
// this build started. If it does, the trial passes; if not, it records why and
// stops, and the installed path it restarts through rolls it back.
// startTrialTimer enforces the same deadline when this never runs.
func watchTrial(ctx context.Context, paths update.Paths, id string, readiness func(context.Context) string) {
	deadline, err := update.TrialDeadline(paths, id)
	if err != nil {
		log.Errorf("Update %s: the trial has no deadline (%v); the installed path rolls it back at its next start", id, err)
		return
	}
	if readiness == nil {
		log.Errorf("UPDATE %s PASSES ITS TRIAL WITHOUT A HEALTH CHECK: the admin listener is off, so readiness cannot be read. Turn admin.enabled on for a node that updates itself.", id)
		if err := update.PassTrial(paths, id); err != nil {
			log.Errorf("Update %s: record the trial: %v", id, err)
		}
		return
	}
	log.Infof("Update %s runs as a trial: it must answer ready by %s", id, deadline.UTC().Format(time.RFC3339))
	last := ""
	for {
		probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout+time.Second)
		last = readiness(probeCtx)
		cancel()
		if last == "" {
			if err := update.PassTrial(paths, id); err != nil {
				log.Errorf("Update %s answered ready but its trial could not be recorded: %v", id, err)
				return
			}
			log.Infof("Update %s passed its trial: ready, health-passed recorded", id)
			return
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	reason := fmt.Sprintf("not ready by its deadline %s: %s", deadline.UTC().Format(time.RFC3339), last)
	log.Errorf("Update %s failed its trial (%s); stopping, and the installed path rolls it back", id, reason)
	if err := update.FailTrial(paths, id, reason); err != nil {
		log.Errorf("Update %s: record the failed trial: %v", id, err)
	}
	update.RequestRestartForUpdate()
}

// restartThroughInstalledPath replaces this process with the installed
// launcher and the daemon's own arguments, in the environment the installed
// launcher had: entries pointing into the home bundle are removed and those
// the installed start check set aside are given back. The start check there
// runs the build just installed as a trial, or rolls back a failed one.
func restartThroughInstalledPath(r *homeRestart) {
	fmt.Fprintf(os.Stderr, "update: the daemon stopped for an update; starting it again through %s\n", r.argv[0])
	if err := execHomeBuild(r.argv[0], r.argv, update.DaemonLockForExec(update.InstalledEnv(os.Environ(), r.homeRoot))); err != nil {
		fmt.Fprintf(os.Stderr, "update: cannot start %s: %v\n", r.argv[0], err)
		os.Exit(1)
	}
}
