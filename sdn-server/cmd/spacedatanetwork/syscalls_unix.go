//go:build !windows

package main

import (
	"errors"
	"runtime"
	"syscall"
)

// The three Unix-only system calls the CLI makes, kept out of the Windows
// build (syscalls_windows.go). The bodies are the ones import_gp_archive.go,
// store_migrate.go and store_migrate_format4.go carried, unchanged.

// gpArchiveProcessAlive reports whether pid names a live process (signal 0).
func gpArchiveProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// freeBytes is the space available to an unprivileged writer on dir's
// filesystem, -1 when unknown.
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// maxRSSBytes is this process's peak resident set size, -1 when unknown.
func maxRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes
	}
	return int64(ru.Maxrss) * 1024 // KiB
}
