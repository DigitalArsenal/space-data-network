//go:build windows

package main

import "os"

// Windows has none of syscall.Kill, syscall.Statfs or syscall.Getrusage, so
// the Unix bodies live in syscalls_unix.go and these take their place. Each
// answers with the value its callers already treat as "unknown", or asks the
// OS directly; none of them changes a Unix build.

// gpArchiveProcessAlive reports whether pid names a live process. On Windows
// os.FindProcess opens the process and fails when it does not exist.
func gpArchiveProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

// freeBytes is not measured on Windows: -1 is "unknown", which skips the
// free-space precheck (store_migrate.go checks free >= 0 first).
func freeBytes(string) int64 { return -1 }

// maxRSSBytes is not measured on Windows: -1 is "unknown" in the report.
func maxRSSBytes() int64 { return -1 }
