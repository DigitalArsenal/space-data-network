package node

import "time"

// updateSignalClock is the wall clock of the update signal lane: time.Now in
// every shipped build. Only a build with the sdn_e2e_clock_back tag replaces
// it (update_clock_back_e2e.go), with rolloutWaitStarted, so the real-daemon
// E2E can move this lane's wall clock back while a rollout wait runs.
var updateSignalClock = time.Now

// rolloutWaitStarted is nil in every shipped build; see updateSignalClock.
var rolloutWaitStarted func()
