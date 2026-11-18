//go:build sdn_e2e_hang_before_store

package main

// A build that starts and then never opens its store or answers anything: it
// blocks for good before the store opens. Only the update lane's real-daemon
// E2E builds it, to prove that a trial build stuck before it can check its own
// readiness is rolled back. No release build carries this tag.
func init() {
	hangBeforeStoreOpen = true
}
