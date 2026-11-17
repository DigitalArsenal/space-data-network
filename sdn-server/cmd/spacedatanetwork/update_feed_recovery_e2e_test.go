package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// These cases drive installed daemons through their HTTPS feed and alert API.
func runRealFeedRecovery(t *testing.T) {
	for _, mode := range []string{"handoff", "replay", "missing-manifest", "install-alert", "candidate-bound"} {
		t.Run(mode, func(t *testing.T) { runRealFeedRecoveryCase(t, mode) })
	}
}

func runRealFeedRecoveryCase(t *testing.T, mode string) {
	work := t.TempDir()
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
	feed.publish(t, v2, key)
	indexPath := "/updates" + feed.lane() + "/index.json"
	feed.mu.Lock()
	rawIndex := append([]byte(nil), feed.files[indexPath]...)
	originalManifest := append([]byte(nil), feed.files["/updates"+feed.lane()+"/"+v2.version+"/manifest.json"]...)
	feed.mu.Unlock()
	var index update.ProviderFeed
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Updates) != 1 {
		t.Fatalf("fixture entries: %d", len(index.Updates))
	}
	original := index.Updates[0]
	empty := index
	empty.Updates = nil
	emptyRaw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var manifests, checks, installChecks atomic.Int64
	var requested sync.Map
	var indexMu sync.Mutex
	var brokenIndex, cleanIndex atomic.Bool
	if mode != "handoff" && mode != "replay" {
		count := 1
		if mode == "candidate-bound" {
			count = 12
		}
		for i := 0; i < count; i++ {
			entry := original
			entry.UpdateID = fmt.Sprintf("missing-%d", i)
			entry.Sequence += int64(i + 1)
			entry.ManifestURL = feed.base + fmt.Sprintf("/missing-%d/manifest.json", i)
			index.Updates = append(index.Updates, entry)
		}
		if mode == "install-alert" || mode == "candidate-bound" {
			index.Updates = index.Updates[1:]
		}
		rawIndex, err = json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A release between the installed build and v2. A failed hand-off of v2
	// must not move the feed check on to it.
	middle := v2
	middle.name, middle.version, middle.updateID, middle.sequence = "middle", "1.1.2-e2e.1.5", "handoff-middle", 150
	if mode == "handoff" {
		feed.publish(t, middle, key)
		feed.mu.Lock()
		rawIndex = append([]byte(nil), feed.files[indexPath]...)
		feed.mu.Unlock()
	}
	releaseIndex, releaseReplay := make(chan struct{}), make(chan struct{})
	releaseSecond := make(chan struct{})
	releaseInstallCheck := make(chan struct{})
	defer func() {
		select {
		case <-releaseInstallCheck:
		default:
			close(releaseInstallCheck)
		}
	}()
	defer func() {
		select {
		case <-releaseSecond:
		default:
			close(releaseSecond)
		}
	}()
	replayEntered := make(chan struct{}, 1)
	defer func() {
		select {
		case <-releaseIndex:
		default:
			close(releaseIndex)
		}
		select {
		case <-releaseReplay:
		default:
			close(releaseReplay)
		}
	}()
	feed.requestHook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/index.json") {
			checkNumber := checks.Add(1)
			if mode == "candidate-bound" && checkNumber >= 2 {
				if checkNumber == 2 {
					select {
					case <-releaseSecond:
					case <-r.Context().Done():
					}
				} else {
					<-r.Context().Done()
					return true
				}
			}
			if mode == "replay" {
				select {
				case <-releaseIndex:
				case <-r.Context().Done():
				}
				http.Error(w, "held index failed", 500)
				return true
			}
			if brokenIndex.Load() {
				http.Error(w, "index unavailable", http.StatusServiceUnavailable)
				return true
			}
			if mode == "install-alert" && !cleanIndex.Load() && installChecks.Add(1) >= 4 {
				select {
				case <-releaseInstallCheck:
				case <-r.Context().Done():
				}
			}
			if cleanIndex.Load() {
				_, _ = w.Write(emptyRaw)
				return true
			}
			indexMu.Lock()
			defer indexMu.Unlock()
			_, _ = w.Write(rawIndex)
			return true
		}
		if strings.Contains(r.URL.Path, "/replay-failure/") {
			select {
			case replayEntered <- struct{}{}:
			default:
			}
			select {
			case <-releaseReplay:
			case <-r.Context().Done():
			}
			http.NotFound(w, r)
			return true
		}
		if strings.Contains(r.URL.Path, "/missing-") {
			manifests.Add(1)
			requested.Store(r.URL.Path, true)
			if mode == "candidate-bound" {
				_, _ = w.Write(originalManifest)
				return true
			}
			http.NotFound(w, r)
			return true
		}
		return false
	}
	d := newPushDaemon(t, filepath.Join(work, "receiver"), v1, feed, roots, health)
	d.configure(t, func(cfg *config.Config) {
		cfg.Update.CheckDelaySeconds = 2
		cfg.Update.CheckIntervalSeconds = 3
		if mode == "replay" {
			cfg.Update.CheckIntervalSeconds = 3600
		}
		if mode == "candidate-bound" {
			cfg.Update.CheckIntervalSeconds = 15
		}
		if mode == "replay" {
			cfg.Update.MinIntervalSeconds = 10
		}
		if mode == "handoff" {
			// The first retry after a failed hand-off waits one minimum interval.
			cfg.Update.MinIntervalSeconds = 20
		}
	})
	// One signed-in client for the whole case: the admin API limits sign-in
	// challenges, and the hand-off case polls alerts for several minutes.
	var admin *adminClient
	alerts := func() []ops.Alert {
		var status struct {
			Alerts []ops.Alert `json:"alerts"`
		}
		if admin == nil {
			admin = d.client(t)
		}
		if err := admin.get(context.Background(), "/api/v1/status/alerts", &status); err != nil {
			t.Fatalf("read active alerts: %v", err)
		}
		return status.Alerts
	}
	hasAlert := func(kind, reason string) bool {
		for _, alert := range alerts() {
			if alert.Kind == kind && strings.Contains(alert.LastError, reason) {
				return true
			}
		}
		return false
	}
	if mode == "handoff" {
		if err := os.MkdirAll(d.paths.Incoming, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d.paths.Incoming, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(d.paths.Incoming, 0o700)
	}
	if mode == "install-alert" || mode == "handoff" {
		brokenIndex.Store(true)
	}
	d.start(t, boot)
	switch mode {
	case "replay":
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
		sub, err := topic.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Cancel()
		if err := sender.Connect(ctx, pushPeer(t, d.adminBase)); err != nil {
			t.Fatal(err)
		}
		waitPush(t, 30*time.Second, "topic peer", func() bool { return len(topic.ListPeers()) > 0 })
		announce := func(bad bool) {
			signal := update.SignalFromFeedEntry(original, feed.base, time.Now(), false)
			if bad {
				signal.UpdateID = "replay-failure"
				signal.Sequence--
				signal.ManifestURL = feed.base + "/replay-failure/manifest.json"
			}
			raw, err := signal.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if err := topic.Publish(ctx, resignPushSignal(t, raw, key, 0)); err != nil {
				t.Fatal(err)
			}
		}
		announce(true)
		waitPush(t, 30*time.Second, "first announcement remembered", func() bool { return strings.Contains(d.logs(), "Update signal replay-failure remembered") })
		close(releaseIndex)
		select {
		case <-replayEntered:
		case <-time.After(30 * time.Second):
			t.Fatal("replay did not fetch its manifest")
		}
		announce(false)
		waitPush(t, 30*time.Second, "second announcement remembered during replay", func() bool { return strings.Contains(d.logs(), "Update signal "+v2.updateID+" remembered") })
		close(releaseReplay)
		waitPush(t, 45*time.Second, "announcement survives failed replay and interval", func() bool { return strings.Contains(d.logs(), "Update signal accepted: "+v2.updateID) })
		d.waitUpgrade(t, v2, boot+2*time.Duration(health)*time.Second)
		t.Log("signal received during failed replay survives the interval floor and installs without another publication")
	case "handoff":
		waitPush(t, 30*time.Second, "index outage before helper failure", func() bool { return hasAlert("update_feed_unavailable", "") })
		brokenIndex.Store(false)
		waitPush(t, boot, "helper preparation refusal", func() bool { return strings.Contains(readFileString(d.helperLog()), "permission denied") })
		requireRealIdentity(t, d.adminBase, v1)
		if !hasAlert("update_feed_unavailable", "") {
			t.Fatal("failed handoff cleared the index outage warning")
		}
		bound := time.Duration(health)*time.Second + update.DefaultStopGrace + update.DefaultStopEscalated + update.DefaultStopKilled
		waitPush(t, bound+30*time.Second, "failed helper handoff alert", func() bool { return hasAlert("update_failed", "helper did not take over") })
		for _, alert := range alerts() {
			if alert.Kind == ops.KindUpdateFailed && (alert.Subject != v2.updateID || !strings.Contains(alert.LastError, v2.updateID)) {
				t.Fatal("handoff alert does not identify the failed update")
			}
		}
		if !hasAlert("update_failed", "nothing was swapped") {
			t.Fatal("handoff alert does not say that nothing was swapped")
		}
		if update.HasFailedUpdate(d.paths, v2.updateID) || update.HasFailedUpdate(d.paths, middle.updateID) {
			t.Fatal("a hand-off that swapped nothing quarantined a release")
		}
		if err := os.Chmod(d.paths.Incoming, 0o700); err != nil {
			t.Fatal(err)
		}
		// The same release installs after the backoff, with no operator step,
		// and the release below it is never attempted.
		d.waitUpgrade(t, v2, boot+2*time.Duration(health)*time.Second)
		var failedAt time.Time
		retried := false
		for _, entry := range readLedger(t, d.paths.Root) {
			if entry.UpdateID == middle.updateID {
				t.Fatalf("the feed check moved on to the older release: %+v", entry)
			}
			recorded, err := time.Parse(time.RFC3339, entry.RecordedAt)
			if err != nil {
				t.Fatalf("ledger time %q: %v", entry.RecordedAt, err)
			}
			switch {
			case entry.Action == "handoff-failed":
				if entry.UpdateID != v2.updateID || !failedAt.IsZero() {
					t.Fatalf("unexpected hand-off failure record: %+v", entry)
				}
				failedAt = recorded
			case !failedAt.IsZero() && !retried && entry.UpdateID == v2.updateID:
				retried = true
				// Ledger times are whole seconds.
				if wait := recorded.Sub(failedAt); wait < 19*time.Second {
					t.Fatalf("retry started %s after the failed hand-off, before the 20s backoff", wait)
				}
			}
		}
		if failedAt.IsZero() || !retried {
			t.Fatalf("the ledger shows no failed hand-off followed by a retry: %v", ledgerActions(t, d.paths.Root))
		}
		t.Logf("helper preparation failed; hold released after %s; the same release installed after the backoff without an operator", bound)
	case "missing-manifest":
		waitPush(t, 45*time.Second, "fallback after missing manifest", func() bool { return strings.Contains(d.logs(), "Update signal accepted: "+v2.updateID) })
		d.waitUpgrade(t, v2, boot+2*time.Duration(health)*time.Second)
		if hasAlert("update_feed_unavailable", "") {
			t.Fatal("entry failure counted as index outage")
		}
		t.Log("newest manifest returned 404; older candidate installed without an outage alert")
	case "install-alert":
		waitPush(t, 30*time.Second, "index outage", func() bool { return hasAlert("update_feed_unavailable", "") })
		brokenIndex.Store(false)
		waitPush(t, 30*time.Second, "repeated install failure alert", func() bool {
			active := hasAlert("update_failed", "404")
			if active && installChecks.Load() < 3 {
				t.Fatal("install warning appeared before three failed checks")
			}
			return active
		})
		for _, alert := range alerts() {
			if alert.Kind == ops.KindUpdateFailed && (alert.Severity != ops.SeverityWarning || !strings.Contains(alert.LastError, "missing-0")) {
				t.Fatal("install warning lost its severity or last candidate reason")
			}
		}
		if strings.Count(d.logs(), "Update feed-install missing-0 failed:") != 1 {
			t.Fatal("entry failure was not reported once")
		}
		if !hasAlert("update_feed_unavailable", "") {
			t.Fatal("install failure cleared the feed outage")
		}
		cleanIndex.Store(true)
		close(releaseInstallCheck)
		waitPush(t, 30*time.Second, "clean empty check clears both warnings", func() bool { return !hasAlert("update_failed", "") && !hasAlert("update_feed_unavailable", "") })
		t.Log("three failed installs raise warning with last reason; empty successful check clears both warnings")
	case "candidate-bound":
		waitPush(t, 40*time.Second, "second index request after first bounded check", func() bool { return checks.Load() >= 2 })
		if got := manifests.Load(); got != 3 {
			t.Fatalf("one feed check fetched %d failing manifests, want 3", got)
		}
		close(releaseSecond)
		waitPush(t, 40*time.Second, "third index request after deferred candidates", func() bool { return checks.Load() >= 3 })
		unique := 0
		requested.Range(func(_, _ any) bool { unique++; return true })
		if unique != 6 || manifests.Load() != 6 {
			t.Fatalf("two bounded checks: %d unique, %d total requests", unique, manifests.Load())
		}
		t.Log("each check attempted three of twelve failing candidates; the following check tried the deferred candidates")
	}
	d.stop(t)
}
