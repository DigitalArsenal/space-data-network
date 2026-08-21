package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/hostsvc"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

func TestLoadBundleManifestAcceptsSignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature",
		"update": {
			"feedBaseUrl": "https://sdn.spaceaware.io/updates",
			"pubsubTopic": "/sdn/updates/v1/beta",
			"updaterModule": "org.spacedatanetwork.updater",
			"updaterWasm": "runtime/modules/org.spacedatanetwork.updater.wasm"
		}
	}`)

	manifest, err := loadBundleManifest(path)
	if err != nil {
		t.Fatalf("loadBundleManifest returned error: %v", err)
	}
	if manifest.Version != "1.2.3" {
		t.Fatalf("Version = %q, want 1.2.3", manifest.Version)
	}
	if manifest.Channel != "beta" {
		t.Fatalf("Channel = %q, want beta", manifest.Channel)
	}
	if manifest.Update.FeedBaseURL != "https://sdn.spaceaware.io/updates" ||
		manifest.Update.PubsubTopic != "/sdn/updates/v1/beta" ||
		manifest.Update.UpdaterModule != "org.spacedatanetwork.updater" ||
		manifest.Update.UpdaterWASM != "runtime/modules/org.spacedatanetwork.updater.wasm" {
		t.Fatalf("Update metadata = %#v", manifest.Update)
	}
}

func TestLoadBundleManifestRejectsMissingUpdateMetadata(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without update metadata")
	}
}

func TestLoadBundleManifestRejectsUnsignedManifest(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": "1.2.3",
		"channel": "beta"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted an unsigned manifest")
	}
}

func TestLoadBundleManifestRejectsMissingVersion(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest without version")
	}
}

func TestLoadBundleManifestRejectsWhitespaceRequiredFields(t *testing.T) {
	path := writeBundleManifest(t, `{
		"schema": "org.spacedatanetwork.bundle.v1",
		"version": " ",
		"channel": "beta",
		"signature": "test-signature"
	}`)

	_, err := loadBundleManifest(path)
	if err == nil {
		t.Fatal("loadBundleManifest accepted a manifest with whitespace-only version")
	}
}

func TestProviderFeedIndexURLUsesCLIBundlePath(t *testing.T) {
	got, err := providerFeedIndexURL("https://sdn.spaceaware.io/updates/", "beta", "linux", "amd64")
	if err != nil {
		t.Fatalf("providerFeedIndexURL returned error: %v", err)
	}
	want := "https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64/index.json"
	if got != want {
		t.Fatalf("providerFeedIndexURL = %q, want %q", got, want)
	}
}

func TestProviderFeedIndexURLRejectsHTTP(t *testing.T) {
	_, err := providerFeedIndexURL("http://sdn.spaceaware.io/updates", "beta", "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("providerFeedIndexURL error = %v, want HTTPS rejection", err)
	}
}

func TestFetchProviderUpdateCandidateSelectsCompatibleUpdate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-bundle/beta/"+runtime.GOOS+"/"+runtime.GOARCH+"/index.json" {
			t.Fatalf("request path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema": "org.spacedatanetwork.update.index.v1",
			"generated_at": "2026-06-22T00:00:00Z",
			"feed_base_url": "` + serverURLPlaceholder + `",
			"updates": [
				{
					"update_id": "cli-bundle-beta-older",
					"version": "1.0.4",
					"sequence": 104,
					"channel": "beta",
					"target": {"platform": "` + runtime.GOOS + `", "arch": "` + runtime.GOARCH + `", "kind": "cli-bundle"},
					"manifest_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.4/manifest.json",
					"carrier_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.4/update.wasm"
				},
				{
					"update_id": "cli-bundle-beta-newer",
					"version": "1.0.5",
					"sequence": 105,
					"channel": "beta",
					"target": {"platform": "` + runtime.GOOS + `", "arch": "` + runtime.GOARCH + `", "kind": "cli-bundle"},
					"manifest_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.5/manifest.json",
					"carrier_url": "` + serverURLPlaceholder + `/cli-bundle/beta/` + runtime.GOOS + `/` + runtime.GOARCH + `/1.0.5/update.wasm"
				}
			]
		}`))
	}))
	defer server.Close()

	update, err := fetchProviderUpdateCandidate(server.Client(), bundleManifest{
		Version: "1.0.4",
		Channel: "beta",
		Update:  bundleUpdateMetadata{FeedBaseURL: server.URL},
	}, 104, providerUpdateFilter{})
	if err != nil {
		t.Fatalf("fetchProviderUpdateCandidate returned error: %v", err)
	}
	if update.UpdateID != "cli-bundle-beta-newer" {
		t.Fatalf("selected update = %s, want cli-bundle-beta-newer", update.UpdateID)
	}
}

func TestReadHTTPSURLRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer server.Close()

	_, err := readHTTPSURL(server.Client(), server.URL, 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("readHTTPSURL error = %v, want size rejection", err)
	}
}

func TestHelperPostApplyRestartRollsBackWhenDaemonHealthFails(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	var starts [][]string
	var killed int
	var rolledBack bool

	healthAttempts := 0
	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:       update.PathsFor(t.TempDir()),
		RestartArgv: []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		AdminURL:    "http://127.0.0.1:5080",
		Out:         &out,
		Err:         &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			starts = append(starts, append([]string(nil), argv...))
			return fakeHelperProcess{
				pid: 1000 + len(starts),
				kill: func() error {
					killed++
					return nil
				},
			}, nil
		},
		WaitHealth: func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error {
			healthAttempts++
			if healthAttempts == 1 {
				return errors.New("new daemon health failed")
			}
			return nil
		},
		Rollback: func(paths update.Paths) (*update.RollbackResult, error) {
			rolledBack = true
			return &update.RollbackResult{
				RestoredVersion: "1.0.0",
				FailedPath:      filepath.Join(paths.Failed, "cli-bundle-beta-bad"),
			}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("helperPostApplyRestart error = %v, want rollback error", err)
	}
	if len(starts) != 2 {
		t.Fatalf("starts = %#v, want failed daemon start and rollback restart", starts)
	}
	if killed != 1 {
		t.Fatalf("killed failed daemon %d times, want 1", killed)
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked after daemon health failure")
	}
	if !strings.Contains(out.String(), "restart=started pid=1001") ||
		!strings.Contains(out.String(), "rollback=applied restored_version=1.0.0") ||
		!strings.Contains(out.String(), "restart=started pid=1002") {
		t.Fatalf("stdout = %q, want restart and rollback status", out.String())
	}
	if !strings.Contains(errOut.String(), "daemon_health=unhealthy") {
		t.Fatalf("stderr = %q, want unhealthy daemon health", errOut.String())
	}
}

// TestHelperPostApplyRestartSupervisedNeverSpawns is the regression test for
// sdn-update-helper-supervisor-mode plus the preflight fix
// (ops-update-lane-restart-policy-preflight): a supervised daemon is
// restarted by an EXPLICIT systemctl restart of its resolved unit — never a
// StartDaemon (a direct spawn escapes the unit's cgroup while the unit loops
// "activating" against the store lock) and never a bare wait on Restart=
// (under on-failure/no a clean exit is a STOP, live incident 2026-08-08).
func TestHelperPostApplyRestartSupervisedNeverSpawns(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	var restarted []string

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Unit:          "sdn-daemon.service",
		RestartPolicy: "always",
		AdminURL:      "http://127.0.0.1:5080",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		RestartUnit: func(unit string) error {
			restarted = append(restarted, unit)
			return nil
		},
		WaitHealth: func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("helperPostApplyRestart (supervised) error = %v", err)
	}
	if spawned {
		t.Fatal("StartDaemon was called under a resolved unit — this is the escapes-systemd bug")
	}
	if len(restarted) != 1 || restarted[0] != "sdn-daemon.service" {
		t.Fatalf("unit restarted = %#v, want exactly [sdn-daemon.service]", restarted)
	}
	if !strings.Contains(out.String(), "restart=supervised unit=sdn-daemon.service policy=always") {
		t.Fatalf("stdout = %q, want the resolved unit and policy on the restart line", out.String())
	}
	if !strings.Contains(out.String(), "restart=requested unit=sdn-daemon.service") {
		t.Fatalf("stdout = %q, want the explicit unit restart", out.String())
	}
	if !strings.Contains(out.String(), "daemon_health=healthy") {
		t.Fatalf("stdout = %q, want daemon_health=healthy", out.String())
	}
}

// TestHelperPostApplyRestartPolicyTable pins the restart flow across the
// policies from the live incident (ops-update-lane-restart-policy-preflight):
// EVERY known policy gets the explicit unit restart — the helper must not
// branch on the policy, because Restart=always turns a clean exit into an
// activating lock-loop and Restart=on-failure/no turn it into a STOP.
func TestHelperPostApplyRestartPolicyTable(t *testing.T) {
	for _, tc := range []struct {
		policy string
	}{
		{policy: "always"},
		{policy: "on-failure"},
		{policy: "no"},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			var out bytes.Buffer
			var errOut bytes.Buffer
			spawned := false
			var restarted []string

			err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
				Paths:         update.PathsFor(t.TempDir()),
				RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
				Unit:          "sdn-daemon.service",
				RestartPolicy: tc.policy,
				AdminURL:      "http://127.0.0.1:5080",
				Out:           &out,
				Err:           &errOut,
				StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
					spawned = true
					return fakeHelperProcess{pid: 9999}, nil
				},
				RestartUnit: func(unit string) error {
					restarted = append(restarted, unit)
					return nil
				},
				WaitHealth: func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error {
					return nil
				},
			})
			if err != nil {
				t.Fatalf("policy %s: error = %v", tc.policy, err)
			}
			if spawned {
				t.Fatalf("policy %s: StartDaemon was called — the restart must go through the unit", tc.policy)
			}
			if len(restarted) != 1 {
				t.Fatalf("policy %s: unit restarts = %#v, want 1", tc.policy, restarted)
			}
			if !strings.Contains(out.String(), "restart=supervised unit=sdn-daemon.service policy="+tc.policy) {
				t.Fatalf("policy %s: stdout = %q, want the policy echoed", tc.policy, out.String())
			}
		})
	}
}

// TestHelperPostApplyRestartSupervisedRollsBackWithoutSpawning proves the
// unhealthy-then-rollback branch also goes through the resolved unit: roll
// the bundle back, restart the SAME unit (its binary is now the previous
// slot), health-wait — and never StartDaemon.
func TestHelperPostApplyRestartSupervisedRollsBackWithoutSpawning(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	rolledBack := false
	var restarted []string
	healthAttempts := 0

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Unit:          "sdn-daemon.service",
		RestartPolicy: "on-failure",
		AdminURL:      "http://127.0.0.1:5080",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		RestartUnit: func(unit string) error {
			restarted = append(restarted, unit)
			return nil
		},
		WaitHealth: func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error {
			healthAttempts++
			if healthAttempts == 1 {
				return errors.New("new daemon health failed")
			}
			return nil
		},
		Rollback: func(paths update.Paths) (*update.RollbackResult, error) {
			rolledBack = true
			return &update.RollbackResult{
				RestoredVersion: "1.0.0",
				FailedPath:      filepath.Join(paths.Failed, "cli-bundle-beta-bad"),
			}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("helperPostApplyRestart (supervised) error = %v, want rollback error", err)
	}
	if spawned {
		t.Fatal("StartDaemon was called under a resolved unit during rollback — this is the escapes-systemd bug")
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked after supervised daemon health failure")
	}
	if len(restarted) != 2 {
		t.Fatalf("unit restarts = %#v, want 2 (initial + post-rollback)", restarted)
	}
	if healthAttempts != 2 {
		t.Fatalf("health attempts = %d, want 2 (initial + post-rollback)", healthAttempts)
	}
	if !strings.Contains(errOut.String(), "daemon_health=unhealthy") {
		t.Fatalf("stderr = %q, want unhealthy daemon health", errOut.String())
	}
}

// TestHelperPostApplyRestartSupervisedExplicitStartFailure covers the
// explicit-start-failure branch of the task: systemctl refuses the restart of
// the resolved unit. The helper rolls back and RESTARTS the unit again; when
// the retry also fails the error names the rollback and the failure.
func TestHelperPostApplyRestartSupervisedExplicitStartFailure(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	rolledBack := false
	restartAttempts := 0

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Unit:          "sdn-daemon.service",
		RestartPolicy: "no",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		RestartUnit: func(unit string) error {
			restartAttempts++
			return errors.New("Failed to restart unit: Connection timed out")
		},
		Rollback: func(paths update.Paths) (*update.RollbackResult, error) {
			rolledBack = true
			return &update.RollbackResult{
				RestoredVersion: "1.0.0",
				FailedPath:      filepath.Join(paths.Failed, "cli-bundle-beta-bad"),
			}, nil
		},
	})
	if err == nil {
		t.Fatal("helperPostApplyRestart (supervised) error = nil, want start-failure error")
	}
	if !strings.Contains(err.Error(), "restarting unit sdn-daemon.service failed") ||
		!strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("error = %v, want unit start failure and rollback failure named", err)
	}
	if spawned {
		t.Fatal("StartDaemon was called — the restart must go through the unit")
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked after the explicit unit start failure")
	}
	if restartAttempts != 2 {
		t.Fatalf("restart attempts = %d, want 2 (initial + post-rollback)", restartAttempts)
	}
	if !strings.Contains(errOut.String(), "restart_daemon_unit=error") {
		t.Fatalf("stderr = %q, want the unit start failure recorded", errOut.String())
	}
}

// TestHelperPostApplyRestartSupervisedRollbackSucceeds covers the successful
// rollback leg: the explicit unit start fails, the rollback restores the
// previous slot, and the retried unit restart comes up healthy.
func TestHelperPostApplyRestartSupervisedRollbackSucceeds(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	rolledBack := false
	restartAttempts := 0

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Unit:          "sdn-daemon.service",
		RestartPolicy: "on-failure",
		AdminURL:      "http://127.0.0.1:5080",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		RestartUnit: func(unit string) error {
			restartAttempts++
			if restartAttempts == 1 {
				return errors.New("Job for sdn-daemon.service failed")
			}
			return nil
		},
		WaitHealth: func(ctx context.Context, client *http.Client, adminURL string, timeout time.Duration) error {
			return nil
		},
		Rollback: func(paths update.Paths) (*update.RollbackResult, error) {
			rolledBack = true
			return &update.RollbackResult{
				RestoredVersion: "1.0.0",
				FailedPath:      filepath.Join(paths.Failed, "cli-bundle-beta-bad"),
			}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back to 1.0.0") {
		t.Fatalf("helperPostApplyRestart (supervised) error = %v, want rolled-back error", err)
	}
	if spawned {
		t.Fatal("StartDaemon was called during rollback — this is the escapes-systemd bug")
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked")
	}
	if restartAttempts != 2 {
		t.Fatalf("restart attempts = %d, want 2", restartAttempts)
	}
	if !strings.Contains(out.String(), "rollback=applied restored_version=1.0.0") ||
		!strings.Contains(out.String(), "restart=requested unit=sdn-daemon.service restored_version=1.0.0") ||
		!strings.Contains(out.String(), "daemon_health=healthy") {
		t.Fatalf("stdout = %q, want rollback + retried unit restart + healthy", out.String())
	}
}

// TestHelperPostApplyRestartUnitRestartNotRequestedWhenNoRestart pins
// --no-restart: even with a resolved unit, the operator's explicit
// no-restart wins and nothing is asked of systemd.
func TestHelperPostApplyRestartUnitRestartNotRequestedWhenNoRestart(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	restarted := false

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Unit:          "sdn-daemon.service",
		RestartPolicy: "always",
		NoRestart:     true,
		Out:           &out,
		Err:           &errOut,
		RestartUnit: func(unit string) error {
			restarted = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("helperPostApplyRestart (no-restart) error = %v", err)
	}
	if restarted {
		t.Fatal("the unit was restarted despite --no-restart")
	}
	if !strings.Contains(out.String(), "restart=manual") {
		t.Fatalf("stdout = %q, want restart=manual", out.String())
	}
}

func withProbeHost(t *testing.T, probe func(ctx context.Context) hostsvc.State) {
	t.Helper()
	prev := probeHost
	probeHost = probe
	t.Cleanup(func() { probeHost = prev })
}

func withLaunchFacts(t *testing.T, unit, policy string) {
	t.Helper()
	prevUnit, prevPolicy := helperApplyDaemonUnit, helperApplyDaemonRestartPolicy
	helperApplyDaemonUnit, helperApplyDaemonRestartPolicy = unit, policy
	t.Cleanup(func() { helperApplyDaemonUnit, helperApplyDaemonRestartPolicy = prevUnit, prevPolicy })
}

func shutdownInfo(supervised bool, unit, policy string) *daemonShutdownInfo {
	return &daemonShutdownInfo{Supervised: supervised, Unit: unit, RestartPolicy: policy}
}

// TestResolveHelperSupervisorPlanUnknownUnitState covers the "unknown unit
// state" requirement (ops-update-lane-restart-policy-preflight): a daemon
// that reports itself supervised without a resolved unit or Restart= policy
// must fail the apply BEFORE any swap — under Restart=on-failure/no a clean
// exit is a STOP and the box stays down (live incident 2026-08-08).
func TestResolveHelperSupervisorPlanUnknownUnitState(t *testing.T) {
	withProbeHost(t, func(ctx context.Context) hostsvc.State {
		return hostsvc.State{Unit: "sdn-self-upgrade-123.service", Detected: true}
	})
	for _, tc := range []struct {
		name   string
		info   *daemonShutdownInfo
		wantIn string
	}{
		{
			name:   "supervised with no unit and no policy",
			info:   shutdownInfo(true, "", ""),
			wantIn: "no owning unit or Restart= policy",
		},
		{
			name:   "supervised with unit but no policy",
			info:   shutdownInfo(true, "sdn-daemon.service", ""),
			wantIn: "no owning unit or Restart= policy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withLaunchFacts(t, "", "")
			if _, err := resolveHelperSupervisorPlan(context.Background(), tc.info); err == nil {
				t.Fatalf("resolveHelperSupervisorPlan accepted an unknown unit state: %#v", tc.info)
			} else if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("error = %v, want it to name %q", err, tc.wantIn)
			}
		})
	}
}

// TestResolveHelperSupervisorPlanHelperEscape covers the helper-escape gate:
// a helper whose own cgroup still resolves to the daemon's unit (setsid
// fallback from inside the daemon) must be refused — the unit teardown that
// follows the daemon exit would SIGTERM it mid-swap, tearing the bundle.
func TestResolveHelperSupervisorPlanHelperEscape(t *testing.T) {
	for _, own := range []string{"sdn-daemon.service", ""} {
		t.Run("own-unit="+own, func(t *testing.T) {
			withLaunchFacts(t, "", "")
			withProbeHost(t, func(ctx context.Context) hostsvc.State {
				return hostsvc.State{Unit: own, Detected: true}
			})
			_, err := resolveHelperSupervisorPlan(context.Background(), shutdownInfo(true, "sdn-daemon.service", "always"))
			if err == nil {
				t.Fatalf("resolveHelperSupervisorPlan accepted a helper inside the daemon's unit (own=%q)", own)
			}
			if !strings.Contains(err.Error(), "has NOT escaped") {
				t.Fatalf("error = %v, want the escape failure named", err)
			}
		})
	}
}

// TestResolveHelperSupervisorPlanResolvesUnit is the happy path: supervised
// daemon with a resolved unit and policy, and a helper proven to live in its
// own transient unit — the plan names exactly the daemon unit to restart.
func TestResolveHelperSupervisorPlanResolvesUnit(t *testing.T) {
	withLaunchFacts(t, "", "")
	withProbeHost(t, func(ctx context.Context) hostsvc.State {
		return hostsvc.State{Unit: "sdn-self-upgrade-123.service", Detected: true}
	})
	plan, err := resolveHelperSupervisorPlan(context.Background(), shutdownInfo(true, "sdn-daemon.service", "no"))
	if err != nil {
		t.Fatalf("resolveHelperSupervisorPlan error = %v", err)
	}
	if plan.Unit != "sdn-daemon.service" || plan.RestartPolicy != "no" {
		t.Fatalf("plan = %#v, want unit sdn-daemon.service policy no", plan)
	}
}

// TestResolveHelperSupervisorPlanRefusesStaleUnit covers the unit-stability
// gate: the unit resolved at launch (--daemon-unit) must equal the unit the
// shutdown response resolved; a rename/recreate between launch and shutdown
// leaves the restart target stale.
func TestResolveHelperSupervisorPlanRefusesStaleUnit(t *testing.T) {
	withLaunchFacts(t, "sdn-daemon-old.service", "always")
	withProbeHost(t, func(ctx context.Context) hostsvc.State {
		return hostsvc.State{Unit: "sdn-self-upgrade-123.service", Detected: true}
	})
	_, err := resolveHelperSupervisorPlan(context.Background(), shutdownInfo(true, "sdn-daemon.service", "always"))
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v, want a stale-unit refusal", err)
	}
}

// TestResolveHelperSupervisorPlanUnsupervisedRefusesLaunchFacts covers the
// conflict gate: the daemon answered and says it is NOT supervised, but the
// launcher claimed it was — the launch facts are stale, refuse.
func TestResolveHelperSupervisorPlanUnsupervisedRefusesLaunchFacts(t *testing.T) {
	withLaunchFacts(t, "sdn-daemon.service", "always")
	plan, err := resolveHelperSupervisorPlan(context.Background(), shutdownInfo(false, "", ""))
	if err == nil || !strings.Contains(err.Error(), "launch facts are stale") {
		t.Fatalf("error = %v, want the stale-launch-facts refusal", err)
	}
	if plan.Unit != "" {
		t.Fatalf("plan = %#v, want empty on refusal", plan)
	}
}

// TestResolveHelperSupervisorPlanUnsupervisedClean passes through the
// unsupervised daemon untouched: no unit, direct-spawn path.
func TestResolveHelperSupervisorPlanUnsupervisedClean(t *testing.T) {
	withLaunchFacts(t, "", "")
	plan, err := resolveHelperSupervisorPlan(context.Background(), shutdownInfo(false, "", ""))
	if err != nil {
		t.Fatalf("resolveHelperSupervisorPlan error = %v", err)
	}
	if plan.Unit != "" || plan.RestartPolicy != "" {
		t.Fatalf("plan = %#v, want empty for an unsupervised daemon", plan)
	}
}

// TestResolveHelperSupervisorPlanNoHandshakeUsesLaunchFacts covers the
// handshake-lost case: the daemon's answer was never heard, but the launcher
// carried the unit + policy it resolved at launch — the plan falls back to
// those facts (still gated by the helper-escape check).
func TestResolveHelperSupervisorPlanNoHandshakeUsesLaunchFacts(t *testing.T) {
	withLaunchFacts(t, "sdn-daemon.service", "no")
	withProbeHost(t, func(ctx context.Context) hostsvc.State {
		return hostsvc.State{Unit: "sdn-self-upgrade-123.service", Detected: true}
	})
	plan, err := resolveHelperSupervisorPlan(context.Background(), nil)
	if err != nil {
		t.Fatalf("resolveHelperSupervisorPlan error = %v", err)
	}
	if plan.Unit != "sdn-daemon.service" || plan.RestartPolicy != "no" {
		t.Fatalf("plan = %#v, want launch facts sdn-daemon.service / no", plan)
	}
}

// TestRequestDaemonUpdateShutdownParsesSupervisorFacts pins the handshake
// contract on the wire: the helper must read unit + restartPolicy out of a
// 202 response so its restart plan is built from the daemon's own resolution,
// not from its own environment.
func TestRequestDaemonUpdateShutdownParsesSupervisorFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/update/shutdown" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"shutdown_requested","pid":1234,"bundleRoot":"/opt/sdn","restartArgv":["/opt/sdn/bin/spacedatanetwork","daemon"],"supervised":true,"unit":"sdn-daemon.service","restartPolicy":"always"}`))
	}))
	defer server.Close()

	// adminEndpointURL appends its endpoint to the raw admin URL's path
	// (giving /api/v1/admin/update/shutdown), so the raw URL carries the base
	// only — a trailing /api/v1/ here would double the prefix.
	info, err := requestDaemonUpdateShutdown(server.Client(), server.URL, "/opt/sdn", "token-123")
	if err != nil {
		t.Fatalf("requestDaemonUpdateShutdown error = %v", err)
	}
	if !info.Supervised || info.Unit != "sdn-daemon.service" || info.RestartPolicy != "always" {
		t.Fatalf("info = %#v, want supervised with sdn-daemon.service / always", info)
	}
	if len(info.RestartArgv) != 2 || info.RestartArgv[0] != "/opt/sdn/bin/spacedatanetwork" {
		t.Fatalf("restart argv = %#v", info.RestartArgv)
	}
}

// TestRequestDaemonUpdateShutdownRefusalIsTyped: a non-202 answer is a
// REFUSAL — abort-before-apply, distinguishable from an outage — carrying the
// daemon's explanatory body.
func TestRequestDaemonUpdateShutdownRefusalIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"update shutdown refused: this daemon is supervised by systemd but its owning unit or Restart= policy could not be resolved"}`))
	}))
	defer server.Close()

	_, err := requestDaemonUpdateShutdown(server.Client(), server.URL, "/opt/sdn", "token-123")
	if err == nil {
		t.Fatal("requestDaemonUpdateShutdown accepted a refusal response")
	}
	var refused *daemonShutdownRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want *daemonShutdownRefusedError", err)
	}
	if !strings.Contains(err.Error(), "could not be resolved") {
		t.Fatalf("error = %v, want the daemon's refusal detail carried", err)
	}
}

// TestRequestDaemonUpdateShutdownTransportErrorIsUnavailable: a transport
// failure is NOT a refusal — it means the daemon's answer was never heard,
// which the lane treats as "unavailable" and continues on.
func TestRequestDaemonUpdateShutdownTransportErrorIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("daemon should never be reached: the server is wired to be closed")
	}))
	url := server.URL
	server.Close()

	_, err := requestDaemonUpdateShutdown(server.Client(), url, "/opt/sdn", "token-123")
	if err == nil {
		t.Fatal("requestDaemonUpdateShutdown succeeded against a dead daemon")
	}
	var refused *daemonShutdownRefusedError
	if errors.As(err, &refused) {
		t.Fatalf("transport error must not be a refusal: %v", err)
	}
}

type fakeHelperProcess struct {
	pid  int
	kill func() error
}

func (p fakeHelperProcess) PID() int {
	return p.pid
}

func (p fakeHelperProcess) Kill() error {
	if p.kill == nil {
		return nil
	}
	return p.kill()
}

func writeBundleManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const serverURLPlaceholder = "https://127.0.0.1"
