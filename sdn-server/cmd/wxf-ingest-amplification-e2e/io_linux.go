//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
)

func diskWritten() uint64 {
	b, err := os.ReadFile("/proc/self/io")
	check(err)
	for _, line := range strings.Split(string(b), "\n") {
		var n uint64
		if _, err := fmt.Sscanf(line, "write_bytes: %d", &n); err == nil {
			return n
		}
	}
	panic("write_bytes unavailable")
}
