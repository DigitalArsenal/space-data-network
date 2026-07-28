package config

// Running-daemon discovery: the tier that answers "which node am I on?" the way
// an operator means it.
//
// OWNER, 2026-07-28, on host-01, running plain `spacedatanetwork show-identity`:
// "CLI STILL BROKEN."
//
// He was right, and the bug was in the fix. The directory scan was a SUBSTITUTE
// for the real question. The spec said the system tier should find "the running
// daemon's config"; scanning /etc/space-data-network/*.yaml only finds files,
// and the standard prod layout has more than one (a sidecar yaml next to the
// node yaml). So the resolver hit its ambiguity error and handed the operator
// back the very question it exists to answer — on a host running exactly ONE
// daemon, whose config is not ambiguous at all. It is knowable: ask the process.
//
// So this tier reads the process table, finds live `spacedatanetwork daemon`
// invocations, and takes the config off the command line. One daemon means one
// answer, and no amount of clutter in /etc can confuse it. Several daemons is a
// REAL ambiguity (several nodes genuinely run here), and only then do we ask.
//
// Failure here is never fatal: if the process table cannot be read, we return
// nothing and the resolver falls through to the directory scan.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// daemonBinaryNames are the argv[0] basenames that count as our node binary.
var daemonBinaryNames = map[string]bool{
	"spacedatanetwork": true,
	"sdn-server":       true,
}

// daemonSubcommand is the subcommand that actually runs a node. Requiring it is
// what keeps this from matching the CLI process asking the question: a
// `spacedatanetwork show-identity` invocation is a spacedatanetwork process too.
const daemonSubcommand = "daemon"

// procRoot is the procfs mount point. A variable ONLY so tests can point it at a
// fixture tree (including the container shape, where the daemon is pid 1).
var procRoot = "/proc"

// psCommand runs the portable fallback used where procfs is absent (darwin).
// A variable ONLY so tests can stub it.
var psCommand = func() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid=,args=").Output()
}

// DaemonProcess is one live node daemon found in the process table.
type DaemonProcess struct {
	// PID is the daemon's process id.
	PID int
	// ConfigPath is the value of its --config/-c flag, or "" when the daemon
	// was started without one (it defaulted, exactly as we are about to).
	ConfigPath string
	// Cmdline is the full argv, for error messages.
	Cmdline []string
}

// Describe renders one daemon for an ambiguity error.
func (d DaemonProcess) Describe() string {
	cfg := d.ConfigPath
	if cfg == "" {
		cfg = "(no --config; using the default)"
	}
	return fmt.Sprintf("pid %d  %s", d.PID, cfg)
}

// configFromArgs extracts the --config/-c value from a daemon's argv, and
// reports whether the argv is a daemon invocation at all.
func configFromArgs(args []string) (cfg string, isDaemon bool) {
	if len(args) == 0 {
		return "", false
	}
	if !daemonBinaryNames[filepath.Base(args[0])] {
		return "", false
	}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == daemonSubcommand:
			isDaemon = true
		case arg == "--config" || arg == "-c":
			if i+1 < len(args) {
				cfg = args[i+1]
				i++
			}
		case strings.HasPrefix(arg, "--config="):
			cfg = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c="):
			cfg = strings.TrimPrefix(arg, "-c=")
		}
	}
	return strings.TrimSpace(cfg), isDaemon
}

// daemonsFromProc reads procfs. Exact: argv arrives NUL-separated, so paths with
// spaces survive. Returns ok=false when there is no readable procfs.
func daemonsFromProc() (found []DaemonProcess, ok bool) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, false
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if cfg, isDaemon := configFromArgs(args); isDaemon {
			found = append(found, DaemonProcess{PID: pid, ConfigPath: cfg, Cmdline: args})
		}
	}
	return found, true
}

// daemonsFromPS is the fallback for systems without procfs (darwin). Splitting
// on whitespace cannot represent an argument containing spaces; procfs is used
// wherever it exists precisely because it does not have that limit.
func daemonsFromPS() []DaemonProcess {
	out, err := psCommand()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var found []DaemonProcess
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidText, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err != nil || pid == self {
			continue
		}
		args := strings.Fields(rest)
		if cfg, isDaemon := configFromArgs(args); isDaemon {
			found = append(found, DaemonProcess{PID: pid, ConfigPath: cfg, Cmdline: args})
		}
	}
	return found
}

// FindRunningDaemons returns every live node daemon, sorted by pid. An
// unreadable process table yields no daemons and no error: discovery is an
// improvement on the directory scan, never a new way for the CLI to fail.
func FindRunningDaemons() []DaemonProcess {
	found, ok := daemonsFromProc()
	if !ok {
		found = daemonsFromPS()
	}
	sort.Slice(found, func(i, j int) bool { return found[i].PID < found[j].PID })
	return found
}

// resolveFromRunningDaemon applies the running-daemon tier.
//
// Exactly one daemon with an explicit config: that config wins, whatever the
// directory looks like. Exactly one daemon that defaulted: fall through, so we
// default through the SAME code path it did. Several daemons: a real ambiguity,
// reported as the daemons themselves rather than as a list of files.
func resolveFromRunningDaemon() (Resolution, bool, error) {
	daemons := FindRunningDaemons()
	switch len(daemons) {
	case 0:
		return Resolution{}, false, nil
	case 1:
		d := daemons[0]
		if d.ConfigPath == "" {
			return Resolution{}, false, nil
		}
		return Resolution{
			Path:   d.ConfigPath,
			Source: ConfigSource(fmt.Sprintf("running daemon (pid %d)", d.PID)),
			Exists: fileExists(d.ConfigPath),
		}, true, nil
	}

	lines := make([]string, 0, len(daemons))
	for _, d := range daemons {
		lines = append(lines, d.Describe())
	}
	return Resolution{Source: SourceMissing}, false, fmt.Errorf(
		"%d node daemons are running here:\n  %s\nCannot tell which node you mean — pass -c <config> or set SDN_CONFIG",
		len(daemons), strings.Join(lines, "\n  "))
}
