package main

// THE UPDATE STATUS GUARD AND THE ROLLOUT'S SIGNAL PATHS, ON REAL DAEMONS.
// Subtests of TestUpdateChannelRealDaemonE2E, gated and run like it (CI group
// `rollout`).
//
//	fleet-status-guard  two daemons (b with update.publish_status false) and
//	                    in-test libp2p hosts. b publishes no status and a's
//	                    fleet view shows it "not reporting". A host that is
//	                    not an SDN node publishes statuses signed by 40 fresh
//	                    keys and its own: a keeps none, relays none. A host
//	                    that identifies as an SDN node publishes a valid
//	                    status: a shows it as self-reported and relays it, and
//	                    its claim of a newer signal does not move a's newest
//	                    signal. Its forged status (a version holding a
//	                    newline, tabs and an escape sequence that print a
//	                    second row) and an oversize one are refused and not
//	                    relayed, and `update fleet` prints no forged row; a
//	                    burst of 30 statuses is relayed at most at the
//	                    per-author rate; once it disconnects, a replay of its
//	                    last status does not make it look seen again.
//	rollout-signals     a publisher and a receiver r (update.rollout_window
//	                    600 s). A signal with no window waits by the config
//	                    window; a newer signal with window 0 replaces that
//	                    wait and installs at once (the first never installs);
//	                    an update withdrawn from the feed index during its
//	                    wait is not installed at its due time; a pending
//	                    signal that expired while r was down is cleared at
//	                    start and the feed check that follows finds the same
//	                    release and waits for r's delay before it installs;
//	                    a due time pushed days ahead is capped at the window
//	                    on restart; a signal pushed while r was down reaches
//	                    r when it joins the topic again (the publisher sends
//	                    it again) and replaces r's resumed wait.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// e2eTopicHost is an in-test libp2p host on the update topic, with every
// message it receives kept by author.
type e2eTopicHost struct {
	host  host.Host
	topic *ps.Topic
	mu    sync.Mutex
	got   map[peer.ID][][]byte
}

func newE2ETopicHost(t *testing.T, ctx context.Context, agent string) *e2eTopicHost {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay(), libp2p.UserAgent(agent))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	gossip, err := ps.NewGossipSub(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := gossip.Join(update.SignalTopic(realE2EChannel))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := topic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	e := &e2eTopicHost{host: h, topic: topic, got: map[peer.ID][][]byte{}}
	go func() {
		for {
			message, err := sub.Next(ctx)
			if err != nil {
				return
			}
			e.mu.Lock()
			e.got[message.GetFrom()] = append(e.got[message.GetFrom()], append([]byte(nil), message.Data...))
			e.mu.Unlock()
		}
	}()
	return e
}

func (e *e2eTopicHost) connect(t *testing.T, ctx context.Context, d *pushDaemon) peer.ID {
	t.Helper()
	info := pushPeer(t, d.adminBase)
	if err := e.host.Connect(ctx, info); err != nil {
		t.Fatal(err)
	}
	waitPush(t, 30*time.Second, "the update topic subscription of "+info.ID.String(), func() bool { return slices.Contains(e.topic.ListPeers(), info.ID) })
	time.Sleep(3 * time.Second)
	return info.ID
}

func (e *e2eTopicHost) from(id peer.ID) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]byte(nil), e.got[id]...)
}

// e2eStatus is a valid status message for id.
func e2eStatus(id peer.ID, version string, at time.Time) map[string]any {
	return map[string]any{
		"schema": update.StatusSchema, "peer_id": id.String(), "version": version, "sequence": 1,
		"state": update.StatusCurrent, "at": at.UTC().Format(time.RFC3339),
	}
}

func e2eJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func runRealFleetStatusGuard(t *testing.T) {
	start := time.Now()
	defer func() { t.Logf("fleet status guard duration: %s", time.Since(start).Round(time.Second)) }()
	work := t.TempDir()
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1 := builds[0]
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	roots := update.TrustedRoots{update.RootKeyID(pub): base64.StdEncoding.EncodeToString(spki)}
	feed := newRealE2EFeed(t, work)

	a := newPushDaemon(t, filepath.Join(work, "a"), v1, feed, roots, health)
	a.start(t, boot)
	aInfo := pushPeer(t, a.adminBase)
	b := newPushDaemon(t, filepath.Join(work, "b"), v1, feed, roots, health)
	b.configure(t, func(cfg *config.Config) {
		cfg.Network.Bootstrap = []string{aInfo.Addrs[0].String() + "/p2p/" + aInfo.ID.String()}
		cfg.Update.PublishStatus = false
	})
	b.start(t, boot)
	bID := pushPeer(t, b.adminBase).ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// b publishes no status, a does. The listener joins both, which makes
	// each announce (a node publishes its status when a peer joins).
	listener := newE2ETopicHost(t, ctx, "e2e-listener/1.0")
	listener.connect(t, ctx, a)
	listener.connect(t, ctx, b)
	waitPush(t, 40*time.Second, "a status from a on the listener", func() bool { return len(listener.from(aInfo.ID)) > 0 })
	time.Sleep(15 * time.Second)
	if got := listener.from(bID); len(got) > 0 {
		t.Errorf("b (update.publish_status false) published %d messages; the first: %s", len(got), got[0])
	}
	waitFleet(t, a, 2*time.Minute, "b listed", func(r *node.FleetReport) bool { return fleetRow(r, bID) != nil })
	if row := fleetRow(fleetOf(t, a), bID); row.State != node.FleetStateNotReporting || row.Source != "identify" {
		t.Errorf("a's row for b (update.publish_status false): %+v, want not reporting from identify", row)
	}
	t.Log("b, with update.publish_status false, published nothing, and a shows it not reporting")

	// Fresh keys: one host that is not an SDN node publishes statuses signed
	// by 40 keys that never connected, and one of its own.
	stranger := newE2ETopicHost(t, ctx, "e2e-stranger/1.0")
	stranger.connect(t, ctx, a)
	var fake []peer.ID
	for i := 0; i < 40; i++ {
		priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		id, err := peer.IDFromPrivateKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		fake = append(fake, id)
		if err := stranger.topic.Publish(ctx, e2eJSON(t, e2eStatus(id, "6.6.6-fake", time.Now())), ps.WithSecretKeyAndPeerId(priv, id)); err != nil {
			t.Fatal(err)
		}
	}
	fake = append(fake, stranger.host.ID())
	if err := stranger.topic.Publish(ctx, e2eJSON(t, e2eStatus(stranger.host.ID(), "6.6.6-fake", time.Now()))); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second)
	for _, d := range []*pushDaemon{a, b} {
		report := fleetOf(t, d)
		for _, row := range report.Rows {
			if row.Version == "6.6.6-fake" || slices.Contains(fake, mustDecodePeer(t, row.PeerID)) {
				t.Errorf("%s keeps a status from a peer it never identified as an SDN node: %+v", d.work, row)
			}
		}
	}
	for _, id := range fake {
		if got := listener.from(id); len(got) > 0 {
			t.Errorf("a relayed %d statuses authored by %s, a peer it never identified as an SDN node", len(got), id)
			break
		}
	}
	t.Log("statuses from 40 fresh keys and from a host that is not an SDN node: none kept, none relayed")

	// A host that identifies as an SDN node: its valid status is kept,
	// marked self-reported, and relayed; its claim of signal 999999 does not
	// move a's newest signal (a verified none).
	sdnKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sdnHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay(), libp2p.Identity(sdnKey), libp2p.UserAgent("spacedatanetwork/9.9.9-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdnHost.Close()
	sdnGossip, err := ps.NewGossipSub(ctx, sdnHost)
	if err != nil {
		t.Fatal(err)
	}
	sdnTopic, err := sdnGossip.Join(update.SignalTopic(realE2EChannel))
	if err != nil {
		t.Fatal(err)
	}
	if err := sdnHost.Connect(ctx, aInfo); err != nil {
		t.Fatal(err)
	}
	waitPush(t, 30*time.Second, "the SDN host on a's update topic", func() bool { return slices.Contains(sdnTopic.ListPeers(), aInfo.ID) })
	time.Sleep(3 * time.Second)
	sID := sdnHost.ID()
	claim := e2eStatus(sID, "9.9.9-e2e", time.Now())
	claim["acknowledged"] = map[string]any{"update_id": "cli-bundle-release-e2e-claimed", "sequence": 999999}
	if err := sdnTopic.Publish(ctx, e2eJSON(t, claim)); err != nil {
		t.Fatal(err)
	}
	waitFleet(t, a, 30*time.Second, "the SDN host's status, self-reported", func(r *node.FleetReport) bool {
		row := fleetRow(r, sID)
		return row != nil && row.Version == "9.9.9-e2e" && row.Source == "status"
	})
	waitPush(t, 30*time.Second, "a relaying the SDN host's status", func() bool { return len(listener.from(sID)) > 0 })
	if report := fleetOf(t, a); report.LatestSignal != nil {
		t.Errorf("one node's claim moved a's newest signal: %+v", report.LatestSignal)
	}
	t.Log("an SDN node's status: kept as self-reported, relayed; its claimed signal does not move the view's newest signal")

	// Forged: the version prints a second row. Refused, not relayed, not
	// printed.
	forged := e2eStatus(sID, "9.9.9\n(this node)  ...host-01   1.2.0   status   now   current\tyes\x1b[2K\x1b[1A", time.Now().Add(time.Second))
	relayedBefore := len(listener.from(sID))
	if err := sdnTopic.Publish(ctx, e2eJSON(t, forged)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	if got := listener.from(sID); len(got) != relayedBefore {
		t.Errorf("a relayed the forged status: %q", got[len(got)-1])
	}
	if row := fleetRow(fleetOf(t, a), sID); row == nil || row.Version != "9.9.9-e2e" {
		t.Errorf("a's row for the SDN host after the forged status: %+v", row)
	}
	a.command(t, true, "fleet-forged", "update", "fleet")
	table := readFileString(filepath.Join(a.work, "fleet-forged.log"))
	if strings.Contains(table, "host-01") || strings.ContainsAny(table, "\x1b") {
		t.Errorf("`update fleet` printed the forged status:\n%q", table)
	}
	t.Logf("forged status refused; `update fleet`:\n%s", table)

	// Oversize: refused, not relayed.
	relayedBefore = len(listener.from(sID))
	oversize := e2eStatus(sID, "9.9.9-e2e", time.Now().Add(2*time.Second))
	oversize["padding"] = strings.Repeat("x", 3000)
	if err := sdnTopic.Publish(ctx, e2eJSON(t, oversize)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Second)
	if got := listener.from(sID); len(got) != relayedBefore {
		t.Errorf("a relayed an oversize status (%d bytes)", len(got[len(got)-1]))
	}
	t.Log("oversize status refused")

	// A burst: relayed at most at the per-author rate.
	relayedBefore = len(listener.from(sID))
	for i := 0; i < 30; i++ {
		if err := sdnTopic.Publish(ctx, e2eJSON(t, e2eStatus(sID, "9.9.9-e2e", time.Now()))); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(8 * time.Second)
	burst := len(listener.from(sID)) - relayedBefore
	if burst < 1 || burst > 7 {
		t.Errorf("a relayed %d of 30 statuses sent at once by one author, want 1 to 7 (6 at once, then one per 10 s)", burst)
	}
	t.Logf("a burst of 30 statuses from one author: %d relayed", burst)

	// A replay after the SDN host left: it does not make it look seen.
	relayed := listener.from(sID)
	last := relayed[len(relayed)-1]
	if err := sdnHost.Close(); err != nil {
		t.Fatal(err)
	}
	var before *node.FleetRow
	waitPush(t, 30*time.Second, "the SDN host offline on a's view", func() bool {
		before = fleetRow(fleetOf(t, a), sID)
		return before != nil && !before.Online
	})
	time.Sleep(3 * time.Second)
	if err := listener.topic.Publish(ctx, last, ps.WithSecretKeyAndPeerId(sdnKey, sID)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	after := fleetRow(fleetOf(t, a), sID)
	if after == nil || after.LastSeen != before.LastSeen || after.StatusAt != before.StatusAt {
		t.Errorf("a replay of the offline SDN host's last status changed its row: before %+v, after %+v", before, after)
	}
	t.Logf("a replay of an offline node's last status: last seen stays %s", before.LastSeen)
	// a dials no peer; b dials only a (its config passed requireIsolatedConfig).
	for _, log := range a.logPaths {
		requireIsolated(t, log)
	}
}

func mustDecodePeer(t *testing.T, s string) peer.ID {
	t.Helper()
	id, err := peer.Decode(s)
	if err != nil {
		t.Fatalf("peer id %q: %v", s, err)
	}
	return id
}

// withdrawIndexEntry removes updateID from the feed index and leaves its
// manifest and carrier where they are.
func (f *realE2EFeed) withdrawIndexEntry(t *testing.T, updateID string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = slices.DeleteFunc(f.entries, func(entry update.ProviderFeedUpdate) bool { return entry.UpdateID == updateID })
	index, err := json.Marshal(update.ProviderFeed{
		Schema:      update.ProviderFeedSchema,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		FeedBaseURL: f.base,
		Updates:     f.entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.files["/updates"+f.lane()+"/index.json"] = index
	if f.disk {
		if err := os.WriteFile(filepath.Join(f.dir, strings.TrimPrefix(f.lane(), "/"), "index.json"), index, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runRealRolloutSignals(t *testing.T) {
	start := time.Now()
	defer func() { t.Logf("rollout signals duration: %s", time.Since(start).Round(time.Second)) }()
	work := t.TempDir()
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2 := builds[0], builds[1]
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second
	const windowSeconds = 600
	window := windowSeconds * time.Second

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
	feed.disk = true
	if err := os.MkdirAll(feed.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	signer := realE2ESigner(t, filepath.Join(work, "builds"))
	rootsPath := filepath.Join(work, "pipeline-roots.json")
	writeE2EJSON(t, rootsPath, roots)
	if err := os.MkdirAll(filepath.Join(work, "pipeline-repo", "sdn-server"), 0o700); err != nil {
		t.Fatal(err)
	}

	a := newPushDaemon(t, filepath.Join(work, "a"), v1, feed, roots, health)
	if err := update.SaveState(a.paths, &update.State{Sequence: 10000}); err != nil {
		t.Fatal(err)
	}
	a.env = append(a.env, "SDN_UPDATE_FEED_DIR="+feed.dir)
	a.start(t, boot)
	feed.mu.Lock()
	feed.forward = a.adminBase
	feed.mu.Unlock()
	aInfo := pushPeer(t, a.adminBase)
	r := newPushDaemon(t, filepath.Join(work, "r"), v1, feed, roots, health)
	r.configure(t, func(cfg *config.Config) {
		cfg.Network.Bootstrap = []string{aInfo.Addrs[0].String() + "/p2p/" + aInfo.ID.String()}
		cfg.Update.RolloutWindowSeconds = windowSeconds
	})
	r.start(t, boot)
	rID := pushPeer(t, r.adminBase).ID
	waitFleet(t, a, 2*time.Minute, "r on v1", func(rep *node.FleetReport) bool {
		row := fleetRow(rep, rID)
		return row != nil && row.Version == v1.version && row.Source == "status"
	})

	// An update id whose delay for r within w is in [lo, hi]: the delay is a
	// function of r's peer id and the update id, and each step needs room.
	pick := func(prefix string, w, lo, hi time.Duration) string {
		for i := 0; i < 20000; i++ {
			id := fmt.Sprintf("cli-bundle-release-e2e-%s-%d", prefix, i)
			if d := update.RolloutDelay(rID.String(), id, w); d >= lo && d <= hi {
				return id
			}
		}
		t.Fatalf("no %s update id with a delay in [%s, %s]", prefix, lo, hi)
		return ""
	}
	release := func(n int, prefix string, sequence int64, w, lo, hi time.Duration) realBuild {
		b := v2
		b.name = fmt.Sprintf("u%d", n)
		b.version = fmt.Sprintf("1.1.2-e2e.2%d", n)
		b.updateID = pick(prefix, w, lo, hi)
		b.sequence = sequence
		return b
	}
	sign := func(label string, windowArg int) {
		args := []string{"--key-env", "SDN_UPDATE_SIGNING_KEY_PEM", "--roots", rootsPath, "--feed-dir", feed.dir}
		if windowArg >= 0 {
			args = append(args, "--rollout-window-seconds", strconv.Itoa(windowArg))
		}
		runSignalPipelineEnv(t, signer, work, "sign-"+label, []string{"SDN_UPDATE_SIGNING_KEY_PEM=" + string(keyPEM)}, args...)
	}
	push := func(label string, b realBuild) {
		a.command(t, true, label, "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", b.version)
	}
	pendingOf := func(id string) *update.Pending {
		p, err := update.ReadPending(r.paths)
		if err != nil || p == nil || p.UpdateID != id {
			return nil
		}
		return p
	}
	ledgerOf := func(id, action string) bool { return ledgerHasIfPresent(r.paths.Root, id, action) }

	// 1. A signal with no window waits by r's configured window.
	u1 := release(1, "config", 210, window, 330*time.Second, 590*time.Second)
	feed.publish(t, u1, key)
	sign("u1", -1)
	push("push-u1", u1)
	var p1 *update.Pending
	waitPush(t, time.Minute, "r pending u1", func() bool { p1 = pendingOf(u1.updateID); return p1 != nil })
	accepted, err := time.Parse(time.RFC3339, p1.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	delay1 := update.RolloutDelay(rID.String(), u1.updateID, window)
	if p1.WindowFrom != "config" || p1.WindowSeconds != windowSeconds || time.Duration(p1.DelaySeconds)*time.Second != delay1 || !p1.Due().Equal(accepted.Add(delay1)) || p1.Trigger != "signal" {
		t.Fatalf("r's pending record for a signal with no window: %+v; want window 600 from config, delay %s", p1, delay1)
	}
	t.Logf("u1 (no signed window): pending by the config window, delay %s, due %s", delay1, p1.DueAt)

	// 2. A newer signal with window 0 replaces that wait and installs at
	// once; u1 never installs.
	u2 := release(2, "urgent", 220, window, 0, window)
	feed.publish(t, u2, key)
	sign("u2", 0)
	pushedU2 := time.Now()
	push("push-u2", u2)
	waitPush(t, 4*time.Minute, "r on u2", func() bool {
		r.trackHelper()
		return pushIdentityMatches(r.adminBase, u2) && ledgerOf(u2.updateID, "health-passed")
	})
	if !strings.Contains(r.logs(), "replaces the update waiting for its due time") {
		t.Errorf("r did not say u2 replaces u1's wait:\n%s", tailString(r.logs(), 30))
	}
	if ledgerOf(u1.updateID, "stop-requested") || ledgerOf(u1.updateID, "health-passed") {
		t.Errorf("u1, replaced while it waited, was installed: %v", ledgerActions(t, r.paths.Root))
	}
	if p, err := update.ReadPending(r.paths); err != nil || p != nil {
		t.Errorf("after u2 the pending record is %+v (%v), want none", p, err)
	}
	t.Logf("u2 (window 0) replaced u1's wait and installed %s after its push", time.Since(pushedU2).Round(time.Second))

	waitFleet(t, a, 2*time.Minute, "r on u2", func(rep *node.FleetReport) bool {
		row := fleetRow(rep, rID)
		return row != nil && row.Version == u2.version
	})

	// 3. Withdrawn from the feed index during its wait: not installed at its
	// due time.
	u3 := release(3, "withdrawn", 230, time.Minute, 25*time.Second, 55*time.Second)
	feed.publish(t, u3, key)
	sign("u3", 60)
	push("push-u3", u3)
	var p3 *update.Pending
	waitPush(t, 20*time.Second, "r pending u3", func() bool { p3 = pendingOf(u3.updateID); return p3 != nil })
	feed.withdrawIndexEntry(t, u3.updateID)
	time.Sleep(time.Until(p3.Due()) + 20*time.Second)
	if !strings.Contains(r.logs(), "Update "+u3.updateID+" (version "+u3.version+"): rollout due time "+p3.DueAt+" reached, and the feed index no longer lists it (withdrawn)") {
		t.Errorf("r did not drop u3, withdrawn from the index, at its due time:\n%s", tailString(r.logs(), 30))
	}
	if ledgerOf(u3.updateID, "stop-requested") || !pushIdentityMatches(r.adminBase, u2) {
		t.Errorf("u3, withdrawn from the index, was installed: %v", ledgerActions(t, r.paths.Root))
	}
	if p := pendingOf(u3.updateID); p != nil {
		t.Errorf("u3's pending record is still there after it was dropped: %+v", p)
	}
	t.Log("u3, withdrawn from the index during its wait, was not installed at its due time")

	// 4. A pending signal that expires while r is down: cleared at start; the
	// feed check that follows finds the release and waits for r's delay.
	u4 := release(4, "expired", 240, window, 150*time.Second, 240*time.Second)
	feed.publish(t, u4, key)
	sign("u4", windowSeconds)
	signedU4, err := os.ReadFile(filepath.Join(feed.dir, strings.TrimPrefix(feed.lane(), "/"), u4.version, "signal.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsedU4, err := update.ParseSignal(signedU4)
	if err != nil {
		t.Fatal(err)
	}
	publishedU4, err := time.Parse(time.RFC3339, parsedU4.PublishedAt)
	if err != nil {
		t.Fatal(err)
	}
	expiresU4 := time.Now().Add(45 * time.Second).Truncate(time.Second)
	shortLived := resignPushSignal(t, signedU4, key, expiresU4.Sub(publishedU4))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := newE2ETopicHost(t, ctx, "e2e-sender/1.0")
	sender.connect(t, ctx, a)
	if err := sender.topic.Publish(ctx, shortLived); err != nil {
		t.Fatal(err)
	}
	waitPush(t, 30*time.Second, "r pending u4 by its signal", func() bool {
		p := pendingOf(u4.updateID)
		return p != nil && p.Trigger == "signal"
	})
	r.stop(t)
	time.Sleep(time.Until(expiresU4) + 5*time.Second)
	restarted := time.Now()
	r.start(t, boot)
	if !strings.Contains(r.logs(), "was pending, and its signal no longer verifies") {
		t.Errorf("r did not clear the expired pending signal at start:\n%s", tailString(r.logs(), 30))
	}
	delay4 := update.RolloutDelay(rID.String(), u4.updateID, window)
	var p4 *update.Pending
	waitPush(t, 40*time.Second, "r pending u4 from a feed check", func() bool {
		p := pendingOf(u4.updateID)
		if p != nil && p.Trigger == "feed-check" {
			p4 = p
		}
		return p4 != nil
	})
	accepted4, err := time.Parse(time.RFC3339, p4.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	if p4.WindowFrom != "config" || time.Duration(p4.DelaySeconds)*time.Second != delay4 || !p4.Due().Equal(accepted4.Add(delay4)) || accepted4.Before(restarted.Truncate(time.Second)) {
		t.Fatalf("r's pending record from the feed check: %+v; want the config window, delay %s, accepted after the restart", p4, delay4)
	}
	r.waitUpgrade(t, u4, time.Until(p4.Due())+boot+2*time.Duration(health)*time.Second)
	at := installStartedAt(t, r.logs(), u4.updateID, p4.Due())
	if at.Before(p4.Due()) || at.After(p4.Due().Add(30*time.Second)) {
		t.Errorf("r started u4 from its feed check at %s, want at its due time %s", at, p4.DueAt)
	}
	if !slices.ContainsFunc(readLedger(t, r.paths.Root), func(e update.DeployLedgerEntry) bool {
		return e.UpdateID == u4.updateID && e.Action == "health-passed" && e.Trigger == "feed-check"
	}) {
		t.Errorf("u4 was not installed by the feed check: %v", ledgerActions(t, r.paths.Root))
	}
	t.Logf("u4: expired pending signal cleared at start; the feed check waited %s (r's delay) and installed it", delay4)

	// 5. A due time pushed days ahead (a clock that was ahead when the update
	// was accepted) is capped at the window on restart.
	waitFleet(t, a, 2*time.Minute, "r on u4", func(rep *node.FleetReport) bool {
		row := fleetRow(rep, rID)
		return row != nil && row.Version == u4.version
	})
	u5 := release(5, "cap", 250, window, 300*time.Second, 590*time.Second)
	feed.publish(t, u5, key)
	sign("u5", windowSeconds)
	push("push-u5", u5)
	var p5 *update.Pending
	waitPush(t, time.Minute, "r pending u5", func() bool { p5 = pendingOf(u5.updateID); return p5 != nil })
	r.stop(t)
	p5.DueAt = time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	writeE2EJSON(t, filepath.Join(r.paths.Updates, "pending.json"), p5)
	restarted = time.Now()
	r.start(t, boot)
	waitPush(t, 30*time.Second, "r's capped due time", func() bool {
		p := pendingOf(u5.updateID)
		return p != nil && !p.Due().After(time.Now().Add(window))
	})
	if p := pendingOf(u5.updateID); p == nil || p.Due().Before(restarted.Add(window-10*time.Second).Truncate(time.Second)) {
		t.Errorf("r's capped pending record: %+v, want due about %s after the restart", p, window)
	}
	if !strings.Contains(r.logs(), "Update "+u5.updateID+": its due time was more than its rollout window (600s) away") {
		t.Errorf("r did not say it capped u5's due time:\n%s", tailString(r.logs(), 30))
	}
	t.Log("u5: a due time three days ahead was capped at the window on restart")

	// 6. Pushed while r is down: the publisher sends it again when r joins
	// the topic, and it replaces r's resumed wait for u5.
	r.stop(t)
	u6 := release(6, "rejoin", 260, window, 0, window)
	feed.publish(t, u6, key)
	sign("u6", 0)
	push("push-u6", u6)
	time.Sleep(5 * time.Second)
	r.start(t, boot)
	ready := time.Now()
	waitPush(t, 3*time.Minute, "r on u6 after it joined again", func() bool {
		r.trackHelper()
		return pushIdentityMatches(r.adminBase, u6) && ledgerOf(u6.updateID, "health-passed")
	})
	if !slices.ContainsFunc(readLedger(t, r.paths.Root), func(e update.DeployLedgerEntry) bool {
		return e.UpdateID == u6.updateID && e.Action == "stop-requested" && e.Trigger == "signal"
	}) {
		t.Errorf("u6 did not reach r as a signal: %v", ledgerActions(t, r.paths.Root))
	}
	if !strings.Contains(a.logs(), "Update signal sent again on") {
		t.Errorf("the publisher did not send u6 again:\n%s", tailString(a.logs(), 30))
	}
	if ledgerOf(u5.updateID, "stop-requested") {
		t.Errorf("u5, replaced while it waited, was installed: %v", ledgerActions(t, r.paths.Root))
	}
	t.Logf("u6, pushed while r was down, reached r %s after it was ready and replaced its wait for u5", time.Since(ready).Round(time.Second))
	for _, log := range a.logPaths {
		requireIsolated(t, log)
	}
}
