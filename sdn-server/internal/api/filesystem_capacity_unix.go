//go:build !windows

package api

import "golang.org/x/sys/unix"

// filesystemCapacity reports the volume holding path: bytes available to this
// caller, and the volume's total size. statfs(2) answers in BLOCKS, so both
// numbers are a block count times the block size.
func filesystemCapacity(path string) (availableBytes, capacityBytes uint64, blockSizeOK bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, false
	}
	if stat.Bsize <= 0 {
		return 0, 0, false
	}
	bsize := uint64(stat.Bsize)
	return uint64(stat.Bavail) * bsize, uint64(stat.Blocks) * bsize, true
}
