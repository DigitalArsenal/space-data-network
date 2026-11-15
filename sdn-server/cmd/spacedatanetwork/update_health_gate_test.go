package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// STACK ISSUE 670. The helper's health gate asks the daemon's readiness
// surface, served here by the real mountHealthRoutes, and judges it by the
// status code alone. The data route answers what it answers in production —
// a $QRP FlatBuffer — and the gate must not care: before the fix it decoded
// that route as JSON, failed every time, and reverted every update.
func TestHelperHealthGateAsksReadyAndJudgesTheStatusCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deps    healthDeps
		wantErr string
	}{
		{name: "ready", deps: healthDeps{engineReady: func() bool { return true }, peerCount: func() int { return 0 }}},
		{name: "engine not answering", deps: healthDeps{engineReady: func() bool { return false }, peerCount: func() int { return 1 }}, wantErr: "engine not answering"},
		{name: "data services warming", deps: healthDeps{engineReady: func() bool { return true }, dataReady: func() bool { return false }, peerCount: func() int { return 1 }}, wantErr: "data services warming"},
		{name: "host down", deps: healthDeps{engineReady: func() bool { return true }, peerCount: func() int { return -1 }}, wantErr: "libp2p host down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mountHealthRoutes(mux, tc.deps)
			mux.HandleFunc("/api/v1/data/health", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.sdn.flatbuffers.stream")
				_, _ = w.Write([]byte{0x10, 0, 0, 0, '$', 'Q', 'R', 'P'})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			err := probeDaemonHealth(context.Background(), srv.Client(), srv.URL)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("a ready daemon failed the gate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("gate error = %v, want a 503 naming %q", err, tc.wantErr)
			}
		})
	}
}

// `update install` hands its helper the node's update.health_timeout_seconds
// (default 600 s), the budget the signal lane already uses, instead of letting
// the helper fall to a bare 60 s that a node warming its data services for
// minutes cannot meet. An explicit --health-timeout still wins.
func TestUpdateInstallHandsTheHelperTheNodeHealthBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		flag   time.Duration
		want   string
	}{
		{name: "default budget", config: "", want: "10m0s"},
		{name: "config budget", config: "update:\n  health_timeout_seconds: 900\n", want: "15m0s"},
		{name: "flag wins", config: "update:\n  health_timeout_seconds: 900\n", flag: 90 * time.Second, want: "1m30s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgFile := filepath.Join(dir, "config.yaml")
			body := "admin:\n  enabled: true\n  listen_addr: \"127.0.0.1:1\"\n  tls_mode: disabled\n" + tc.config
			if err := os.WriteFile(cfgFile, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			savedConfig, savedFlag := configPath, updateInstallHealthTimeout
			configPath, updateInstallHealthTimeout = cfgFile, tc.flag
			t.Cleanup(func() { configPath, updateInstallHealthTimeout = savedConfig, savedFlag })

			plan, err := prepareUpdateHelper(update.PathsFor(filepath.Join(dir, "bundle")), "e2e-v2", "token", "")
			if err != nil {
				t.Fatalf("prepareUpdateHelper: %v", err)
			}
			i := slices.Index(plan.Args, "--health-timeout")
			if i < 0 || i+1 >= len(plan.Args) || plan.Args[i+1] != tc.want {
				t.Fatalf("helper args %v: want --health-timeout %s", plan.Args, tc.want)
			}
		})
	}
}
