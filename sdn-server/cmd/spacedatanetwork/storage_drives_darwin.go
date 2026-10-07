//go:build darwin

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var darwinDriveTypes = map[string]bool{"apfs": true, "hfs": true, "exfat": true, "msdos": true, "ntfs": true, "smbfs": true, "afpfs": true, "nfs": true}

// listDrives is every writable volume this node could keep its data on.
func listDrives() []storageDrive {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return nil
	}
	buf := make([]unix.Statfs_t, n)
	if n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT); err != nil {
		return nil
	}
	var out []storageDrive
	for _, st := range buf[:n] {
		mount := unix.ByteSliceToString(st.Mntonname[:])
		if !darwinDriveTypes[unix.ByteSliceToString(st.Fstypename[:])] || st.Flags&unix.MNT_RDONLY != 0 {
			continue
		}
		// The system's own volumes (VM, Preboot, Update, ...) are not places
		// to keep data; the Data volume is where /Users lives.
		if strings.HasPrefix(mount, "/System/Volumes/") && mount != "/System/Volumes/Data" {
			continue
		}
		if mount == "/private/var/vm" || strings.HasPrefix(mount, "/Library/Developer/") {
			continue
		}
		out = append(out, storageDrive{Mount: mount, Name: darwinDriveName(mount), TotalBytes: int64(st.Blocks) * int64(st.Bsize), FreeBytes: int64(st.Bavail) * int64(st.Bsize), Writable: true})
	}
	return out
}

func darwinDriveName(mount string) string {
	if mount == "/System/Volumes/Data" || mount == "/" {
		return "Macintosh HD"
	}
	return filepath.Base(mount)
}

// mountOf is the mount point of the volume holding path (or its nearest
// existing parent). Statfs names it directly, firmlinks included.
func mountOf(path string) string {
	var st unix.Statfs_t
	if err := unix.Statfs(existingParent(path), &st); err != nil {
		return ""
	}
	return unix.ByteSliceToString(st.Mntonname[:])
}

// statDrive is the size and free space of the volume holding path.
func statDrive(path string) (total, free int64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(existingParent(path), &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), true
}
