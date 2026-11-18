//go:build windows

package update

// fileLock is a no-op on Windows, which has no home bundle.
type fileLock struct{}

func lockFile(string, string, bool) (*fileLock, bool, error) { return &fileLock{}, true, nil }

func lockFileWait(string, string) (*fileLock, error) { return &fileLock{}, nil }

// Release drops the lock.
func (l *fileLock) Release() {}

func (l *fileLock) sameFile(string) bool { return l != nil }

// DaemonLockEnv is not used on Windows (no exec, no home bundle).
const DaemonLockEnv = "SDN_UPDATE_DAEMON_LOCK_FD"

var heldDaemon *fileLock

// AdoptDaemonLock does nothing on Windows.
func AdoptDaemonLock(bool) {}

// DaemonLockForExec returns env unchanged on Windows.
func DaemonLockForExec(env []string) []string { return env }
