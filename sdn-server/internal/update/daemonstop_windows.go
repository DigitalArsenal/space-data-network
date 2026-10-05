//go:build windows

package update

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// processRunning reports whether pid is a live process: it can be opened and
// waiting on it times out. A process that exists but denies the open is
// running.
func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	event, err := windows.WaitForSingleObject(h, 0)
	return err == nil && event == uint32(windows.WAIT_TIMEOUT)
}

// terminateProcess is the hard stop on Windows, which has no SIGTERM for
// another process: the graceful stop there is the update shutdown handshake.
func terminateProcess(pid int) error {
	return killProcess(pid)
}

// killProcess stops pid outright.
func killProcess(pid int) error {
	if pid <= 0 {
		return errNoProcess
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
