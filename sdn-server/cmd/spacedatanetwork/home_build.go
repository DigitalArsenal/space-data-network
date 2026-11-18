package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// runHomeBuildIfVerified runs, in place of this process, the build an update
// put in this account's home bundle (internal/update/home.go), when this
// process was started from an installed bundle the account cannot write and
// that build verifies against the installed build's update roots. Every
// command goes through it, so a command typed by hand runs the build the
// daemon runs, and a supervisor that restarts the installed path gets the
// updated build. A refused build is named on stderr and this build runs.
func runHomeBuildIfVerified() {
	update.HomeStorageFallback = storagePathFromArgs
	exe, err := os.Executable()
	if err != nil {
		return
	}
	build, err := update.ChooseHomeBuild(exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: %v; running the installed build\n", err)
		return
	}
	if build == nil {
		return
	}
	if err := execHomeBuild(build.Executable, os.Args, update.HomeBuildEnv(os.Environ(), build.Installed)); err != nil {
		fmt.Fprintf(os.Stderr, "update: cannot start the build under %s: %v; running the installed build\n", build.Root, err)
	}
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
