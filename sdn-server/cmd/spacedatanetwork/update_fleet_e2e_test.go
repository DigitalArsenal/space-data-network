package main

// STAGGERED ROLLOUT, THE FLEET VERSION VIEW, AND `update install --config`
// UNDER TLS, ON REAL DAEMONS. Subtests of TestUpdateChannelRealDaemonE2E,
// gated and run like it (CI group `fleet`).
//
//	rollout-fleet     a publisher and three receivers on loopback. Before:
//	                  every node's `update fleet` row shows v1, current. One
//	                  signal for v2, signed with a rollout window: each
//	                  receiver records its due time (accepted time plus its
//	                  own delay, update.RolloutDelay of its peer id and the
//	                  update id, within the signed window, which replaces the
//	                  nodes' configured window); the publisher's fleet view
//	                  shows each one pending until that time; the receiver
//	                  with the latest due time is restarted during its wait
//	                  and keeps its due time; each one installs at or after
//	                  its due time, in the order of their delays, and no two
//	                  are down at the same time (helper stop to health-passed,
//	                  from their ledgers). After: every row shows v2, current,
//	                  and that it accepted the newest signal. Then v3 (never
//	                  ready) is signed with window 0 and reaches one receiver
//	                  only: it installs at once, fails, is rolled back, and
//	                  the publisher's fleet view shows it failed and rolled
//	                  back, from the receiver's own status.
//	config-flag-tls   a daemon whose admin listener serves a self-signed
//	                  certificate for a name other than 127.0.0.1 (as host-01
//	                  serves its origin certificate): `update install
//	                  --config <its config>` goes through the helper and
//	                  installs v2 (stack issue 839).

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// statusAuthors counts the update status messages nextUpdateSignal skipped,
// by author.
var statusAuthors sync.Map

// nextUpdateSignal is sub.Next without the update status messages 1.2.0 nodes
// publish on the update topic: the next message that is not one.
func nextUpdateSignal(ctx context.Context, sub *ps.Subscription) (*ps.Message, error) {
	for {
		message, err := sub.Next(ctx)
		if err != nil {
			return nil, err
		}
		if status, err := update.ParseStatus(message.Data); err == nil && status.PeerID == message.GetFrom().String() {
			count, _ := statusAuthors.LoadOrStore(status.PeerID, new(int))
			*count.(*int)++
			continue
		}
		return message, nil
	}
}

func statusesSeenFrom(id peer.ID) int {
	if count, ok := statusAuthors.Load(id.String()); ok {
		return *count.(*int)
	}
	return 0
}

// requirePublishedReceiverIgnoredNewMessages: while the published receiver
// ran, the 1.2.0 publisher's status messages were on the topic, the signal it
// installed carried rollout_window_seconds under its signature, and its
// handler saw only that signal and acted on it at once.
func requirePublishedReceiverIgnoredNewMessages(t *testing.T, signal []byte, publisher peer.ID, receiverLog string) {
	t.Helper()
	parsed, err := update.ParseSignal(signal)
	if err != nil || parsed.RolloutWindowSeconds == nil || *parsed.RolloutWindowSeconds != 600 {
		t.Fatalf("the signal sent to the published receiver does not carry rollout_window_seconds 600 (%v)", err)
	}
	if statusesSeenFrom(publisher) == 0 {
		t.Fatal("the 1.2.0 publisher published no update status message on the topic the published receiver follows")
	}
	text := readFileString(receiverLog)
	if got := strings.Count(text, "Update signal handler received a validated message"); got != 1 {
		t.Fatalf("the published receiver's handler received %d messages, want 1 (the signal only):\n%s", got, tailString(text, 40))
	}
	for _, bad := range []string{"panicked", "staggering"} {
		if strings.Contains(text, bad) {
			t.Fatalf("the published receiver logged %q:\n%s", bad, tailString(text, 40))
		}
	}
	t.Logf("published receiver: %d status messages from the publisher on its topic, one signal handled, acted at once", statusesSeenFrom(publisher))
}

// fleetOf reads d's GET /api/peers/versions.
func fleetOf(t *testing.T, d *pushDaemon) *node.FleetReport {
	t.Helper()
	var report node.FleetReport
	if err := d.client(t).get(context.Background(), "/api/peers/versions", &report); err != nil {
		t.Fatalf("GET /api/peers/versions: %v", err)
	}
	return &report
}

func fleetRow(report *node.FleetReport, id peer.ID) *node.FleetRow {
	for i := range report.Rows {
		if report.Rows[i].PeerID == id.String() {
			return &report.Rows[i]
		}
	}
	return nil
}

func waitFleet(t *testing.T, d *pushDaemon, budget time.Duration, what string, ok func(*node.FleetReport) bool) *node.FleetReport {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last *node.FleetReport
	for time.Now().Before(deadline) {
		last = fleetOf(t, d)
		if ok(last) {
			return last
		}
		time.Sleep(2 * time.Second)
	}
	raw, _ := json.MarshalIndent(last, "", "  ")
	t.Fatalf("timed out after %s waiting for the fleet view to show %s:\n%s", budget, what, raw)
	return nil
}

// downtime is when a receiver was down for an update: its helper's first
// stop request to its health-passed, from its ledger.
func downtime(t *testing.T, root, updateID string) (time.Time, time.Time) {
	t.Helper()
	var stopped, passed time.Time
	for _, e := range readLedger(t, root) {
		if e.UpdateID != updateID {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.RecordedAt)
		if err != nil {
			t.Fatal(err)
		}
		if e.Action == "stop-requested" && stopped.IsZero() {
			stopped = at
		}
		if e.Action == "health-passed" {
			passed = at
		}
	}
	if stopped.IsZero() || passed.IsZero() {
		t.Fatalf("no stop-requested and health-passed for %s in %s: %v", updateID, root, ledgerActions(t, root))
	}
	return stopped, passed
}

func runRealRolloutFleet(t *testing.T) {
	start := time.Now()
	defer func() { t.Logf("rollout and fleet duration: %s", time.Since(start).Round(time.Second)) }()
	work := t.TempDir()
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2, v3 := builds[0], builds[1], builds[2]
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second
	window := time.Duration(envInt(t, "SDN_E2E_ROLLOUT_WINDOW_SECONDS", 480)) * time.Second
	gap := time.Duration(envInt(t, "SDN_E2E_ROLLOUT_GAP_SECONDS", 120)) * time.Second

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

	receivers := map[string]*pushDaemon{}
	ids := map[string]peer.ID{}
	// Each receiver dials the publisher and the receivers started before it,
	// as the nodes of a fleet are connected to each other.
	bootstrap := []string{aInfo.Addrs[0].String() + "/p2p/" + aInfo.ID.String()}
	for _, name := range []string{"b", "c", "d"} {
		r := newPushDaemon(t, filepath.Join(work, name), v1, feed, roots, health)
		dial := append([]string(nil), bootstrap...)
		r.configure(t, func(cfg *config.Config) {
			cfg.Network.Bootstrap = dial
			// The signed window below replaces this.
			cfg.Update.RolloutWindowSeconds = 7200
		})
		r.start(t, boot)
		receivers[name] = r
		info := pushPeer(t, r.adminBase)
		ids[name] = info.ID
		bootstrap = append(bootstrap, info.Addrs[0].String()+"/p2p/"+info.ID.String())
	}
	names := []string{"b", "c", "d"}

	// Before: the publisher sees every receiver on v1, current; a receiver
	// sees the others through the publisher (statuses relayed on the topic).
	waitFleet(t, a, 3*time.Minute, "every receiver on v1, current", func(r *node.FleetReport) bool {
		for _, name := range names {
			row := fleetRow(r, ids[name])
			if row == nil || row.Version != v1.version || row.State != update.StatusCurrent {
				return false
			}
		}
		return true
	})
	waitFleet(t, receivers["b"], 3*time.Minute, "the publisher and the other receivers on v1", func(r *node.FleetReport) bool {
		for _, id := range []peer.ID{aInfo.ID, ids["c"], ids["d"]} {
			row := fleetRow(r, id)
			if row == nil || row.Version != v1.version {
				return false
			}
		}
		return true
	})
	receivers["b"].command(t, true, "fleet-before", "update", "fleet")
	table := readFileString(filepath.Join(receivers["b"].work, "fleet-before.log"))
	for _, id := range []peer.ID{aInfo.ID, ids["c"], ids["d"]} {
		if !strings.Contains(table, peerTail(id.String())) {
			t.Fatalf("`update fleet` on b does not list %s:\n%s", id, table)
		}
	}
	if !strings.Contains(table, v1.version) || !strings.Contains(table, "current") {
		t.Fatalf("`update fleet` on b does not show v1 current:\n%s", table)
	}
	t.Logf("fleet before:\n%s", table)

	// One update id whose delays keep the receivers apart by gap, with the
	// latest late enough in the window to restart that receiver while it
	// waits. The delays are a function of the peer ids and the update id
	// only; the test picks the update id.
	type slot struct {
		name  string
		delay time.Duration
	}
	var order []slot
	updateID := ""
	for i := 0; i < 5000 && updateID == ""; i++ {
		candidate := fmt.Sprintf("cli-bundle-release-e2e-rollout-%d", i)
		order = order[:0]
		for _, name := range names {
			order = append(order, slot{name, update.RolloutDelay(ids[name].String(), candidate, window)})
		}
		sort.Slice(order, func(i, j int) bool { return order[i].delay < order[j].delay })
		if order[1].delay-order[0].delay >= gap && order[2].delay-order[1].delay >= gap && order[2].delay >= window*3/4 {
			updateID = candidate
		}
	}
	if updateID == "" {
		t.Fatal("no update id spreads the three receivers")
	}
	t.Logf("update %s: delays %s %s, %s %s, %s %s", updateID, order[0].name, order[0].delay, order[1].name, order[1].delay, order[2].name, order[2].delay)
	v2r := v2
	v2r.updateID = updateID

	sign := func(label string, windowSeconds int) {
		runSignalPipelineEnv(t, signer, work, "sign-"+label, []string{"SDN_UPDATE_SIGNING_KEY_PEM=" + string(keyPEM)},
			"--key-env", "SDN_UPDATE_SIGNING_KEY_PEM", "--roots", rootsPath, "--feed-dir", feed.dir, "--rollout-window-seconds", strconv.Itoa(windowSeconds))
	}
	feed.publish(t, v2r, key)
	sign("v2", int(window/time.Second))
	a.command(t, true, "push-v2", "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v2r.version)

	// Each receiver: pending, due at its accepted time plus its own delay.
	// Gossip gives no delivery guarantee (a node that misses a signal takes
	// the release at its next feed check): the publisher sends the same
	// signal once more when a receiver has not taken it within 20 seconds.
	pendingOn := func(r *pushDaemon) bool {
		p, err := update.ReadPending(r.paths)
		return err == nil && p != nil && p.UpdateID == updateID
	}
	resendBy := time.Now().Add(20 * time.Second)
	for time.Now().Before(resendBy) && !(pendingOn(receivers["b"]) && pendingOn(receivers["c"]) && pendingOn(receivers["d"])) {
		time.Sleep(time.Second)
	}
	for _, name := range names {
		if !pendingOn(receivers[name]) {
			t.Logf("%s has not taken the signal after 20 s; sending it again", name)
			a.command(t, true, "push-v2-again", "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v2r.version)
			break
		}
	}
	due := map[string]time.Time{}
	for _, s := range order {
		r := receivers[s.name]
		waitPush(t, time.Minute, s.name+" pending", func() bool { return pendingOn(r) })
		p, _ := update.ReadPending(r.paths)
		accepted, err := time.Parse(time.RFC3339, p.AcceptedAt)
		if err != nil {
			t.Fatal(err)
		}
		if p.WindowFrom != "signal" || p.WindowSeconds != int64(window/time.Second) || time.Duration(p.DelaySeconds)*time.Second != s.delay || !p.Due().Equal(accepted.Add(s.delay)) || p.Trigger != "signal" {
			t.Fatalf("%s's pending record: %+v; want the signed window %s, delay %s, due accepted+delay", s.name, p, window, s.delay)
		}
		due[s.name] = p.Due()
		r.command(t, true, "check-pending", "update", "check")
		check := readFileString(filepath.Join(r.work, "check-pending.log"))
		if !strings.Contains(check, "update_pending="+updateID+" ") || !strings.Contains(check, "due_at="+p.DueAt) {
			t.Fatalf("`update check` on %s does not show the pending update and its due time:\n%s", s.name, check)
		}
	}
	waitFleet(t, a, 2*time.Minute, "every receiver pending until its due time", func(r *node.FleetReport) bool {
		for _, name := range names {
			row := fleetRow(r, ids[name])
			if row == nil || row.State != update.StatusPending || row.Pending == nil || row.Pending.DueAt != due[name].UTC().Format(time.RFC3339) {
				return false
			}
		}
		return true
	})
	a.command(t, true, "fleet-pending", "update", "fleet")
	t.Logf("fleet while pending:\n%s", readFileString(filepath.Join(a.work, "fleet-pending.log")))

	// last_seen on /api/peers/sdn stays current for peers on a long-lived
	// connection (it used to stop at the time the connection was made).
	var observed []struct {
		ID       string    `json:"id"`
		LastSeen time.Time `json:"last_seen"`
	}
	if err := a.client(t).get(context.Background(), "/api/peers/sdn", &observed); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		found := false
		for _, row := range observed {
			if row.ID == ids[name].String() {
				found = true
				if age := time.Since(row.LastSeen); age > 100*time.Second {
					t.Fatalf("a's /api/peers/sdn says %s was last seen %s ago (%s), connected since its start", name, age.Round(time.Second), row.LastSeen)
				}
			}
		}
		if !found {
			t.Fatalf("a's /api/peers/sdn has no row for %s", name)
		}
	}

	// From here on, in the order of their due times (a receiver that took
	// the signal on its second sending accepted it later).
	sort.Slice(order, func(i, j int) bool { return due[order[i].name].Before(due[order[j].name]) })

	// The latest receiver restarts during its wait and keeps its due time.
	last := receivers[order[2].name]
	if time.Until(due[order[2].name]) < 2*time.Minute {
		t.Fatalf("%s is due in %s, too soon to restart it during its wait", order[2].name, time.Until(due[order[2].name]))
	}
	last.stop(t)
	last.start(t, boot)
	if p, err := update.ReadPending(last.paths); err != nil || p == nil || !p.Due().Equal(due[order[2].name]) {
		t.Fatalf("after its restart %s's pending record is %+v (%v), want due at %s", order[2].name, p, err, due[order[2].name])
	}
	if want := "Update " + updateID + " (version " + v2r.version + ") is pending from before this start; due at " + due[order[2].name].UTC().Format(time.RFC3339); !strings.Contains(last.logs(), want) {
		t.Fatalf("%s did not resume its wait after the restart (want %q):\n%s", order[2].name, want, tailString(last.logs(), 30))
	}

	for _, s := range order {
		receivers[s.name].waitUpgrade(t, v2r, time.Until(due[s.name])+boot+2*time.Duration(health)*time.Second)
	}
	type interval struct {
		name       string
		down, back time.Time
	}
	var intervals []interval
	for _, s := range order {
		down, back := downtime(t, receivers[s.name].paths.Root, updateID)
		if down.Before(due[s.name]) {
			t.Fatalf("%s stopped for the update at %s, before its due time %s", s.name, down, due[s.name])
		}
		intervals = append(intervals, interval{s.name, down, back})
		t.Logf("%s down %s to %s (due %s)", s.name, down.Format(time.RFC3339), back.Format(time.RFC3339), due[s.name].Format(time.RFC3339))
	}
	for i := 1; i < len(intervals); i++ {
		if !intervals[i].down.After(intervals[i-1].back) {
			t.Fatalf("%s was down from %s while %s was down until %s", intervals[i].name, intervals[i].down, intervals[i-1].name, intervals[i-1].back)
		}
	}

	// After: every receiver on v2, current, and it accepted the newest signal.
	waitFleet(t, a, 3*time.Minute, "every receiver on v2, current, signal accepted", func(r *node.FleetReport) bool {
		for _, name := range names {
			row := fleetRow(r, ids[name])
			if row == nil || row.Version != v2r.version || row.State != update.StatusCurrent || row.AcknowledgedLatest == nil || !*row.AcknowledgedLatest {
				return false
			}
		}
		return r.LatestSignal != nil && r.LatestSignal.UpdateID == updateID
	})
	a.command(t, true, "fleet-after", "update", "fleet")
	after := readFileString(filepath.Join(a.work, "fleet-after.log"))
	if !strings.Contains(after, v2r.version) || !strings.Contains(after, "latest_signal="+updateID) {
		t.Fatalf("`update fleet` after the rollout:\n%s", after)
	}
	t.Logf("fleet after:\n%s", after)

	// A failed update, reported by the node that failed it.
	for _, name := range []string{"c", "d"} {
		receivers[name].stop(t)
	}
	b := receivers["b"]
	feed.publish(t, v3, key)
	sign("v3", 0)
	a.command(t, true, "push-v3", "update", "signal", "--from-feed", "--channel", realE2EChannel, "--version", v3.version)
	waitPush(t, boot+3*time.Duration(health)*time.Second, "v3 rolled back on b", func() bool {
		b.trackHelper()
		return update.HasFailedUpdate(b.paths, v3.updateID) && pushIdentityMatches(b.adminBase, v2r)
	})
	if p, _ := update.ReadPending(b.paths); p != nil && p.UpdateID == v3.updateID {
		t.Fatalf("v3, signed with window 0, was recorded as pending: %+v", p)
	}
	waitFleet(t, a, 3*time.Minute, "b failed and rolled back", func(r *node.FleetReport) bool {
		row := fleetRow(r, ids["b"])
		return row != nil && row.State == update.StatusFailed && row.Failed != nil && row.Failed.UpdateID == v3.updateID && row.Failed.RolledBack && row.Version == v2r.version
	})
	a.command(t, true, "fleet-failed", "update", "fleet")
	failed := readFileString(filepath.Join(a.work, "fleet-failed.log"))
	if !strings.Contains(failed, "failed "+v3.version+", rolled back") {
		t.Fatalf("`update fleet` does not show b failed and rolled back:\n%s", failed)
	}
	t.Logf("fleet after the failure:\n%s", failed)
	b.command(t, true, "check-failed", "update", "check")
	if check := readFileString(filepath.Join(b.work, "check-failed.log")); !strings.Contains(check, "update_failed="+v3.updateID+" ") || !strings.Contains(check, "rolled_back=true") {
		t.Fatalf("`update check` on b does not report the failed update:\n%s", check)
	}
	// The publisher dials no peer; the receivers dial only loopback daemons
	// of this test (their configs pass requireIsolatedConfig at configure).
	for _, log := range a.logPaths {
		requireIsolated(t, log)
	}
}

// selfSignedAdminCert writes a certificate and key for name only (no IP
// address), as an origin certificate names only its public domain.
func selfSignedAdminCert(t *testing.T, dir, name string) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "admin-cert.pem"), filepath.Join(dir, "admin-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AddCert(leaf)
	return certPath, keyPath, pool
}

func runRealConfigFlagTLS(t *testing.T) {
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2 := builds[0], builds[1]
	health := envInt(t, "SDN_E2E_REAL_DAEMON_HEALTH_SECONDS", 180)
	boot := time.Duration(envInt(t, "SDN_E2E_REAL_DAEMON_BOOT_SECONDS", 600)) * time.Second
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
	d := newPushDaemon(t, filepath.Join(work, "node"), v1, feed, roots, health)
	const certName = "sdn-e2e.example"
	certPath, keyPath, pool := selfSignedAdminCert(t, d.work, certName)
	d.configure(t, func(cfg *config.Config) {
		cfg.Admin.TLSMode = "static"
		cfg.Admin.TLSCertFile = certPath
		cfg.Admin.TLSKeyFile = keyPath
	})
	base := strings.Replace(d.adminBase, "http://", "https://", 1)
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: certName, MinVersion: tls.VersionTLS12}}}
	// The control: the certificate names no loopback address, so system
	// roots (and SSL_CERT_FILE, the feed's) cannot reach the daemon.
	logPath := filepath.Join(d.work, "daemon-0.log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(d.exe, "daemon", "--allow-multi-daemon", "--config", d.cfg)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = d.work, d.env, out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out.Close()
	d.tracker.add(cmd.Process.Pid)
	go func() { _ = cmd.Wait() }()
	waitPush(t, boot, "the TLS daemon ready", func() bool {
		resp, err := client.Get(base + daemonReadyPath)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(base + daemonReadyPath); err == nil {
		resp.Body.Close()
		t.Fatal("the daemon's certificate verified with system roots; the test needs one that does not")
	}

	feed.publish(t, v2, key)
	installOut, installErr := runRealCLI(t, d.work, "install-config-flag", d.exe, d.cfg, d.env)
	d.tracker.addFrom(installOut)
	text := readFileString(installOut)
	if installErr != nil || !strings.Contains(text, "helper_started=") {
		t.Fatalf("`update install --config` with the daemon serving TLS (%v):\n%s", installErr, text)
	}
	waitRealHelper(t, d.tracker, installOut, boot+2*time.Duration(health)*time.Second)
	waitPush(t, boot, "v2 serving TLS", func() bool {
		id, err := probeDaemonIdentity(context.Background(), client, base)
		return err == nil && id.BundleVersion == v2.version && id.BuildSHA256 == v2.sha
	})
	if !ledgerHasIfPresent(d.paths.Root, v2.updateID, "health-passed") {
		t.Fatalf("no health-passed for v2: %v", ledgerActions(t, d.paths.Root))
	}
	if strings.Contains(text, "already open for writing") {
		t.Fatalf("the install met the store lock:\n%s", text)
	}
	requireIsolated(t, logPath)
	t.Log("update install --config through the helper with the daemon serving a certificate system roots do not trust")
}
