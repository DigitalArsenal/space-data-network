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
// sdn-update-helper-supervisor-mode AND ops-update-lane-restart-policy-preflight:
// when the daemon reported it was started by systemd, the helper must NEVER
// call StartDaemon (that direct spawn is exactly what escapes the unit's
// cgroup while the unit loops "activating" against the store lock). Instead it
// starts the EXACT resolved unit through the supervisor (StartUnit), health-
// waits, and otherwise the swap is good.
func TestHelperPostApplyRestartSupervisedNeverSpawns(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	var startedUnits []string

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Supervised:    true,
		Unit:          "space-data-network.service",
		RestartPolicy: "always",
		AdminURL:      "http://127.0.0.1:5080",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		StartUnit: func(ctx context.Context, unit string) error {
			startedUnits = append(startedUnits, unit)
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
		t.Fatal("StartDaemon was called under Supervised=true — this is the escapes-systemd bug")
	}
	if len(startedUnits) != 1 || startedUnits[0] != "space-data-network.service" {
		t.Fatalf("StartUnit calls = %#v, want the resolved unit exactly once", startedUnits)
	}
	if !strings.Contains(out.String(), "restart=supervised unit=space-data-network.service policy=always") {
		t.Fatalf("stdout = %q, want restart=supervised naming the resolved unit", out.String())
	}
	if !strings.Contains(out.String(), "unit_start=started unit=space-data-network.service") {
		t.Fatalf("stdout = %q, want unit_start=started", out.String())
	}
	if !strings.Contains(out.String(), "daemon_health=healthy") {
		t.Fatalf("stdout = %q, want daemon_health=healthy", out.String())
	}
}

// TestHelperPostApplyRestartSupervisedRollsBackWithoutSpawning proves the
// unhealthy-then-rollback branch also never spawns: after a rollback the
// helper starts the SAME resolved unit again (now on the previous slot) and
// health-waits again — a direct spawn would escape the unit's cgroup and
// leave the unit itself dead.
func TestHelperPostApplyRestartSupervisedRollsBackWithoutSpawning(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	rolledBack := false
	healthAttempts := 0
	var startedUnits []string

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Supervised:    true,
		Unit:          "space-data-network.service",
		RestartPolicy: "on-failure",
		AdminURL:      "http://127.0.0.1:5080",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		StartUnit: func(ctx context.Context, unit string) error {
			startedUnits = append(startedUnits, unit)
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
		t.Fatal("StartDaemon was called under Supervised=true during rollback — this is the escapes-systemd bug")
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked after supervised daemon health failure")
	}
	if len(startedUnits) != 2 {
		t.Fatalf("StartUnit calls = %#v, want one start before each health wait", startedUnits)
	}
	for _, unit := range startedUnits {
		if unit != "space-data-network.service" {
			t.Fatalf("StartUnit started %q, want the resolved unit every time", unit)
		}
	}
	if healthAttempts != 2 {
		t.Fatalf("health attempts = %d, want 2 (initial + post-rollback)", healthAttempts)
	}
}

// TestHelperPostApplyRestartSupervisedExplicitStartFailureRollsBack covers
// the explicit start failing (systemctl start refuses — masked unit, missing
// binary, start-limit): the helper rolls the bundle back and retries the
// start against the previous slot. A start failure must never be the bare
// "box stays down".
func TestHelperPostApplyRestartSupervisedExplicitStartFailureRollsBack(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	spawned := false
	rolledBack := false
	startAttempts := 0

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:         update.PathsFor(t.TempDir()),
		RestartArgv:   []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Supervised:    true,
		Unit:          "space-data-network.service",
		RestartPolicy: "on-failure",
		AdminURL:      "http://127.0.0.1:5080",
		Out:           &out,
		Err:           &errOut,
		StartDaemon: func(argv []string, stdout io.Writer, stderr io.Writer) (helperStartedProcess, error) {
			spawned = true
			return fakeHelperProcess{pid: 9999}, nil
		},
		StartUnit: func(ctx context.Context, unit string) error {
			startAttempts++
			if startAttempts == 1 {
				return errors.New("systemctl start space-data-network.service: Unit is masked.")
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
		t.Fatalf("helperPostApplyRestart (supervised) error = %v, want rollback error after failed start", err)
	}
	if spawned {
		t.Fatal("StartDaemon was called under Supervised=true — the cgroup-escape regression")
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked after the explicit start failed")
	}
	if startAttempts != 2 {
		t.Fatalf("start attempts = %d, want 2 (failed start + retry after rollback)", startAttempts)
	}
	if !strings.Contains(errOut.String(), "unit_start=error") {
		t.Fatalf("stderr = %q, want the failed start reported", errOut.String())
	}
	if !strings.Contains(out.String(), "rollback=applied restored_version=1.0.0") {
		t.Fatalf("stdout = %q, want rollback=applied", out.String())
	}
}

// TestHelperPostApplyRestartSupervisedRefusesUnresolvedUnit is the helper's
// fail-closed half of the gate: a supervised run with no resolved unit must
// refuse rather than guess — starting the wrong unit or nothing would strand
// the box. (The daemon refuses such shutdowns first; this is the belt.)
func TestHelperPostApplyRestartSupervisedRefusesUnresolvedUnit(t *testing.T) {
	var out bytes.Buffer
	var errOut bytes.Buffer

	err := helperPostApplyRestart(context.Background(), helperPostApplyOptions{
		Paths:       update.PathsFor(t.TempDir()),
		RestartArgv: []string{"/opt/sdn/bin/spacedatanetwork", "daemon"},
		Supervised:  true,
		AdminURL:    "http://127.0.0.1:5080",
		Out:         &out,
		Err:         &errOut,
	})
	if err == nil || !strings.Contains(err.Error(), "owning systemd unit") {
		t.Fatalf("helperPostApplyRestart (supervised) error = %v, want unresolved-unit refusal", err)
	}
}

// TestRequestDaemonUpdateShutdownParsesUnitAndPolicy proves the helper's
// decoder accepts the shutdown response's resolved-unit fields
// (ops-update-lane-restart-policy-preflight): the unit and Restart= policy
// the daemon resolved on ITS OWN process are exactly what the supervised
// branch starts after the swap.
func TestRequestDaemonUpdateShutdownParsesUnitAndPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/update/shutdown" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{
			"status": "shutdown_requested",
			"pid": 4242,
			"bundleRoot": "/opt/spacedatanetwork",
			"restartArgv": ["/opt/spacedatanetwork/bin/spacedatanetwork", "daemon"],
			"supervised": true,
			"unit": "space-data-network.service",
			"restartPolicy": "on-failure"
		}`))
	}))
	defer server.Close()

	info, err := requestDaemonUpdateShutdown(server.Client(), server.URL, "/opt/spacedatanetwork", "token-123")
	if err != nil {
		t.Fatalf("requestDaemonUpdateShutdown error = %v", err)
	}
	if !info.Supervised {
		t.Fatal("Supervised = false, want true")
	}
	if info.Unit != "space-data-network.service" {
		t.Fatalf("Unit = %q, want the resolved owning unit", info.Unit)
	}
	if info.RestartPolicy != "on-failure" {
		t.Fatalf("RestartPolicy = %q, want on-failure", info.RestartPolicy)
	}
	if len(info.RestartArgv) != 2 || info.RestartArgv[0] != "/opt/spacedatanetwork/bin/spacedatanetwork" {
		t.Fatalf("RestartArgv = %#v", info.RestartArgv)
	}
}

func TestRequestDaemonUpdateShutdownSurfacesRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"update shutdown refused: unit space-data-network.service has Restart=\"no\" ..."}`))
	}))
	defer server.Close()

	_, err := requestDaemonUpdateShutdown(server.Client(), server.URL, "/opt/spacedatanetwork", "token-123")
	if err == nil {
		t.Fatal("requestDaemonUpdateShutdown accepted a refusal response")
	}
	if !strings.Contains(err.Error(), "update shutdown refused") {
		t.Fatalf("error = %v, want the daemon's actionable refusal surfaced", err)
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
