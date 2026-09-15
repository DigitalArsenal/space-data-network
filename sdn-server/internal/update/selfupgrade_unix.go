//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// A new session detaches the helper from the daemon's controlling terminal and
// process group. On a box with no supervisor that is sufficient: there is no
// cgroup teardown to survive.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
