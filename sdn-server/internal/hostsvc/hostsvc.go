// Package hostsvc is the node's ONE connector to the process supervisor that
// runs it — and nothing else.
//
// It answers a single question honestly ("what does my supervisor say about
// me?") and, in supervisor.go, performs the two actions the owner authorized on
// 2026-07-30 (graph task sdn-dashboard-wave3-service-lifecycle: RESTART / STOP,
// under the Seal Council's conditions). It holds no policy: who may ask, whether
// the capability is enabled at all, and what the UI renders are decisions made by
// the caller (cmd/spacedatanetwork/node_service_api.go).
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
