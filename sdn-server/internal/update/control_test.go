package update

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
)

func TestControlHandlerAcceptsLoopbackOneTimeToken(t *testing.T) {
	root := t.TempDir()
	paths := PathsFor(root)
	if err := WriteControlToken(paths, "token-123"); err != nil {
		t.Fatalf("WriteControlToken failed: %v", err)
	}

	shutdown := make(chan struct{}, 1)
	handler := NewControlHandler(ControlHandlerOptions{
		BundleRoot: root,
		Shutdown: func() {
			shutdown <- struct{}{}
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "127.0.0.1:43123"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(controlTokenPath(paths)); !os.IsNotExist(err) {
		t.Fatalf("control token should be consumed, stat err=%v", err)
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

// TestControlHandlerReportsSystemdSupervision is the regression test for
// sdn-update-helper-supervisor-mode plus the preflight refusal
// (ops-update-lane-restart-policy-preflight): the shutdown response must tell
// the helper whether THIS daemon was started by systemd (INVOCATION_ID) and,
// when it was, the EXACT unit and Restart= policy (hostsvc.Probe), because
// the helper cannot infer any of it from its own environment. A supervised
// daemon whose unit or policy cannot be resolved is REFUSED with the token
// left unconsumed so a corrected retry needs no fresh launch.
func TestControlHandlerReportsSystemdSupervision(t *testing.T) {
	resolvedState := func(unit, policy string) hostsvc.State {
		return hostsvc.State{
			Supervisor:    "systemd",
			Unit:          unit,
			ActiveState:   "active",
			SubState:      "running",
			Autostart:     "enabled",
			RestartPolicy: policy,
			Detected:      true,
		}
	}
	for _, tc := range []struct {
		name           string
		invocationID   string
		probe          func(ctx context.Context) hostsvc.State
		wantCode       int
		wantSupervised bool
		wantUnit       string
		wantPolicy     string
	}{
		{
			name:           "unsupervised (no INVOCATION_ID)",
			invocationID:   "",
			probe:          func(ctx context.Context) hostsvc.State { return resolvedState("", "") },
			wantCode:       http.StatusAccepted,
			wantSupervised: false,
		},
		{
			name:           "supervised resolves unit and policy",
			invocationID:   "abc123deadbeef",
			probe:          func(ctx context.Context) hostsvc.State { return resolvedState("sdn-daemon.service", "always") },
			wantCode:       http.StatusAccepted,
			wantSupervised: true,
			wantUnit:       "sdn-daemon.service",
			wantPolicy:     "always",
		},
		{
			name:         "supervised but unit and policy unknown is a refusal",
			invocationID: "abc123deadbeef",
			probe:        func(ctx context.Context) hostsvc.State { return hostsvc.State{} },
			wantCode:     http.StatusServiceUnavailable,
		},
		{
			name:         "supervised with unit but no policy is a refusal",
			invocationID: "abc123deadbeef",
			probe:        func(ctx context.Context) hostsvc.State { return resolvedState("sdn-daemon.service", "") },
			wantCode:     http.StatusServiceUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", tc.invocationID)

			root := t.TempDir()
			paths := PathsFor(root)
			if err := WriteControlToken(paths, "token-123"); err != nil {
				t.Fatalf("WriteControlToken failed: %v", err)
			}
			shutdownCalled := make(chan struct{}, 1)
			handler := NewControlHandler(ControlHandlerOptions{
				BundleRoot: root,
				Probe:      tc.probe,
				Shutdown: func() {
					shutdownCalled <- struct{}{}
				},
			})

			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Supervised    bool   `json:"supervised"`
				Unit          string `json:"unit"`
				RestartPolicy string `json:"restartPolicy"`
			}
			if tc.wantCode == http.StatusAccepted {
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
				}
				if response.Supervised != tc.wantSupervised {
					t.Fatalf("supervised = %v, want %v (body=%s)", response.Supervised, tc.wantSupervised, rec.Body.String())
				}
				if response.Unit != tc.wantUnit || response.RestartPolicy != tc.wantPolicy {
					t.Fatalf("response unit=%q restart=%q, want unit=%q restart=%q", response.Unit, response.RestartPolicy, tc.wantUnit, tc.wantPolicy)
				}
				select {
				case <-shutdownCalled:
				case <-time.After(time.Second):
					t.Fatal("shutdown callback should have been called on 202")
				}
			} else {
				// REFUSED: the one-time token must NOT be consumed (a fixed retry
				// needs no fresh launch) and nothing may shut down.
				if _, err := os.Stat(controlTokenPath(paths)); err != nil {
					t.Fatalf("control token must remain on refusal, stat err=%v", err)
				}
				select {
				case <-shutdownCalled:
					t.Fatal("shutdown callback must not run on refusal")
				case <-time.After(200 * time.Millisecond):
				}
				if !strings.Contains(rec.Body.String(), "could not be resolved") {
					t.Fatalf("refusal body should name the resolution failure: %s", rec.Body.String())
				}
			}
		})
	}
}

// TestControlHandlerRestartPolicyTable pins the preflight behavior across the
// policies that mattered in the live incident (ops-update-lane-restart-policy-
// preflight): the endpoint answers 202 and echoes the policy for EVERY known
// Restart= value — the helper must not branch its restart plan on the policy
// (a clean exit is a STOP under on-failure and no, and under always a clean
// exit feeds the activating lock-loop), so the daemon's job is to resolve and
// report it, never to accept it as a respawn guarantee.
func TestControlHandlerRestartPolicyTable(t *testing.T) {
	for _, policy := range []string{"always", "on-failure", "no", "on-success", "on-abnormal", "restart-if-healthy"} {
		t.Run(policy, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", "pol-"+policy)
			root := t.TempDir()
			if err := WriteControlToken(PathsFor(root), "token-123"); err != nil {
				t.Fatal(err)
			}
			handler := NewControlHandler(ControlHandlerOptions{
				BundleRoot: root,
				Probe: func(ctx context.Context) hostsvc.State {
					return hostsvc.State{Supervisor: "systemd", Unit: "sdn-daemon.service", RestartPolicy: policy, Autostart: "enabled", Detected: true}
				},
				Shutdown: func() {},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("policy %s: status = %d body=%s (a known policy must never be refused)", policy, rec.Code, rec.Body.String())
			}
			var response struct {
				Unit          string `json:"unit"`
				RestartPolicy string `json:"restartPolicy"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Unit != "sdn-daemon.service" || response.RestartPolicy != policy {
				t.Fatalf("unit=%q restart=%q, want sdn-daemon.service / %q", response.Unit, response.RestartPolicy, policy)
			}
		})
	}
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestControlHandlerRejectsNonLoopback(t *testing.T) {
	root := t.TempDir()
	if err := WriteControlToken(PathsFor(root), "token-123"); err != nil {
		t.Fatal(err)
	}
	handler := NewControlHandler(ControlHandlerOptions{BundleRoot: root, Shutdown: func() {}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "203.0.113.9:43123"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestControlHandlerRejectsWrongTokenAndBundleRoot(t *testing.T) {
	root := t.TempDir()
	if err := WriteControlToken(PathsFor(root), "token-123"); err != nil {
		t.Fatal(err)
	}
	handler := NewControlHandler(ControlHandlerOptions{BundleRoot: root, Shutdown: func() { t.Fatal("shutdown should not be called") }})

	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{
			name: "wrong token",
			body: `{"token":"wrong","bundleRoot":"` + filepath.ToSlash(root) + `"}`,
			want: http.StatusForbidden,
		},
		{
			name: "wrong bundle root",
			body: `{"token":"token-123","bundleRoot":"` + filepath.ToSlash(filepath.Join(root, "other")) + `"}`,
			want: http.StatusConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", strings.NewReader(tc.body))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
