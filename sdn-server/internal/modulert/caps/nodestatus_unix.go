//go:build !windows

package caps

import "golang.org/x/sys/unix"

// A statfs(2) probe of the filesystem holding path.
func StatDisk(path string) (DiskStat, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return DiskStat{}, err
	}
	bsize := uint64(stat.Bsize)
	return DiskStat{
		CapacityBytes:  stat.Blocks * bsize,
		FreeBytes:      stat.Bfree * bsize,
		AvailableBytes: stat.Bavail * bsize,
	}, nil
}
