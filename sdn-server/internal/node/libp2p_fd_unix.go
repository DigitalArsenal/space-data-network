//go:build !windows && !wasm && !js

package node

import "golang.org/x/sys/unix"

// numFDs is the process's RLIMIT_NOFILE soft limit, as kubo reads it
// (core/node/libp2p/fd) to size the resource manager.
func numFDs() int {
	var l unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &l); err != nil {
		return 0
	}
	return int(l.Cur)
}
