//go:build windows

package main

import "io"

// raiseFileDescriptorLimit is a no-op on Windows, which has no RLIMIT_NOFILE:
// handle counts are bounded per-process by the kernel rather than by an
// inherited soft limit a process can lift.
func raiseFileDescriptorLimit(io.Writer) {}
