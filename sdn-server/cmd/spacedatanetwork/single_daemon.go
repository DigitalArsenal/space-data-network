package main

// single_daemon.go — one SDN daemon per box.
//
// OWNER LAW (2026-07-28), verbatim: "never ever have more than one instance
// running on a box from here on out with our current deployment."
//
// This is enforced HERE, at the daemon's own start, because that is the only
// place that cannot be bypassed by a deploy script somebody forgot to update. A
// box that briefly ran two daemons produced, in one afternoon: two nodes
// fighting over one 2-vCPU machine while a catalog ingest needed all of it, a
// CLI that could no longer tell which node an operator meant (every `key`,
// `escrow` and `prewarm` invocation had to name a config), and two peers on the
// board where the operator expected one.
//
// The check is a REFUSAL, not a warning: starting anyway is how the state we
// just spent an afternoon unwinding gets recreated. The override exists for
// development (running a second node against a temp config on a laptop) and
// says so loudly in the log, so it can never be mistaken for a supported
// production shape.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// allowMultiDaemonEnv lets a development box run more than one node. Any value
// other than empty/"0"/"false" enables it.
const allowMultiDaemonEnv = "SDN_ALLOW_MULTI_DAEMON"

// allowMultiDaemonFlag is the --allow-multi-daemon flag's bound value.
var allowMultiDaemonFlag bool

// multiDaemonOverrideEnabled reports whether the operator has explicitly asked
// for the unsupported shape, and how they asked, for the log line.
func multiDaemonOverrideEnabled() (bool, string) {
	if allowMultiDaemonFlag {
		return true, "--allow-multi-daemon"
	}
	raw := strings.TrimSpace(os.Getenv(allowMultiDaemonEnv))
	if raw == "" {
		return false, ""
	}
	if enabled, err := strconv.ParseBool(raw); err == nil {
		if !enabled {
			return false, ""
		}
	}
	return true, allowMultiDaemonEnv + "=" + raw
}

// otherRunningDaemons lists live node daemons that are not this process.
//
// Self-exclusion is by PID rather than by config path: two daemons sharing one
// config is exactly the collision worth catching, and a daemon started without
// --config reports no path at all.
func otherRunningDaemons(selfPID int) []config.DaemonProcess {
	all := config.FindRunningDaemons()
	others := make([]config.DaemonProcess, 0, len(all))
	for _, d := range all {
		if d.PID == selfPID {
			continue
		}
		others = append(others, d)
	}
	return others
}

// enforceSingleDaemonPerBox refuses to start when another SDN daemon is already
// running here. Returns nil when this node is alone, or when an explicit
// override is set (which it logs at WARN, naming the override).
func enforceSingleDaemonPerBox() error {
	others := otherRunningDaemons(os.Getpid())
	if len(others) == 0 {
		return nil
	}

	lines := make([]string, 0, len(others))
	for _, d := range others {
		lines = append(lines, d.Describe())
	}
	running := strings.Join(lines, "\n  ")

	if enabled, how := multiDaemonOverrideEnabled(); enabled {
		log.Warnf(
			"STARTING ALONGSIDE %d OTHER SDN DAEMON(S) because %s is set — this is a DEVELOPMENT override and is NOT a supported production shape (owner law: one instance per box). Already running:\n  %s",
			len(others), how, running)
		return nil
	}

	return fmt.Errorf(
		"refusing to start: %d SDN daemon(s) already running on this box:\n  %s\n"+
			"Owner law (2026-07-28): one node instance per box. Stop the other daemon "+
			"(systemctl stop <unit>) or move this node to its own host.\n"+
			"For development only, re-run with --allow-multi-daemon or %s=1.",
		len(others), running, allowMultiDaemonEnv)
}
