//go:build windows

package update

// fileLock is a no-op on Windows, which has no home bundle.
type fileLock struct{}

func lockFile(string, string, bool) (*fileLock, bool, error) { return &fileLock{}, true, nil }

// Release drops the lock.
func (l *fileLock) Release() {}
