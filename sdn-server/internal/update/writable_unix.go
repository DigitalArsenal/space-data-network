//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// dirWritable reports whether this process may create and rename entries in
// dir: its permissions, its owner and a read-only mount all count.
func dirWritable(dir string) bool {
	return unix.Access(dir, unix.W_OK|unix.X_OK) == nil
}

// ownedNotShared requires path to be owned by this process's effective user
// and not writable by its group or by others.
func ownedNotShared(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", path)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is owned by uid %d, not by this account (uid %d)", path, st.Uid, os.Geteuid())
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by its group or by others (mode %04o)", path, info.Mode().Perm())
	}
	return nil
}
