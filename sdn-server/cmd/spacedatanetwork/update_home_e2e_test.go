package main

// UPDATES OF A NODE THAT CANNOT WRITE ITS INSTALL FOLDER, UNDER A SUPERVISOR,
// WITH NO HELPER, ON REAL DAEMONS (stack issue 846).
//
// The node is installed in a folder its account cannot write (every folder of
// the bundle is mode 0555; the test runs as an ordinary user), and it runs
// under a supervisor that does what a system unit with Restart=always does
// and nothing more: it starts the INSTALLED command and starts it again when
// it exits. Updates arrive as signed signals on the update topic. Steps, in
// order, on one install:
//
//	signal-good      v2 is announced: the daemon installs it into
//	                 $HOME/.spacedatanetwork/bundle, restarts through the
//	                 installed path in its own process, and v2 passes its
//	                 trial (health-passed). The install folder is unchanged; a
//	                 command typed against the installed path runs v2.
//	signal-bad       v3 (never ready) is announced: it is installed and
//	                 started as a trial, does not answer ready within the
//	                 health timeout, and is rolled back to v2 and quarantined
//	                 (health-failed, health-reverted, updates/failed/<v3>).
//	tampered-pending with the node stopped, `update install` puts v4 in the
//	                 home bundle as a pending trial; one byte is appended to
//	                 its binary; the next start refuses it, quarantines it and
//	                 runs v2.
//	typed-install    with the node running, `update install` of v5 restarts
//	                 the daemon for it and reports daemon_health=healthy.
//
// Throughout: the supervisor started the daemon once (every restart for an
// update is the same process), no helper ran (no updates/helper/ and no
// stop-requested in the ledger), and nothing was written in the install
// folder. Gated and run like TestUpdateChannelRealDaemonE2E, of which it is a
// subtest.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	ps "github.com/libp2p/go-libp2p-pubsub"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

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

// lastLog is the log of the daemon the loop started last.
func (l *restartLoop) lastLog() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.log
}

func (l *restartLoop) state() (starts, pid int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts, l.pid
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

func runRealHomeUpdate(t *testing.T) {
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second

	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2, v3 := builds[0], builds[1], builds[2]
	v4, v5 := builds[1], builds[1]
	v4.name, v4.version, v4.updateID, v4.sequence = "v4", "1.1.2-e2e.4", "cli-bundle-release-e2e-4", 400
	v5.name, v5.version, v5.updateID, v5.sequence = "v5", "1.1.2-e2e.5", "cli-bundle-release-e2e-5", 500

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

	// The installed bundle (v1), then made read-only for this account.
	d := newPushDaemon(t, filepath.Join(work, "node"), v1, feed, roots, health)
	d.tracker.root = work
	root := d.paths.Root
	setTreeWritable(t, root, false)
	t.Cleanup(func() { setTreeWritable(t, root, true) })
	if f, err := os.Create(filepath.Join(root, "probe")); err == nil {
		f.Close()
		t.Fatal("the install folder is writable by this account; the test needs one it cannot write")
	}
	homeRoot := filepath.Join(d.work, "home", ".spacedatanetwork", "bundle")
	homePaths := func() update.Paths { return update.PathsFor(homeRoot) }
	installedUntouched := func(t *testing.T) {
		t.Helper()
		if got := e2eFileSHA256(d.exe); got != v1.sha {
			t.Fatalf("the installed binary changed: sha256 %s, want v1 %s", got, v1.sha)
		}
		if _, err := os.Stat(filepath.Join(root, "updates")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("something was written into the install folder (updates/: %v)", err)
		}
	}
	noHelper := func(t *testing.T) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(homeRoot, "updates", "helper")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("an update helper was prepared (updates/helper: %v)", err)
		}
		if ledgerHasAction(homeRoot, "stop-requested") {
			t.Fatalf("the ledger shows a helper's stop request: %v", ledgerActions(t, homeRoot))
		}
	}
	waitFor := func(t *testing.T, what string, budget time.Duration, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(budget)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out after %s waiting for %s\nledger: %v\n%s", budget, what, ledgerActionsIfPresent(homeRoot), tailString(d.logs(), 60))
			}
			time.Sleep(time.Second)
		}
	}
	typed := func(t *testing.T, label string, args ...string) (string, error) {
		t.Helper()
		path := filepath.Join(d.work, label+".log")
		out, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		cmd := exec.Command(d.exe, append(args, "--config", d.cfg)...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.work, d.env, out, out
		err = cmd.Run()
		return readFileString(path), err
	}

	// The signal sender: a peer on the update topic, as the publishing node is.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	gossip, err := ps.NewGossipSub(ctx, sender)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := gossip.Join(update.SignalTopic(realE2EChannel))
	if err != nil {
		t.Fatal(err)
	}
	defer topic.Close()
	announce := func(t *testing.T, b realBuild) {
		t.Helper()
		feed.publish(t, b, key)
		feed.mu.Lock()
		var entry update.ProviderFeedUpdate
		for _, e := range feed.entries {
			if e.UpdateID == b.updateID {
				entry = e
			}
		}
		feed.mu.Unlock()
		raw, err := update.SignalFromFeedEntry(entry, feed.base, time.Now(), false).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		signed := resignPushSignal(t, raw, key, 0)
		// The daemon restarted for the last update has a new listen address.
		if err := sender.Connect(ctx, pushPeer(t, d.adminBase)); err != nil {
			t.Fatal(err)
		}
		waitPush(t, 30*time.Second, "the daemon on the update topic", func() bool { return len(topic.ListPeers()) >= 1 })
		time.Sleep(3 * time.Second)
		if err := topic.Publish(ctx, signed); err != nil {
			t.Fatal(err)
		}
		t.Logf("announced %s", b.updateID)
	}

	loop := startRestartLoop(t, d)
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			loop.stop(t)
		}
	})
	waitRealReady(t, d.adminBase, boot, loop.lastLog())
	requireRealIdentity(t, d.adminBase, v1)
	_, servingPID := loop.state()
	sameProcess := func(t *testing.T) {
		t.Helper()
		if starts, pid := loop.state(); starts != 1 || pid != servingPID || !processAlive(servingPID) {
			t.Fatalf("the supervisor started the daemon %d times (pid %d, first %d): an update restart must be the same process", starts, pid, servingPID)
		}
	}

	if !t.Run("signal-good", func(t *testing.T) {
		announce(t, v2)
		waitFor(t, "v2 to pass its trial", boot+time.Duration(health)*time.Second, func() bool {
			return pushIdentityMatches(d.adminBase, v2) && ledgerHasIfPresent(homeRoot, v2.updateID, "health-passed")
		})
		requireRealIdentity(t, d.adminBase, v2)
		if trial, _ := update.ReadTrial(homePaths()); trial != nil {
			t.Fatalf("v2 passed but its trial is still pending: %+v", trial)
		}
		sameProcess(t)
		noHelper(t)
		installedUntouched(t)
		if text, err := typed(t, "version-v2", "version"); err != nil || !strings.Contains(text, "e2e-v2") {
			t.Fatalf("a command typed against the installed path did not run v2 (%v):\n%s", err, text)
		}
	}) {
		return
	}

	if !t.Run("signal-bad", func(t *testing.T) {
		announce(t, v3)
		waitFor(t, "v3 to be reverted", boot+3*time.Duration(health)*time.Second, func() bool {
			return update.HasFailedUpdate(homePaths(), v3.updateID) && ledgerHasIfPresent(homeRoot, v3.updateID, "health-reverted") && pushIdentityMatches(d.adminBase, v2)
		})
		requireRealIdentity(t, d.adminBase, v2)
		for _, action := range []string{"apply", "health-failed", "health-reverted"} {
			if !ledgerHas(t, homeRoot, v3.updateID, action) {
				t.Fatalf("no %s for v3 in the ledger: %v", action, ledgerActions(t, homeRoot))
			}
		}
		if !strings.Contains(d.logs(), "failed its trial") {
			t.Fatalf("the daemon did not log the failed trial:\n%s", tailString(d.logs(), 60))
		}
		sameProcess(t)
		noHelper(t)
		installedUntouched(t)
	}) {
		return
	}

	if !t.Run("tampered-pending", func(t *testing.T) {
		loop.stop(t)
		stopped = true
		feed.publish(t, v4, key)
		text, err := typed(t, "install-v4", "update", "install")
		if err != nil || !strings.Contains(text, "installed_update="+v4.updateID) || !strings.Contains(text, "next=start the node") {
			t.Fatalf("update install of v4 with no daemon (%v):\n%s", err, text)
		}
		if trial, _ := update.ReadTrial(homePaths()); trial == nil || trial.UpdateID != v4.updateID {
			t.Fatalf("v4 is not a pending trial: %+v", trial)
		}
		binary := filepath.Join(homeRoot, "bin", "spacedatanetwork")
		f, err := os.OpenFile(binary, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte{0}); err != nil {
			t.Fatal(err)
		}
		f.Close()
		// A typed command trusts v4's recorded verification only while its
		// files are unchanged: this one verifies again and refuses it.
		if text, err := typed(t, "version-tampered", "version"); err != nil || !strings.Contains(text, "is refused") || !strings.Contains(text, "e2e-v1") {
			t.Fatalf("a command typed against the installed path did not refuse the changed v4 (%v):\n%s", err, text)
		}
		loop = startRestartLoop(t, d)
		stopped = false
		waitRealReady(t, d.adminBase, boot, loop.lastLog())
		requireRealIdentity(t, d.adminBase, v2)
		if !update.HasFailedUpdate(homePaths(), v4.updateID) {
			t.Fatalf("the tampered v4 was not quarantined")
		}
		log := readFileString(loop.lastLog())
		if !strings.Contains(log, "bin/spacedatanetwork differs") || !strings.Contains(log, "quarantined") {
			t.Fatalf("the start did not say why it refused v4:\n%s", tailString(log, 40))
		}
		_, servingPID = loop.state()
		noHelper(t)
		installedUntouched(t)
	}) {
		return
	}

	t.Run("typed-install", func(t *testing.T) {
		feed.publish(t, v5, key)
		text, err := typed(t, "install-v5", "update", "install")
		if err != nil || !strings.Contains(text, "daemon_restart=requested") || !strings.Contains(text, "daemon_health=healthy") ||
			!strings.Contains(text, "daemon_identity=verified bundle_version="+v5.version) || strings.Contains(text, "helper_") {
			t.Fatalf("update install of v5 with the daemon running (%v):\n%s", err, text)
		}
		requireRealIdentity(t, d.adminBase, v5)
		sameProcess(t)
		noHelper(t)
		installedUntouched(t)
	})

	for _, log := range d.logPaths {
		requireIsolated(t, log)
	}
}

func ledgerHasAction(root, action string) bool {
	return strings.Contains(readFileString(filepath.Join(root, "deploy-ledger.jsonl")), `"action":"`+action+`"`)
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
