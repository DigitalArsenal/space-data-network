package update

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
}

type controlShutdownRequest struct {
	Token      string `json:"token"`
	BundleRoot string `json:"bundleRoot"`
}

// ControlShutdownPath is the daemon's loopback route for the update shutdown
// handshake.
const ControlShutdownPath = "/api/v1/admin/update/shutdown"

// ControlShutdown is the daemon's answer to the update shutdown handshake.
type ControlShutdown struct {
	Status string `json:"status"`
	// PID is the daemon process that accepted the shutdown. The helper swaps
	// nothing until it has watched this pid exit (daemonstop.go).
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
}

func NewControlHandler(opts ControlHandlerOptions) http.Handler {
	root := filepath.Clean(strings.TrimSpace(opts.BundleRoot))
	return &controlHandler{
		bundleRoot: root,
		paths:      PathsFor(root),
		shutdown:   opts.Shutdown,
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(ControlShutdown{
		Status:      "shutdown_requested",
		PID:         os.Getpid(),
		BundleRoot:  h.bundleRoot,
		RestartArgv: os.Args,
		Supervised:  SupervisedBySystemd(),
	})
	if h.shutdown != nil {
		go h.shutdown()
	}
}

// RequestShutdown is the helper's side of the handshake: it presents the
// one-time token for bundleRoot at shutdownURL (the daemon's loopback
// ControlShutdownPath) and returns the daemon's answer.
func RequestShutdown(client *http.Client, shutdownURL, bundleRoot, token string) (*ControlShutdown, error) {
	body, err := json.Marshal(controlShutdownRequest{Token: token, BundleRoot: bundleRoot})
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(shutdownURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("daemon update shutdown rejected: %s %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var answer ControlShutdown
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil, err
	}
	return &answer, nil
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
