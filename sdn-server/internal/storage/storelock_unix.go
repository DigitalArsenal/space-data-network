//go:build !windows

package storage

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive takes a non-blocking exclusive flock on f. flock locks
// belong to the open file description, so they conflict across processes
// AND across independent opens within one process, and the kernel releases
// them automatically when the holder dies (including SIGKILL).
func lockFileExclusive(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errors.New("lock held")
	}
	return fmt.Errorf("flock: %w", err)
}

// unlockFile releases the flock (also released implicitly on close/exit).
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
