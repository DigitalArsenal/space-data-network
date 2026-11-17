//go:build !windows

package storage

import "golang.org/x/sys/unix"

// refreshFileTimeNow sets path's access and modification times to now:
// utimensat with no times (the same as UTIME_NOW for both), which needs
// only write access to the file, not its ownership.
func refreshFileTimeNow(path string) error {
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, nil, 0)
}
