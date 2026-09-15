package caps

import "golang.org/x/sys/windows"

// GetDiskFreeSpaceEx is the windows equivalent of statfs(2) for what this
// reports. It answers in BYTES, so there is no block size to multiply by, and
// it distinguishes free-on-volume from available-to-this-user exactly as
// statfs's Bfree and Bavail do.
func StatDisk(path string) (DiskStat, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskStat{}, err
	}
	var availableToCaller, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &availableToCaller, &total, &free); err != nil {
		return DiskStat{}, err
	}
	return DiskStat{
		CapacityBytes:  total,
		FreeBytes:      free,
		AvailableBytes: availableToCaller,
	}, nil
}
