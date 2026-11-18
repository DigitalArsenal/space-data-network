package main

// UPDATES FROM A READ-ONLY INSTALL, ON REAL DAEMONS (stack issue 846).
//
// The node is installed in a folder its account cannot write (every folder of
// the bundle is mode 0555, and the test runs as an ordinary user), the way a
// bundle under /opt owned by root is run by an ordinary account. Three
// behaviors, in order, on one install:
//
//	updates   `update install` of v2 goes through the helper into the home
//	          bundle ($HOME/.spacedatanetwork/bundle) and passes the health
//	          gate; the install folder is unchanged; a restart of the
//	          INSTALLED path, as a supervisor does it, runs v2, and so does a
//	          command typed against the installed path.
//	rollback  `update install` of v3 (never ready) fails its health gate, is
//	          quarantined in the home bundle, and v2 runs again; a restart of
//	          the installed path runs v2.
//	tampered  a byte appended to the home build's binary: a restart of the
//	          installed path refuses the home build, says so, and runs the
//	          installed v1.
//
// Gated and run like TestUpdateChannelRealDaemonE2E, of which it is a subtest.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

func runRealHomeUpdate(t *testing.T) {
	healthSeconds := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	bootBudget := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second

	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2, v3 := builds[0], builds[1], builds[2]

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	feed := newRealE2EFeed(t, work)

	// The installed bundle: v1, its roots, then made read-only.
	root := filepath.Join(work, "install")
	exe := filepath.Join(root, "bin", "spacedatanetwork")
	copyExecutable(t, v1.path, exe)
	writeE2EJSON(t, filepath.Join(root, "manifest.json"), realBundleManifest(v1, feed.base))
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	writeE2EJSON(t, filepath.Join(root, "trust", "update-roots.json"), update.TrustedRoots{update.RootKeyID(pub): base64.StdEncoding.EncodeToString(spki)})
	setTreeWritable(t, root, false)
	t.Cleanup(func() { setTreeWritable(t, root, true) })
	if f, err := os.Create(filepath.Join(root, "probe")); err == nil {
		f.Close()
		t.Fatal("the install folder is writable by this account; the test needs one it cannot write")
	}

	home := filepath.Join(work, "home")
	store := filepath.Join(work, "store")
	for _, dir := range []string{home, store} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	homeRoot := filepath.Join(home, ".spacedatanetwork", "bundle")
	homePaths := update.PathsFor(homeRoot)
	adminPort := freeLoopbackPort(t)
	cfg := filepath.Join(work, "config.yaml")
	writeRealDaemonConfig(t, cfg, store, adminPort, freeLoopbackPort(t), freeLoopbackPort(t), healthSeconds)
	requireIsolatedConfig(t, cfg)
	adminBase := "http://127.0.0.1:" + strconv.Itoa(adminPort)
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
	if os.Getenv("XDG_CACHE_HOME") == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			env = append(env, "XDG_CACHE_HOME="+cache)
		}
	}

	// The test's processes run from the install folder or from the home
	// bundle, both under work.
	tracker := &realDaemonTracker{root: work}
	t.Cleanup(tracker.stopAll)
	s := &realScenario{work: work, exe: exe, cfg: cfg, adminBase: adminBase, store: store, env: env, feed: feed, healthSeconds: healthSeconds}
	daemonPID := 0
	daemonLogs := []string{}
	start := func(label string) string {
		t.Helper()
		logPath := filepath.Join(work, "daemon-"+label+".log")
		file, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { file.Close() })
		// The installed path, as a service unit names it.
		daemon := exec.Command(exe, "daemon", "--allow-multi-daemon", "--config", cfg)
		daemon.Dir, daemon.Env, daemon.Stdout, daemon.Stderr = work, env, file, file
		if err := daemon.Start(); err != nil {
			t.Fatal(err)
		}
		daemonPID = daemon.Process.Pid
		tracker.add(daemonPID)
		go func() { _ = daemon.Wait() }()
		waitRealReady(t, adminBase, bootBudget, logPath)
		requireIsolated(t, logPath)
		daemonLogs = append(daemonLogs, logPath)
		return logPath
	}
	stop := func() {
		t.Helper()
		if daemonPID == 0 || !processAlive(daemonPID) {
			t.Fatalf("no running daemon to stop (pid %d)", daemonPID)
		}
		_ = syscall.Kill(daemonPID, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Minute)
		for processAlive(daemonPID) {
			if time.Now().After(deadline) {
				t.Fatalf("the daemon (pid %d) did not stop within 2m", daemonPID)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	install := func(b realBuild, label string) string {
		t.Helper()
		feed.publish(t, b, priv)
		out, err := installThroughCLI(t, s, b, label)
		tracker.addFrom(out)
		if err != nil {
			t.Fatalf("update install (%s): %v\n%s", label, err, readFileString(out))
		}
		if !strings.Contains(readFileString(out), "helper_started=") {
			t.Fatalf("update install (%s) did not go through the helper:\n%s", label, readFileString(out))
		}
		waitRealHelper(t, tracker, out, time.Duration(3*healthSeconds)*time.Second+bootBudget)
		requireIsolated(t, out)
		daemonLogs = append(daemonLogs, out)
		// The helper restarted the daemon through the installed path: the
		// last pid it names is the daemon now serving.
		if matches := restartPIDPattern.FindAllStringSubmatch(readFileString(out), -1); len(matches) > 0 {
			daemonPID, _ = strconv.Atoi(matches[len(matches)-1][1])
		}
		return out
	}
	typedVersion := func(label string) string {
		t.Helper()
		cmd := exec.Command(exe, "version")
		cmd.Dir, cmd.Env = work, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("version (%s): %v\n%s", label, err, out)
		}
		return string(out)
	}
	installedUntouched := func() {
		t.Helper()
		if got := e2eFileSHA256(exe); got != v1.sha {
			t.Fatalf("the installed binary changed: sha256 %s, want v1 %s", got, v1.sha)
		}
		if _, err := os.Stat(filepath.Join(root, "updates")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("something was written into the install folder (updates/: %v)", err)
		}
	}

	start("v1")
	requireRealIdentity(t, adminBase, v1)

	if !t.Run("updates", func(t *testing.T) {
		out := install(v2, "v2")
		requireRealIdentity(t, adminBase, v2)
		if !ledgerHas(t, homeRoot, v2.updateID, "health-passed") {
			t.Fatalf("the home ledger shows no health-passed for %s: %v\n%s", v2.updateID, ledgerActions(t, homeRoot), readFileString(out))
		}
		if state, err := update.LoadState(homePaths); err != nil || state.UpdateID != v2.updateID {
			t.Fatalf("home state after v2 = %+v (%v)", state, err)
		}
		installedUntouched()
		// A supervisor's restart: the installed path again.
		stop()
		start("v2-restarted")
		requireRealIdentity(t, adminBase, v2)
		if text := typedVersion("after v2"); !strings.Contains(text, "e2e-v2") {
			t.Fatalf("a command typed against the installed path did not run v2:\n%s", text)
		}
	}) {
		return
	}

	if !t.Run("rollback", func(t *testing.T) {
		out := install(v3, "v3")
		if text := readFileString(out); !strings.Contains(text, "rolled back to "+v2.version) {
			t.Fatalf("the helper did not report a rollback to %s:\n%s", v2.version, text)
		}
		requireRealIdentity(t, adminBase, v2)
		if !update.HasFailedUpdate(homePaths, v3.updateID) {
			t.Fatalf("v3 was reverted but not quarantined in the home bundle")
		}
		if !ledgerHas(t, homeRoot, v3.updateID, "health-failed") {
			t.Fatalf("the home ledger shows no health-failed for %s: %v", v3.updateID, ledgerActions(t, homeRoot))
		}
		installedUntouched()
		stop()
		start("v2-after-rollback")
		requireRealIdentity(t, adminBase, v2)
	}) {
		return
	}

	t.Run("tampered", func(t *testing.T) {
		stop()
		binary := filepath.Join(homeRoot, "bin", "spacedatanetwork")
		f, err := os.OpenFile(binary, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte{0}); err != nil {
			t.Fatal(err)
		}
		f.Close()
		logPath := start("tampered")
		requireRealIdentity(t, adminBase, v1)
		text := readFileString(logPath)
		if !strings.Contains(text, "is refused") || !strings.Contains(text, "bin/spacedatanetwork differs") || !strings.Contains(text, "running the installed build") {
			t.Fatalf("the daemon did not say why it refused the home build:\n%s", tailString(text, 40))
		}
		if text := typedVersion("tampered"); !strings.Contains(text, "e2e-v1") || !strings.Contains(text, "is refused") {
			t.Fatalf("a command typed against the installed path did not refuse the tampered build:\n%s", text)
		}
		installedUntouched()
	})

	for _, log := range daemonLogs {
		requireIsolated(t, log)
	}
}

// setTreeWritable makes every folder of root writable by its owner, or no
// longer writable by anyone.
func setTreeWritable(t *testing.T, root string, writable bool) {
	t.Helper()
	mode := os.FileMode(0o555)
	if writable {
		mode = 0o755
	}
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for _, dir := range dirs {
		if err := os.Chmod(dir, mode); err != nil {
			t.Errorf("chmod %s: %v", dir, err)
		}
	}
}
