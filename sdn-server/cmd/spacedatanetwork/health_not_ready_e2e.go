//go:build sdn_e2e_not_ready

package main

// A build that boots, serves, reports its identity — and is never ready: its
// /api/v1/ready answers 503 for good. Only the update lane's real-daemon E2E
// (update_real_daemon_e2e_test.go) builds it, with -tags sdn_e2e_not_ready, to
// prove that the helper reverts a build that fails the readiness gate and
// quarantines it. No release build carries this tag.
func init() {
	readinessOverride = "built with the sdn_e2e_not_ready tag, which is never ready"
}
