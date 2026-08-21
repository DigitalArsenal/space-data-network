package update

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
)

const controlTokenName = "control-token"

type ControlHandlerOptions struct {
	BundleRoot string
	Shutdown   func()
}

type controlHandler struct {
	bundleRoot string
	paths      Paths
	shutdown   func()
	// probe resolves the OWNING unit and its Restart= policy from the daemon's
	// own process (cgroup + MainPID), never from the request. Injected in
	// tests only; production is supervisionProbe.
	probe func() (unit string, restartPolicy string)
}

type controlShutdownRequest struct {
	Token      string `json:"token"`
	BundleRoot string `json:"bundleRoot"`
}

type controlShutdownResponse struct {
	Status      string   `json:"status"`
	PID         int      `json:"pid"`
	BundleRoot  string   `json:"bundleRoot"`
	RestartArgv []string `json:"restartArgv,omitempty"`
	// Supervised reports whether THIS daemon process was started by a
	// supervising init (systemd sets INVOCATION_ID for every process it
	// manages, system or --user scope). The helper direct-spawning
	// RestartArgv after a supervised daemon exits leaves the live
	// replacement outside the unit's cgroup while the unit itself loops
	// "activating" against the store's single-writer lock (six occurrences,
	// graph task sdn-update-helper-supervisor-mode) — the helper must skip
	// the direct spawn and let the supervisor's own Restart= policy respawn
	// it when this is true.
	Supervised bool `json:"supervised"`
	// Unit is the resolved systemd unit that OWNS this daemon process
	// ("space-data-network.service"), resolved from the daemon's own cgroup
	// and confirmed by MainPID — hostsvc.Probe, nothing inferred. It is the
	// exact unit the helper must start after the swap, and it is empty when
	// this daemon is not supervised.
	Unit string `json:"unit,omitempty"`
	// RestartPolicy is that unit's Restart= setting as systemd reports it
	// ("always", "on-failure", "no", …). The shutdown request is REFUSED
	// unless it is one the lane can rely on, so this is only ever a safe
	// policy by the time a guest sees it.
	RestartPolicy string `json:"restartPolicy,omitempty"`
}

// isSupervisedBySystemd reports whether the CURRENT process was started by
// systemd (INVOCATION_ID is set for every unit-managed process since systemd
// 232, system and user scope alike; nothing else sets it).
func isSupervisedBySystemd() bool {
	return strings.TrimSpace(os.Getenv("INVOCATION_ID")) != ""
}

// supervisionProbe resolves THIS process's owning systemd unit and its
// Restart= policy. It is a variable so tests can answer for a process that is
// not actually systemd-managed; production is hostsvc.Probe, which reads
// /proc/self/cgroup and refuses to believe a unit until systemd's MainPID for
// it is this process's own pid.
var supervisionProbe = func() (unit string, restartPolicy string) {
	st := hostsvc.Probe(context.Background())
	return st.Unit, st.RestartPolicy
}

// isSafeRestartPolicy reports whether a unit's Restart= policy lets the lane
// hold the box up across a swap. "always" brings a cleanly-exiting daemon
// back on its own; "on-failure" does not (a clean exit is not a failure), but
// the helper explicitly starts the resolved unit after the swap, so both are
// safe. Everything else ("no", "on-success", "on-abnormal", "on-abort",
// "on-watchdog", or an unresolvable value) either never restarts or restarts
// only on specific failure classes, and a clean update shutdown could leave
// the box down — those are refused, fail-closed.
func isSafeRestartPolicy(policy string) bool {
	return policy == "always" || policy == "on-failure"
}

// supervisedShutdownRefusal builds the actionable refusal for a supervised
// process whose unit or Restart= policy the lane cannot rely on. It names the
// fix, because a refusal nobody can act on is just a failure with adverbs.
func supervisedShutdownRefusal(unit, policy string) string {
	if strings.TrimSpace(unit) == "" {
		return "update shutdown refused: the daemon reports it is supervised by systemd but its owning unit could not be resolved; a clean exit could leave this box down with no supervisor to bring it back. Verify the daemon runs under a systemd .service unit (systemctl status spacedatanetwork) and retry."
	}
	return fmt.Sprintf("update shutdown refused: unit %s has Restart=%q, which the update lane cannot rely on; after a clean update shutdown no supervisor would bring the daemon back and the box would stay down. Set Restart=always or Restart=on-failure for %s (systemctl edit %s) and retry, or run `update install --direct` with an operator who restarts the daemon by hand.", unit, strings.TrimSpace(policy), unit, unit)
}

func NewControlHandler(opts ControlHandlerOptions) http.Handler {
	root := filepath.Clean(strings.TrimSpace(opts.BundleRoot))
	return &controlHandler{
		bundleRoot: root,
		paths:      PathsFor(root),
		shutdown:   opts.Shutdown,
		probe:      supervisionProbe,
	}
}

func WriteControlToken(paths Paths, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("update control token is empty")
	}
	if err := os.MkdirAll(paths.Updates, 0o700); err != nil {
		return err
	}
	return os.WriteFile(controlTokenPath(paths), []byte(token), 0o600)
}

func controlTokenPath(paths Paths) string {
	return filepath.Join(paths.Updates, controlTokenName)
}

func (h *controlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeControlError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		writeControlError(w, http.StatusForbidden, "update control is only available to local clients")
		return
	}
	var req controlShutdownRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeControlError(w, http.StatusBadRequest, "invalid update control request: "+err.Error())
		return
	}
	if filepath.Clean(req.BundleRoot) != h.bundleRoot {
		writeControlError(w, http.StatusConflict, "update control bundle root mismatch")
		return
	}
	expected, err := os.ReadFile(controlTokenPath(h.paths))
	if err != nil {
		writeControlError(w, http.StatusForbidden, "update control token is not available")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), expected) != 1 {
		writeControlError(w, http.StatusForbidden, "update control token rejected")
		return
	}
	if err := os.Remove(controlTokenPath(h.paths)); err != nil && !os.IsNotExist(err) {
		writeControlError(w, http.StatusInternalServerError, fmt.Sprintf("consume update control token: %v", err))
		return
	}

	// THE RESTART POLICY GATE (ops-update-lane-restart-policy-preflight).
	// Before this daemon agrees to exit it must prove that exiting cannot
	// strand the box: resolve the owning unit and its Restart= policy NOW,
	// while the daemon is still up (after it exits nobody can resolve them),
	// and refuse an unresolvable or non-restarting policy with a message that
	// names the fix. The helper's own environment cannot answer this — the
	// helper runs in a transient unit of its own — which is why the refusal
	// lives HERE, daemon-side, before `shutdown` is scheduled. A refusal
	// leaves the daemon serving and the staged update untouched.
	supervised := isSupervisedBySystemd()
	unit, restartPolicy := "", ""
	if supervised {
		unit, restartPolicy = h.probe()
		if unit == "" || !isSafeRestartPolicy(restartPolicy) {
			msg := supervisedShutdownRefusal(unit, restartPolicy)
			fmt.Fprintf(os.Stderr, "update control: refusing shutdown: %s\n", msg)
			writeControlError(w, http.StatusConflict, msg)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(controlShutdownResponse{
		Status:        "shutdown_requested",
		PID:           os.Getpid(),
		BundleRoot:    h.bundleRoot,
		RestartArgv:   os.Args,
		Supervised:    supervised,
		Unit:          unit,
		RestartPolicy: restartPolicy,
	})
	if h.shutdown != nil {
		go h.shutdown()
	}
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeControlError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
