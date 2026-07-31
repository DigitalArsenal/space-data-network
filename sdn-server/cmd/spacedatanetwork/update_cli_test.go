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
			"feedBaseUrl": "https://sdn.spaceaware.io/api/v1/updates",
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
	if manifest.Update.FeedBaseURL != "https://sdn.spaceaware.io/api/v1/updates" ||
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
	got, err := providerFeedIndexURL("https://sdn.spaceaware.io/api/v1/updates/", "beta", "linux", "amd64")
	if err != nil {
		t.Fatalf("providerFeedIndexURL returned error: %v", err)
	}
	want := "https://sdn.spaceaware.io/api/v1/updates/cli-bundle/beta/linux/amd64/index.json"
	if got != want {
		t.Fatalf("providerFeedIndexURL = %q, want %q", got, want)
	}
}

func TestProviderFeedIndexURLRejectsHTTP(t *testing.T) {
	_, err := providerFeedIndexURL("http://sdn.spaceaware.io/api/v1/updates", "beta", "linux", "amd64")
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
