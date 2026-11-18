//go:build sdn_e2e_crash_mid_apply

package update

import (
	"os"
	"syscall"
)

// A build that is killed in the middle of a home install, after the state
// write and before trial.json. Only the update lane's real-daemon E2E builds
// it. No release build carries this tag.
func init() {
	crashAfterHomeStateWrite = func() {
		log.Errorf("update: this build is killed after the state write of a home install (built with the sdn_e2e_crash_mid_apply tag)")
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
}
