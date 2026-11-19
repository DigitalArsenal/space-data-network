//go:build sdn_e2e_crash_mid_pass

package update

import (
	"os"
	"syscall"
)

// A build that is killed in the middle of passing its own trial, after the
// state write and before trial.json is removed. Only the update lane's
// real-daemon E2E builds it. No release build carries this tag.
func init() {
	crashAfterPassStateWrite = func() {
		log.Errorf("update: this build is killed after the state write of its trial pass (built with the sdn_e2e_crash_mid_pass tag)")
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
}
