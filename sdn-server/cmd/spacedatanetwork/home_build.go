package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// trialUpdateID is the update this daemon runs as a trial, if any.
var trialUpdateID string

// execAfterDaemon is the argv this process replaces itself with once the
// daemon has stopped for an update of its home bundle (trial.go).
var execAfterDaemon []string

// runHomeBuildIfVerified runs, in place of this process, the build an update
// put in this account's home bundle (internal/update/home.go), when this
// process was started from an installed bundle the account cannot write and
// that build verifies against the installed build's update roots. Every
// command goes through it, so a command typed by hand runs the build the
// daemon runs, and a supervisor that restarts the installed path gets the
// updated build. A refused build is named on stderr and this build runs.
func runHomeBuildIfVerified() {
	update.HomeStorageFallback = storagePathFromArgs
	// A trial is named only by the start check below, for one process: the
	// trial build reads it here and no child inherits it.
	trialUpdateID = strings.TrimSpace(os.Getenv(update.TrialEnv))
	_ = os.Unsetenv(update.TrialEnv)
	exe, err := os.Executable()
	if err != nil {
		return
	}
	check := update.ChooseHomeBuild(exe, isDaemonCommand(os.Args[1:]), storagePathFromArgs())
	for _, note := range check.Notes {
		fmt.Fprintf(os.Stderr, "update: %s\n", note)
	}
	build := check.Build
	if build == nil {
		return
	}
	env := update.HomeBuildEnv(os.Environ(), build.Installed)
	if build.Trial != "" {
		env = append(env, update.TrialEnv+"="+build.Trial)
		fmt.Fprintf(os.Stderr, "update: starting %s (update %s) as a trial: it must answer ready within the health timeout\n", build.Version, build.Trial)
	}
	if err := execHomeBuild(build.Executable, os.Args, env); err != nil {
		fmt.Fprintf(os.Stderr, "update: cannot start the build under %s: %v; running the installed build\n", build.Root, err)
	}
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

// storagePathFromArgs is storage.path of the config this command names (-c /
// --config, else the usual resolution), read before cobra parses flags. The
// home bundle falls back to it when HOME is unset or cannot be written.
func storagePathFromArgs() string {
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
	cfg, _, err := config.LoadResolved(path)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Storage.Path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(cfg.Storage.Path); err == nil {
		return abs
	}
	return cfg.Storage.Path
}

// watchTrial is the trial build's health gate (internal/update/trial.go): it
// must answer ready within timeout. If it does, the trial passes; if not, it
// records why and stops, and the installed path it restarts through rolls it
// back.
func watchTrial(ctx context.Context, paths update.Paths, id string, timeout time.Duration, readiness func(context.Context) string) {
	if readiness == nil {
		log.Warnf("Update %s: the admin listener is off, so readiness cannot be read; the trial passes without a health gate", id)
		if err := update.PassTrial(paths, id); err != nil {
			log.Errorf("Update %s: record the trial: %v", id, err)
		}
		return
	}
	log.Infof("Update %s runs as a trial: it must answer ready within %s", id, timeout)
	deadline := time.Now().Add(timeout)
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
	reason := fmt.Sprintf("not ready within %s: %s", timeout, last)
	log.Errorf("Update %s failed its trial (%s); stopping, and the installed path rolls it back", id, reason)
	if err := update.FailTrial(paths, id, reason); err != nil {
		log.Errorf("Update %s: record the failed trial: %v", id, err)
	}
	update.RequestRestartForUpdate()
}

// restartThroughInstalledPath replaces this process, its daemon stopped, with
// the installed launcher and the daemon's own arguments: the start check
// there runs the build just installed, as a trial.
func restartThroughInstalledPath(argv []string) {
	fmt.Fprintf(os.Stderr, "update: the daemon stopped for an update; starting it again through %s\n", argv[0])
	if err := execHomeBuild(argv[0], argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "update: cannot start %s: %v\n", argv[0], err)
		os.Exit(1)
	}
}
