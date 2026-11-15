//go:build unix

package config

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// pathOwnerUID returns the uid owning path. For a procfs process directory
// that is the process's effective uid.
func pathOwnerUID(path string) (int, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// loginDefsPath is read for UID_MIN on Linux. A variable ONLY so tests can
// point it at a fixture.
var loginDefsPath = "/etc/login.defs"

// firstRegularUID is the lowest uid of a regular (login) account: below it
// are root and the system accounts that services run as. Linux takes UID_MIN
// from /etc/login.defs (default 1000); macOS gives regular users 501 and up.
func firstRegularUID() int {
	if runtime.GOOS == "darwin" {
		return 500
	}
	if f, err := os.Open(loginDefsPath); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) == 2 && fields[0] == "UID_MIN" {
				if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 1000
}
