//go:build windows

package storage

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive takes a non-blocking exclusive LockFileEx range lock on
// f. Like flock, the lock is kernel-owned and released automatically when
// the holding process terminates for any reason.
func lockFileExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errors.New("lock held")
	}
	return fmt.Errorf("LockFileEx: %w", err)
}

// unlockFile releases the LockFileEx lock.
func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
