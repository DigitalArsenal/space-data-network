//go:build !windows

package update

import "golang.org/x/sys/unix"

// dirWritable reports whether this process may create and rename entries in
// dir: its permissions, its owner and a read-only mount all count.
func dirWritable(dir string) bool {
	return unix.Access(dir, unix.W_OK|unix.X_OK) == nil
}
