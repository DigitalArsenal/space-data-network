//go:build !windows

package config

import "syscall"

// diskTotalBytes returns the total capacity of the filesystem holding
// path, via statfs(2). This is a narrow, intentional duplicate of
// internal/ingest/disk_unix.go's availableDiskBytes (that package is out
// of scope for Task D3, and this needs TOTAL capacity — stat.Blocks — not
// available free space — stat.Bavail — since storage.max_size resolves a
// percentage of disk SIZE, not of free space).
func diskTotalBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Blocks) * uint64(stat.Bsize), nil
}
