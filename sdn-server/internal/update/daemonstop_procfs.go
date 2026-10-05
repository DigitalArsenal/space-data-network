//go:build unix && !darwin

package update

import (
	"bytes"
	"os"
	"strconv"
)

// processIsZombie reads the process's state from /proc/<pid>/stat: the field
// after the command name, which is parenthesised and may itself contain spaces
// and parentheses, so the state follows the LAST ')'. Without procfs nothing
// can be said, and the process counts as running.
func processIsZombie(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 || i+2 >= len(raw) {
		return false
	}
	return raw[i+2] == 'Z'
}
