//go:build sdn_e2e_clock_back

package node

import (
	"sync/atomic"
	"time"
)

// A build whose update lane's wall clock moves back 30 minutes when its first
// rollout wait starts, as a clock corrected backward during the wait would.
// Only the update lane's real-daemon E2E builds it. No release build carries
// this tag.
func init() {
	var back atomic.Int64
	updateSignalClock = func() time.Time { return time.Now().Add(-time.Duration(back.Load())) }
	rolloutWaitStarted = func() {
		if back.CompareAndSwap(0, int64(30*time.Minute)) {
			log.Errorf("update: this build's update lane clock moved back 30 minutes as its rollout wait started (built with the sdn_e2e_clock_back tag)")
		}
	}
}
