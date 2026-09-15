package update

import (
	"os/exec"
	"syscall"
)

// Windows has no sessions. CREATE_NEW_PROCESS_GROUP is the equivalent that
// matters here: it stops the helper dying with the console Ctrl event that
// kills the daemon, which is the whole point of detaching.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
