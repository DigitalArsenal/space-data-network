package update

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
// sdn-update-helper-supervisor-mode: the shutdown response must tell the
// helper whether THIS daemon was started by systemd (INVOCATION_ID), since
// the helper cannot infer that from its own environment. Under the
// ops-update-lane-restart-policy-preflight gate it also carries the RESOLVED
// owning unit and its Restart= policy — the exact unit the helper must start
// after the swap.
func TestControlHandlerReportsSystemdSupervision(t *testing.T) {
	for _, tc := range []struct {
		name           string
		invocationID   string
		wantSupervised bool
		unit           string
		policy         string
	}{
		{name: "unsupervised (no INVOCATION_ID)", invocationID: "", wantSupervised: false},
		{name: "supervised (systemd sets INVOCATION_ID)", invocationID: "abc123deadbeef", wantSupervised: true, unit: "space-data-network.service", policy: "always"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", tc.invocationID)

			root := t.TempDir()
			paths := PathsFor(root)
			if err := WriteControlToken(paths, "token-123"); err != nil {
				t.Fatalf("WriteControlToken failed: %v", err)
			}
			ctrl := NewControlHandler(ControlHandlerOptions{
				BundleRoot: root,
				Shutdown:   func() {},
			}).(*controlHandler)
			ctrl.probe = func() (unit string, restartPolicy string) {
				return tc.unit, tc.policy
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			ctrl.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"supervised":`+boolJSON(tc.wantSupervised)) {
				t.Fatalf("body = %s, want supervised=%v", rec.Body.String(), tc.wantSupervised)
			}
			if tc.wantSupervised {
				if !strings.Contains(rec.Body.String(), `"unit":"`+tc.unit+`"`) {
					t.Fatalf("body = %s, want resolved unit %q", rec.Body.String(), tc.unit)
				}
				if !strings.Contains(rec.Body.String(), `"restartPolicy":"`+tc.policy+`"`) {
					t.Fatalf("body = %s, want restartPolicy %q", rec.Body.String(), tc.policy)
				}
			} else if strings.Contains(rec.Body.String(), `"restartPolicy"`) {
				t.Fatalf("body = %s, unsupervised daemon must not report a restart policy", rec.Body.String())
			}
		})
	}
}

// TestControlHandlerRefusesUnsafeRestartPolicy is the daemon-side half of
// ops-update-lane-restart-policy-preflight: a supervised daemon whose owning
// unit does not promise to bring it back (Restart=no, Restart=on-success, …)
// must REFUSE the shutdown with an actionable message and keep serving. A
// clean exit under those policies leaves the box down — the exact incident
// this gate exists to prevent.
func TestControlHandlerRefusesUnsafeRestartPolicy(t *testing.T) {
	for _, policy := range []string{"no", "on-success", "on-abnormal"} {
		t.Run("restart="+policy, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", "abc123deadbeef")

			root := t.TempDir()
			paths := PathsFor(root)
			if err := WriteControlToken(paths, "token-123"); err != nil {
				t.Fatalf("WriteControlToken failed: %v", err)
			}
			shutdownCalled := false
			ctrl := NewControlHandler(ControlHandlerOptions{
				BundleRoot: root,
				Shutdown: func() {
					shutdownCalled = true
				},
			}).(*controlHandler)
			ctrl.probe = func() (unit string, restartPolicy string) {
				return "space-data-network.service", policy
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
			req.RemoteAddr = "127.0.0.1:43123"
			rec := httptest.NewRecorder()

			ctrl.ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%s, want 409 refusal", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "Restart=") {
				t.Fatalf("refusal body = %s, want the policy named", rec.Body.String())
			}
			if shutdownCalled {
				t.Fatal("shutdown was scheduled despite an unsafe Restart= policy")
			}
		})
	}
}

// TestControlHandlerRefusesUnknownUnitState covers the fail-closed refusal: a
// daemon that reports INVOCATION_ID but whose cgroup/MainPID resolution finds
// no owning unit is exactly as dangerous as Restart=no — the lane cannot
// prove any supervisor will bring the box back — so the shutdown is refused.
func TestControlHandlerRefusesUnknownUnitState(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123deadbeef")

	root := t.TempDir()
	paths := PathsFor(root)
	if err := WriteControlToken(paths, "token-123"); err != nil {
		t.Fatalf("WriteControlToken failed: %v", err)
	}
	shutdownCalled := false
	ctrl := NewControlHandler(ControlHandlerOptions{
		BundleRoot: root,
		Shutdown: func() {
			shutdownCalled = true
		},
	}).(*controlHandler)
	ctrl.probe = func() (unit string, restartPolicy string) {
		return "", ""
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "127.0.0.1:43123"
	rec := httptest.NewRecorder()

	ctrl.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409 refusal", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "could not be resolved") {
		t.Fatalf("refusal body = %s, want the unresolvable unit named", rec.Body.String())
	}
	if shutdownCalled {
		t.Fatal("shutdown was scheduled despite an unresolved owning unit")
	}
}

// TestControlHandlerAcceptsSafeOnFailurePolicy complements the unsafe table:
// Restart=on-failure is safe because the helper starts the resolved unit
// explicitly after the swap (it never relies on the policy alone).
func TestControlHandlerAcceptsSafeOnFailurePolicy(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123deadbeef")

	root := t.TempDir()
	paths := PathsFor(root)
	if err := WriteControlToken(paths, "token-123"); err != nil {
		t.Fatalf("WriteControlToken failed: %v", err)
	}
	shutdown := make(chan struct{}, 1)
	ctrl := NewControlHandler(ControlHandlerOptions{
		BundleRoot: root,
		Shutdown: func() {
			shutdown <- struct{}{}
		},
	}).(*controlHandler)
	ctrl.probe = func() (unit string, restartPolicy string) {
		return "space-data-network.service", "on-failure"
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "127.0.0.1:43123"
	rec := httptest.NewRecorder()

	ctrl.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want accepted", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"restartPolicy":"on-failure"`) {
		t.Fatalf("body = %s, want restartPolicy on-failure", rec.Body.String())
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown was not scheduled for the safe on-failure policy")
	}
}

// TestControlHandlerUnsupervisedIgnoresUnitResolution proves the gate only
// applies to supervised daemons: without INVOCATION_ID the helper owns the
// restart itself (direct spawn), so even a hostile/meaningless probe result
// must not block an accepted shutdown.
func TestControlHandlerUnsupervisedIgnoresUnitResolution(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")

	root := t.TempDir()
	paths := PathsFor(root)
	if err := WriteControlToken(paths, "token-123"); err != nil {
		t.Fatalf("WriteControlToken failed: %v", err)
	}
	shutdown := make(chan struct{}, 1)
	ctrl := NewControlHandler(ControlHandlerOptions{
		BundleRoot: root,
		Shutdown: func() {
			shutdown <- struct{}{}
		},
	}).(*controlHandler)
	ctrl.probe = func() (unit string, restartPolicy string) {
		return "some-other.service", "no"
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", bytes.NewBufferString(`{"token":"token-123","bundleRoot":"`+filepath.ToSlash(root)+`"}`))
	req.RemoteAddr = "127.0.0.1:43123"
	rec := httptest.NewRecorder()

	ctrl.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want accepted for unsupervised daemon", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"supervised":false`) {
		t.Fatalf("body = %s, want supervised:false", rec.Body.String())
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown was not scheduled for the unsupervised daemon")
	}
}

// TestLaunchSelfUpgradeRefusesUnsafeRestartPolicy is the pre-flight half of
// the same gate: LaunchSelfUpgrade must refuse WITH NO SIDE EFFECTS when the
// box is supervised with an unsafe policy, so a signal-driven run fails in
// the daemon's own log instead of spawning a transient unit that the control
// endpoint then refuses.
func TestLaunchSelfUpgradeRefusesUnsafeRestartPolicy(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123deadbeef")
	orig := supervisionProbe
	defer func() { supervisionProbe = orig }()
	supervisionProbe = func() (unit string, restartPolicy string) {
		return "space-data-network.service", "no"
	}

	_, err := LaunchSelfUpgrade(PathsFor(t.TempDir()), SelfUpgradeOptions{
		UpdateID: "cli-bundle-beta-105",
	})
	if err == nil {
		t.Fatal("LaunchSelfUpgrade accepted an unsafe Restart=no policy")
	}
	if !strings.Contains(err.Error(), "Restart=") {
		t.Fatalf("refusal = %q, want the policy named", err.Error())
	}
}

// TestLaunchSelfUpgradeProceedsOnSafeRestartPolicy proves the pre-flight lets
// a safe policy through: with Restart=always the launch proceeds past the
// gate (a harmless stub executable is used so the detached launch is cheap
// and exits on its own) — the refusal only ever fires for unsafe/unknown
// policies.
func TestLaunchSelfUpgradeProceedsOnSafeRestartPolicy(t *testing.T) {
	t.Setenv("INVOCATION_ID", "abc123deadbeef")
	orig := supervisionProbe
	defer func() { supervisionProbe = orig }()
	supervisionProbe = func() (unit string, restartPolicy string) {
		return "space-data-network.service", "always"
	}
	src := filepath.Join(t.TempDir(), "fake-sdn-bin")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	launch, err := LaunchSelfUpgrade(PathsFor(t.TempDir()), SelfUpgradeOptions{
		UpdateID:         "cli-bundle-beta-105",
		SourceExecutable: src,
	})
	if err != nil {
		t.Fatalf("LaunchSelfUpgrade refused a safe policy: %v", err)
	}
	if launch == nil {
		t.Fatal("LaunchSelfUpgrade returned no launch for a safe policy")
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
