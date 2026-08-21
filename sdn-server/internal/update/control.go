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
	// Probe resolves THIS daemon's supervisor state before the handler
	// answers a shutdown request. Nil uses hostsvc.Probe. The seam exists
	// so the supervisor contract is testable on hosts (macOS, CI,
	// containers) that have no systemd to probe.
	Probe func(ctx context.Context) hostsvc.State
}

type controlHandler struct {
	bundleRoot string
	paths      Paths
	shutdown   func()
	probe      func(ctx context.Context) hostsvc.State
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
	// manages, system or --user scope). It is a backstop signal; the fields
	// the helper's restart plan is built from are Unit and RestartPolicy.
	Supervised bool `json:"supervised"`
	// Unit is the systemd unit this daemon resolved from its own cgroup and
	// proved it owns via MainPID (hostsvc.Probe). "" when no supervisor was
	// proven. When the daemon IS supervised this is the EXACT unit the
	// helper must start explicitly after the swap — never RestartArgv, which
	// a direct spawn would place outside this unit's cgroup while the unit
	// loops "activating" against the store's single-writer lock (six
	// occurrences, graph task sdn-update-helper-supervisor-mode).
	Unit string `json:"unit"`
	// RestartPolicy is the resolved unit's Restart= setting verbatim
	// ("always", "on-failure", "no", …). The helper does NOT bank on it to
	// respawn the daemon (a clean exit is a STOP under on-failure and no,
	// live incident 2026-08-08): every known policy gets an explicit unit
	// restart. An UNKNOWN policy is a refusal, not a plan.
	RestartPolicy string `json:"restartPolicy"`
}

// isSupervisedBySystemd reports whether the CURRENT process was started by
// systemd (INVOCATION_ID is set for every unit-managed process since systemd
// 232, system and user scope alike; nothing else sets it).
func isSupervisedBySystemd() bool {
	return strings.TrimSpace(os.Getenv("INVOCATION_ID")) != ""
}

func NewControlHandler(opts ControlHandlerOptions) http.Handler {
	root := filepath.Clean(strings.TrimSpace(opts.BundleRoot))
	probe := opts.Probe
	if probe == nil {
		probe = hostsvc.Probe
	}
	return &controlHandler{
		bundleRoot: root,
		paths:      PathsFor(root),
		shutdown:   opts.Shutdown,
		probe:      probe,
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

	// RESOLVE THE SUPERVISOR BEFORE SHUTDOWN (ops-update-lane-restart-policy-
	// preflight). The helper's restart plan is built from this response, so the
	// owning unit and its Restart= policy must be established BEFORE this
	// process exits. A daemon that a supervisor owns but whose unit/policy
	// cannot be resolved is REFUSED, not answered: under Restart=on-failure or
	// Restart=no a clean exit is a STOP and the box stays down (live incident
	// 2026-08-08), and under an unknown policy neither the supervisor nor the
	// helper can be trusted to bring it back. The refusal happens BEFORE the
	// one-time token is consumed, so a corrected retry does not need a fresh
	// launch.
	supervised := isSupervisedBySystemd()
	state := h.probe(r.Context())
	if supervised && (strings.TrimSpace(state.Unit) == "" || strings.TrimSpace(state.RestartPolicy) == "") {
		writeControlError(w, http.StatusServiceUnavailable, fmt.Sprintf(
			"update shutdown refused: this daemon is supervised by systemd but its owning unit or Restart= policy could not be resolved (unit=%q restart=%q); a clean daemon exit would leave the box down and no explicit restart can be planned. Verify the unit and its Restart= setting (systemctl status, systemctl show), then retry; no files were changed", state.Unit, state.RestartPolicy))
		return
	}
	if err := os.Remove(controlTokenPath(h.paths)); err != nil && !os.IsNotExist(err) {
		writeControlError(w, http.StatusInternalServerError, fmt.Sprintf("consume update control token: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(controlShutdownResponse{
		Status:        "shutdown_requested",
		PID:           os.Getpid(),
		BundleRoot:    h.bundleRoot,
		RestartArgv:   os.Args,
		Supervised:    supervised,
		Unit:          state.Unit,
		RestartPolicy: state.RestartPolicy,
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
