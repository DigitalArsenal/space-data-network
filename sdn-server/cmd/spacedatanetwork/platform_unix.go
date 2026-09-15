//go:build !windows

package main

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// fileOwnerName resolves the owning user of dir, so a permission-denied message
// can name who actually owns the key directory. Best effort: an unknown owner
// still yields a useful error, it just says "the service user".
func fileOwnerName(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10)); err == nil {
		return u.Username
	}
	return "uid " + strconv.FormatUint(uint64(stat.Uid), 10)
}

// killSelf is the shutdown watchdog's last resort. SIGKILL cannot be caught, so
// the process goes down even if a goroutine is wedged.
func killSelf() {
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
}
