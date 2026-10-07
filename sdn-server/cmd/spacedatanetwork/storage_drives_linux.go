//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var linuxDriveTypes = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true, "bcachefs": true, "jfs": true, "nfs": true, "nfs4": true, "cifs": true, "smb3": true}

type linuxMount struct{ mount, fstype, options string }

func linuxMounts() []linuxMount {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var out []linuxMount
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		out = append(out, linuxMount{mount: unescape.Replace(fields[1]), fstype: fields[2], options: fields[3]})
	}
	return out
}

// listDrives is every writable disk filesystem this node could keep its data
// on (not /proc, /sys, /dev, /run, /boot, snaps or container layers).
func listDrives() []storageDrive {
	seen := map[string]bool{}
	var out []storageDrive
	for _, m := range linuxMounts() {
		if !linuxDriveTypes[m.fstype] || seen[m.mount] || skipLinuxMount(m.mount) {
			continue
		}
		readOnly := false
		for _, option := range strings.Split(m.options, ",") {
			readOnly = readOnly || option == "ro"
		}
		total, free, ok := statDrive(m.mount)
		if readOnly || !ok || total <= 0 {
			continue
		}
		seen[m.mount] = true
		name := filepath.Base(m.mount)
		if m.mount == "/" {
			name = "System drive"
		}
		out = append(out, storageDrive{Mount: m.mount, Name: name, TotalBytes: total, FreeBytes: free, Writable: true})
	}
	return out
}

func skipLinuxMount(mount string) bool {
	for _, prefix := range []string{"/proc", "/sys", "/dev", "/run", "/boot", "/snap", "/var/snap", "/var/lib/docker", "/var/lib/containers"} {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return true
		}
	}
	return false
}

// mountOf is the mount point of the filesystem holding path (or its nearest
// existing parent): the longest mount that contains it.
func mountOf(path string) string {
	target := existingParent(path)
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	}
	best := ""
	for _, m := range linuxMounts() {
		if (m.mount == "/" || target == m.mount || strings.HasPrefix(target, m.mount+"/")) && len(m.mount) > len(best) {
			best = m.mount
		}
	}
	return best
}

// statDrive is the size and free space of the filesystem holding path.
func statDrive(path string) (total, free int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(existingParent(path), &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), true
}
