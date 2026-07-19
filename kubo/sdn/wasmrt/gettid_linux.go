//go:build linux

package wasmrt

import "golang.org/x/sys/unix"

// osThreadID returns the calling OS thread's kernel thread id (Linux gettid).
// Used only for observability/proof: it lets the wasi-threads host record the
// DISTINCT kernel threads its spawned workers ran on, so a test (or benchmark)
// can prove real OS-level parallelism rather than cooperative interleaving.
func osThreadID() int64 { return int64(unix.Gettid()) }
