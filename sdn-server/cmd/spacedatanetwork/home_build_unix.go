//go:build !windows

package main

import "syscall"

// execHomeBuild replaces this process with the home build: the same process
// id, so a supervisor watching it sees one process.
func execHomeBuild(executable string, argv []string, env []string) error {
	args := append([]string{executable}, argv[1:]...)
	return syscall.Exec(executable, args, env)
}
