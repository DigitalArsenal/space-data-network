package main

// THE UPDATE CHANNEL, END TO END, ON REAL DAEMONS (stack issue 670).
//
// The helper tests in update_cli_test.go run a fixture daemon. This one runs
// the node itself: three real builds of ./cmd/spacedatanetwork, a real bundle,
// a signed HTTPS feed, and `spacedatanetwork update install` exactly as an
// operator types it, with a real daemon answering on loopback so the install
// goes through the helper (stop, swap, restart, gate on /api/v1/ready, revert).
//
//	pass       v1 -> v2: /api/v1/id reports v2's bundle_version and
//	           build_sha256, state.json has v2's sequence, nothing is
//	           quarantined, and the ledger shows health-passed.
//	unhealthy  v2 -> v3, a build whose /api/v1/ready answers 503 for good
//	           (-tags sdn_e2e_not_ready): the helper reports "rolled back to"
//	           v2, v2 runs again, and v3 is quarantined under updates/failed/.
//	wrong key  v4 signed by a key the bundle does not trust: refused with
//	           "untrusted update signing key" before anything is staged.
//
// Gated: SDN_E2E_REAL_DAEMON=1. Linux only: the CLI fetches the feed with the
// system roots, and the test makes it trust the feed's certificate through
// SSL_CERT_FILE, which Go ignores on macOS. CI runs it in the ci.yml job
// "Update channel E2E (real daemon)". SDN_E2E_REAL_DAEMON_BIN_DIR may name a
// directory holding prebuilt spacedatanetwork-v1, -v2 and -v3 (v3 built with
// -tags sdn_e2e_not_ready); otherwise the test builds them through
// scripts/go-with-wasmedge.sh, which needs WASMEDGE_DIR.
//
// Every key is a throwaway generated here. The feed and the admin API are on
// loopback and the daemons dial no peer (writeRealDaemonConfig;
// requireIsolatedConfig refuses, before any daemon starts, a config whose
// bootstrap list would fall back to the built-in peers, and requireIsolated
// checks every daemon's log, the helper-started ones included). The daemon's
// own bond attestation still queries the public chain services it always does.
//
// Also asserted: `update install` hands the helper the node's
// update.health_timeout_seconds (the helper's argv, and how long v3's gate
// waited), and every helper and daemon the test caused is stopped at the end,
// however it ends.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	libp2p "github.com/libp2p/go-libp2p"
	ps "github.com/libp2p/go-libp2p-pubsub"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
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

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
	walletwasm "github.com/spacedatanetwork/sdn-server/internal/wasm"
)

const (
	realE2EEnv       = "SDN_E2E_REAL_DAEMON"
	realE2EBinDirEnv = "SDN_E2E_REAL_DAEMON_BIN_DIR"
	realE2EChannel   = "release"
)

// realBuild is one build of the node and the update that ships it.
type realBuild struct {
	name     string
	version  string
	updateID string
	sequence int64
	path     string
	sha      string
}

func TestUpdateChannelRealDaemonE2E(t *testing.T) {
	if os.Getenv(realE2EEnv) != "1" {
		t.Skip("set SDN_E2E_REAL_DAEMON=1 to run the update channel against real daemons (Linux)")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("Linux only: the CLI trusts the test feed through SSL_CERT_FILE, which Go ignores on %s", runtime.GOOS)
	}
	if os.Geteuid() == 0 {
		t.Fatal("real-daemon tests require a non-root user for permission checks and readable artifacts")
	}
	builds := realE2EBuilds(t, filepath.Join(t.TempDir(), "builds"))
	t.Setenv(realE2EBinDirEnv, filepath.Dir(builds[0].path))
	t.Run("pull", func(t *testing.T) { runRealDaemonUpdateChannel(t, installThroughCLI) })
	t.Run("push-catchup-withdrawal-validation", runRealPushChannel)

}

// realScenario is one installed bundle, its daemon's config and its feed.
type realScenario struct {
	work, exe, cfg, adminBase, store string
	env                              []string
	paths                            update.Paths
	feed                             *realE2EFeed
	healthSeconds                    int
}

// realInstaller installs the release just published for b, as `update install`
// does, and returns its output file and the install command's error.
type realInstaller func(t *testing.T, s *realScenario, b realBuild, label string) (string, error)

// installThroughCLI is the operator's command: `spacedatanetwork update install`.
func installThroughCLI(t *testing.T, s *realScenario, _ realBuild, label string) (string, error) {
	return runRealCLI(t, s.work, label, s.exe, s.cfg, s.env)
}

func runRealDaemonUpdateChannel(t *testing.T, install realInstaller) {
	healthSeconds := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	bootBudget := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second

	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2, v3, v4 := builds[0], builds[1], builds[2], builds[1]
	v4.name, v4.version, v4.updateID, v4.sequence = "v4", "1.1.2-e2e.4", "cli-bundle-release-e2e-4", 400

	// The release key the bundle trusts, and one it does not.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, strangerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	feed := newRealE2EFeed(t, work)

	// The installed bundle: v1, its manifest naming the test feed, its roots.
	root := filepath.Join(work, "bundle")
	paths := update.PathsFor(root)
	exe := filepath.Join(root, "bin", "spacedatanetwork")
	copyExecutable(t, v1.path, exe)
	writeE2EJSON(t, filepath.Join(root, "manifest.json"), realBundleManifest(v1, feed.base))
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	writeE2EJSON(t, paths.Trust, update.TrustedRoots{update.RootKeyID(pub): base64.StdEncoding.EncodeToString(spki)})

	home := filepath.Join(work, "home")
	store := filepath.Join(work, "store")
	for _, dir := range []string{home, store} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	adminPort := freeLoopbackPort(t)
	cfg := filepath.Join(work, "config.yaml")
	writeRealDaemonConfig(t, cfg, store, adminPort, freeLoopbackPort(t), freeLoopbackPort(t), healthSeconds)
	adminBase := "http://127.0.0.1:" + strconv.Itoa(adminPort)
	// A Linux node whose account cannot read the machine fingerprint's DMI
	// source refuses its first start without a key password file, and the
	// installers give such a node one. Whether a hosted CI runner is such a
	// host differs from run to run, so these daemons have the file on every
	// host. It is named in their environment, as a service unit names it, and
	// never in this test process's own.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	keyPassword := filepath.Join(work, "key-password")
	if err := os.WriteFile(keyPassword, []byte(hex.EncodeToString(secret)), 0o400); err != nil {
		t.Fatal(err)
	}
	env := append(environWithout("INVOCATION_ID", "SDN_CONFIG", "SDN_UPDATE_TRUST_ROOTS", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", config.EnvKeyPassword, config.EnvKeyPasswordFile),
		"HOME="+home, "SSL_CERT_FILE="+feed.certFile, config.EnvKeyPasswordFile+"="+keyPassword)
	// The compiled FlatSQL engine is cached per host (graph/e2e/sdn-node.mjs
	// shares it too): the daemons reuse the runner's cache instead of
	// compiling it again under the throwaway HOME.
	if os.Getenv("XDG_CACHE_HOME") == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			env = append(env, "XDG_CACHE_HOME="+cache)
		}
	}

	tracker := &realDaemonTracker{root: root}
	t.Cleanup(tracker.stopAll)
	s := &realScenario{work: work, exe: exe, cfg: cfg, adminBase: adminBase, store: store, env: env, paths: paths, feed: feed, healthSeconds: healthSeconds}
	healthBudget := time.Duration(healthSeconds) * time.Second
	installed := func(b realBuild, label string) string {
		t.Helper()
		out, err := install(t, s, b, label)
		// Tracked before anything can fail: the helper and every daemon it
		// starts are stopped when the test ends (waitRealHelper keeps adding).
		tracker.addFrom(out)
		if err != nil {
			t.Fatalf("update install (%s): %v\n%s", label, err, readFileString(out))
		}
		if text := readFileString(out); !strings.Contains(text, "helper_started=") {
			t.Fatalf("update install (%s) did not go through the helper; a daemon is running:\n%s", label, text)
		}
		return out
	}

	// Isolation is checked BEFORE any daemon starts, on the config the daemon
	// reads: a fallback to the built-in bootstrap peers would already have
	// dialed production nodes by the time a log said so.
	requireIsolatedConfig(t, cfg)

	// Says in the log whether this host would have refused a node without the
	// key password file (ok, absent or refused).
	t.Logf("machine fingerprint for this account on this host: %s", keys.CheckMachineFingerprint().Status)

	// v1, started by hand like an operator's first run.
	daemonLogs := []string{filepath.Join(work, "daemon-v1.log")}
	v1Log, err := os.Create(daemonLogs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer v1Log.Close()
	daemon := exec.Command(exe, "daemon", "--allow-multi-daemon", "--config", cfg)
	daemon.Dir, daemon.Env, daemon.Stdout, daemon.Stderr = work, env, v1Log, v1Log
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	tracker.add(daemon.Process.Pid)
	go func() { _ = daemon.Wait() }()
	waitRealReady(t, adminBase, bootBudget, filepath.Join(work, "daemon-v1.log"))
	requireIsolated(t, filepath.Join(work, "daemon-v1.log"))
	requireRealIdentity(t, adminBase, v1)

	// PASS: v1 -> v2 through the helper.
	feed.publish(t, v2, priv)
	out := installed(v2, "v2")
	daemonLogs = append(daemonLogs, out)
	requireHelperHealthBudget(t, out, healthBudget)
	waitRealHelper(t, tracker, out, time.Duration(2*healthSeconds)*time.Second+bootBudget)
	// The daemons the helper starts write into its output: none may have
	// reached a bootstrap peer either.
	requireIsolated(t, out)
	requireRealIdentity(t, adminBase, v2)
	if state, err := update.LoadState(paths); err != nil || state.Sequence != v2.sequence || state.UpdateID != v2.updateID {
		t.Fatalf("state after v2 = %+v (%v), want sequence %d update %s", state, err, v2.sequence, v2.updateID)
	}
	if update.HasFailedUpdate(paths, v2.updateID) {
		t.Fatalf("v2 was quarantined:\n%s", readFileString(out))
	}
	if !ledgerHas(t, root, v2.updateID, "health-passed") {
		t.Fatalf("the ledger shows no health-passed for %s: %v\nhelper output:\n%s", v2.updateID, ledgerActions(t, root), readFileString(out))
	}

	// UNHEALTHY: v2 -> v3, never ready. Reverted to v2 and quarantined.
	feed.publish(t, v3, priv)
	out = installed(v3, "v3")
	daemonLogs = append(daemonLogs, out)
	requireHelperHealthBudget(t, out, healthBudget)
	waitRealHelper(t, tracker, out, time.Duration(3*healthSeconds)*time.Second+bootBudget)
	requireIsolated(t, out)
	// The gate gave v3 the configured budget: neither the helper's old bare
	// 60 s nor the 600 s default.
	requireGateWaited(t, root, v3, healthBudget)
	if text := readFileString(out); !strings.Contains(text, "rolled back to "+v2.version) {
		t.Fatalf("the helper did not report a rollback to %s:\n%s", v2.version, text)
	}
	requireRealIdentity(t, adminBase, v2)
	if !update.HasFailedUpdate(paths, v3.updateID) {
		t.Fatalf("v3 was reverted but not quarantined under updates/failed/%s", v3.updateID)
	}
	if !ledgerHas(t, root, v3.updateID, "health-failed") {
		t.Fatalf("the ledger shows no health-failed for %s: %v", v3.updateID, ledgerActions(t, root))
	}
	if state, err := update.LoadState(paths); err != nil || state.Sequence != v2.sequence {
		t.Fatalf("state after the revert = %+v (%v), want v2's sequence %d", state, err, v2.sequence)
	}

	// WRONG KEY: v4 signed by a key the bundle does not trust.
	feed.publish(t, v4, strangerPriv)
	out, err = install(t, s, v4, "v4")
	if err == nil {
		t.Fatalf("update install (v4) succeeded, want a refusal:\n%s", readFileString(out))
	}
	if text := readFileString(out); !strings.Contains(text, "untrusted update signing key") || strings.Contains(text, "helper_pid=") {
		t.Fatalf("a release signed by an untrusted key was not refused before the helper:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(paths.Staged, v4.updateID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the untrusted release was staged (%v)", err)
	}
	requireRealIdentity(t, adminBase, v2)

	// Once more at the end: the daemons kept logging after their helper exited.
	for _, log := range daemonLogs {
		requireIsolated(t, log)
	}
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return n
}

// realE2EBuilds returns v1, v2 and v3 (never ready), prebuilt or built here.
func realE2EBuilds(t *testing.T, dir string) []realBuild {
	t.Helper()
	builds := []realBuild{
		{name: "v1", version: "1.1.2-e2e.1", updateID: "cli-bundle-release-e2e-1", sequence: 100},
		{name: "v2", version: "1.1.2-e2e.2", updateID: "cli-bundle-release-e2e-2", sequence: 200},
		{name: "v3", version: "1.1.2-e2e.3", updateID: "cli-bundle-release-e2e-3", sequence: 300},
	}
	prebuilt := strings.TrimSpace(os.Getenv(realE2EBinDirEnv))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range builds {
		b := &builds[i]
		if prebuilt != "" {
			b.path = filepath.Join(prebuilt, "spacedatanetwork-"+b.name)
		} else {
			b.path = filepath.Join(dir, "spacedatanetwork-"+b.name)
			args := []string{"build", "-o", b.path}
			if b.name == "v3" {
				args = append(args, "-tags", "sdn_e2e_not_ready")
			}
			args = append(args, "./cmd/spacedatanetwork")
			moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			build := exec.Command(filepath.Join(moduleRoot, "..", "scripts", "go-with-wasmedge.sh"), args...)
			build.Dir = moduleRoot
			// The release stamp travels in SDN_GO_LDFLAGS: an -ldflags argument
			// would replace the wrapper's static WasmEdge link line.
			build.Env = append(environWithout("SDN_GO_LDFLAGS"),
				"SDN_GO_LDFLAGS=-X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=e2e-"+b.name)
			start := time.Now()
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %s: %v\n%s", b.name, err, output)
			}
			t.Logf("built %s in %s", b.name, time.Since(start).Round(time.Second))
		}
		b.sha = e2eFileSHA256(b.path)
		if b.sha == "" {
			t.Fatalf("no %s build at %s", b.name, b.path)
		}
	}
	if builds[0].sha == builds[1].sha || builds[1].sha == builds[2].sha {
		t.Fatal("the builds are not distinct binaries")
	}
	return builds
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// realBundleManifest is the manifest.json of a bundle running b, on the
// release channel of the test feed.
func realBundleManifest(b realBuild, feedBase string) map[string]any {
	return map[string]any{
		"schema":    "org.spacedatanetwork.bundle.v1",
		"version":   b.version,
		"channel":   realE2EChannel,
		"signature": "e2e",
		"os":        runtime.GOOS,
		"arch":      runtime.GOARCH,
		"update": map[string]any{
			"feedBaseUrl":   feedBase,
			"pubsubTopic":   "/sdn/updates/v1/" + realE2EChannel,
			"updaterModule": "org.spacedatanetwork.updater",
			"updaterWasm":   "runtime/modules/org.spacedatanetwork.updater.wasm",
		},
		"artifacts": []map[string]any{{"path": "bin/spacedatanetwork", "sha256": b.sha}},
	}
}

// writeRealDaemonConfig is an isolated test node: loopback only, no DHT, relay
// or mDNS, loopback admin without TLS — and NO REAL BOOTSTRAP PEER. An empty
// network.bootstrap falls back to the built-in defaults, which are the
// production SDN nodes and the public IPFS bootstrappers; a configured list
// replaces them. So the list names one throwaway peer on a loopback port
// nothing listens on: the node dials nothing off this machine.
func writeRealDaemonConfig(t *testing.T, path, store string, adminPort, kuboPort, gatewayPort, healthSeconds int) {
	t.Helper()
	nowhere, _, err := libp2pcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nowhereID, err := peer.IDFromPrivateKey(nowhere)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", freeLoopbackPort(t), nowhereID)
	lines := []string{
		"mode: full",
		"network:", "  listen:", "    - /ip4/127.0.0.1/tcp/0", "  bootstrap:", fmt.Sprintf("    - %q", bootstrapAddr), "  edge_relays: []", "  enable_relay: false",
		"storage:", fmt.Sprintf("  path: %q", store), "  max_size: 1GB",
		"peers:", "  strict_mode: false", "  enable_dht: false", "  enable_mdns: false",
		"admin:", "  enabled: true", fmt.Sprintf("  listen_addr: \"127.0.0.1:%d\"", adminPort), "  require_auth: true", "  tls_mode: disabled",
		fmt.Sprintf("  ipfs_api_url: \"http://127.0.0.1:%d\"", kuboPort), fmt.Sprintf("  ipfs_gateway_url: \"http://127.0.0.1:%d\"", gatewayPort),
		"setup:", fmt.Sprintf("  data_path: %q", store),
		// No --health-timeout is passed: `update install` must hand the helper
		// this budget (installHealthTimeout). requireHelperHealthBudget checks
		// the helper's argv, requireGateWaited how long v3's gate waited.
		"update:", "  enabled: true", fmt.Sprintf("  health_timeout_seconds: %d", healthSeconds),
		"",
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// realE2EFeed is an HTTPS update feed on loopback.
type realE2EFeed struct {
	srv      *httptest.Server
	base     string
	certFile string
	dir      string

	forward string
	disk    bool
	mu      sync.Mutex
	files   map[string][]byte
	entries []update.ProviderFeedUpdate
}

func newRealE2EFeed(t *testing.T, work string) *realE2EFeed {
	t.Helper()
	f := &realE2EFeed{files: map[string][]byte{}, dir: filepath.Join(work, "feed")}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		forward := f.forward
		f.mu.Unlock()
		if forward != "" {
			target, _ := url.Parse(forward)
			httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		body, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	f.base = f.srv.URL + "/updates"
	f.certFile = filepath.Join(work, "feed-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw})
	if err := os.WriteFile(f.certFile, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *realE2EFeed) lane() string {
	return "/cli-bundle/" + realE2EChannel + "/" + runtime.GOOS + "/" + runtime.GOARCH
}

// publish wraps b as the release lane does — a bundle archive with no trust/,
// in an inert carrier, under a manifest signed through updatesign (statement
// domain) — and adds it to the index.
func (f *realE2EFeed) publish(t *testing.T, b realBuild, signingKey ed25519.PrivateKey) {
	t.Helper()
	archive := realBundleArchive(t, b, f.base)
	carrier := update.BuildCarrier(archive)
	signerPub := signingKey.Public().(ed25519.PublicKey)
	spki, err := x509.MarshalPKIXPublicKey(signerPub)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	doc := map[string]any{
		"schema":     update.ManifestSchema,
		"update_id":  b.updateID,
		"version":    b.version,
		"sequence":   b.sequence,
		"channel":    realE2EChannel,
		"created_at": now.Add(-time.Hour).Format(time.RFC3339),
		"expires_at": now.Add(24 * time.Hour).Format(time.RFC3339),
		"target":     map[string]any{"platform": runtime.GOOS, "arch": runtime.GOARCH, "kind": "cli-bundle"},
		"bundle":     map[string]any{"hash": e2eSHA256(archive), "size": int64(len(archive)), "format": "tar.gz"},
		"wasm":       map[string]any{"hash": e2eSHA256(carrier)},
		"signing": map[string]any{
			"key_id":           update.RootKeyID(signerPub),
			"algorithm":        "Ed25519",
			"public_key":       base64.StdEncoding.EncodeToString(spki),
			"statement_domain": sigdomain.DomainUpdateManifestV1,
		},
	}
	signer, err := updatesign.NewSigner(signingKey, updatesign.NewAuditLog(filepath.Join(f.dir, "audit.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	result, err := signer.Sign(updatesign.Request{Manifest: unsigned, Requester: "e2e"})
	if err != nil {
		t.Fatalf("sign %s: %v", b.name, err)
	}
	doc["signing"].(map[string]any)["signature"] = result.SignatureB64
	manifest, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	versionPath := "/updates" + f.lane() + "/" + b.version
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[versionPath+"/manifest.json"] = manifest
	f.files[versionPath+"/update.wasm"] = carrier
	f.entries = append(f.entries, update.ProviderFeedUpdate{
		UpdateID:    b.updateID,
		Version:     b.version,
		Sequence:    b.sequence,
		Channel:     realE2EChannel,
		Target:      update.ManifestTarget{Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: "cli-bundle"},
		ExpiresAt:   doc["expires_at"].(string),
		ManifestURL: f.srv.URL + versionPath + "/manifest.json",
		CarrierURL:  f.srv.URL + versionPath + "/update.wasm",
		BundleHash:  e2eSHA256(archive),
		BundleSize:  int64(len(archive)),
		WasmHash:    e2eSHA256(carrier),
		WasmSize:    int64(len(carrier)),
	})
	index, err := json.Marshal(update.ProviderFeed{
		Schema:      update.ProviderFeedSchema,
		GeneratedAt: now.Format(time.RFC3339),
		FeedBaseURL: f.base,
		Updates:     f.entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.files["/updates"+f.lane()+"/index.json"] = index
	if f.disk {
		dir := filepath.Join(f.dir, strings.TrimPrefix(f.lane(), "/"), b.version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string][]byte{"manifest.json": manifest, "update.wasm": carrier, "../index.json": index} {
			if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// realBundleArchive is the bundle payload for b: a wrapper directory holding
// bin/spacedatanetwork and manifest.json, and no trust/ (a payload carrying
// trust/ is refused by every node).
func realBundleArchive(t *testing.T, b realBuild, feedBase string) []byte {
	t.Helper()
	stage := filepath.Join(t.TempDir(), "spacedatanetwork-"+b.version+"-"+runtime.GOOS+"-"+runtime.GOARCH)
	copyExecutable(t, b.path, filepath.Join(stage, "bin", "spacedatanetwork"))
	includeWalletRuntime(t, stage)
	writeE2EJSON(t, filepath.Join(stage, "manifest.json"), realBundleManifest(b, feedBase))
	archive := stage + ".tar.gz"
	tarCmd := exec.Command("tar", "-czf", archive, "-C", filepath.Dir(stage), filepath.Base(stage))
	tarCmd.Env = append(os.Environ(), "COPYFILE_DISABLE=1")
	if output, err := tarCmd.CombinedOutput(); err != nil {
		t.Fatalf("tar %s: %v\n%s", b.name, err, output)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// runRealCLI runs `update install` and returns its output file, which the
// helper it starts keeps writing to after the command exits.
func runRealCLI(t *testing.T, work, label, exe, cfg string, env []string) (string, error) {
	t.Helper()
	path := filepath.Join(work, "install-"+label+".log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "update", "install", "--config", cfg)
	// An *os.File, not a buffer: the helper inherits it and writes to it after
	// this command has exited, and a pipe would make Wait wait for the helper.
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = work, env, file, file
	return path, cmd.Run()
}

var (
	helperPIDPattern  = regexp.MustCompile(`helper_pid=(\d+)`)
	restartPIDPattern = regexp.MustCompile(`restart=started pid=(\d+)`)
)

// waitRealHelper waits for the helper named in out to exit. Every daemon the
// helper starts is handed to tracker as soon as the helper names it, so a
// failure anywhere still stops it.
func waitRealHelper(t *testing.T, tracker *realDaemonTracker, out string, budget time.Duration) {
	t.Helper()
	pid := realHelperPID(t, out)
	deadline := time.Now().Add(budget)
	for processAlive(pid) {
		tracker.addFrom(out)
		if time.Now().After(deadline) {
			t.Fatalf("the helper (pid %d) is still running after %s:\n%s", pid, budget, readFileString(out))
		}
		time.Sleep(time.Second)
	}
	tracker.addFrom(out)
	t.Logf("helper %d finished:\n%s", pid, tailString(readFileString(out), 60))
}

func realHelperPID(t *testing.T, out string) int {
	t.Helper()
	match := helperPIDPattern.FindStringSubmatch(readFileString(out))
	if match == nil {
		t.Fatalf("no helper pid in:\n%s", readFileString(out))
	}
	pid, _ := strconv.Atoi(match[1])
	return pid
}

// requireHelperHealthBudget: the helper `update install` started was handed
// the node's update.health_timeout_seconds (no --health-timeout was given to
// install), read from the helper's own argv while it runs (Linux /proc).
func requireHelperHealthBudget(t *testing.T, out string, want time.Duration) {
	t.Helper()
	pid := realHelperPID(t, out)
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		t.Fatalf("read the helper's argv (pid %d): %v\n%s", pid, err, readFileString(out))
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	i := slices.Index(args, "--health-timeout")
	if i < 0 || i+1 >= len(args) || args[i+1] != want.String() {
		t.Fatalf("the helper was not handed update.health_timeout_seconds (--health-timeout %s): argv %q", want, args)
	}
}

// requireGateWaited: v3 never becomes ready, so its gate runs out its whole
// budget; the ledger's restart and health-failed times for v3 show how long
// that was. Within [budget-2s, budget+90s]: not the helper's bare 60 s, not
// the 600 s default.
func requireGateWaited(t *testing.T, root string, b realBuild, budget time.Duration) {
	t.Helper()
	var restarted, failed time.Time
	for _, e := range readLedger(t, root) {
		if e.UpdateID != b.updateID {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.RecordedAt)
		if err != nil {
			t.Fatalf("ledger time %q: %v", e.RecordedAt, err)
		}
		if e.Action == "restart" && e.Version == b.version && restarted.IsZero() {
			restarted = at
		}
		if e.Action == "health-failed" && failed.IsZero() {
			failed = at
		}
	}
	if restarted.IsZero() || failed.IsZero() {
		t.Fatalf("the ledger has no restart and health-failed for %s: %v", b.updateID, ledgerActions(t, root))
	}
	waited := failed.Sub(restarted)
	if waited < budget-2*time.Second || waited > budget+90*time.Second {
		t.Fatalf("%s's gate waited %s after its restart, want the configured %s", b.name, waited, budget)
	}
	t.Logf("%s's gate waited %s (budget %s)", b.name, waited, budget)
}

// processAlive: the pid exists and is not a zombie.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	return !strings.HasPrefix(processField(pid, "stat"), "Z")
}

// processField reads one ps field of pid ("stat" or "command"); empty when
// the process is gone.
func processField(pid int, field string) string {
	out, err := exec.Command("ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func waitRealReady(t *testing.T, base string, budget time.Duration, logPath string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	start := time.Now()
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + daemonReadyPath); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("daemon ready after %s", time.Since(start).Round(time.Second))
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the daemon was not ready within %s:\n%s", budget, tailString(readFileString(logPath), 40))
}

// requireIsolatedConfig fails the test before any daemon starts unless the
// config it will read keeps it on this machine: bootstrap peers that resolve
// without the fallback to the built-in defaults (an empty or invalid list
// falls back to the production nodes), every one of them on loopback, no DHT
// or mDNS, and the admin API on loopback.
func requireIsolatedConfig(t *testing.T, cfgPath string) {
	t.Helper()
	if err := isolatedConfigProblem(cfgPath); err != nil {
		t.Fatalf("the test daemon would not be isolated: %v", err)
	}
}

func isolatedConfigProblem(cfgPath string) error {
	cfg, _, err := config.LoadResolved(cfgPath)
	if err != nil {
		return fmt.Errorf("load its config: %w", err)
	}
	peers, usedFallback, err := bootstrap.ResolveBootstrapPeers(cfg.Network.Bootstrap)
	if err != nil || usedFallback || len(peers) == 0 {
		return fmt.Errorf("its bootstrap list %q falls back to the built-in peers (fallback=%t, err=%v)", cfg.Network.Bootstrap, usedFallback, err)
	}
	for _, p := range peers {
		for _, addr := range p.AddrInfo.Addrs {
			if !strings.HasPrefix(addr.String(), "/ip4/127.") {
				return fmt.Errorf("its bootstrap peer %s is not on loopback (%s)", p.AddrInfo.ID, addr)
			}
		}
	}
	if cfg.Peers.EnableDHT || cfg.Peers.EnableMDNS {
		return fmt.Errorf("it would discover peers (dht=%t mdns=%t)", cfg.Peers.EnableDHT, cfg.Peers.EnableMDNS)
	}
	if !strings.HasPrefix(cfg.Admin.ListenAddr, "127.0.0.1:") {
		return fmt.Errorf("its admin API is not on loopback: %q", cfg.Admin.ListenAddr)
	}
	return nil
}

// Not gated: the config the real-daemon E2E writes passes the isolation check,
// and the check refuses the configs that would reach production peers (an
// empty or unparsable bootstrap list falls back to the built-in defaults).
func TestRealDaemonConfigIsIsolated(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	writeRealDaemonConfig(t, cfg, filepath.Join(dir, "store"), freeLoopbackPort(t), freeLoopbackPort(t), freeLoopbackPort(t), 180)
	if err := isolatedConfigProblem(cfg); err != nil {
		t.Fatalf("the E2E's own config: %v", err)
	}
	written, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapLine := regexp.MustCompile(`(?m)^  bootstrap:\n    - "/ip4/127\.0\.0\.1/tcp/\d+/p2p/(\w+)"\n`)
	match := bootstrapLine.FindSubmatch(written)
	if match == nil {
		t.Fatalf("no loopback bootstrap peer in the E2E config:\n%s", written)
	}
	for name, replacement := range map[string]string{
		"empty bootstrap list": "  bootstrap: []\n",
		"no peer id":           "  bootstrap:\n    - \"/ip4/127.0.0.1/tcp/4001\"\n",
		// TEST-NET-1 (RFC 5737): only parsed here, never dialed.
		"a peer off this machine": "  bootstrap:\n    - \"/ip4/192.0.2.10/tcp/4001/p2p/" + string(match[1]) + "\"\n",
	} {
		variant := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".yaml")
		if err := os.WriteFile(variant, bootstrapLine.ReplaceAll(written, []byte(replacement)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := isolatedConfigProblem(variant); err == nil {
			t.Errorf("%s: accepted as isolated", name)
		}
	}
}

// requireIsolated fails the test if a daemon's log shows it fell back to the
// built-in bootstrap peers (production nodes) or connected to one. It backs
// up requireIsolatedConfig, which is what keeps the daemons from dialing.
func requireIsolated(t *testing.T, logPath string) {
	t.Helper()
	text := readFileString(logPath)
	for _, leak := range []string{"falling back to built-in defaults", "Connected to bootstrap peer"} {
		if strings.Contains(text, leak) {
			t.Fatalf("the test daemon is not isolated (%q):\n%s", leak, tailString(text, 40))
		}
	}
}

func requireRealIdentity(t *testing.T, base string, want realBuild) {
	t.Helper()
	identity, err := probeDaemonIdentity(context.Background(), &http.Client{Timeout: 10 * time.Second}, base)
	if err != nil {
		t.Fatalf("/api/v1/id: %v", err)
	}
	if identity.BundleVersion != want.version || identity.BuildSHA256 != want.sha {
		t.Fatalf("/api/v1/id = %s, want %s bundle_version=%s build_sha256=%s", identity, want.name, want.version, want.sha)
	}
}

func readLedger(t *testing.T, root string) []update.DeployLedgerEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "deploy-ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []update.DeployLedgerEntry
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry update.DeployLedgerEntry
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

func ledgerHas(t *testing.T, root, updateID, action string) bool {
	t.Helper()
	return slices.ContainsFunc(readLedger(t, root), func(e update.DeployLedgerEntry) bool {
		return e.UpdateID == updateID && e.Action == action
	})
}

func ledgerActions(t *testing.T, root string) []string {
	t.Helper()
	var actions []string
	for _, e := range readLedger(t, root) {
		actions = append(actions, e.UpdateID+":"+e.Action)
	}
	return actions
}

func readFileString(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func tailString(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// realDaemonTracker stops, at the end, every daemon this test started or that
// a helper it ran started — and only those: a pid is signalled only while its
// command line still names this test's bundle.
type realDaemonTracker struct {
	root string
	mu   sync.Mutex
	pids []int
}

func (r *realDaemonTracker) add(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Contains(r.pids, pid) {
		r.pids = append(r.pids, pid)
	}
}

// addFrom tracks the helper an install output names and every daemon that
// helper started.
func (r *realDaemonTracker) addFrom(out string) {
	text := readFileString(out)
	if m := helperPIDPattern.FindStringSubmatch(text); m != nil {
		if pid, err := strconv.Atoi(m[1]); err == nil {
			r.add(pid)
		}
	}
	for _, m := range restartPIDPattern.FindAllStringSubmatch(text, -1) {
		if pid, err := strconv.Atoi(m[1]); err == nil {
			r.add(pid)
		}
	}
}

func (r *realDaemonTracker) ours(pid int) bool {
	return strings.Contains(processField(pid, "command"), r.root)
}

func (r *realDaemonTracker) stopAll() {
	r.mu.Lock()
	pids := append([]int(nil), r.pids...)
	r.mu.Unlock()
	for _, pid := range pids {
		if processAlive(pid) && r.ours(pid) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for _, pid := range pids {
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
		}
		if processAlive(pid) && r.ours(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// runRealPushChannel drives the sender while the receiving daemon acts autonomously.
func runRealPushChannel(t *testing.T) {
	start := time.Now()
	defer func() { t.Logf("push channel duration: %s", time.Since(start).Round(time.Second)) }()
	work := t.TempDir()
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2, v3, v4 := builds[0], builds[1], builds[2], builds[1]
	v4.name, v4.version, v4.updateID, v4.sequence = "v4", "1.1.2-e2e.4", "cli-bundle-release-e2e-4", 400
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	roots := update.TrustedRoots{update.RootKeyID(pub): base64.StdEncoding.EncodeToString(spki)}
	feed := newRealE2EFeed(t, work)
	feed.disk = true
	if err := os.MkdirAll(feed.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(work, "pipeline-test-key.pem")
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	rootsPath := filepath.Join(work, "pipeline-roots.json")
	writeE2EJSON(t, rootsPath, roots)
	writeE2EJSON(t, filepath.Join(work, "pipeline-repo", "deployment", "release", "fleet-trust-roots.json"), roots)
	if err := os.MkdirAll(filepath.Join(work, "pipeline-repo", "sdn-server"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := newPushDaemon(t, filepath.Join(work, "a"), v1, feed, roots, health)
	// The publisher follows the topic but has no newer installable sequence.
	if err := update.SaveState(a.paths, &update.State{Sequence: 10000}); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "SDN_UPDATE_FEED_DIR="+feed.dir)
	a.start(t, boot)
	feed.mu.Lock()
	feed.forward = a.adminBase
	feed.mu.Unlock()
	b := newPushDaemon(t, filepath.Join(work, "b"), v1, feed, roots, health)
	aInfo := pushPeer(t, a.adminBase)
	b.configure(t, func(cfg *config.Config) {
		cfg.Network.Bootstrap = []string{aInfo.Addrs[0].String() + "/p2p/" + aInfo.ID.String()}
	})
	b.start(t, boot)
	originalPeer := pushPeer(t, b.adminBase).ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	third, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	gossip, err := ps.NewGossipSub(ctx, third)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := gossip.Join(update.SignalTopic(realE2EChannel))
	if err != nil {
		t.Fatal(err)
	}
	defer topic.Close()
	subscription, err := topic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	if err := third.Connect(ctx, aInfo); err != nil {
		t.Fatal(err)
	}
	if err := third.Connect(ctx, pushPeer(t, b.adminBase)); err != nil {
		t.Fatal(err)
	}
	waitPush(t, 30*time.Second, "update topic subscriptions", func() bool { return len(topic.ListPeers()) >= 2 })
	time.Sleep(3 * time.Second)

	signalPath := func(v realBuild) string {
		return filepath.Join(feed.dir, strings.TrimPrefix(feed.lane(), "/"), v.version, "signal.json")
	}
	publish := func(v realBuild) []byte {
		feed.publish(t, v, key)
		runSignalPipeline(t, v1.path, work, "sign-"+v.name, "update", "sign-signals", "--key-file", keyPath, "--roots", rootsPath, "--feed-dir", feed.dir)
		runSignalPipeline(t, v1.path, work, "verify-"+v.name, "update", "sign-signals", "--verify-only", "--feed-dir", feed.dir)
		raw, err := os.ReadFile(signalPath(v))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	send := func(v realBuild, label string) {
		a.command(t, true, label, "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v.version)
	}
	checkTrigger := func(v realBuild, trigger string) {
		if !slices.ContainsFunc(readLedger(t, b.paths.Root), func(e update.DeployLedgerEntry) bool { return e.UpdateID == v.updateID && e.Trigger == trigger }) {
			t.Fatalf("no %s trigger for %s", trigger, v.name)
		}
	}
	v2signal := publish(v2)
	// Noncanonical whitespace must survive the authenticated route byte for byte.
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, v2signal, "", "  "); err != nil {
		t.Fatal(err)
	}
	v2signal = formatted.Bytes()
	if err := os.WriteFile(signalPath(v2), v2signal, 0o644); err != nil {
		t.Fatal(err)
	}
	send(v2, "push-v2")
	messageCtx, messageCancel := context.WithTimeout(ctx, 30*time.Second)
	message, err := subscription.Next(messageCtx)
	messageCancel()
	if err != nil || !bytes.Equal(message.Data, v2signal) {
		t.Fatalf("published signal bytes changed: %v", err)
	}
	b.waitUpgrade(t, v2, boot+2*time.Duration(health)*time.Second)
	checkTrigger(v2, "signal")
	t.Log("push installed v2 and preserved its signed wire bytes")
	if pushPeer(t, b.adminBase).ID != originalPeer {
		t.Fatal("push changed the peer identity")
	}

	publish(v3)
	send(v3, "push-v3")
	waitPush(t, boot+3*time.Duration(health)*time.Second, "v3 quarantine and restored v2", func() bool {
		b.trackHelper()
		return update.HasFailedUpdate(b.paths, v3.updateID) && pushIdentityMatches(b.adminBase, v2)
	})
	waitRealReady(t, b.adminBase, boot, b.helperLog())
	requireGateWaited(t, b.paths.Root, v3, time.Duration(health)*time.Second)
	checkTrigger(v3, "signal")
	var alerts json.RawMessage
	if err := b.client(t).get(ctx, "/api/v1/status/alerts", &alerts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(alerts, []byte("update_failed")) || !bytes.Contains(alerts, []byte(v3.updateID)) {
		t.Fatalf("restored daemon has no failed-update alert: %s", alerts)
	}
	waitPush(t, boot, "completed rollback", func() bool { return strings.Contains(readFileString(b.helperLog()), "rolled back to "+v2.version) })
	t.Log("bad pushed build reverted and restored its failure alert")
	countV3 := len(readLedger(t, b.paths.Root))
	runSignalPipeline(t, v1.path, work, "resign-v3", "update", "sign-signals", "--key-file", keyPath, "--roots", rootsPath, "--feed-dir", feed.dir)
	send(v3, "push-v3-again")
	time.Sleep(4 * time.Second)
	if len(readLedger(t, b.paths.Root)) != countV3 {
		t.Fatal("quarantined update was attempted again")
	}
	requireRealIdentity(t, b.adminBase, v2)

	b.stop(t)
	publish(v4)
	send(v4, "push-v4-offline")
	b.configure(t, func(cfg *config.Config) { cfg.Update.CheckDelaySeconds = 2; cfg.Update.CheckIntervalSeconds = 3 })
	b.start(t, boot)
	b.waitUpgrade(t, v4, boot+2*time.Duration(health)*time.Second)
	checkTrigger(v4, "feed-check")
	t.Log("offline recipient caught up through its own feed check")
	if pushPeer(t, b.adminBase).ID != originalPeer {
		t.Fatal("catch-up changed the peer identity")
	}

	// A later candidate is withheld, then withdrawn while B is offline.
	b.stop(t)
	v5 := v4
	v5.name, v5.version, v5.updateID, v5.sequence = "v5", "1.1.2-e2e.5", "cli-bundle-release-e2e-5", 500
	signed := publish(v5)
	stranger := resignPushSignal(t, signed, wrong, 0)
	if err := os.WriteFile(signalPath(v5), stranger, 0o644); err != nil {
		t.Fatal(err)
	}
	a.command(t, false, "wrong-key", "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v5.version)
	if !strings.Contains(readFileString(filepath.Join(a.work, "wrong-key.log")), "untrusted update signal signing key") {
		t.Fatal("sender did not identify the untrusted signal key")
	}
	if err := os.WriteFile(signalPath(v5), signed, 0o644); err != nil {
		t.Fatal(err)
	}
	// An otherwise valid signature with an excessive lifetime is refused too.
	excessive := resignPushSignal(t, signed, key, 8*24*time.Hour)
	if err := os.WriteFile(signalPath(v5), excessive, 0o644); err != nil {
		t.Fatal(err)
	}
	a.command(t, false, "excessive-expiry", "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v5.version)
	if !strings.Contains(readFileString(filepath.Join(a.work, "excessive-expiry.log")), "within seven days") {
		t.Fatal("sender did not identify the excessive signal lifetime")
	}
	if err := os.WriteFile(signalPath(v5), signed, 0o644); err != nil {
		t.Fatal(err)
	}
	a.command(t, true, "withdraw-v5", "update", "withdraw", "--channel", realE2EChannel, "--version", v5.version)
	a.command(t, false, "send-withdrawn", "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v5.version)
	for _, name := range []string{"manifest.json", "update.wasm", "signal.json"} {
		response, err := feed.srv.Client().Get(feed.base + feed.lane() + "/" + v5.version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("withdrawn %s served: %d", name, response.StatusCode)
		}
	}
	b.start(t, boot)
	if err := third.Connect(ctx, pushPeer(t, b.adminBase)); err != nil {
		t.Fatal(err)
	}
	waitPush(t, 30*time.Second, "rejoined update subscription", func() bool { return slices.Contains(topic.ListPeers(), originalPeer) })
	time.Sleep(4 * time.Second)
	before := strings.Count(b.logs(), "Update signal handler received")
	for _, raw := range [][]byte{[]byte("junk"), stranger, excessive, bytes.Repeat([]byte("x"), update.SignalMaxBytes+1)} {
		if err := topic.Publish(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(5 * time.Second)
	if got := strings.Count(b.logs(), "Update signal handler received"); got != before {
		t.Fatalf("invalid gossip reached handler: before %d after %d", before, got)
	}
	if _, err := os.Stat(filepath.Join(b.paths.Staged, v5.updateID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted or withdrawn update staged: %v", err)
	}
	requireRealIdentity(t, b.adminBase, v4)
	// Valid old gossip is delivered, but the withdrawn manifest cannot be fetched.
	if err := topic.Publish(ctx, signed); err != nil {
		t.Fatal(err)
	}
	waitPush(t, 30*time.Second, "withdrawn manifest refusal", func() bool {
		return strings.Contains(b.logs(), "fetch update manifest:") && strings.Contains(b.logs(), "HTTP 404")
	})
	time.Sleep(5 * time.Second)
	requireRealIdentity(t, b.adminBase, v4)
	if got := strings.Count(b.logs(), "Update signal "+v5.updateID+" failed:"); got != 1 {
		t.Fatalf("withdrawn fetch reported %d times", got)
	}
	t.Log("push, rollback, restored alert, catch-up, withdrawal, and gossip rejection verified")
}

type pushDaemon struct {
	*realScenario
	tracker  *realDaemonTracker
	logPaths []string
}

func newPushDaemon(t *testing.T, work string, build realBuild, feed *realE2EFeed, roots update.TrustedRoots, health int) *pushDaemon {
	t.Helper()
	for _, name := range []string{"home", "store", "bundle/bin"} {
		if err := os.MkdirAll(filepath.Join(work, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(work, "bundle")
	d := &pushDaemon{realScenario: &realScenario{work: work, exe: filepath.Join(root, "bin", "spacedatanetwork"), cfg: filepath.Join(work, "config.yaml"), store: filepath.Join(work, "store"), paths: update.PathsFor(root), feed: feed, healthSeconds: health}, tracker: &realDaemonTracker{root: root}}
	copyExecutable(t, build.path, d.exe)
	includeWalletRuntime(t, root)
	writeE2EJSON(t, filepath.Join(root, "manifest.json"), realBundleManifest(build, feed.base))
	writeE2EJSON(t, d.paths.Trust, roots)
	port := freeLoopbackPort(t)
	d.adminBase = "http://127.0.0.1:" + strconv.Itoa(port)
	writeRealDaemonConfig(t, d.cfg, d.store, port, freeLoopbackPort(t), freeLoopbackPort(t), health)
	d.configure(t, func(cfg *config.Config) { cfg.Update.MinIntervalSeconds = 1; cfg.Update.CheckDelaySeconds = 3600 })
	password := filepath.Join(work, "test-key-password")
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(password, []byte(hex.EncodeToString(secret)), 0o400); err != nil {
		t.Fatal(err)
	}
	d.env = append(environWithout("INVOCATION_ID", "SDN_CONFIG", "SDN_UPDATE_TRUST_ROOTS", "SDN_UPDATE_FEED_DIR", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", config.EnvKeyPassword, config.EnvKeyPasswordFile), "HOME="+filepath.Join(work, "home"), "SSL_CERT_FILE="+feed.certFile, config.EnvKeyPasswordFile+"="+password, "SDN_ALLOW_MULTI_DAEMON=1", "HD_WALLET_WASM_PATH="+filepath.Join(root, "runtime", "modules", "hd-wallet-wasi.wasm"))
	t.Cleanup(func() { d.trackHelper(); d.tracker.stopAll() })
	return d
}

func (d *pushDaemon) configure(t *testing.T, change func(*config.Config)) {
	t.Helper()
	cfg, _, err := config.LoadResolved(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	change(cfg)
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	requireIsolatedConfig(t, d.cfg)
}

func (d *pushDaemon) start(t *testing.T, budget time.Duration) {
	t.Helper()
	name := filepath.Join(d.work, fmt.Sprintf("daemon-%d.log", len(d.logPaths)))
	d.logPaths = append(d.logPaths, name)
	out, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd := exec.Command(d.exe, "daemon", "--allow-multi-daemon", "--config", d.cfg)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.work, d.env, out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	d.tracker.add(cmd.Process.Pid)
	go func() { _ = cmd.Wait() }()
	waitRealReady(t, d.adminBase, budget, name)
}

func (d *pushDaemon) stop(t *testing.T) { t.Helper(); d.trackHelper(); d.tracker.stopAll() }
func (d *pushDaemon) helperLog() string {
	return filepath.Join(d.paths.Updates, "helper", "self-upgrade.log")
}
func (d *pushDaemon) logs() string {
	var out strings.Builder
	for _, p := range append(append([]string{}, d.logPaths...), d.helperLog()) {
		out.WriteString(readFileString(p))
	}
	return out.String()
}
func (d *pushDaemon) trackHelper() {
	d.tracker.addFrom(d.helperLog())
	for _, match := range regexp.MustCompile(`Self-upgrade to .* pid (\d+)\)`).FindAllStringSubmatch(d.logs(), -1) {
		pid, _ := strconv.Atoi(match[1])
		d.tracker.add(pid)
	}
}
func (d *pushDaemon) waitUpgrade(t *testing.T, build realBuild, budget time.Duration) {
	t.Helper()
	waitPush(t, budget, "healthy "+build.name, func() bool {
		d.trackHelper()
		return pushIdentityMatches(d.adminBase, build) && ledgerHasIfPresent(d.paths.Root, build.updateID, "health-passed")
	})
	requireRealIdentity(t, d.adminBase, build)
}
func ledgerHasIfPresent(root, id, action string) bool {
	raw := readFileString(filepath.Join(root, "deploy-ledger.jsonl"))
	for _, line := range strings.Split(raw, "\n") {
		var e update.DeployLedgerEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.UpdateID == id && e.Action == action {
			return true
		}
	}
	return false
}
func pushIdentityMatches(base string, b realBuild) bool {
	id, err := probeDaemonIdentity(context.Background(), &http.Client{Timeout: time.Second}, base)
	return err == nil && id.BundleVersion == b.version && id.BuildSHA256 == b.sha
}
func waitPush(t *testing.T, budget time.Duration, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s after %s", what, budget)
}

func pushPeer(t *testing.T, base string) peer.AddrInfo {
	t.Helper()
	response, err := http.Get(base + "/api/v1/id")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		PeerID    string   `json:"peer_id"`
		Addresses []string `json:"listen_addresses"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	id, err := peer.Decode(body.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	info := peer.AddrInfo{ID: id}
	for _, s := range body.Addresses {
		if strings.HasPrefix(s, "/ip4/127.") {
			addr, err := ma.NewMultiaddr(s)
			if err != nil {
				t.Fatal(err)
			}
			info.Addrs = append(info.Addrs, addr)
		}
	}
	if len(info.Addrs) == 0 {
		t.Fatal("daemon has no loopback peer address")
	}
	return info
}

func (d *pushDaemon) command(t *testing.T, success bool, label string, args ...string) {
	t.Helper()
	out, err := os.Create(filepath.Join(d.work, label+".log"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	args = append(args, "--config", d.cfg)
	cmd := exec.Command(d.exe, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.work, d.env, out, out
	err = cmd.Run()
	if (err == nil) != success {
		t.Fatalf("%s: %v\n%s", label, err, readFileString(out.Name()))
	}
}

func (d *pushDaemon) client(t *testing.T) *adminClient {
	t.Helper()
	old := configPath
	configPath = d.cfg
	defer func() { configPath = old }()
	for _, kv := range d.env {
		key, value, ok := strings.Cut(kv, "=")
		if ok && (key == "HOME" || key == config.EnvKeyPasswordFile || key == "HD_WALLET_WASM_PATH") {
			t.Setenv(key, value)
		}
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	client, err := newAdminClient(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func resignPushSignal(t *testing.T, raw []byte, key ed25519.PrivateKey, ttl time.Duration) []byte {
	t.Helper()
	signal, err := update.ParseSignal(raw)
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	signal.Signing.KeyID, signal.Signing.PublicKey = update.RootKeyID(pub), base64.StdEncoding.EncodeToString(spki)
	if ttl > 0 {
		published, err := time.Parse(time.RFC3339, signal.PublishedAt)
		if err != nil {
			t.Fatal(err)
		}
		signal.ExpiresAt = published.Add(ttl).Format(time.RFC3339)
	}
	unsigned, err := signal.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := update.SignalStatement(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	signal.Signing.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, statement))
	signed, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// keepRealLogs preserves diagnostics without retaining test keys or payloads.
func keepRealLogs(t *testing.T, work string) {
	destination := os.Getenv("SDN_E2E_LOG_DIR")
	if destination == "" {
		return
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(work, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".log") && entry.Name() != "deploy-ledger.jsonl") {
				return nil
			}
			relative, err := filepath.Rel(work, path)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			target := filepath.Join(destination, relative)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.WriteFile(target, raw, 0o600)
		})
	})
}

func runSignalPipeline(t *testing.T, exe, work, label string, args ...string) {
	t.Helper()
	home := filepath.Join(work, "pipeline-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(work, "pipeline-"+label+".log"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd := exec.Command(exe, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = filepath.Join(work, "pipeline-repo", "sdn-server"), out, out
	cmd.Env = append(environWithout("HOME", "SDN_CONFIG", "SDN_UPDATE_TRUST_ROOTS", config.EnvKeyPassword, config.EnvKeyPasswordFile), "HOME="+home)
	if err := cmd.Run(); err != nil {
		t.Fatalf("pipeline %s: %v\n%s", label, err, readFileString(out.Name()))
	}
}

func includeWalletRuntime(t *testing.T, root string) {
	t.Helper()
	raw := walletwasm.EmbeddedHDWalletWasm()
	if len(raw) == 0 {
		t.Fatal("embedded wallet runtime failed its hash check")
	}
	dir := filepath.Join(root, "runtime", "modules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hd-wallet-wasi.wasm"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
