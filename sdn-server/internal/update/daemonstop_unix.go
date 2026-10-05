//go:build unix

package update

import (
	"errors"
	"syscall"
)

// processRunning reports whether pid is a live process: kill(pid, 0) finds it
// (EPERM still means it exists) and it is not a zombie.
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	return !processIsZombie(pid)
}

// terminateProcess asks pid to stop: SIGTERM, which the daemon handles exactly
// like the update shutdown handshake.
func terminateProcess(pid int) error {
	if pid <= 0 {
		return errNoProcess
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// killProcess stops pid outright.
func killProcess(pid int) error {
	if pid <= 0 {
		return errNoProcess
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
