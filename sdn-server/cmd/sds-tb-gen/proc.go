package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// rssBytes: the resident set now on Linux (/proc/self/status), the peak
// elsewhere (getrusage).
func rssBytes() int64 {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/self/status")
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "VmRSS:") {
					f := strings.Fields(line)
					if len(f) >= 2 {
						kb, _ := strconv.ParseInt(f[1], 10, 64)
						return kb * 1024
					}
				}
			}
		}
		return 0
	}
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes on darwin
	}
	return int64(ru.Maxrss) * 1024
}

// openFDs counts the process's file descriptors.
func openFDs() int {
	dir := "/dev/fd"
	if runtime.GOOS == "linux" {
		dir = "/proc/self/fd"
	}
	e, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(e)
}

func loadLine() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			return "load " + strings.Join(f[:3], " ")
		}
	}
	return "load n/a"
}
