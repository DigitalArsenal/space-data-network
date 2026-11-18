//go:build !windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// fileLock is an advisory lock on one file of a home bundle's updates/.
type fileLock struct{ f *os.File }

// lockFile takes the lock named name in dir: shared or exclusive, never
// waiting. ok is false when another process holds a conflicting lock.
func lockFile(dir, name string, exclusive bool) (lock *fileLock, ok bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{f: f}, true, nil
}

// Release drops the lock. Safe on nil.
func (l *fileLock) Release() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}
