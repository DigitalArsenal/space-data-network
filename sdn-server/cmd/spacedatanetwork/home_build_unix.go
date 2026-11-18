//go:build !windows

package main

import (
	"os"
	"syscall"
)

// execHomeBuild replaces this process with the home build: the same process
// id, so a supervisor watching it sees one process.
func execHomeBuild(executable string, argv []string, env []string) error {
	args := append([]string{executable}, argv[1:]...)
	return syscall.Exec(executable, args, env)
}

// reraise sends sig to this process again, once its handler is reset, so the
// process ends as that signal ends it.
func reraise(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(os.Getpid(), s)
	}
}
