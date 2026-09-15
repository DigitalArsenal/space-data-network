package api

import "golang.org/x/sys/windows"

// filesystemCapacity reports the volume holding path. GetDiskFreeSpaceEx
// answers in BYTES, so unlike statfs(2) there is no block size to multiply by;
// it reports available-to-this-caller separately from free-on-volume, which is
// the distinction statfs draws between Bavail and Bfree.
func filesystemCapacity(path string) (availableBytes, capacityBytes uint64, blockSizeOK bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var availableToCaller, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &availableToCaller, &total, &free); err != nil {
		return 0, 0, false
	}
	return availableToCaller, total, true
}
