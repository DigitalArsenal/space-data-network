package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
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

func writeBundleManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const serverURLPlaceholder = "https://127.0.0.1"

// ---------------------------------------------------------------------------
// THE UPDATE HELPER, END TO END (expert review PLAT-01 / SD-5).
//
// Real bundles, real signed updates, real swaps and real processes. The daemon
// is this test binary copied into a bundle and re-executed as
// TestUpdateHelperE2EDaemon: it serves the real update control handler and the
// real /api/v1/id, holds the real store lock, and takes as long to stop as its
// build says. Every "build" is the same binary plus a trailer, so every build
// has its own sha256, and the trailer says how the build behaves.
// ---------------------------------------------------------------------------

const (
	e2eBuildMarker   = "SDN-E2E-BUILD:"
	e2eFixtureConfig = "e2e-fixture.json"
	e2eV1Version     = "1.0.0-e2e.v1"
	e2eV2Version     = "1.0.0-e2e.v2"
	e2eV2UpdateID    = "e2e-v2"
)

// e2eBuild is how one build of the fixture daemon behaves.
type e2eBuild struct {
	Tag         string `json:"tag"`
	StopDelayMS int    `json:"stop_delay_ms"`
	Unhealthy   bool   `json:"unhealthy,omitempty"`
	// WriteFormat4 makes the build begin a migration to format 4 as it
	// starts: from its first marker the store refuses every build stamped
	// below 4.
	WriteFormat4 bool `json:"write_format4,omitempty"`
}

// e2eFixture configures the fixture daemon. It is local data in the bundle
// root, which a swap leaves alone.
type e2eFixture struct {
	Addr      string `json:"addr"`
	StoreRoot string `json:"store_root"`
	Events    string `json:"events"`
}

// e2eEvent is one line of the fixture daemons' shared event log.
type e2eEvent struct {
	Event string    `json:"event"`
	PID   int       `json:"pid"`
	Tag   string    `json:"tag"`
	At    time.Time `json:"at"`
	Via   string    `json:"via,omitempty"`
	// BundleBinary is the sha256 of the bundle's daemon binary at the moment
	// of the event.
	BundleBinary string `json:"bundle_binary_sha256,omitempty"`
}

// TestUpdateHelperE2EDaemon is the fixture daemon. In a normal run it skips;
// it runs only when this binary sits in a test bundle.
func TestUpdateHelperE2EDaemon(t *testing.T) {
	layout := bundle.ResolveCurrent()
	if layout.Root == "" {
		t.Skip("the fixture daemon of the update-helper E2E tests; it runs only from a test bundle")
	}
	raw, err := os.ReadFile(filepath.Join(layout.Root, e2eFixtureConfig))
	if err != nil {
		t.Skip("the fixture daemon of the update-helper E2E tests; it runs only from a test bundle")
	}
	os.Exit(runE2EDaemon(layout.Root, raw))
}

func runE2EDaemon(root string, raw []byte) int {
	var cfg e2eFixture
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 2
	}
	build, err := readE2EBuild()
	if err != nil {
		return 2
	}
	logEvent := func(ev e2eEvent) {
		ev.PID, ev.Tag, ev.At = os.Getpid(), build.Tag, time.Now()
		line, _ := json.Marshal(ev)
		if f, err := os.OpenFile(cfg.Events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	// Like the daemon, open the store first: a second daemon on one store
	// stops right here.
	lock, err := storage.LockStoreForMaintenance(cfg.StoreRoot)
	if err != nil {
		logEvent(e2eEvent{Event: "store-locked-out"})
		return 3
	}
	defer lock.Release()
	if build.WriteFormat4 {
		_ = os.MkdirAll(filepath.Join(cfg.StoreRoot, marker.Dir), 0o700)
		_ = os.WriteFile(filepath.Join(cfg.StoreRoot, marker.Dir, marker.MigratedFile), []byte("migration to format 4 in progress"), 0o600)
	}

	shutdown := make(chan string, 2)
	mux := http.NewServeMux()
	mux.Handle(update.ControlShutdownPath, update.NewControlHandler(update.ControlHandlerOptions{
		BundleRoot: root,
		Shutdown:   func() { shutdown <- "update-handshake" },
	}))
	// The daemon's own identity surface, /api/v1/id among the core routes.
	api.NewCoreAPIHandler(peer.ID("e2e-fixture-daemon"), nil, nil, nil, nil, nil, nil, nil, nil).RegisterRoutes(mux)
	mux.HandleFunc("/api/v1/data/health", func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		if build.Unhealthy {
			status = "unhealthy"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":%q}`, status)
	})
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		logEvent(e2eEvent{Event: "listen-failed"})
		return 4
	}
	go func() { _ = http.Serve(listener, mux) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signals
		shutdown <- "SIGTERM"
	}()
	logEvent(e2eEvent{Event: "started", BundleBinary: e2eFileSHA256(filepath.Join(root, "bin", "spacedatanetwork"))})

	via := <-shutdown
	logEvent(e2eEvent{Event: "shutdown", Via: via})
	// Like the daemon, stop serving first and finish the rest of the shutdown
	// after: from here only the pid says whether this process is gone.
	_ = listener.Close()
	time.Sleep(time.Duration(build.StopDelayMS) * time.Millisecond)
	logEvent(e2eEvent{Event: "exit", BundleBinary: e2eFileSHA256(filepath.Join(root, "bin", "spacedatanetwork"))})
	return 0
}

// readE2EBuild reads this executable's build trailer.
func readE2EBuild() (e2eBuild, error) {
	var build e2eBuild
	exe, err := os.Executable()
	if err != nil {
		return build, err
	}
	f, err := os.Open(exe)
	if err != nil {
		return build, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return build, err
	}
	offset := max(info.Size()-64<<10, 0)
	tail := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(tail, offset); err != nil {
		return build, err
	}
	i := bytes.LastIndex(tail, []byte(e2eBuildMarker))
	if i < 0 {
		return build, errors.New("no build trailer")
	}
	rest := tail[i+len(e2eBuildMarker):]
	if j := bytes.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return build, json.Unmarshal(rest, &build)
}

// writeE2EDaemon writes one build of the fixture daemon to dest: this test
// binary, then the build's trailer. lowerStamp > 0 appends a store-format
// stamp, which the guard reads as the build's (the lowest stamp wins).
func writeE2EDaemon(t *testing.T, dest string, build e2eBuild, lowerStamp int) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	trailer, err := json.Marshal(build)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Write([]byte("\n" + e2eBuildMarker + string(trailer) + "\n")); err != nil {
		t.Fatal(err)
	}
	if lowerStamp > 0 {
		if _, err := dst.Write(versioninfo.StoreFormatStamp(lowerStamp)); err != nil {
			t.Fatal(err)
		}
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return e2eFileSHA256(dest)
}

func e2eFileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// e2eBundle is an installed bundle running build v1, with its store, its
// update trust root and the fixture configuration.
type e2eBundle struct {
	paths  update.Paths
	store  string
	events string
	addr   string
	keyID  string
	priv   ed25519.PrivateKey
	roots  update.TrustedRoots
	v1SHA  string
}

func newE2EBundle(t *testing.T, v1 e2eBuild, v1LowerStamp int) *e2eBundle {
	t.Helper()
	// Resolved: the daemon names its bundle root through EvalSymlinks, and
	// the handshake compares that root with the helper's.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := &e2eBundle{
		paths:  update.PathsFor(filepath.Join(dir, "bundle")),
		store:  filepath.Join(dir, "store"),
		events: filepath.Join(dir, "events.jsonl"),
		keyID:  "e2e-release",
	}
	if err := os.MkdirAll(b.store, 0o755); err != nil {
		t.Fatal(err)
	}
	b.v1SHA = writeE2EDaemon(t, filepath.Join(b.paths.Root, "bin", "spacedatanetwork"), v1, v1LowerStamp)
	writeE2EJSON(t, filepath.Join(b.paths.Root, "manifest.json"), e2eBundleManifest(e2eV1Version, b.v1SHA))

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	b.priv = priv
	b.roots = update.TrustedRoots{b.keyID: base64.StdEncoding.EncodeToString(spki)}
	writeE2EJSON(t, b.paths.Trust, b.roots)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b.addr = listener.Addr().String()
	_ = listener.Close()
	writeE2EJSON(t, filepath.Join(b.paths.Root, e2eFixtureConfig), e2eFixture{Addr: b.addr, StoreRoot: b.store, Events: b.events})
	return b
}

func e2eBundleManifest(version, daemonSHA string) map[string]any {
	return map[string]any{
		"schema":    "org.spacedatanetwork.bundle.v1",
		"version":   version,
		"channel":   "beta",
		"signature": "e2e",
		"artifacts": []map[string]any{{"path": "bin/spacedatanetwork", "sha256": daemonSHA}},
	}
}

func writeE2EJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (b *e2eBundle) daemonArgv() []string {
	return []string{filepath.Join(b.paths.Root, "bin", "spacedatanetwork"), "-test.run=^TestUpdateHelperE2EDaemon$", "-test.timeout=0"}
}

func (b *e2eBundle) adminURL() string { return "http://" + b.addr }

// stage signs and stages build v2 as a full-bundle update, the way the fleet
// lane publishes one: the daemon at bin/, a bundle manifest naming its sha256.
func (b *e2eBundle) stage(t *testing.T, build e2eBuild) string {
	t.Helper()
	daemon := filepath.Join(t.TempDir(), "spacedatanetwork")
	sha := writeE2EDaemon(t, daemon, build, 0)

	var archive bytes.Buffer
	gz, err := gzip.NewWriterLevel(&archive, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(gz)
	wrapper := "spacedatanetwork-" + e2eV2Version + "-" + runtime.GOOS + "-" + runtime.GOARCH + "/"
	src, err := os.Open(daemon)
	if err != nil {
		t.Fatal(err)
	}
	info, err := src.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: wrapper + "bin/spacedatanetwork", Mode: 0o755, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tw, src); err != nil {
		t.Fatal(err)
	}
	_ = src.Close()
	manifest, err := json.Marshal(e2eBundleManifest(e2eV2Version, sha))
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: wrapper + "manifest.json", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	bundleBytes := archive.Bytes()
	carrier := update.BuildCarrier(bundleBytes)
	doc := map[string]any{
		"schema":     update.ManifestSchema,
		"update_id":  e2eV2UpdateID,
		"version":    e2eV2Version,
		"sequence":   int64(42),
		"channel":    "beta",
		"created_at": "2026-06-01T00:00:00Z",
		"expires_at": "2030-01-01T00:00:00Z",
		"target":     map[string]any{"platform": runtime.GOOS, "arch": runtime.GOARCH, "kind": "cli-bundle"},
		"bundle":     map[string]any{"hash": e2eSHA256(bundleBytes), "size": int64(len(bundleBytes)), "format": "tar.gz"},
		"wasm":       map[string]any{"hash": e2eSHA256(carrier)},
		"signing":    map[string]any{"key_id": b.keyID, "algorithm": "Ed25519"},
	}
	canonical, err := update.CanonicalManifestBytes(e2eSortedJSON(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(b.priv, canonical))
	if _, err := update.Stage(b.paths, e2eSortedJSON(t, doc), carrier, update.HostVerifyOptions(b.roots, 0, time.Now())); err != nil {
		t.Fatalf("stage v2: %v", err)
	}
	return sha
}

func e2eSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func e2eSortedJSON(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

// runHelper writes the one-time control token (as `update install` does) and
// runs the helper's whole sequence against the bundle.
func (b *e2eBundle) runHelper(t *testing.T, healthTimeout time.Duration, stop update.StopBounds, supervisor daemonSupervisor) (stdout, stderr *e2eOutput, err error) {
	t.Helper()
	token := "e2e-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	if err := update.WriteControlToken(b.paths, token); err != nil {
		t.Fatal(err)
	}
	out, errOut := &e2eOutput{}, &e2eOutput{}
	opts := helperApplyOptions{
		Paths:         b.paths,
		UpdateID:      e2eV2UpdateID,
		AdminURL:      b.adminURL(),
		Token:         token,
		StoreRoot:     b.store,
		HealthTimeout: healthTimeout,
		Stop:          stop,
		Client:        &http.Client{Timeout: 10 * time.Second},
		Out:           out,
		Err:           errOut,
	}
	if supervisor != nil {
		opts.Supervisor = func(context.Context, int) daemonSupervisor { return supervisor }
	}
	err = runHelperApply(context.Background(), opts)
	t.Logf("helper stdout:\n%s\nhelper stderr:\n%s", out, errOut)
	return out, errOut, err
}

// e2eOutput is an io.Writer the helper and the daemons it spawns share.
type e2eOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *e2eOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *e2eOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (b *e2eBundle) waitHealthy(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(b.adminURL() + "/api/v1/data/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the fixture daemon did not come up")
}

func (b *e2eBundle) ledger(t *testing.T) []update.DeployLedgerEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.paths.Root, "deploy-ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []update.DeployLedgerEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry update.DeployLedgerEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("ledger line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func (b *e2eBundle) eventsOf(t *testing.T) []e2eEvent {
	t.Helper()
	raw, err := os.ReadFile(b.events)
	if err != nil {
		t.Fatal(err)
	}
	var events []e2eEvent
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev e2eEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func e2eFind(events []e2eEvent, tag, event string) (e2eEvent, bool) {
	for _, ev := range events {
		if ev.Tag == tag && ev.Event == event {
			return ev, true
		}
	}
	return e2eEvent{}, false
}

// e2eActions lists the ledger's actions in order.
func e2eActions(entries []update.DeployLedgerEntry) []string {
	actions := make([]string, 0, len(entries))
	for _, entry := range entries {
		actions = append(actions, entry.Action)
	}
	return actions
}

// requireInOrder asserts want is a subsequence of got.
func requireInOrder(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("ledger actions %v do not contain %v in order", got, want)
	}
}

// runningExecutableSHA256 hashes the executable a live process is running:
// /proc/<pid>/exe on Linux; on darwin the text vnode lsof reports, which
// follows the file through renames exactly as /proc/<pid>/exe does.
func runningExecutableSHA256(t *testing.T, pid int) string {
	t.Helper()
	switch runtime.GOOS {
	case "linux":
		return e2eFileSHA256("/proc/" + strconv.Itoa(pid) + "/exe")
	case "darwin":
		out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
		if err != nil {
			t.Fatalf("lsof -p %d: %v", pid, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "n") {
				return e2eFileSHA256(strings.TrimPrefix(line, "n"))
			}
		}
		t.Fatalf("lsof named no executable for pid %d: %s", pid, out)
	}
	t.Skipf("no way to read a running process's executable on %s", runtime.GOOS)
	return ""
}

func requireE2EPlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("the update-helper E2E tests run on linux and darwin, not %s", runtime.GOOS)
	}
}

// environWithout copies the environment minus the named variables.
func environWithout(names ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, name) {
			env = append(env, kv)
		}
	}
	return env
}

// e2eSupervisor stands in for systemd with Restart=always: it respawns the
// daemon restartSec after any exit it did not ask for, and its Stop sticks
// until Start. The supervised tests hand it to the helper as the supervisor
// connector; the daemons it runs see INVOCATION_ID and report supervised.
type e2eSupervisor struct {
	argv       []string
	env        []string
	restartSec time.Duration

	mu      sync.Mutex
	current *exec.Cmd
	stopped bool
	closed  bool
	started []int
	exits   map[int]int
}

func newE2ESupervisor(argv []string, restartSec time.Duration) *e2eSupervisor {
	return &e2eSupervisor{
		argv:       argv,
		env:        append(environWithout("INVOCATION_ID"), "INVOCATION_ID=e2e-supervisor"),
		restartSec: restartSec,
		exits:      map[int]int{},
	}
}

func (s *e2eSupervisor) Name() string { return "e2e supervisor" }

func (s *e2eSupervisor) spawnLocked() error {
	cmd := exec.Command(s.argv[0], s.argv[1:]...)
	cmd.Env = s.env
	if err := cmd.Start(); err != nil {
		return err
	}
	s.current = cmd
	s.started = append(s.started, cmd.Process.Pid)
	go s.reap(cmd)
	return nil
}

func (s *e2eSupervisor) reap(cmd *exec.Cmd) {
	_ = cmd.Wait()
	s.mu.Lock()
	s.exits[cmd.Process.Pid] = cmd.ProcessState.ExitCode()
	if s.current == cmd {
		s.current = nil
	}
	respawn := !s.stopped && !s.closed
	s.mu.Unlock()
	if !respawn {
		return
	}
	time.Sleep(s.restartSec)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped && !s.closed && s.current == nil {
		_ = s.spawnLocked()
	}
}

func (s *e2eSupervisor) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.current != nil {
		return s.current.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

func (s *e2eSupervisor) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = false
	if s.current == nil {
		return s.spawnLocked()
	}
	return nil
}

func (s *e2eSupervisor) MainPID(context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return 0
	}
	return s.current.Process.Pid
}

func (s *e2eSupervisor) snapshot() (started []int, exits map[int]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exits = make(map[int]int, len(s.exits))
	for pid, code := range s.exits {
		exits[pid] = code
	}
	return append([]int(nil), s.started...), exits
}

func (s *e2eSupervisor) close() {
	s.mu.Lock()
	s.closed = true
	current := s.current
	s.mu.Unlock()
	if current != nil {
		_ = current.Process.Kill()
	}
}

// TestUpdateHelperWaitsForASlowDaemonAndRunsTheInstalledBuild: a daemon whose
// shutdown takes 60 s is not swapped until it exits, and afterwards the
// process running is the binary the applied manifest names.
func TestUpdateHelperWaitsForASlowDaemonAndRunsTheInstalledBuild(t *testing.T) {
	requireE2EPlatform(t)
	t.Parallel()
	const stopDelay = 60 * time.Second
	b := newE2EBundle(t, e2eBuild{Tag: "v1", StopDelayMS: int(stopDelay / time.Millisecond)}, 0)

	// v1 runs unsupervised, started by hand.
	v1 := exec.Command(b.daemonArgv()[0], b.daemonArgv()[1:]...)
	v1.Env = environWithout("INVOCATION_ID")
	if err := v1.Start(); err != nil {
		t.Fatal(err)
	}
	v1Exited := make(chan struct{})
	go func() {
		_ = v1.Wait()
		close(v1Exited)
	}()
	t.Cleanup(func() {
		_ = v1.Process.Kill()
		<-v1Exited
	})
	b.waitHealthy(t)
	v2SHA := b.stage(t, e2eBuild{Tag: "v2", StopDelayMS: 200})

	// While v1 lives, the bundle must still hold v1 and nothing may have
	// been moved aside.
	var samples int
	var early []string
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		slot := filepath.Join(b.paths.Rollback, e2eV2UpdateID)
		for {
			select {
			case <-v1Exited:
				return
			case <-time.After(250 * time.Millisecond):
			}
			select {
			case <-v1Exited:
				return
			default:
			}
			samples++
			if sha := e2eFileSHA256(filepath.Join(b.paths.Root, "bin", "spacedatanetwork")); sha != b.v1SHA {
				early = append(early, "bundle binary "+sha)
			}
			if _, err := os.Stat(slot); err == nil {
				early = append(early, "rollback slot "+slot)
			}
		}
	}()

	out, _, err := b.runHelper(t, 60*time.Second, update.StopBounds{}, nil)
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	<-watched
	if len(early) > 0 {
		t.Fatalf("the bundle changed while v1 was still running: %v", early)
	}
	if samples < int(stopDelay/(500*time.Millisecond)) {
		t.Fatalf("only %d samples were taken while v1 stopped", samples)
	}

	// v1 took its 60 s, and at the moment it exited the bundle still held it.
	events := b.eventsOf(t)
	shutdown, ok := e2eFind(events, "v1", "shutdown")
	if !ok || shutdown.Via != "update-handshake" {
		t.Fatalf("v1 shutdown event = %+v, %v", shutdown, ok)
	}
	exited, ok := e2eFind(events, "v1", "exit")
	if !ok {
		t.Fatal("v1 logged no exit")
	}
	if took := exited.At.Sub(shutdown.At); took < stopDelay-time.Second {
		t.Fatalf("v1 exited %s after the shutdown request, want ~%s", took, stopDelay)
	}
	if exited.BundleBinary != b.v1SHA {
		t.Fatalf("when v1 exited the bundle held %s, want v1 %s", exited.BundleBinary, b.v1SHA)
	}

	// The ledger: waited for the pid, took the store lock, then swapped —
	// and never needed to escalate inside the 90 s bound.
	entries := b.ledger(t)
	actions := e2eActions(entries)
	requireInOrder(t, actions, []string{"stop-requested", "stop-waiting", "daemon-exited", "store-locked", "apply", "restart", "health-passed"})
	if slices.Contains(actions, "stop-escalated") || slices.Contains(actions, "stop-killed") {
		t.Fatalf("a 60 s stop inside the 90 s bound was escalated: %v", actions)
	}
	for _, entry := range entries {
		if entry.Action == "apply" {
			applied, err := time.Parse(time.RFC3339, entry.RecordedAt)
			if err != nil {
				t.Fatal(err)
			}
			if applied.Before(exited.At.Truncate(time.Second)) {
				t.Fatalf("the apply was recorded at %s, before v1 exited at %s", applied, exited.At)
			}
		}
	}

	// The process now running is the binary the applied manifest names.
	match := regexp.MustCompile(`restart=started pid=(\d+)`).FindStringSubmatch(out.String())
	if match == nil {
		t.Fatalf("the helper started no daemon:\n%s", out)
	}
	pid, _ := strconv.Atoi(match[1])
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	var applied struct {
		Version   string `json:"version"`
		Artifacts []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
	}
	raw, err := os.ReadFile(filepath.Join(b.paths.Root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Version != e2eV2Version || len(applied.Artifacts) != 1 || applied.Artifacts[0].SHA256 != v2SHA {
		t.Fatalf("applied manifest = %+v, want v2 %s", applied, v2SHA)
	}
	if running := runningExecutableSHA256(t, pid); running != v2SHA {
		t.Fatalf("pid %d runs %s, want the applied manifest's %s", pid, running, v2SHA)
	}
	identity, err := probeDaemonIdentity(context.Background(), http.DefaultClient, b.adminURL())
	if err != nil {
		t.Fatal(err)
	}
	if identity.BuildSHA256 != v2SHA || identity.BundleVersion != e2eV2Version {
		t.Fatalf("/api/v1/id = %+v, want %s %s", identity, e2eV2Version, v2SHA)
	}
}

// TestUpdateHelperRevertsAnUnhealthyBuildThroughTheSupervisor: an unhealthy
// new build is stopped through the supervisor connector, the guard runs again,
// the previous build is swapped back and restarted through the connector —
// and the process running at the end is the old binary. The unhealthy build
// also never finishes stopping, so the revert climbs the whole ladder
// (supervisor stop, SIGTERM, SIGKILL) before it renames anything.
func TestUpdateHelperRevertsAnUnhealthyBuildThroughTheSupervisor(t *testing.T) {
	requireE2EPlatform(t)
	t.Parallel()
	b := newE2EBundle(t, e2eBuild{Tag: "v1", StopDelayMS: 300}, 0)
	supervisor := newE2ESupervisor(b.daemonArgv(), 2*time.Second)
	t.Cleanup(supervisor.close)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.waitHealthy(t)
	b.stage(t, e2eBuild{Tag: "v2", StopDelayMS: int(time.Hour / time.Millisecond), Unhealthy: true})

	ladder := update.StopBounds{Grace: 2 * time.Second, Escalated: 2 * time.Second, Killed: 10 * time.Second}
	out, _, err := b.runHelper(t, 8*time.Second, ladder, supervisor)
	if err == nil || !strings.Contains(err.Error(), "rolled back to "+e2eV1Version) {
		t.Fatalf("helper error = %v, want a rollback to %s", err, e2eV1Version)
	}

	pid := supervisor.MainPID(context.Background())
	if pid == 0 {
		t.Fatal("no daemon is running after the revert")
	}
	if running := runningExecutableSHA256(t, pid); running != b.v1SHA {
		t.Fatalf("pid %d runs %s after the revert, want v1 %s", pid, running, b.v1SHA)
	}
	if sha := e2eFileSHA256(filepath.Join(b.paths.Root, "bin", "spacedatanetwork")); sha != b.v1SHA {
		t.Fatalf("the bundle holds %s after the revert, want v1", sha)
	}
	if !update.HasFailedUpdate(b.paths, e2eV2UpdateID) {
		t.Fatal("the reverted update is not quarantined")
	}
	requireInOrder(t, e2eActions(b.ledger(t)), []string{
		"apply", "health-failed",
		"stop-requested", "stop-escalated", "stop-killed", "daemon-exited",
		"store-locked", "rollback", "restart", "health-passed",
	})
	// v2 never got as far as exiting by itself: it was killed, and only then
	// did anything move.
	if _, exited := e2eFind(b.eventsOf(t), "v2", "exit"); exited {
		t.Fatal("v2 logged an exit; the hung build was supposed to need SIGKILL")
	}
	// Only the supervisor ever started a daemon, and no daemon ever found the
	// store already held.
	if strings.Contains(out.String(), "restart=started") {
		t.Fatalf("the helper spawned a daemon under a supervisor:\n%s", out)
	}
	if !strings.Contains(out.String(), `restart=supervisor unit="e2e supervisor"`) {
		t.Fatalf("the restored build was not started through the supervisor:\n%s", out)
	}
	started, exits := supervisor.snapshot()
	for pid, code := range exits {
		if code == 3 {
			t.Fatalf("daemon %d found the store locked: two daemons overlapped (starts %v)", pid, started)
		}
	}
}

// TestUpdateHelperLeavesTheBuildRunningWhenTheGuardRefusesTheRevert: the new
// build is unhealthy, but it has begun a migration to format 4 and the
// previous build opens format 3 at most. The revert is refused before anything
// is stopped, the alert is raised, and the current build keeps running.
func TestUpdateHelperLeavesTheBuildRunningWhenTheGuardRefusesTheRevert(t *testing.T) {
	requireE2EPlatform(t)
	t.Parallel()
	b := newE2EBundle(t, e2eBuild{Tag: "v1", StopDelayMS: 300}, 3)
	supervisor := newE2ESupervisor(b.daemonArgv(), 2*time.Second)
	t.Cleanup(supervisor.close)
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.waitHealthy(t)
	v2SHA := b.stage(t, e2eBuild{Tag: "v2", StopDelayMS: 300, Unhealthy: true, WriteFormat4: true})

	_, errOut, err := b.runHelper(t, 8*time.Second, update.StopBounds{}, supervisor)
	var refusal *update.StoreFormatRefusal
	if !errors.As(err, &refusal) || refusal.Store.Format != 4 || refusal.SlotMaxStoreFormat != 3 {
		t.Fatalf("helper error = %v, want the store-format guard refusing the revert (store 4, slot 3)", err)
	}
	if !strings.Contains(errOut.String(), "ops_alert=raised kind=update_failed subject="+e2eV2UpdateID+" severity=error") {
		t.Fatalf("no update_failed alert was raised:\n%s", errOut)
	}

	// v2 is still the running build: started once, never stopped.
	started, _ := supervisor.snapshot()
	if len(started) != 2 {
		t.Fatalf("the supervisor started %v; want v1 then v2 and nothing after", started)
	}
	if pid := supervisor.MainPID(context.Background()); pid != started[1] {
		t.Fatalf("running pid %d, want v2's %d", pid, started[1])
	}
	if running := runningExecutableSHA256(t, started[1]); running != v2SHA {
		t.Fatalf("pid %d runs %s, want v2 %s", started[1], running, v2SHA)
	}
	// Nothing was half-reverted: the bundle is v2 and the v1 slot is whole.
	if sha := e2eFileSHA256(filepath.Join(b.paths.Root, "bin", "spacedatanetwork")); sha != v2SHA {
		t.Fatalf("the bundle holds %s, want v2", sha)
	}
	if sha := e2eFileSHA256(filepath.Join(b.paths.Rollback, e2eV2UpdateID, "bin", "spacedatanetwork")); sha != b.v1SHA {
		t.Fatalf("the v1 slot holds %s, want v1", sha)
	}
	if update.HasFailedUpdate(b.paths, e2eV2UpdateID) {
		t.Fatal("an update that was not reverted is quarantined")
	}
	actions := e2eActions(b.ledger(t))
	requireInOrder(t, actions, []string{"apply", "health-failed", "revert-refused"})
	if slices.Contains(actions, "rollback") {
		t.Fatalf("a refused revert still rolled back: %v", actions)
	}
}
