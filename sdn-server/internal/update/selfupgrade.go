package update

// LAUNCHING THE SWAP FROM INSIDE THE DAEMON.
//
// The self-upgrade is two acts with very different risk profiles, and this file
// is only about the second one.
//
// ACT ONE happens in the daemon's own process, online, while it keeps serving:
// fetch the manifest and carrier over HTTPS, verify the signature against the
// bundle trust roots, verify the hashes and sizes, and Stage the payload. None
// of it touches the running bundle, so a failure at any point costs nothing but
// a log line. That is what "install while up" means here, and it is why the
// daemon does this part itself rather than shelling out.
//
// ACT TWO is the swap, and it CANNOT run inside the daemon: the daemon has to
// stop for its own binary to be replaced, so whatever performs the swap must
// outlive it. That is the existing helper — a copy of the current executable
// under updates/helper/, which asks the daemon to shut down through the
// loopback control endpoint, applies, waits for the supervisor to bring the
// daemon back, health-checks it, and reverses to the previous slot if it does
// not come up. All of that is already built and tested. This file only starts
// it, correctly.
//
// "CORRECTLY" IS THE WHOLE PROBLEM, and it is a cgroup problem. A child of the
// daemon lives in the daemon's systemd cgroup. When the daemon exits, the unit
// enters deactivating and systemd SIGTERMs everything left in that cgroup —
// including a helper that is halfway through renaming the bundle tree. A torn
// bundle is the worst outcome this lane can produce, strictly worse than never
// upgrading. Every operator run so far avoided this by accident: they typed the
// command over ssh, so the helper was born in the ssh session's scope, not the
// daemon's.
//
// So on a supervised box the helper is started as its OWN transient systemd
// unit (systemd-run --unit ... --collect). It is that unit's MAIN process, so
// the unit lives exactly as long as the helper does and is garbage-collected
// after — no orphan, no second cgroup to reap, and its output lands in the
// journal beside everything else about the roll. Where there is no systemd
// (containers, dev boxes), a new session (setsid) is enough, because there is
// no supervisor to kill the cgroup in the first place.
//
// PROVEN LIVE on host-01, 2026-08-09: the daemon heard a signed signal, fetched
// and staged while continuing to serve, and launched the swap as
// sdn-self-upgrade-<epoch>.service. The transient unit's output lands in the
// journal beside the daemon's own, which is how the first run's helper panic was
// diagnosed in one command, and --collect reaped the failed unit so the next
// launch was not blocked by a leftover failed unit of the same name.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// SelfUpgradeOptions describes one launch of the swap phase.
type SelfUpgradeOptions struct {
	// SourceExecutable is the binary to copy into updates/helper/. Normally
	// the running daemon's own executable.
	SourceExecutable string
	// UpdateID is the staged update to apply.
	UpdateID string
	// AdminURL is the daemon's own admin base URL, used by the helper for the
	// shutdown handshake and the post-restart health gate. Without it the
	// helper cannot stop the daemon and will not restart it either.
	AdminURL string
	// HealthTimeout bounds the post-restart health wait. Zero keeps the
	// helper's 60 s default, which is far too short for a store-heavy node
	// whose boot replays the record catalog for minutes — callers on such a
	// box must set it.
	HealthTimeout time.Duration
	// AllowRollback carries an operator's explicit acceptance of a declared
	// source-lineage rollback. A signal-driven upgrade NEVER sets it: a
	// broadcast is not an operator.
	AllowRollback bool
	// Trigger and SignalKeyID are carried into the helper's deploy-ledger line
	// so an unattended self-upgrade is distinguishable, after the fact, from an
	// operator who ran `update install`.
	Trigger     string
	SignalKeyID string
	// AdminCAFile is the certificate the daemon serves; see
	// HelperPlanOptions.AdminCAFile.
	AdminCAFile string
	// UnitPrefix names the transient systemd unit. Defaults to
	// "sdn-self-upgrade".
	UnitPrefix string
}

// SelfUpgradeLaunch reports how the swap phase was started, for the log line
// and for the tests.
type SelfUpgradeLaunch struct {
	// Mode is "systemd-transient" or "detached-session".
	Mode string
	// Unit is the transient unit name, when there is one.
	Unit string
	// PID is the launcher's pid. Under systemd-run it is the pid of the
	// short-lived systemd-run client, NOT the helper: the helper's pid belongs
	// to the transient unit and is in the journal.
	PID        int
	Executable string
	Args       []string
}

// helperRuntimeEnvAllow is the environment the helper needs to run at all, and
// nothing else.
//
// It is an ALLOW-list rather than "inherit everything" for one reason: under
// systemd-run every passed variable becomes a property of the transient unit
// and is readable via `systemctl show`. The daemon's environment can carry key
// passwords and mnemonics (config.EnvKeyPassword and friends), and copying
// those into a queryable unit property would turn a deploy mechanism into a
// secret-disclosure mechanism. What the helper actually needs is the dynamic
// linker's view of the world plus the update lane's own knobs.
var helperRuntimeEnvAllow = []string{
	"PATH", "HOME", "LANG", "TMPDIR",
	"LD_LIBRARY_PATH", "WASMEDGE_DIR", "WASMEDGE_PLUGIN_PATH",
	"HD_WALLET_WASM_PATH", "ORBPRO_LICENSING_WASM_PATH",
	TrustRootsEnv,
}

// helperRuntimeEnvSecretish never travels, even when it matches an allowed
// prefix. Belt and braces around the SDN_ prefix rule below.
var helperRuntimeEnvSecretish = regexp.MustCompile(`(?i)(PASS|SECRET|MNEMONIC|SEED|TOKEN|PRIVATE|CREDENTIAL)`)

// helperRuntimeEnv builds the environment for the swap process.
func helperRuntimeEnv(environ []string) []string {
	allowed := make(map[string]bool, len(helperRuntimeEnvAllow))
	for _, name := range helperRuntimeEnvAllow {
		allowed[name] = true
	}
	var out []string
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if helperRuntimeEnvSecretish.MatchString(name) {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "SDN_UPDATE_") {
			out = append(out, kv)
		}
	}
	return out
}

// SupervisedBySystemd reports whether the CURRENT process was started by
// systemd. INVOCATION_ID is set for every unit-managed process since systemd
// 232, system and user scope alike, and nothing else sets it.
func SupervisedBySystemd() bool {
	return strings.TrimSpace(os.Getenv("INVOCATION_ID")) != ""
}

// LaunchSelfUpgrade starts the swap phase for an already-staged update.
//
// It writes the one-time control token first, then copies the helper, then
// launches. Order matters: the helper's very first act is to present that token
// to the daemon, so a helper that starts before the token exists would be
// refused and the box would sit with a staged update and no explanation.
func LaunchSelfUpgrade(paths Paths, opts SelfUpgradeOptions) (*SelfUpgradeLaunch, error) {
	if strings.TrimSpace(opts.UpdateID) == "" {
		return nil, fmt.Errorf("self-upgrade requires a staged update id")
	}
	source := strings.TrimSpace(opts.SourceExecutable)
	if source == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve self-upgrade source executable: %w", err)
		}
		source = exe
	}
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		source = resolved
	}

	token, err := newControlToken()
	if err != nil {
		return nil, err
	}
	if err := WriteControlToken(paths, token); err != nil {
		return nil, fmt.Errorf("write update control token: %w", err)
	}

	plan, err := PrepareHelperPlan(HelperPlanOptions{
		Paths:            paths,
		SourceExecutable: source,
		UpdateID:         opts.UpdateID,
		AdminURL:         strings.TrimSpace(opts.AdminURL),
		Token:            token,
		HealthTimeout:    opts.HealthTimeout,
		AllowRollback:    opts.AllowRollback,
		Trigger:          opts.Trigger,
		SignalKeyID:      opts.SignalKeyID,
		AdminCAFile:      opts.AdminCAFile,
	})
	if err != nil {
		return nil, err
	}

	env := helperRuntimeEnv(os.Environ())
	if SupervisedBySystemd() {
		if launch, err := launchViaSystemdRun(plan, env, opts); err == nil {
			return launch, nil
		} else if !isSystemdRunUnavailable(err) {
			// A supervised box where systemd-run exists but REFUSED is not a
			// box to fall back on: the fallback would put the helper in the
			// daemon's cgroup, which is the exact failure this avoids.
			return nil, err
		}
	}
	return launchDetached(plan, env)
}

func launchViaSystemdRun(plan *HelperPlan, env []string, opts SelfUpgradeOptions) (*SelfUpgradeLaunch, error) {
	binary, err := exec.LookPath("systemd-run")
	if err != nil {
		return nil, errSystemdRunUnavailable{err}
	}
	prefix := strings.TrimSpace(opts.UnitPrefix)
	if prefix == "" {
		prefix = "sdn-self-upgrade"
	}
	unit := fmt.Sprintf("%s-%d", prefix, time.Now().UTC().Unix())

	args := []string{
		"--unit=" + unit,
		// --collect: the transient unit is garbage-collected even when the
		// helper exits non-zero (which is exactly what a rolled-back upgrade
		// does). Without it a failed self-upgrade leaves a failed unit behind
		// that blocks the next launch on the same name.
		"--collect",
		"--description=SDN self-upgrade swap (update " + plan.Args[len(plan.Args)-1] + ")",
	}
	for _, kv := range env {
		args = append(args, "--setenv="+kv)
	}
	// systemd-run inherits the MANAGER's environment, not the caller's, and a
	// box with SDN_CONFIG set there hands the helper a config for a DIFFERENT
	// node. Blank it explicitly so the helper resolves through the running
	// daemon it is about to stop, which is the only config that describes it.
	args = append(args, "--setenv=SDN_CONFIG=")
	args = append(args, plan.Executable)
	args = append(args, plan.Args...)

	cmd := exec.Command(binary, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("systemd-run %s: %w: %s", unit, err, strings.TrimSpace(string(output)))
	}
	return &SelfUpgradeLaunch{
		Mode:       "systemd-transient",
		Unit:       unit,
		PID:        cmd.ProcessState.Pid(),
		Executable: plan.Executable,
		Args:       plan.Args,
	}, nil
}

func launchDetached(plan *HelperPlan, env []string) (*SelfUpgradeLaunch, error) {
	cmd := exec.Command(plan.Executable, plan.Args...)
	cmd.Env = env
	// A new session detaches the helper from the daemon's controlling terminal
	// and process group. On a box with no supervisor that is sufficient: there
	// is no cgroup teardown to survive.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logPath := filepath.Join(filepath.Dir(plan.Executable), "self-upgrade.log")
	if logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start self-upgrade helper: %w", err)
	}
	// Reap it so the daemon does not accumulate a zombie if it outlives the
	// helper (it usually will not — the helper stops it — but a failed apply
	// returns before any shutdown is requested).
	go func() { _ = cmd.Wait() }()
	return &SelfUpgradeLaunch{
		Mode:       "detached-session",
		PID:        cmd.Process.Pid,
		Executable: plan.Executable,
		Args:       plan.Args,
	}, nil
}

type errSystemdRunUnavailable struct{ err error }

func (e errSystemdRunUnavailable) Error() string {
	return "systemd-run is not available: " + e.err.Error()
}
func (e errSystemdRunUnavailable) Unwrap() error { return e.err }

func isSystemdRunUnavailable(err error) bool {
	_, ok := err.(errSystemdRunUnavailable)
	return ok
}

func newControlToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate update control token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// HasFailedUpdate reports whether this box already tried this update and
// reversed it (updates/failed/<update-id>/ exists).
//
// This is the durable quarantine that makes a replayed signal harmless. Without
// it, the sequence gate alone is not enough: a box that self-rolled-back from
// sequence N to N-1 has a CURRENT sequence below N again, so a replayed signal
// for N looks like news, and the box would reinstall the very build it just
// judged unhealthy — forever, on a loop, one replay at a time. The failed
// directory is written by Apply and by Rollback, survives restarts, and is
// removed only by an operator, which is the right authority for "try it again".
func HasFailedUpdate(paths Paths, updateID string) bool {
	updateID = strings.TrimSpace(updateID)
	if updateID == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(paths.Failed, updateID))
	return err == nil && info.IsDir()
}
