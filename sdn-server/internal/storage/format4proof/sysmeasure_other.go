//go:build !darwin

package format4proof

import (
	"os"
	"strconv"
	"strings"
)

// diskIO is the bytes this process has written to and read from storage
// (/proc/self/io write_bytes and read_bytes on Linux), 0 when unavailable.
func diskIO() (written, read uint64) {
	b, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "write_bytes":
			written = n
		case "read_bytes":
			read = n
		}
	}
	return written, read
}
