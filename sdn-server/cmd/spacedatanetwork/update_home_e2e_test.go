package main

// UPDATES OF A NODE THAT CANNOT WRITE ITS INSTALL FOLDER, UNDER A SUPERVISOR,
// WITH NO HELPER, ON REAL DAEMONS (stack issue 846).
//
// Each subtest installs the node in the release layout (the shell launcher
// bin/spacedatanetwork, the daemon in runtime/sdn/, runtime/wasmedge/,
// runtime/modules/), in a folder its account cannot write (every folder mode
// 0555, the test runs as an ordinary user), reached through a symlink, and
// runs it under a supervisor that does what a system unit with
// Restart=always does and nothing more: it starts the INSTALLED command and
// starts it again when it exits. The home bundle is where the node's account
// keeps it: under the home folder of its passwd entry, not $HOME (the daemons'
// HOME is another folder). Updates arrive as signed signals on the update
// topic or through `update install`.
//
//	signals
//	  first-bad        v3 (never ready) is the first update: its trial fails
//	                   and the node returns to the installed build (the empty
//	                   slot); the installed launcher runs with its own
//	                   runtime environment, not the trial's.
//	  good             v2 passes its trial; the home build runs with its own
//	                   runtime environment, not the install's (reached through
//	                   the symlink); a typed command runs v2.
//	  service-path     `service install --dry-run` from the node names the
//	                   installed launcher, not the home build.
//	  group-writable   a home bundle writable by its group is refused.
//	  bundle-folder-writable  a home build whose bin/ others can write is
//	                   refused.
//	  shared-symlink   ~/.spacedatanetwork as a symlink into a folder others
//	                   can write (no sticky bit) is refused.
//	  tampered-pending a pending v4 with one byte appended is refused by a
//	                   typed command and quarantined at the next start.
//	  typed-install    `update install` of v5 with the daemon running, umask
//	                   0, and an archive whose folders and files others may
//	                   write: installed writable by its owner only, and runs.
//	  second-daemon, unreadable-trial, unreadable-home-record
//	  crash-at-start   v11 exits with an error at start: rolled back after
//	                   the supervisor's restart.
//	  restart-during-trial  a SIGTERM while v12 is on trial and not yet
//	                   ready: the trial runs again, and v12 passes.
//	  restart-bound    v13 (never ready) stopped by SIGTERM at each trial
//	                   start: after update.MaxTrialInterruptions re-runs the
//	                   next stop fails it, and it is rolled back.
//	  interrupted-revert  the trial record a revert stopped after its
//	                   rollback leaves: the next start clears it and keeps the
//	                   build that ran before the trial.
//	hang-timer         v6 blocks for good before it opens its store: its
//	                   deadline timer rolls it back to the installed build.
//	hang-no-timer      v7 blocks the same way and has no timer: after its
//	                   deadline a typed command leaves it alone while its
//	                   daemon is alive; once the hung process is killed, the
//	                   supervisor's restart rolls it back.
//	unreachable-home   the account cannot enter its home folder (as under
//	                   ProtectHome=): the node updates into
//	                   <storage.path>/update-bundle.
//
// Throughout: every restart for an update keeps the daemon's process (the
// supervisor started it once, plus the restarts a subtest causes), no helper
// is prepared, and nothing is written in the install folder. Gated and run
// like TestUpdateChannelRealDaemonE2E, of which these are subtests.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	ps "github.com/libp2p/go-libp2p-pubsub"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// releaseLauncher is the shell launcher a release bundle ships (the shape
// deployment/release/build-self-contained-cli.mjs writes when it stages a
// WasmEdge runtime folder).
const releaseLauncher = `#!/bin/sh
set -eu

SCRIPT_PATH="$0"
while [ -L "$SCRIPT_PATH" ]; do
  SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$SCRIPT_PATH")" && pwd)"
  LINK_TARGET="$(readlink "$SCRIPT_PATH")"
  case "$LINK_TARGET" in
    /*) SCRIPT_PATH="$LINK_TARGET" ;;
    *) SCRIPT_PATH="$SCRIPT_DIR/$LINK_TARGET" ;;
  esac
done

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$SCRIPT_PATH")" && pwd)"
BUNDLE_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"
export WASMEDGE_DIR="${WASMEDGE_DIR:-$BUNDLE_ROOT/runtime/wasmedge}"

if [ -n "${WASMEDGE_DIR:-}" ] && [ -d "$WASMEDGE_DIR/lib" ]; then
  if [ -n "${LD_LIBRARY_PATH:-}" ]; then
    export LD_LIBRARY_PATH="$WASMEDGE_DIR/lib:$LD_LIBRARY_PATH"
  else
    export LD_LIBRARY_PATH="$WASMEDGE_DIR/lib"
  fi
fi

exec "$BUNDLE_ROOT/runtime/sdn/spacedatanetwork" "$@"
`

// restartLoop stands in for a system unit with Restart=always: it starts the
// installed command and starts it again whenever it exits.
type restartLoop struct {
	d        *pushDaemon
	mu       sync.Mutex
	starts   int
	pid      int
	stopping bool
	log      string
	done     chan struct{}
}

func startRestartLoop(t *testing.T, d *pushDaemon) *restartLoop {
	t.Helper()
	l := &restartLoop{d: d, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for {
			l.mu.Lock()
			if l.stopping {
				l.mu.Unlock()
				return
			}
			l.starts++
			name := filepath.Join(d.work, fmt.Sprintf("supervised-%d.log", len(d.logPaths)))
			d.logPaths = append(d.logPaths, name)
			l.log = name
			l.mu.Unlock()
			out, err := os.Create(name)
			if err != nil {
				return
			}
			cmd := exec.Command(d.exe, "daemon", "--allow-multi-daemon", "--config", d.cfg)
			cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.work, d.env, out, out
			if err := cmd.Start(); err != nil {
				out.Close()
				return
			}
			l.mu.Lock()
			l.pid = cmd.Process.Pid
			l.mu.Unlock()
			d.tracker.add(cmd.Process.Pid)
			_ = cmd.Wait()
			out.Close()
			time.Sleep(time.Second)
		}
	}()
	for {
		if starts, pid := l.state(); starts > 0 && pid > 0 {
			return l
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *restartLoop) state() (starts, pid int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts, l.pid
}

// lastLog is the log of the daemon the loop started last.
func (l *restartLoop) lastLog() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.log
}

func (l *restartLoop) stop(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	l.stopping = true
	pid := l.pid
	l.mu.Unlock()
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	select {
	case <-l.done:
	case <-time.After(3 * time.Minute):
		t.Fatalf("the supervised daemon (pid %d) did not stop within 3m", pid)
	}
}

// homeNode is one read-only release install under a restart loop.
type homeNode struct {
	d        *pushDaemon
	loop     *restartLoop
	stopped  bool
	link     string // the install as the node is started: through a symlink
	real     string // the install folder itself
	homeRoot string
	feed     *realE2EFeed
	key      ed25519.PrivateKey
	health   int
	boot     time.Duration
	pid      int
	// wantStarts is how many times the supervisor has started the daemon
	// since its loop started: 1, plus each restart a subtest caused.
	wantStarts int
	// homeTemplate is the release layout every update archive of this node
	// ships.
	homeTemplate string
	announceTo   func(t *testing.T, signed []byte)
}

func newHomeNode(t *testing.T, label string, installed realBuild) *homeNode {
	t.Helper()
	return newHomeNodeWith(t, label, installed, false)
}

// newHomeNodeWith is newHomeNode; with unreachableHome the account's home
// folder (its passwd entry's) is made one it cannot enter, as ProtectHome= in a
// system unit does, so the node updates into <storage.path>/update-bundle.
func newHomeNodeWith(t *testing.T, label string, installed realBuild, unreachableHome bool) *homeNode {
	t.Helper()
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keepRealLogs(t, work)
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	roots := update.TrustedRoots{update.RootKeyID(pub): base64.StdEncoding.EncodeToString(spki)}
	feed := newRealE2EFeed(t, work)
	d := newPushDaemon(t, filepath.Join(work, label), installed, feed, roots, health)
	d.tracker.root = work
	// The node starts with no runtime environment of its own, as a service
	// unit starts it, so the launchers decide WASMEDGE_DIR and the library
	// path (the checks below read them). A WASMEDGE_DIR the operator sets
	// outside both bundles is kept as it is.
	var env []string
	for _, kv := range d.env {
		name, _, _ := strings.Cut(kv, "=")
		if name != "WASMEDGE_DIR" && name != "LD_LIBRARY_PATH" && name != "DYLD_LIBRARY_PATH" {
			env = append(env, kv)
		}
	}
	d.env = env
	n := &homeNode{d: d, feed: feed, key: key, health: health, boot: boot}

	// The release layout, then the install moved behind a symlink.
	n.link = d.paths.Root
	n.real = filepath.Join(filepath.Dir(n.link), "install-real")
	if err := os.Rename(n.link, n.real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("install-real", n.link); err != nil {
		t.Fatal(err)
	}
	writeReleaseLayout(t, n.real, installed, feed.base)
	// The archives every update of this node ships: the same layout, no trust/.
	tmpl := filepath.Join(work, "release-template")
	if out, err := exec.Command("cp", "-a", n.real, tmpl).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.RemoveAll(filepath.Join(tmpl, "trust")); err != nil {
		t.Fatal(err)
	}
	n.homeTemplate = tmpl

	setTreeWritable(t, n.real, false)
	t.Cleanup(func() { setTreeWritable(t, n.real, true) })
	if f, err := os.Create(filepath.Join(n.real, "probe")); err == nil {
		f.Close()
		t.Fatal("the install folder is writable by this account; the test needs one it cannot write")
	}
	if unreachableHome {
		account, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(account.HomeDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(account.HomeDir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(account.HomeDir, info.Mode().Perm()) })
		update.HomeStorageFallback = func() string { return d.store }
		t.Cleanup(func() { update.HomeStorageFallback = nil })
	}
	n.homeRoot, err = update.HomeUpdateRoot(n.real)
	if err != nil {
		t.Fatalf("this account has no home bundle: %v", err)
	}
	if unreachableHome {
		if !strings.HasPrefix(n.homeRoot, filepath.Join(d.store, "update-bundle")+"/") {
			t.Fatalf("with its home folder unreachable the node updates into %s, want <storage.path>/update-bundle under %s", n.homeRoot, d.store)
		}
	} else if strings.HasPrefix(n.homeRoot, work) {
		t.Fatalf("the home bundle %s follows the daemons' $HOME, not the account's passwd entry", n.homeRoot)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(n.homeRoot, func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		_ = os.RemoveAll(n.homeRoot)
		for _, kind := range []string{"install", "inuse", "daemon"} {
			_ = os.Remove(n.homeRoot + "." + kind + ".lock")
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	host, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	gossip, err := ps.NewGossipSub(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := gossip.Join(update.SignalTopic(realE2EChannel))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { topic.Close() })
	n.announceTo = func(t *testing.T, signed []byte) {
		t.Helper()
		// The daemon restarted for the last update has a new listen address.
		if err := host.Connect(ctx, pushPeer(t, d.adminBase)); err != nil {
			t.Fatal(err)
		}
		waitPush(t, 30*time.Second, "the daemon on the update topic", func() bool { return len(topic.ListPeers()) >= 1 })
		time.Sleep(3 * time.Second)
		if err := topic.Publish(ctx, signed); err != nil {
			t.Fatal(err)
		}
	}

	n.loop = startRestartLoop(t, d)
	n.wantStarts = 1
	t.Cleanup(func() {
		if !n.stopped {
			n.loop.stop(t)
		}
	})
	waitRealReady(t, d.adminBase, boot, n.loop.lastLog())
	requireRealIdentity(t, d.adminBase, installed)
	_, n.pid = n.loop.state()
	return n
}

// writeReleaseLayout turns the bare install at root into the release layout.
func writeReleaseLayout(t *testing.T, root string, b realBuild, feedBase string) {
	t.Helper()
	for _, dir := range []string{"runtime/sdn", "runtime/wasmedge/lib"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(root, "bin", "spacedatanetwork"), filepath.Join(root, "runtime", "sdn", "spacedatanetwork")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "wasmedge", "lib", "README"), []byte("runtime libraries\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "bin", "spacedatanetwork")
	if err := os.WriteFile(launcher, []byte(releaseLauncher), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := realBundleManifest(b, feedBase)
	manifest["artifacts"] = []any{
		map[string]any{"path": "bin/spacedatanetwork", "sha256": e2eFileSHA256(launcher)},
		map[string]any{"path": "runtime/sdn/spacedatanetwork", "sha256": b.sha},
	}
	writeE2EJSON(t, filepath.Join(root, "manifest.json"), manifest)
}

func (n *homeNode) publish(t *testing.T, b realBuild) {
	t.Helper()
	b.template = n.homeTemplate
	n.feed.publish(t, b, n.key)
}

func (n *homeNode) announce(t *testing.T, b realBuild) {
	t.Helper()
	n.publish(t, b)
	n.feed.mu.Lock()
	var entry update.ProviderFeedUpdate
	for _, e := range n.feed.entries {
		if e.UpdateID == b.updateID {
			entry = e
		}
	}
	n.feed.mu.Unlock()
	raw, err := update.SignalFromFeedEntry(entry, n.feed.base, time.Now(), false).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	n.announceTo(t, resignPushSignal(t, raw, n.key, 0))
	t.Logf("announced %s", b.updateID)
}

func (n *homeNode) paths() update.Paths { return update.PathsFor(n.homeRoot) }

func (n *homeNode) waitFor(t *testing.T, what string, budget time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s\nledger: %v\n%s", budget, what, ledgerActionsIfPresent(n.homeRoot), tailString(n.d.logs(), 60))
		}
		time.Sleep(time.Second)
	}
}

func (n *homeNode) typed(t *testing.T, label string, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(n.d.work, label+".log")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd := exec.Command(n.d.exe, append(args, "--config", n.d.cfg)...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = n.d.work, n.d.env, out, out
	err = cmd.Run()
	return readFileString(path), err
}

// typedUmask0 is typed, run with umask 0.
func (n *homeNode) typedUmask0(t *testing.T, label string, args ...string) (string, error) {
	t.Helper()
	path := filepath.Join(n.d.work, label+".log")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd := exec.Command("/bin/sh", append([]string{"-c", `umask 000; exec "$0" "$@"`, n.d.exe}, append(args, "--config", n.d.cfg)...)...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = n.d.work, n.d.env, out, out
	err = cmd.Run()
	return readFileString(path), err
}

// countLogs counts s in every log of the node's daemons.
func (n *homeNode) countLogs(s string) int { return strings.Count(n.d.logs(), s) }

// signalDaemon sends sig to the supervised daemon, as `systemctl restart` or
// a reboot does, and counts the supervisor's restart it causes.
func (n *homeNode) signalDaemon(t *testing.T, sig syscall.Signal) {
	t.Helper()
	_, pid := n.loop.state()
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatal(err)
	}
	n.wantStarts++
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if starts, newPID := n.loop.state(); starts == n.wantStarts && newPID != pid {
			n.pid = newPID
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the supervisor did not start the daemon again after %v", sig)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitTrialRunning waits until the count-th process to run update id as a
// trial has started (its line in the logs).
func (n *homeNode) waitTrialRunning(t *testing.T, id string, count int) {
	t.Helper()
	line := "update: this process runs update " + id + " as a trial"
	deadline := time.Now().Add(n.boot)
	for n.countLogs(line) < count {
		if time.Now().After(deadline) {
			t.Fatalf("trial start %d of %s did not happen\n%s", count, id, tailString(n.d.logs(), 40))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (n *homeNode) sameProcess(t *testing.T) {
	t.Helper()
	if starts, pid := n.loop.state(); starts != n.wantStarts || pid != n.pid || !processAlive(n.pid) {
		t.Fatalf("the supervisor started the daemon %d times (pid %d, first %d): an update restart must be the same process", starts, pid, n.pid)
	}
}

func (n *homeNode) noHelperNoWrite(t *testing.T, installed realBuild) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(n.homeRoot, "updates", "helper")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an update helper was prepared (updates/helper: %v)", err)
	}
	if ledgerHasAction(n.homeRoot, "stop-requested") {
		t.Fatalf("the ledger shows a helper's stop request: %v", ledgerActionsIfPresent(n.homeRoot))
	}
	if got := e2eFileSHA256(filepath.Join(n.real, "runtime", "sdn", "spacedatanetwork")); got != installed.sha {
		t.Fatalf("the installed daemon changed: sha256 %s, want %s", got, installed.sha)
	}
	if _, err := os.Stat(filepath.Join(n.real, "updates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was written into the install folder (updates/: %v)", err)
	}
}

// processEnv is the environment of a running process (Linux /proc).
func processEnv(t *testing.T, pid int) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		t.Fatalf("read the environment of pid %d: %v", pid, err)
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(raw), "\x00") {
		if name, value, ok := strings.Cut(kv, "="); ok {
			env[name] = value
		}
	}
	return env
}

func versionOf(b realBuild, name, version, id string, seq int64) realBuild {
	b.name, b.version, b.updateID, b.sequence = name, version, id, seq
	return b
}

func runRealHomeUpdate(t *testing.T) {
	builds := realE2EBuilds(t, filepath.Join(t.TempDir(), "builds"))
	v1, v2, v3 := builds[0], builds[1], builds[2]
	hang, hangNoTimer, crash, crashMidApply, crashMidPass := realE2EHangBuilds(t, filepath.Join(t.TempDir(), "hang-builds"))

	t.Run("signals", func(t *testing.T) {
		n := newHomeNode(t, "node", v1)

		// The environment and path checks below report with Errorf, so the
		// steps after them still run when only they fail.
		t.Run("first-bad", func(t *testing.T) {
			n.announce(t, v3)
			n.waitFor(t, "v3 to be rolled back to the installed build", n.boot+3*time.Duration(n.health)*time.Second, func() bool {
				return update.HasFailedUpdate(n.paths(), v3.updateID) && ledgerHasIfPresent(n.homeRoot, v3.updateID, "health-reverted") && pushIdentityMatches(n.d.adminBase, v1)
			})
			requireRealIdentity(t, n.d.adminBase, v1)
			if _, err := os.Stat(filepath.Join(n.homeRoot, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the home bundle still holds a build after the rollback to the installed one (%v)", err)
			}
			n.sameProcess(t)
			n.noHelperNoWrite(t, v1)
			// The installed build runs on the install's runtime folder, not
			// the trial's.
			env := processEnv(t, n.pid)
			if !strings.HasPrefix(env["WASMEDGE_DIR"], n.link+"/") && !strings.HasPrefix(env["WASMEDGE_DIR"], n.real+"/") {
				t.Errorf("after the rollback the installed build runs with WASMEDGE_DIR=%q, want the install's runtime/wasmedge", env["WASMEDGE_DIR"])
			}
		})

		t.Run("good", func(t *testing.T) {
			n.announce(t, v2)
			n.waitFor(t, "v2 to pass its trial", n.boot+time.Duration(n.health)*time.Second, func() bool {
				return pushIdentityMatches(n.d.adminBase, v2) && ledgerHasIfPresent(n.homeRoot, v2.updateID, "health-passed")
			})
			if trial, _ := update.ReadTrial(n.paths()); trial != nil {
				t.Fatalf("v2 passed but its trial is still pending: %+v", trial)
			}
			n.sameProcess(t)
			n.noHelperNoWrite(t, v1)
			// The home build runs on its own runtime folder, though the
			// install was reached through a symlink.
			if env := processEnv(t, n.pid); env["WASMEDGE_DIR"] != filepath.Join(n.homeRoot, "runtime", "wasmedge") {
				t.Errorf("the home build runs with WASMEDGE_DIR=%q, want %s", env["WASMEDGE_DIR"], filepath.Join(n.homeRoot, "runtime", "wasmedge"))
			}
			if text, err := n.typed(t, "version-v2", "version"); err != nil || !strings.Contains(text, "e2e-v2") {
				t.Fatalf("a command typed against the installed path did not run v2 (%v):\n%s", err, text)
			}
		})

		t.Run("service-path", func(t *testing.T) {
			text, err := n.typed(t, "service-dry-run", "service", "install", "--dry-run")
			if err != nil {
				t.Fatalf("service install --dry-run: %v\n%s", err, text)
			}
			want := "service_executable=" + filepath.Join(n.real, "bin", "spacedatanetwork") + "\n"
			if !strings.Contains(text, want) || strings.Contains(text, n.homeRoot) {
				t.Errorf("service install from a node running a home build does not name the installed launcher (want %q):\n%s", want, text)
			}
		})

		t.Run("group-writable", func(t *testing.T) {
			if err := os.Chmod(n.homeRoot, 0o770); err != nil {
				t.Fatal(err)
			}
			text, err := n.typed(t, "version-group-writable", "version")
			if cerr := os.Chmod(n.homeRoot, 0o700); cerr != nil {
				t.Fatal(cerr)
			}
			if err != nil || !strings.Contains(text, "writable by its group or by others") || !strings.Contains(text, "running the installed build") || !strings.Contains(text, "e2e-v1") {
				t.Errorf("a home bundle writable by its group was not refused (%v):\n%s", err, text)
			}
		})

		t.Run("bundle-folder-writable", func(t *testing.T) {
			bin := filepath.Join(n.homeRoot, "bin")
			info, err := os.Stat(bin)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(bin, 0o777); err != nil {
				t.Fatal(err)
			}
			text, err := n.typed(t, "version-bin-writable", "version")
			if cerr := os.Chmod(bin, info.Mode().Perm()); cerr != nil {
				t.Fatal(cerr)
			}
			if err != nil || !strings.Contains(text, bin+" is writable by its group or by others") || !strings.Contains(text, "running the installed build") || !strings.Contains(text, "e2e-v1") {
				t.Errorf("a home build whose bin/ folder others can write was not refused (%v):\n%s", err, text)
			}
		})

		t.Run("shared-symlink", func(t *testing.T) {
			// ~/.spacedatanetwork as a symlink into a folder every account can
			// write (no sticky bit): another account could swap the build.
			dot := filepath.Dir(filepath.Dir(n.homeRoot))
			shared := filepath.Join(n.d.work, "shared")
			if err := os.Mkdir(shared, 0o777); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(shared, 0o777); err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(shared, "sdn")
			if err := os.Rename(dot, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(moved, dot); err != nil {
				t.Fatal(err)
			}
			text, err := n.typed(t, "version-shared-symlink", "version")
			if rerr := os.Remove(dot); rerr != nil {
				t.Fatal(rerr)
			}
			if rerr := os.Rename(moved, dot); rerr != nil {
				t.Fatal(rerr)
			}
			if err != nil || !strings.Contains(text, shared+" is writable by its group or by others") || !strings.Contains(text, "without the sticky bit") || !strings.Contains(text, "e2e-v1") {
				t.Errorf("a home bundle reached through a symlink into a folder others can write was not refused (%v):\n%s", err, text)
			}
		})

		v4 := versionOf(v2, "v4", "1.1.2-e2e.4", "cli-bundle-release-e2e-4", 400)
		if !t.Run("tampered-pending", func(t *testing.T) {
			n.loop.stop(t)
			n.stopped = true
			n.publish(t, v4)
			text, err := n.typed(t, "install-v4", "update", "install")
			if err != nil || !strings.Contains(text, "installed_update="+v4.updateID) || !strings.Contains(text, "next=start the node") {
				t.Fatalf("update install of v4 with no daemon (%v):\n%s", err, text)
			}
			if trial, _ := update.ReadTrial(n.paths()); trial == nil || trial.UpdateID != v4.updateID {
				t.Fatalf("v4 is not a pending trial: %+v", trial)
			}
			f, err := os.OpenFile(filepath.Join(n.homeRoot, "runtime", "sdn", "spacedatanetwork"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte{0}); err != nil {
				t.Fatal(err)
			}
			f.Close()
			if text, err := n.typed(t, "version-tampered", "version"); err != nil || !strings.Contains(text, "is refused") || !strings.Contains(text, "e2e-v1") {
				t.Fatalf("a command typed against the installed path did not refuse the changed v4 (%v):\n%s", err, text)
			}
			n.loop = startRestartLoop(t, n.d)
			n.wantStarts = 1
			n.stopped = false
			waitRealReady(t, n.d.adminBase, n.boot, n.loop.lastLog())
			requireRealIdentity(t, n.d.adminBase, v2)
			if !update.HasFailedUpdate(n.paths(), v4.updateID) {
				t.Fatalf("the tampered v4 was not quarantined")
			}
			log := readFileString(n.loop.lastLog())
			if !strings.Contains(log, "runtime/sdn/spacedatanetwork differs") || !strings.Contains(log, "quarantined") {
				t.Fatalf("the start did not say why it refused v4:\n%s", tailString(log, 40))
			}
			_, n.pid = n.loop.state()
			n.noHelperNoWrite(t, v1)
		}) {
			return
		}

		v5 := versionOf(v2, "v5", "1.1.2-e2e.5", "cli-bundle-release-e2e-5", 500)
		t.Run("typed-install", func(t *testing.T) {
			// v5's archive has folders and files every account may write, and
			// the install runs with umask 0: the build on disk must still be
			// writable only by its owner.
			loose := filepath.Join(n.d.work, "release-template-loose")
			if out, err := exec.Command("cp", "-a", n.homeTemplate, loose).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			for name, mode := range map[string]os.FileMode{"bin": 0o777, "runtime/wasmedge": 0o777, "runtime/wasmedge/lib": 0o777, "runtime/wasmedge/lib/README": 0o666} {
				if err := os.Chmod(filepath.Join(loose, name), mode); err != nil {
					t.Fatal(err)
				}
			}
			v5.template = loose
			n.feed.publish(t, v5, n.key)
			text, err := n.typedUmask0(t, "install-v5", "update", "install")
			if err != nil || !strings.Contains(text, "daemon_restart=requested") || !strings.Contains(text, "daemon_health=healthy") ||
				!strings.Contains(text, "daemon_identity=verified bundle_version="+v5.version) || strings.Contains(text, "helper_") {
				t.Fatalf("update install of v5 with the daemon running (%v):\n%s", err, text)
			}
			requireRealIdentity(t, n.d.adminBase, v5)
			n.sameProcess(t)
			n.noHelperNoWrite(t, v1)
			_ = filepath.WalkDir(n.homeRoot, func(p string, e fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p == filepath.Join(n.homeRoot, "updates") {
					return filepath.SkipDir
				}
				if info, lerr := os.Lstat(p); lerr == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
					t.Errorf("%s is writable by its group or by others (mode %04o) after the install", p, info.Mode().Perm())
				}
				return nil
			})
		})

		t.Run("second-daemon", func(t *testing.T) {
			text, err := n.typed(t, "second-daemon", "daemon", "--allow-multi-daemon")
			if err == nil || !strings.Contains(text, "another daemon already runs from the install") {
				t.Errorf("a second daemon from the same install was not refused (%v):\n%s", err, text)
			}
			requireRealIdentity(t, n.d.adminBase, v5)
		})

		v9 := versionOf(v2, "v9", "1.1.2-e2e.9", "cli-bundle-release-e2e-9", 900)
		t.Run("unreadable-trial", func(t *testing.T) {
			n.loop.stop(t)
			n.stopped = true
			n.publish(t, v9)
			if text, err := n.typed(t, "install-v9", "update", "install"); err != nil || !strings.Contains(text, "installed_update="+v9.updateID) {
				t.Fatalf("update install of v9 with no daemon (%v):\n%s", err, text)
			}
			if err := os.WriteFile(filepath.Join(n.homeRoot, "updates", "trial.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			n.loop = startRestartLoop(t, n.d)
			n.wantStarts = 1
			n.stopped = false
			waitRealReady(t, n.d.adminBase, n.boot, n.loop.lastLog())
			requireRealIdentity(t, n.d.adminBase, v5)
			if !update.HasFailedUpdate(n.paths(), v9.updateID) {
				t.Fatalf("v9, whose trial record could not be read, was not rolled back:\n%s", tailString(readFileString(n.loop.lastLog()), 30))
			}
		})

		v10 := versionOf(v2, "v10", "1.1.2-e2e.10", "cli-bundle-release-e2e-10", 1000)
		t.Run("unreadable-home-record", func(t *testing.T) {
			n.loop.stop(t)
			n.stopped = true
			if err := os.WriteFile(filepath.Join(n.homeRoot, "updates", "home.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			n.publish(t, v10)
			if text, err := n.typed(t, "install-v10", "update", "install"); err != nil || !strings.Contains(text, "installed_update="+v10.updateID) {
				t.Fatalf("update install of v10 over an unreadable home record (%v):\n%s", err, text)
			}
			n.loop = startRestartLoop(t, n.d)
			n.wantStarts = 1
			n.stopped = false
			n.waitFor(t, "v10 to pass its trial", n.boot+time.Duration(n.health)*time.Second, func() bool {
				return pushIdentityMatches(n.d.adminBase, v10) && ledgerHasIfPresent(n.homeRoot, v10.updateID, "health-passed")
			})
			_, n.pid = n.loop.state()
		})

		t.Run("crash-at-start", func(t *testing.T) {
			// v11 exits with an error at start, before it is ready: the
			// supervisor starts the installed path again, which rolls it back.
			n.announce(t, crash)
			n.waitFor(t, "the crashing v11 to be rolled back to v10", n.boot+time.Duration(n.health)*time.Second, func() bool {
				return update.HasFailedUpdate(n.paths(), crash.updateID) && ledgerHasIfPresent(n.homeRoot, crash.updateID, "health-reverted") && pushIdentityMatches(n.d.adminBase, v10)
			})
			requireRealIdentity(t, n.d.adminBase, v10)
			if !strings.Contains(n.d.logs(), "this build exits at start") {
				t.Fatalf("v11 did not run as the crashing build:\n%s", tailString(n.d.logs(), 40))
			}
			if !ledgerReasonHas(n.homeRoot, crash.updateID, "health-failed", "ended before it answered ready") {
				t.Fatalf("no health-failed for v11 saying its start ended before it was ready: %v", ledgerActionsIfPresent(n.homeRoot))
			}
			n.wantStarts = 2
			_, n.pid = n.loop.state()
			n.sameProcess(t)
		})

		v12 := versionOf(v2, "v12", "1.1.2-e2e.12", "cli-bundle-release-e2e-12", 1200)
		t.Run("restart-during-trial", func(t *testing.T) {
			// A service restart while v12 is on trial and not yet ready: the
			// trial runs again with a new deadline, and v12 passes it.
			n.announce(t, v12)
			n.waitTrialRunning(t, v12.updateID, 1)
			n.signalDaemon(t, syscall.SIGTERM)
			n.waitFor(t, "v12 to pass its trial after the restart", n.boot+time.Duration(n.health)*time.Second, func() bool {
				return pushIdentityMatches(n.d.adminBase, v12) && ledgerHasIfPresent(n.homeRoot, v12.updateID, "health-passed")
			})
			if !strings.Contains(n.d.logs(), "during the trial of "+v12.updateID+": the next start runs its trial again") {
				t.Fatalf("the stop during v12's trial was not recorded as one (it may have passed before the signal):\n%s", tailString(n.d.logs(), 40))
			}
			if got := n.countLogs("update: this process runs update " + v12.updateID + " as a trial"); got != 2 {
				t.Fatalf("v12 ran its trial %d times, want 2 (once, then again after the restart)", got)
			}
			if update.HasFailedUpdate(n.paths(), v12.updateID) || ledgerHasIfPresent(n.homeRoot, v12.updateID, "health-failed") {
				t.Fatalf("v12 was failed for a restart during its trial: %v", ledgerActionsIfPresent(n.homeRoot))
			}
			n.sameProcess(t)
		})

		v13 := versionOf(v3, "v13", "1.1.2-e2e.13", "cli-bundle-release-e2e-13", 1300)
		t.Run("restart-bound", func(t *testing.T) {
			// v13 never answers ready and is stopped by a signal at every
			// trial start: after update.MaxTrialInterruptions re-runs the next
			// stop fails it, and it is rolled back to v12.
			n.announce(t, v13)
			for i := 1; i <= update.MaxTrialInterruptions+1; i++ {
				n.waitTrialRunning(t, v13.updateID, i)
				n.signalDaemon(t, syscall.SIGTERM)
			}
			n.waitFor(t, "v13 to be rolled back to v12", n.boot+time.Duration(n.health)*time.Second, func() bool {
				return update.HasFailedUpdate(n.paths(), v13.updateID) && pushIdentityMatches(n.d.adminBase, v12)
			})
			requireRealIdentity(t, n.d.adminBase, v12)
			if want := fmt.Sprintf("stopped %d times during its trial", update.MaxTrialInterruptions+1); !ledgerReasonHas(n.homeRoot, v13.updateID, "health-failed", want) {
				t.Fatalf("no health-failed for v13 saying %q: %v", want, ledgerActionsIfPresent(n.homeRoot))
			}
			if got := n.countLogs("update: this process runs update " + v13.updateID + " as a trial"); got != update.MaxTrialInterruptions+1 {
				t.Fatalf("v13 ran its trial %d times, want %d", got, update.MaxTrialInterruptions+1)
			}
			n.sameProcess(t)
		})

		v14 := versionOf(v2, "v14", "1.1.2-e2e.14", "cli-bundle-release-e2e-14", 1400)
		t.Run("interrupted-revert", func(t *testing.T) {
			// The record a revert leaves when it stops after its rollback and
			// before it clears the trial (or when a late fail rewrites the
			// trial after a revert): the trial names v14, which is already in
			// quarantine, and v12 is the build. The next start must keep v12.
			n.loop.stop(t)
			n.stopped = true
			n.publish(t, v14)
			if text, err := n.typed(t, "install-v14", "update", "install"); err != nil || !strings.Contains(text, "installed_update="+v14.updateID) {
				t.Fatalf("update install of v14 with no daemon (%v):\n%s", err, text)
			}
			trial, err := update.ReadTrial(n.paths())
			if err != nil || trial == nil || trial.UpdateID != v14.updateID {
				t.Fatalf("v14 is not a pending trial: %+v (%v)", trial, err)
			}
			trial.Starts, trial.FailReason = 1, "it did not answer ready by its deadline (e2e)"
			raw, err := json.Marshal(trial)
			if err != nil {
				t.Fatal(err)
			}
			trialPath := filepath.Join(n.homeRoot, "updates", "trial.json")
			if err := os.WriteFile(trialPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if text, err := n.typed(t, "version-revert-v14", "version"); err != nil || !strings.Contains(text, "failed its trial") || !strings.Contains(text, "e2e-v2") {
				t.Fatalf("a typed command with no daemon did not roll the failed v14 back to v12 (%v):\n%s", err, text)
			}
			if !update.HasFailedUpdate(n.paths(), v14.updateID) {
				t.Fatalf("v14 was not quarantined")
			}
			if err := os.WriteFile(trialPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			n.loop = startRestartLoop(t, n.d)
			n.wantStarts = 1
			n.stopped = false
			waitRealReady(t, n.d.adminBase, n.boot, n.loop.lastLog())
			requireRealIdentity(t, n.d.adminBase, v12)
			if update.HasFailedUpdate(n.paths(), v12.updateID) {
				t.Fatalf("the start after an interrupted revert quarantined v12, the build that was running before the trial: %v", ledgerActionsIfPresent(n.homeRoot))
			}
			if trial, _ := update.ReadTrial(n.paths()); trial != nil {
				t.Fatalf("the trial record of v14 is still there: %+v", trial)
			}
			if !strings.Contains(readFileString(n.loop.lastLog()), "was already rolled back") {
				t.Fatalf("the start did not say it cleared v14's trial record:\n%s", tailString(readFileString(n.loop.lastLog()), 30))
			}
		})
		for _, log := range n.d.logPaths {
			requireIsolated(t, log)
		}
	})

	t.Run("hang-timer", func(t *testing.T) {
		n := newHomeNode(t, "node", v1)
		n.announce(t, hang)
		n.waitFor(t, "the hung v6 to be rolled back", n.boot+3*time.Duration(n.health)*time.Second, func() bool {
			return update.HasFailedUpdate(n.paths(), hang.updateID) && pushIdentityMatches(n.d.adminBase, v1)
		})
		requireRealIdentity(t, n.d.adminBase, v1)
		if !ledgerReasonHas(n.homeRoot, hang.updateID, "health-failed", "deadline") {
			t.Fatalf("no health-failed naming the deadline for v6: %v", ledgerActionsIfPresent(n.homeRoot))
		}
		if !strings.Contains(n.d.logs(), "never opens its store") {
			t.Fatalf("v6 did not run as the hung build:\n%s", tailString(n.d.logs(), 40))
		}
		n.sameProcess(t)
		n.noHelperNoWrite(t, v1)
	})

	t.Run("hang-no-timer", func(t *testing.T) {
		n := newHomeNode(t, "node", v1)
		n.announce(t, hangNoTimer)
		var deadline time.Time
		n.waitFor(t, "v7 to start as a trial", n.boot, func() bool {
			trial, _ := update.ReadTrial(n.paths())
			if trial == nil || trial.UpdateID != hangNoTimer.updateID || trial.Starts != 1 {
				return false
			}
			deadline, _ = time.Parse(time.RFC3339Nano, trial.Deadline)
			return !deadline.IsZero()
		})
		time.Sleep(time.Until(deadline) + 5*time.Second)
		if !strings.Contains(n.d.logs(), "never opens its store") || pushIdentityMatches(n.d.adminBase, v1) {
			t.Fatalf("v7 is not the hung build still running past its deadline:\n%s", tailString(n.d.logs(), 40))
		}
		// A command typed after the deadline, while the hung trial daemon is
		// still alive, does not move the build away under it: it runs the
		// installed build and leaves the rollback to the next daemon start.
		text, err := n.typed(t, "version-after-deadline", "version")
		if err != nil || !strings.Contains(text, "did not pass its trial, and the daemon running it is still alive") || !strings.Contains(text, "e2e-v1") {
			t.Fatalf("a command typed after the deadline under the live trial daemon (%v):\n%s", err, text)
		}
		if update.HasFailedUpdate(n.paths(), hangNoTimer.updateID) || ledgerHasIfPresent(n.homeRoot, hangNoTimer.updateID, "rollback") {
			t.Fatalf("a typed command rolled v7 back while its daemon was alive: %v", ledgerActionsIfPresent(n.homeRoot))
		}
		if _, err := os.Stat(filepath.Join(n.homeRoot, "runtime", "sdn", "spacedatanetwork")); err != nil {
			t.Fatalf("the files of the running trial build were moved: %v", err)
		}
		// The hung process has to be ended (here as an operator would); the
		// supervisor's restart rolls it back and runs the installed build.
		_, pid := n.loop.state()
		_ = syscall.Kill(pid, syscall.SIGKILL)
		n.waitFor(t, "v7 rolled back and the installed build after the restart", n.boot, func() bool {
			return update.HasFailedUpdate(n.paths(), hangNoTimer.updateID) && pushIdentityMatches(n.d.adminBase, v1)
		})
		if !ledgerReasonHas(n.homeRoot, hangNoTimer.updateID, "health-failed", "deadline") {
			t.Fatalf("no health-failed for v7 from the daemon start: %v", ledgerActionsIfPresent(n.homeRoot))
		}
		n.noHelperNoWrite(t, v1)
	})

	t.Run("unreachable-home", func(t *testing.T) {
		// The account's home folder cannot be entered (what ProtectHome= does
		// to a system unit's account): the node updates itself into
		// <storage.path>/update-bundle.
		n := newHomeNodeWith(t, "node", v1, true)
		n.announce(t, v2)
		n.waitFor(t, "v2 to pass its trial from the storage folder", n.boot+time.Duration(n.health)*time.Second, func() bool {
			return pushIdentityMatches(n.d.adminBase, v2) && ledgerHasIfPresent(n.homeRoot, v2.updateID, "health-passed")
		})
		if target, err := os.Readlink("/proc/" + strconv.Itoa(n.pid) + "/exe"); err != nil || !strings.HasPrefix(target, n.homeRoot+"/") {
			t.Fatalf("the daemon runs %s (%v), want the build under %s", target, err, n.homeRoot)
		}
		n.sameProcess(t)
		n.noHelperNoWrite(t, v1)
	})

	t.Run("crash-mid-apply", func(t *testing.T) {
		// The installed build v8 is killed in the middle of a home install of
		// v2, after the state write and before trial.json: the next start
		// still runs v2 as a trial, and v2 passes it.
		n := newHomeNode(t, "node", crashMidApply)
		n.loop.stop(t)
		n.stopped = true
		n.publish(t, v2)
		text, err := n.typed(t, "install-crash-mid-apply", "update", "install")
		if err == nil {
			t.Fatalf("the v8 install was not killed mid-apply:\n%s", text)
		}
		state, serr := update.LoadState(n.paths())
		if serr != nil || state.UpdateID != v2.updateID || state.TrialUpdateID != v2.updateID {
			t.Fatalf("after the kill the state is %+v (%v), want v2 marked on trial:\n%s", state, serr, text)
		}
		if trial, terr := update.ReadTrial(n.paths()); trial != nil || terr != nil {
			t.Fatalf("trial.json exists after a kill before it was written: %+v (%v)", trial, terr)
		}
		n.loop = startRestartLoop(t, n.d)
		n.wantStarts = 1
		n.stopped = false
		n.waitFor(t, "v2 to pass its trial after the interrupted install", n.boot+time.Duration(n.health)*time.Second, func() bool {
			return pushIdentityMatches(n.d.adminBase, v2) && ledgerHasIfPresent(n.homeRoot, v2.updateID, "health-passed")
		})
		if !strings.Contains(n.d.logs(), "its trial record was not written") {
			t.Fatalf("the start did not say it restored v2's trial:\n%s", tailString(n.d.logs(), 40))
		}
		if got := n.countLogs("update: this process runs update " + v2.updateID + " as a trial"); got != 1 {
			t.Fatalf("v2 ran as a trial %d times, want 1", got)
		}
		n.noHelperNoWrite(t, crashMidApply)
	})

	t.Run("crash-mid-pass", func(t *testing.T) {
		// v15 is killed in the middle of passing its own trial, after
		// PassTrial's state write and before trial.json is removed. The
		// supervisor starts the installed path again: the start finishes the
		// pass, and v15 runs, not as a trial again (a trial run again could
		// roll a good build back on a slow start).
		n := newHomeNode(t, "node", v1)
		n.announce(t, crashMidPass)
		killed := "this build is killed after the state write of its trial pass"
		n.waitFor(t, "v15 killed in the middle of its pass", n.boot+time.Duration(n.health)*time.Second, func() bool {
			return n.countLogs(killed) == 1
		})
		n.wantStarts++
		n.waitFor(t, "v15 serving after the supervisor's restart", n.boot+time.Duration(n.health)*time.Second, func() bool {
			starts, pid := n.loop.state()
			if starts == n.wantStarts && pid != n.pid {
				n.pid = pid
			}
			return starts == n.wantStarts && pushIdentityMatches(n.d.adminBase, crashMidPass)
		})
		if !strings.Contains(n.d.logs(), "update "+crashMidPass.updateID+" passed its trial (the pass stopped after its state write)") {
			t.Errorf("the start did not say it finished v15's pass:\n%s", tailString(n.d.logs(), 40))
		}
		if got := n.countLogs("update: this process runs update " + crashMidPass.updateID + " as a trial"); got != 1 {
			t.Errorf("v15 ran as a trial %d times, want 1 (the pass was decided before the kill)", got)
		}
		passed := 0
		for _, e := range readLedger(t, n.homeRoot) {
			if e.UpdateID != crashMidPass.updateID {
				continue
			}
			switch e.Action {
			case "health-passed":
				passed++
			case "health-failed", "health-reverted", "revert-refused":
				t.Errorf("v15 has %s in its ledger: %v", e.Action, ledgerActionsIfPresent(n.homeRoot))
			}
		}
		if passed != 1 {
			t.Errorf("v15 has %d health-passed lines, want 1: %v", passed, ledgerActionsIfPresent(n.homeRoot))
		}
		if trial, err := update.ReadTrial(n.paths()); trial != nil || err != nil {
			t.Errorf("trial.json is still there after the pass was finished: %+v (%v)", trial, err)
		}
		if state, err := update.LoadState(n.paths()); err != nil || state.TrialUpdateID != "" || state.TrialPassed != crashMidPass.updateID {
			t.Errorf("state after the finished pass: %+v (%v), want no trial mark and trial_passed %s", state, err, crashMidPass.updateID)
		}
		requireRealIdentity(t, n.d.adminBase, crashMidPass)
		n.noHelperNoWrite(t, v1)
	})
}

// realE2EHangBuilds are v6 (blocks before its store opens), v7 (the same,
// with no trial deadline timer) and v11 (exits with an error at start),
// prebuilt beside the other fixtures or built here.
func realE2EHangBuilds(t *testing.T, dir string) (realBuild, realBuild, realBuild, realBuild, realBuild) {
	t.Helper()
	builds := []realBuild{
		{name: "v6", version: "1.1.2-e2e.6", updateID: "cli-bundle-release-e2e-6", sequence: 600},
		{name: "v7", version: "1.1.2-e2e.7", updateID: "cli-bundle-release-e2e-7", sequence: 700},
		{name: "v11", version: "1.1.2-e2e.11", updateID: "cli-bundle-release-e2e-11", sequence: 1100},
		{name: "v8", version: "1.1.2-e2e.8", updateID: "cli-bundle-release-e2e-8", sequence: 80},
		{name: "v15", version: "1.1.2-e2e.15", updateID: "cli-bundle-release-e2e-15", sequence: 150},
	}
	tags := map[string]string{"v6": "sdn_e2e_hang_before_store", "v7": "sdn_e2e_hang_before_store,sdn_e2e_no_trial_timer", "v11": "sdn_e2e_crash_at_start", "v8": "sdn_e2e_crash_mid_apply", "v15": "sdn_e2e_crash_mid_pass"}
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
			moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
			if err != nil {
				t.Fatal(err)
			}
			build := exec.Command(filepath.Join(moduleRoot, "..", "scripts", "go-with-wasmedge.sh"), "build", "-o", b.path, "-tags", tags[b.name], "./cmd/spacedatanetwork")
			build.Dir = moduleRoot
			build.Env = append(environWithout("SDN_GO_LDFLAGS"),
				"SDN_GO_LDFLAGS=-X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=e2e-"+b.name)
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %s: %v\n%s", b.name, err, output)
			}
		}
		b.sha = e2eFileSHA256(b.path)
		if b.sha == "" {
			t.Fatalf("no %s build at %s", b.name, b.path)
		}
	}
	return builds[0], builds[1], builds[2], builds[3], builds[4]
}

func ledgerHasAction(root, action string) bool {
	return strings.Contains(readFileString(filepath.Join(root, "deploy-ledger.jsonl")), `"action":"`+action+`"`)
}

func ledgerReasonHas(root, id, action, word string) bool {
	for _, line := range strings.Split(readFileString(filepath.Join(root, "deploy-ledger.jsonl")), "\n") {
		if strings.Contains(line, `"update_id":"`+id+`"`) && strings.Contains(line, `"action":"`+action+`"`) && strings.Contains(line, word) {
			return true
		}
	}
	return false
}

func ledgerActionsIfPresent(root string) []string {
	var out []string
	for _, line := range strings.Split(readFileString(filepath.Join(root, "deploy-ledger.jsonl")), "\n") {
		if i := strings.Index(line, `"action":"`); i >= 0 {
			rest := line[i+len(`"action":"`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				out = append(out, rest[:j])
			}
		}
	}
	return out
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
