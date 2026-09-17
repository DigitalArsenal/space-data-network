//go:build !windows

package main

import (
	"fmt"
	"io"
	"syscall"
)

// raiseFileDescriptorLimit lifts RLIMIT_NOFILE's soft limit to its hard limit.
//
// libp2p's resource manager sizes its ENTIRE connection and stream budget from
// the inherited limit, so whatever the process starts with decides how many
// peers this node can carry. The systemd units ship LimitNOFILE (65536, and
// 200000 for the relay), which is why this was never missed there — but nothing
// else sets it. A container commonly starts at 1024, and so do the desktop app
// and any hand-started daemon, which silently caps the node far below what the
// operator asked for and shows up as unexplained refused inbound streams.
//
// Raising soft to hard needs no privilege and cannot exceed what the OS or the
// container already allows, so it is safe to do unconditionally. Failure is not
// fatal: the node runs, just with a smaller budget, and says so.
func raiseFileDescriptorLimit(out io.Writer) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		fmt.Fprintf(out, "warning: could not read RLIMIT_NOFILE, libp2p will size its budget from the inherited limit: %v\n", err)
		return
	}
	// TO A FLOOR, NOT TO THE CEILING. Raising straight to the hard limit means
	// RLIM_INFINITY where the OS allows it, and rcmgr would then size a budget
	// off an unbounded number — the opposite of the bounded posture the relay
	// limits exist to hold. 65536 is what spacedatanetwork.service already
	// ships, so this makes an unmanaged start match a managed one instead of
	// inventing a new number.
	//
	// Only ever raises. A unit that granted more (the relay unit asks for
	// 200000) is left alone.
	const wantSoft = 65536
	if lim.Cur >= wantSoft {
		return
	}
	target := uint64(wantSoft)
	if lim.Max < target {
		target = lim.Max
	}
	raised := lim
	raised.Cur = target
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &raised); err != nil {
		fmt.Fprintf(out, "warning: could not raise RLIMIT_NOFILE from %d to %d; libp2p's connection budget stays sized for %d: %v\n",
			lim.Cur, target, lim.Cur, err)
		return
	}
	fmt.Fprintf(out, "raised RLIMIT_NOFILE %d -> %d (libp2p sizes its connection budget from this)\n", lim.Cur, target)
}
