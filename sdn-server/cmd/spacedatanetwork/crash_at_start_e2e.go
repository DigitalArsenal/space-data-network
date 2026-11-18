//go:build sdn_e2e_crash_at_start

package main

// A build whose daemon exits with an error at start, before it opens its store
// or answers anything. Only the update lane's real-daemon E2E builds it, to
// prove that a trial build that crashes at start is rolled back. No release
// build carries this tag.
func init() {
	crashAtStart = true
}
