//go:build windows

package config

import "golang.org/x/sys/windows"

// diskTotalBytes returns the total capacity of the filesystem holding
// path. golang.org/x/sys is already a go.mod dependency (used by
// internal/ingest/disk_windows.go for the analogous available-space
// query); this adds no new dependency.
func diskTotalBytes(path string) (uint64, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var totalBytes uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, nil, &totalBytes, nil); err != nil {
		return 0, err
	}
	return totalBytes, nil
}
