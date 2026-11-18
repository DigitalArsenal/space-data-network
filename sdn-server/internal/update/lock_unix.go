//go:build !windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// fileLock is an advisory lock on one file of a home bundle's updates/.
type fileLock struct {
	f    *os.File
	path string
}

func openLockFile(dir, name string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
}

// lockFile takes the lock named name in dir: shared or exclusive, never
// waiting. ok is false when another process holds a conflicting lock.
func lockFile(dir, name string, exclusive bool) (lock *fileLock, ok bool, err error) {
	f, err := openLockFile(dir, name)
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
	return &fileLock{f: f, path: filepath.Join(dir, name)}, true, nil
}

// lockFileWait takes the exclusive lock named name in dir, waiting for it.
func lockFileWait(dir, name string) (*fileLock, error) {
	f, err := openLockFile(dir, name)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err == nil {
			return &fileLock{f: f, path: filepath.Join(dir, name)}, nil
		}
		if !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
	}
}

// Release drops the lock. Safe on nil.
func (l *fileLock) Release() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// sameFile reports whether the lock's open file is the file at path now.
func (l *fileLock) sameFile(path string) bool {
	if l == nil || l.f == nil {
		return false
	}
	held, err := l.f.Stat()
	if err != nil {
		return false
	}
	now, err := os.Stat(path)
	return err == nil && os.SameFile(held, now)
}

// DaemonLockEnv passes the daemon lock of a home bundle across an exec: its
// value is the number of the open file that holds the lock. The lock is an
// flock on that open file, so the process that execs and the process it
// becomes hold it with no moment between them where another daemon can take
// it.
const DaemonLockEnv = "SDN_UPDATE_DAEMON_LOCK_FD"

// heldDaemon is the daemon lock this process holds: taken by its start check
// or by the daemon, or adopted from the process it replaced.
var heldDaemon *fileLock

// AdoptDaemonLock takes over the daemon lock a process passed across its exec
// (DaemonLockEnv), and removes the variable so no child inherits it. Only a
// daemon keeps it; any other command closes it.
func AdoptDaemonLock(daemon bool) {
	value, ok := os.LookupEnv(DaemonLockEnv)
	if !ok {
		return
	}
	_ = os.Unsetenv(DaemonLockEnv)
	fd, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || fd < 3 {
		return
	}
	f := os.NewFile(uintptr(fd), "daemon.lock")
	if f == nil {
		return
	}
	unix.CloseOnExec(fd)
	if !daemon {
		f.Close()
		return
	}
	heldDaemon = &fileLock{f: f}
}

// DaemonLockForExec makes the daemon lock this process holds survive the
// exec it is about to make and returns env with DaemonLockEnv naming it (or
// env without it when no lock is held).
func DaemonLockForExec(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, DaemonLockEnv+"=") {
			out = append(out, kv)
		}
	}
	if heldDaemon == nil || heldDaemon.f == nil {
		return out
	}
	fd := int(heldDaemon.f.Fd())
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		return out
	}
	return append(out, fmt.Sprintf("%s=%d", DaemonLockEnv, fd))
}
