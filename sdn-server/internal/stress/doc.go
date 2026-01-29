// Package stress provides isolated stress tests for high-volume FlatBuffer
// operations including generation, pinning, transfer, and streaming.
//
// These tests are isolated from the normal test suite using build tags.
// To run stress tests:
//
//	go test -v -tags=stress -timeout=4h ./internal/stress/...
//
// Normal test runs will NOT include these tests:
//
//	go test ./...  # stress tests excluded
//
// Environment variables:
//   - STRESS_TARGET_SIZE: Target data size in bytes (default: 10GB)
//   - STRESS_NODE1_ADDR: First node multiaddr for transfer tests
//   - STRESS_NODE2_ADDR: Second node multiaddr for transfer tests
package stress
