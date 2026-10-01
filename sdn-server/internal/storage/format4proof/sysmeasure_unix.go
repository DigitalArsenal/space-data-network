//go:build unix

package format4proof

import (
	"io/fs"
	"os"
	"runtime"
	"syscall"
)

// MaxRSSMB is the process's peak resident set, in MiB.
func MaxRSSMB() float64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return maxrssMB(int64(ru.Maxrss))
}

// maxrssMB converts a rusage ru_maxrss (bytes on darwin, KiB elsewhere) to MiB.
func maxrssMB(v int64) float64 {
	if runtime.GOOS == "darwin" {
		return float64(v) / (1 << 20)
	}
	return float64(v) / 1024
}

// ChildMaxRSSMB is a finished child process's peak resident set, in MiB.
func ChildMaxRSSMB(ps *os.ProcessState) float64 {
	if ps == nil {
		return 0
	}
	if ru, ok := ps.SysUsage().(*syscall.Rusage); ok && ru != nil {
		return maxrssMB(int64(ru.Maxrss))
	}
	return 0
}

// allocatedBytes is the space a file occupies (allocated blocks × 512).
func allocatedBytes(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}
