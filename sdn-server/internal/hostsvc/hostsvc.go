// Package hostsvc is the node's ONE connector to the process supervisor that
// runs it — and nothing else.
//
// It answers a single question honestly ("what does my supervisor say about
// me?") and — below the divider at the bottom of this file — performs the two
// actions the owner authorized on 2026-07-30 (graph task
// sdn-dashboard-wave3-service-lifecycle: RESTART / STOP, under the Seal Council's
// conditions). It holds no policy: who may ask, whether the capability is enabled
// at all, and what the UI renders are decisions made by the callers
// (cmd/spacedatanetwork/node_service_api.go and node_service_control.go).
//
// WHY A PROBE AND NOT A CONFIG VALUE. Every fact here is READ from the running
// system, never configured and never assumed:
//
//   - the UNIT is resolved from THIS process's own cgroup (/proc/self/cgroup),
//     so the daemon can only ever describe — and later control — the unit it is
//     actually running under. A configured unit name would let a config edit
//     point the control surface at some other unit on the box.
//   - the unit is then CONFIRMED to be ours: systemd's MainPID for it must equal
//     this process's pid. If it does not, the probe reports NO supervisor. A
//     mismatch means the cgroup mapping is not what it looks like (a sidecar
//     sharing a slice, a container namespace), and the correct answer to "may I
//     restart myself" in that situation is no.
//   - AUTOSTART is systemd's own UnitFileState string (enabled / disabled /
//     static / indirect / masked / …). This is the honest source that the
//     dashboard's AUTOSTART cell had been missing: the node_status_read
//     capability reports `autostart_known:false` because IT has no supervisor
//     surface, which is true of it and stays true. This package is that surface.
//
// FAIL-CLOSED: every failure path — no /proc, no systemctl, a non-zero exit, a
// timeout, an unparseable answer, a pid mismatch — returns a State with
// Supervisor "" and Detected false. There is no partial success in which a
// lifecycle verb becomes available.
//
// PLATFORM: systemd only, and that is deliberate rather than a gap. macOS
// (launchd) and container runtimes are not systemd, so on those hosts the probe
// reports nothing, the control surface refuses, and the dashboard renders no
// lifecycle buttons at all — which is exactly the intended behaviour for a
// developer laptop and for a Docker node.
package hostsvc

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// SystemctlPath is the ABSOLUTE path to systemctl, per the Seal Council's
// ship-time condition (Hephaestus, 2026-07-30): "allow-list absolute
// /usr/bin/systemctl". Never resolved through PATH — a PATH lookup on a
// process-control path is a lookup an attacker can influence.
const SystemctlPath = "/usr/bin/systemctl"

// probeTimeout bounds the systemctl call. `systemctl show` on a healthy box
// answers in milliseconds; a systemd that does not answer in two seconds is a
// supervisor this node should not claim to have.
const probeTimeout = 2 * time.Second

// State is what the supervisor says about this process. Every field is read;
// none is inferred.
type State struct {
	// Supervisor is "systemd" when a unit was resolved AND confirmed to be
	// this process, "" otherwise. The empty string is the whole refusal.
	Supervisor string
	// Unit is the resolved unit name (e.g.
	// "space-data-network-module-delivery.service").
	Unit string
	// ActiveState / SubState are systemd's own words ("active"/"running").
	ActiveState string
	SubState    string
	// Autostart is systemd's UnitFileState verbatim: "enabled", "disabled",
	// "static", "indirect", "masked", "enabled-runtime", … It is NEVER
	// normalised into a boolean — "static" and "indirect" are neither
	// enabled nor disabled, and flattening them would be the same
	// fabrication the dashboard's hardcoded ENABLED was.
	Autostart string
	// RestartPolicy is the unit's Restart= setting ("always", "on-failure",
	// "no", …). It is the difference between a STOP that sticks and a STOP
	// systemd immediately undoes, so the operator is told which one they have.
	RestartPolicy string
	// Detected is true only when Supervisor != "" — i.e. a unit was resolved
	// and its MainPID matched this process.
	Detected bool
}

// Probe reads this process's supervisor state. It never returns an error: a
// probe that cannot prove a supervisor reports that it found none, because
// "unknown" and "absent" have the same consequence here and a caller that had to
// distinguish them would be tempted to treat one of them as permission.
func Probe(ctx context.Context) State {
	unit := unitFromCgroup(readSelfCgroup())
	if unit == "" {
		return State{}
	}

	props, err := showUnit(ctx, unit)
	if err != nil {
		return State{}
	}

	// The unit must be OURS. systemd reports MainPID for the unit; for a
	// Type=simple daemon that is this process. Anything else and we are not
	// looking at ourselves.
	mainPID, err := strconv.Atoi(strings.TrimSpace(props["MainPID"]))
	if err != nil || mainPID != os.Getpid() {
		return State{}
	}

	return State{
		Supervisor:    "systemd",
		Unit:          unit,
		ActiveState:   props["ActiveState"],
		SubState:      props["SubState"],
		Autostart:     props["UnitFileState"],
		RestartPolicy: props["Restart"],
		Detected:      true,
	}
}

// readSelfCgroup returns /proc/self/cgroup's contents, or "" on any host that
// does not have it (macOS, and any environment where procfs is not mounted).
func readSelfCgroup() string {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return string(raw)
}

// unitFromCgroup extracts the systemd unit name from a cgroup file.
//
// cgroup v2 gives one line, "0::/system.slice/some-name.service"; v1 gives
// several, of which the systemd controller's line carries the same path. Either
// way the unit is the LAST path element ending in ".service".
//
// Deliberately narrow: only ".service" is accepted. A ".scope" is a transient
// unit (a login session, a `systemd-run` invocation) and restarting it is not a
// meaningful operation; a ".slice" is not a process at all. Anything else — a
// docker cgroup path, an empty file — yields "", i.e. no supervisor.
func unitFromCgroup(contents string) string {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// v1 lines are "hierarchy-id:controller-list:path"; v2 is "0::path".
		fields := strings.SplitN(line, ":", 3)
		path := fields[len(fields)-1]
		// The INNERMOST .service element, not the first: a nested path
		// (systemd-nspawn, a unit started inside another unit's cgroup) is owned
		// by the deepest unit, and that is the only one whose MainPID can be this
		// process. Taking the outer one would fail the pid check and disable the
		// surface, which is safe but wrong about the reason.
		unit := ""
		for _, element := range strings.Split(path, "/") {
			if strings.HasSuffix(element, ".service") {
				// A unit name from the kernel's own accounting, never from
				// a request or a config file.
				unit = element
			}
		}
		if unit != "" {
			return unit
		}
	}
	return ""
}

// showUnit reads the properties this package needs, as Key=Value lines.
//
// `--property` is repeated rather than using `--value`, because `--value` prints
// bare values in an order the caller has to trust; keyed lines cannot silently
// shift meaning if systemd reorders or omits one.
func showUnit(ctx context.Context, unit string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, SystemctlPath,
		"show", unit,
		"--property=ActiveState",
		"--property=SubState",
		"--property=UnitFileState",
		"--property=Restart",
		"--property=MainPID",
	)
	// A fixed, empty environment: nothing about this call should depend on the
	// daemon's own environment, and systemctl inherits no locale or paging
	// behaviour that could reshape its output.
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseShowOutput(string(out)), nil
}

// parseShowOutput turns systemctl's Key=Value lines into a map. Exported for
// tests in this package only — the format is the contract, and a change in it
// must fail a test rather than silently disable the surface.
func parseShowOutput(out string) map[string]string {
	props := make(map[string]string, 5)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		props[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return props
}

// ---------------------------------------------------------------------------
// THE DESTRUCTIVE HALF (owner authorization 2026-07-30 + Seal Council
// conditions). Everything above this line is a read; everything below acts.
// ---------------------------------------------------------------------------

// Action is a lifecycle verb. There are exactly two, and they are the two the
// owner authorized — this type exists so no third can be added by passing a
// string through from somewhere else.
type Action string

const (
	// ActionRestart asks the supervisor to restart the unit. Under systemd this
	// is a real restart under ANY Restart= policy, which is why it does not
	// self-exit: host-01 runs Restart=always (so a graceful exit would be
	// resurrected as a restart, coincidentally correct) while host-02's
	// retriever runs Restart=on-failure (so the same exit would be a permanent
	// STOP). One mechanism that is honest on both is the supervisor's.
	ActionRestart Action = "restart"
	// ActionStop stops the unit, and it STICKS: an explicit systemd stop is not
	// undone by Restart=. That is the point and it is also the danger.
	ActionStop Action = "stop"
)

// systemctlVerb maps an Action to systemd's own subcommand. A verb absent from
// this map is not executable — the switch is exhaustive by construction.
func systemctlVerb(action Action) (string, bool) {
	switch action {
	case ActionRestart:
		return "restart", true
	case ActionStop:
		return "stop", true
	default:
		return "", false
	}
}

// Control performs a lifecycle Action on the unit named in state.
//
// SEAL COUNCIL CONDITIONS, all structural rather than documentary:
//
//   - `--no-block` is REQUIRED (Hephaestus): systemctl is exec'd from inside the
//     unit's OWN cgroup, so for a restart or a stop systemd will kill this
//     process — including the systemctl child — before it can report. Waiting on
//     it means waiting for our own death, and the wait's failure would look like
//     the command's failure. With --no-block the job is enqueued and we return.
//   - the UNIT comes from `state`, which resolved it from /proc/self/cgroup and
//     proved it ours by MainPID. It is NEVER taken from a request; there is no
//     parameter here that a caller could aim at another unit.
//   - ABSOLUTE systemctl path, no shell: exec.Command with a fixed argv, so
//     nothing is word-split, expanded or resolved through PATH.
//
// It returns an error the caller can log verbatim. A refusal (no supervisor, an
// unknown verb) is an error too, not a silent no-op: the caller logs and answers.
func Control(ctx context.Context, state State, action Action) error {
	if !state.Detected || state.Supervisor != "systemd" || state.Unit == "" {
		return errNoSupervisor
	}
	verb, ok := systemctlVerb(action)
	if !ok {
		return errUnknownAction
	}

	// A bound on the ENQUEUE, not on the restart: --no-block returns as soon as
	// systemd accepts the job.
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, SystemctlPath, verb, "--no-block", state.Unit)
	cmd.Env = []string{}
	if out, err := cmd.CombinedOutput(); err != nil {
		return &ControlError{Action: action, Unit: state.Unit, Err: err, Output: strings.TrimSpace(string(out))}
	}
	return nil
}

// ControlError carries what systemd said, so the refusal log can name the
// concrete failure rather than "restart failed".
type ControlError struct {
	Action Action
	Unit   string
	Err    error
	Output string
}

func (e *ControlError) Error() string {
	msg := "systemctl " + string(e.Action) + " " + e.Unit + ": " + e.Err.Error()
	if e.Output != "" {
		msg += ": " + e.Output
	}
	return msg
}

func (e *ControlError) Unwrap() error { return e.Err }

// Sentinel refusals. Exported so the HTTP layer can distinguish "this host has
// nothing to control" (a 409/501 shape) from "systemd said no" (a 500 shape)
// without string matching.
var (
	errNoSupervisor  = errNoSupervisorType{}
	errUnknownAction = errUnknownActionType{}
)

type errNoSupervisorType struct{}

func (errNoSupervisorType) Error() string {
	return "no supervisor proven for this process: refusing to act"
}

type errUnknownActionType struct{}

func (errUnknownActionType) Error() string { return "unknown lifecycle action" }

// ErrNoSupervisor reports whether err is the "nothing to control" refusal.
func ErrNoSupervisor(err error) bool {
	_, ok := err.(errNoSupervisorType)
	return ok
}
