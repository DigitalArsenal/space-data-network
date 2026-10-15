package main

// Round-2 regression tests for the 2026-10-15 security audit (verifier
// follow-ups). They use only APIs that exist on origin/main, so this file
// fails there on behaviour, not on compilation.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

const r2EnvHelper = "SDN_R2_CONSOLE_ENV_HELPER"

// r2ConsoleTokenEnv is r2ConsoleTokenEnv, spelled out so this file also
// compiles against origin/main.
const r2ConsoleTokenEnv = "SDN_CONSOLE_TOKEN"

// The desktop shell's console secret reached every process the daemon
// started before the admin listener was built: Tor (and Kubo) inherited
// SDN_CONSOLE_TOKEN, the key to the node's root console. The variable must be
// gone from the process environment before any code runs.
func TestR2_ConsoleSecretIsGoneBeforeTheDaemonStartsAnything(t *testing.T) {
	const secret = "r2-console-secret-0123456789abcdefghijklmnopqrstuv"
	cmd := exec.Command(os.Args[0], "-test.run=^TestR2_ConsoleSecretEnvHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), r2EnvHelper+"=1", r2ConsoleTokenEnv+"="+secret)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the console secret was visible at process start or to a child process: %v\n%s", err, out)
	}
	if strings.Contains(string(out), secret) {
		t.Fatalf("the console secret leaked into output:\n%s", out)
	}
}

// TestR2_ConsoleSecretEnvHelper runs only inside the process the test above
// starts with SDN_CONSOLE_TOKEN set, the way the desktop shell starts the
// daemon.
func TestR2_ConsoleSecretEnvHelper(t *testing.T) {
	if os.Getenv(r2EnvHelper) != "1" {
		t.Skip("helper process only")
	}
	if _, ok := os.LookupEnv(r2ConsoleTokenEnv); ok {
		t.Fatalf("%s is still in the environment when the daemon's code starts", r2ConsoleTokenEnv)
	}
	out, err := exec.Command("/usr/bin/env").Output()
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if strings.Contains(string(out), r2ConsoleTokenEnv+"=") {
		t.Fatalf("a child process inherited %s", r2ConsoleTokenEnv)
	}
}

// The Tor onion service forwarded to the admin listener, so an auth-off node
// with a loopback listener passed adminListenerSafety while serving every
// read and non-operator write to the whole Tor network.
func TestR2_AuthOffNodeRefusesToPublishItsAdminOverTor(t *testing.T) {
	for name, target := range map[string]string{
		"default target":        "",
		"the admin listener":    "127.0.0.1:5001",
		"an alias of it":        "localhost:5001",
		"a port-only target":    ":5001",
		"a wildcard of it":      "0.0.0.0:5001",
		"the LAN address of it": "192.168.1.5:5001",
	} {
		cfg := &config.Config{}
		cfg.Admin.Enabled = true
		cfg.Admin.RequireAuth = false
		cfg.Admin.ListenAddr = "127.0.0.1:5001"
		cfg.Tor.Enabled = true
		cfg.Tor.HiddenServiceEnabled = true
		cfg.Tor.HiddenServiceTarget = target
		if err := adminListenerSafety(cfg); err == nil {
			t.Fatalf("%s: an auth-off admin surface behind the onion service was accepted", name)
		}
	}

	// A hidden service for some OTHER local service is not the admin surface.
	cfg := &config.Config{}
	cfg.Admin.Enabled = true
	cfg.Admin.RequireAuth = false
	cfg.Admin.ListenAddr = "127.0.0.1:5001"
	cfg.Tor.Enabled = true
	cfg.Tor.HiddenServiceEnabled = true
	cfg.Tor.HiddenServiceTarget = "127.0.0.1:8088"
	if err := adminListenerSafety(cfg); err != nil {
		t.Fatalf("an onion service for another port was refused: %v", err)
	}
}

// A flow's own route declaration (or gateway.anonymous.allow) could open a
// */query SQL route to anonymous callers: the wall consulted the anonymous
// policy before the SQL rule.
func TestR2_SQLRouteIsNeverAnonymous(t *testing.T) {
	reached := false
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	everythingAnonymous := func(string, string) bool { return true }
	for _, requireAuth := range []bool{true, false} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, path := range []string{"/api/v1/data/query", "/api/v1/data/omm/query", "/api/v1/data/omm/QUERY/"} {
				reached = false
				r := httptest.NewRequest(method, path, strings.NewReader(`{"sql":"SELECT 1"}`))
				r.RemoteAddr = "203.0.113.9:4000"
				w := httptest.NewRecorder()
				serveAdminMuxRequest(w, r, mux, requireAuth, false, nil, everythingAnonymous, nil, true)
				if reached || w.Code == http.StatusOK {
					t.Fatalf("require_auth=%v %s %s: an anonymous caller reached the SQL route (%d)", requireAuth, method, path, w.Code)
				}
			}
		}
	}
}
