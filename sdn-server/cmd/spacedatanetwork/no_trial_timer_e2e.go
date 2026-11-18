//go:build sdn_e2e_no_trial_timer

package main

// A build whose trial deadline timer never starts. Only the update lane's
// real-daemon E2E builds it (with sdn_e2e_hang_before_store), to prove that a
// later start of the installed path rolls back a trial whose deadline has
// passed when the trial build could not end itself. No release build carries
// this tag.
func init() {
	trialTimerDisabled = true
}
