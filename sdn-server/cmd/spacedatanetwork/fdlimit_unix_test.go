//go:build !windows

package main

import (
	"bytes"
	"syscall"
	"testing"
)

// libp2p's resource manager sizes its entire connection and stream budget from
// RLIMIT_NOFILE at construction. The systemd units set it; a container, the
// desktop app and any hand-started daemon inherit whatever they are given —
// commonly 1024 — which silently caps the node far below what the operator
// asked for and surfaces only as unexplained refused inbound streams.
func TestRaiseFileDescriptorLimitLiftsALowSoftLimit(t *testing.T) {
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
		t.Skipf("cannot read RLIMIT_NOFILE: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original) })

	if original.Max < 4096 {
		t.Skipf("hard limit %d is too low to exercise a raise", original.Max)
	}

	low := original
	low.Cur = 1024
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &low); err != nil {
		t.Skipf("cannot lower RLIMIT_NOFILE for the test: %v", err)
	}

	var out bytes.Buffer
	raiseFileDescriptorLimit(&out)

	var after syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after); err != nil {
		t.Fatalf("Getrlimit after raise: %v", err)
	}
	if after.Cur <= 1024 {
		t.Fatalf("soft limit still %d after raise; libp2p would size its budget for that. output: %q", after.Cur, out.String())
	}

	// A FLOOR, NOT THE CEILING. Raising to the hard limit yields RLIM_INFINITY
	// where the OS allows it, and rcmgr would size a budget off an unbounded
	// number — the opposite of the bounded posture the relay limits hold.
	want := uint64(65536)
	if original.Max < want {
		want = original.Max
	}
	if after.Cur != want {
		t.Fatalf("soft limit = %d, want the 65536 floor capped by the hard limit (%d)", after.Cur, want)
	}
}

// A unit that already granted more — the relay unit asks for 200000 — must not
// be pulled back down to the floor.
func TestRaiseFileDescriptorLimitNeverLowers(t *testing.T) {
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
		t.Skipf("cannot read RLIMIT_NOFILE: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original) })

	if original.Max < 200000 {
		t.Skipf("hard limit %d cannot hold a value above the floor", original.Max)
	}
	high := original
	high.Cur = 200000
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &high); err != nil {
		t.Skipf("cannot raise RLIMIT_NOFILE for the test: %v", err)
	}

	var out bytes.Buffer
	raiseFileDescriptorLimit(&out)

	var after syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &after); err != nil {
		t.Fatalf("Getrlimit after: %v", err)
	}
	if after.Cur != 200000 {
		t.Fatalf("soft limit = %d, want 200000 left untouched", after.Cur)
	}
	if out.Len() != 0 {
		t.Errorf("no-op case should say nothing, said %q", out.String())
	}
}
