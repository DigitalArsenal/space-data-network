package main

// THE UPDATE LANE UNDER SIGNALS THAT ARRIVE CLOSE TOGETHER, ON REAL DAEMONS.
// A subtest of TestUpdateChannelRealDaemonE2E, gated and run like it (CI group
// `rollout`). An in-test libp2p host sends every signal to the receiver r
// (update.rollout_window_seconds 120 from step 3 on).
//
//	rollout-races  1. UI packages: the signal for package N, whose carrier
//	                  the feed serves 15 s late, then the one for N+1: r
//	                  ends on N+1 and serves it.
//	               2. A release r waits for (signed window 600 s; v6, which
//	                  never starts its update lane) is typed in by hand,
//	                  fails its health gate and is rolled back and
//	                  quarantined: after its restart r reports no
//	                  pending update and keeps no pending record.
//	               3. While r's feed check waits for release N+1, the signal
//	                  of the older release N (window 0, not in the feed
//	                  index) arrives; N+1's carrier then fails to download:
//	                  r does not install N.
//	               4. r6 runs v16, whose update lane's wall clock moves back
//	                  30 minutes as its rollout wait starts: r6 still
//	                  installs within the window (60 s) of the wait's start.

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
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
)

// e2eUIPackage is a signed UI package on the test feed and its signal.
type e2eUIPackage struct {
	updateID, version string
	sequence          int64
	carrierPath       string
	signal            []byte
}

// nodeEnvelopeRecipient is d's sealed-transport key, as its /api/node/info
// advertises it (what `update seal` reads).
func nodeEnvelopeRecipient(t *testing.T, d *pushDaemon) update.EnvelopeRecipient {
	t.Helper()
	response, err := http.Get(d.adminBase + "/api/node/info")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var info struct {
		Sealed *struct {
			EncryptionKey string `json:"encryption_key"`
			Fingerprint   string `json:"fingerprint"`
		} `json:"sealed_transport"`
	}
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil || info.Sealed == nil {
		t.Fatalf("/api/node/info of %s has no sealed transport key (%v)", d.work, err)
	}
	pub, err := hex.DecodeString(info.Sealed.EncryptionKey)
	if err != nil || walletderive.Fingerprint(pub) != info.Sealed.Fingerprint {
		t.Fatalf("/api/node/info of %s: encryption key %q does not match fingerprint %q", d.work, info.Sealed.EncryptionKey, info.Sealed.Fingerprint)
	}
	return update.EnvelopeRecipient{KeyID: []byte(info.Sealed.Fingerprint), PublicKey: pub, KeyExchange: ecies.Secp256k1}
}

// publishUIPackage puts a UI package of sequence for build on the feed,
// sealed for recipient and signed by key, the way publish-ui-update.mjs, `update
// seal` and `update sign-manifest` make one, and returns its signal.
func publishUIPackage(t *testing.T, f *realE2EFeed, key ed25519.PrivateKey, sequence int64, build string, recipient update.EnvelopeRecipient) e2eUIPackage {
	t.Helper()
	version := fmt.Sprintf("ui.%d.e2e", sequence)
	files := map[string][]byte{
		"ui/dashboard.html":   []byte("<!doctype html><title>dashboard " + version + "</title>"),
		"ui/dashboard.csp":    []byte("default-src 'self'"),
		"ui/homepage.html":    []byte("<!doctype html><title>homepage " + version + "</title>"),
		"ui/homepage.csp":     []byte("default-src 'self'"),
		"ui/media/e2e-0a.mp4": []byte("video " + version),
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var artifacts, modules []map[string]string
	for _, name := range names {
		sum := e2eSHA256(files[name])
		artifacts = append(artifacts, map[string]string{"path": name, "sha256": sum})
		modules = append(modules, map[string]string{"id": strings.TrimPrefix(name, "ui/"), "hash": sum, "path": name})
	}
	bundleManifest, err := json.Marshal(map[string]any{"schema": "org.spacedatanetwork.bundle.v1", "version": version, "channel": realE2EChannel, "artifacts": artifacts})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	put := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: "ui-bundle/" + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	put("manifest.json", bundleManifest)
	for _, name := range names {
		put(name, files[name])
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	bundle := archive.Bytes()

	pub := key.Public().(ed25519.PublicKey)
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	updateID := "sdn-ui-bundle-" + version
	unsigned, err := json.Marshal(map[string]any{
		"schema": update.ManifestSchema, "update_id": updateID, "version": version, "sequence": sequence, "channel": realE2EChannel,
		"created_at": now.Add(-time.Hour).Format(time.RFC3339), "expires_at": now.Add(24 * time.Hour).Format(time.RFC3339),
		"target": map[string]any{"platform": update.TargetAny, "arch": update.TargetAny, "kind": update.TargetKindUIBundle},
		"bundle": map[string]any{"hash": e2eSHA256(bundle), "size": len(bundle), "format": "tar.gz"},
		"wasm":   map[string]any{"hash": strings.Repeat("0", 64)},
		"signing": map[string]any{
			"key_id": update.RootKeyID(pub), "algorithm": "Ed25519", "public_key": base64.StdEncoding.EncodeToString(spki),
			"statement_domain": sigdomain.DomainUpdateManifestV1,
		},
		"modules":       modules,
		"compatibility": map[string]any{"bundle_versions": []string{build}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(unsigned))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	carrier, err := update.SealPayload(doc, bundle, []update.EnvelopeRecipient{recipient})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := update.CanonicalManifestBytes(sealed)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	statement, err := sigdomain.Statement(sigdomain.DomainUpdateManifestV1, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, statement))
	manifest, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dir := "/updates/ui-bundle/" + realE2EChannel + "/any/any/" + version
	f.mu.Lock()
	f.files[dir+"/manifest.json"] = manifest
	f.files[dir+"/update.wasm"] = carrier
	f.mu.Unlock()

	signal := &update.Signal{
		Schema: update.SignalSchema, UpdateID: updateID, Version: version, Sequence: sequence, Channel: realE2EChannel,
		Target:      update.ManifestTarget{Platform: update.TargetAny, Arch: update.TargetAny, Kind: update.TargetKindUIBundle},
		FeedBaseURL: f.base, ManifestURL: f.srv.URL + dir + "/manifest.json", CarrierURL: f.srv.URL + dir + "/update.wasm",
		BundleHash: e2eSHA256(bundle), BundleSize: int64(len(bundle)), WasmHash: e2eSHA256(carrier), WasmSize: int64(len(carrier)),
		PublishedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
		Signing: update.SignalSigning{Algorithm: update.SignalSignatureAlgorithm, StatementDomain: sigdomain.DomainUpdateSignalV1},
	}
	raw, err := signal.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return e2eUIPackage{updateID: updateID, version: version, sequence: sequence, carrierPath: dir + "/update.wasm", signal: resignPushSignal(t, raw, key, 0)}
}

// servedUILabel is the X-SDN-UI header of d's homepage: the UI package version
// it serves, or "embedded".
func servedUILabel(d *pushDaemon) string {
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(d.adminBase + "/home")
	if err != nil {
		return ""
	}
	response.Body.Close()
	return response.Header.Get("X-SDN-UI")
}

// realE2EClockBackBuild is v16: a build whose update lane's wall clock moves
// back 30 minutes as its first rollout wait starts (sdn_e2e_clock_back).
func realE2EClockBackBuild(t *testing.T, dir string) realBuild {
	t.Helper()
	b := realBuild{name: "v16", version: "1.1.2-e2e.16", updateID: "cli-bundle-release-e2e-16", sequence: 160}
	if prebuilt := strings.TrimSpace(os.Getenv(realE2EBinDirEnv)); prebuilt != "" {
		b.path = filepath.Join(prebuilt, "spacedatanetwork-"+b.name)
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b.path = filepath.Join(dir, "spacedatanetwork-"+b.name)
		moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		build := exec.Command(filepath.Join(moduleRoot, "..", "scripts", "go-with-wasmedge.sh"), "build", "-o", b.path, "-tags", "sdn_e2e_clock_back", "./cmd/spacedatanetwork")
		build.Dir = moduleRoot
		build.Env = append(environWithout("SDN_GO_LDFLAGS"), "SDN_GO_LDFLAGS=-X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=e2e-"+b.name)
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b.name, err, output)
		}
	}
	if b.sha = e2eFileSHA256(b.path); b.sha == "" {
		t.Fatalf("no %s build at %s", b.name, b.path)
	}
	return b
}

func runRealRolloutRaces(t *testing.T) {
	start := time.Now()
	defer func() { t.Logf("rollout races duration: %s", time.Since(start).Round(time.Second)) }()
	work := t.TempDir()
	keepRealLogs(t, work)
	builds := realE2EBuilds(t, filepath.Join(work, "builds"))
	v1, v2 := builds[0], builds[1]
	// v6 never opens its store, so it never starts its update lane either.
	v6, _, _, _, _ := realE2EHangBuilds(t, filepath.Join(work, "builds"))
	v16 := realE2EClockBackBuild(t, filepath.Join(work, "builds"))
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
	sign := func(label string, window int) {
		runSignalPipelineEnv(t, signer, work, "sign-"+label, []string{"SDN_UPDATE_SIGNING_KEY_PEM=" + string(keyPEM)},
			"--key-env", "SDN_UPDATE_SIGNING_KEY_PEM", "--roots", rootsPath, "--feed-dir", feed.dir, "--rollout-window-seconds", strconv.Itoa(window))
	}
	signalOf := func(b realBuild) []byte {
		raw, err := os.ReadFile(filepath.Join(feed.dir, strings.TrimPrefix(feed.lane(), "/"), b.version, "signal.json"))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := newPushDaemon(t, filepath.Join(work, "r"), v1, feed, roots, health)
	r.start(t, boot)
	rID := pushPeer(t, r.adminBase).ID
	sender := newE2ETopicHost(t, ctx, "e2e-sender/1.0")
	sender.connect(t, ctx, r)
	send := func(raw []byte) {
		t.Helper()
		if err := sender.topic.Publish(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	// An update id whose delay for id within w is in [lo, hi].
	pick := func(id, prefix string, w, lo, hi time.Duration) string {
		for i := 0; i < 20000; i++ {
			candidate := fmt.Sprintf("cli-bundle-release-e2e-%s-%d", prefix, i)
			if d := update.RolloutDelay(id, candidate, w); d >= lo && d <= hi {
				return candidate
			}
		}
		t.Fatalf("no %s update id with a delay in [%s, %s]", prefix, lo, hi)
		return ""
	}

	// 1. UI packages N and N+1, N's carrier served 15 s late: r ends on N+1.
	recipient := nodeEnvelopeRecipient(t, r)
	uiN := publishUIPackage(t, feed, key, 7000, v1.version, recipient)
	uiN1 := publishUIPackage(t, feed, key, 7001, v1.version, recipient)
	var slowServed atomic.Int64
	feed.mu.Lock()
	feed.requestHook = func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == uiN.carrierPath {
			time.Sleep(15 * time.Second)
			slowServed.Add(1)
		}
		return false
	}
	feed.mu.Unlock()
	send(uiN.signal)
	time.Sleep(2 * time.Second)
	send(uiN1.signal)
	waitPush(t, 90*time.Second, "N's late carrier served and both UI signals handled", func() bool {
		return slowServed.Load() > 0 && ledgerHasIfPresent(r.paths.Root, uiN1.updateID, "ui-install")
	})
	time.Sleep(10 * time.Second)
	feed.mu.Lock()
	feed.requestHook = nil
	feed.mu.Unlock()
	active, err := update.ActiveUIPackage(r.paths)
	if err != nil || active == nil || active.UpdateID != uiN1.updateID {
		t.Errorf("after UI package signals N then N+1 (N's carrier late), r's installed UI package is %+v (%v), want %s; ledger %v", active, err, uiN1.updateID, ledgerActions(t, r.paths.Root))
	}
	if label := servedUILabel(r); label != uiN1.version {
		t.Errorf("r serves UI %q, want %s", label, uiN1.version)
	}
	if raw, err := os.ReadFile(update.UICurrentPath(r.paths)); err != nil || !json.Valid(raw) {
		t.Errorf("updates/ui/current.json is not one whole JSON document (%v): %q", err, raw)
	}
	if active != nil {
		t.Logf("UI packages N (carrier 15 s late) and N+1: r installed %s and serves %s", active.Version, servedUILabel(r))
	}

	// 2. A release r waits for, typed in by hand, fails its health gate and
	// is rolled back: after the restart no pending update.
	bad := v6
	bad.sequence = 300
	bad.updateID = pick(rID.String(), "bad", 600*time.Second, 300*time.Second, 590*time.Second)
	feed.publish(t, bad, key)
	sign("bad", 600)
	send(signalOf(bad))
	waitPush(t, time.Minute, "r pending the bad release", func() bool {
		p, err := update.ReadPending(r.paths)
		return err == nil && p != nil && p.UpdateID == bad.updateID
	})
	out, err := runRealCLI(t, r.work, "bad", r.exe, r.cfg, r.env)
	t.Logf("typed `update install` of the bad release: %v\n%s", err, tailString(readFileString(out), 20))
	waitPush(t, boot+4*time.Duration(health)*time.Second, "the bad release rolled back and quarantined", func() bool {
		r.trackHelper()
		r.tracker.addFrom(out)
		return update.HasFailedUpdate(r.paths, bad.updateID) && pushIdentityMatches(r.adminBase, v1)
	})
	time.Sleep(10 * time.Second)
	if p, err := update.ReadPending(r.paths); err != nil || p != nil {
		t.Errorf("after the bad release was rolled back and quarantined, r keeps the pending record %+v (%v)", p, err)
	}
	r.command(t, true, "check-after-rollback", "update", "check")
	if check := readFileString(filepath.Join(r.work, "check-after-rollback.log")); !strings.Contains(check, "update_pending=none") {
		t.Errorf("`update check` on r after the rollback reports a pending update:\n%s", check)
	}
	t.Log("the release r waited for, typed in by hand, rolled back and quarantined: no pending update after the restart")

	// 3. During the feed check's wait for N+1, the signal of the older N
	// arrives; N+1's carrier then fails: r does not install N.
	r.stop(t)
	const window = 120 * time.Second
	older := v2
	older.name, older.version, older.sequence = "older", "1.1.2-e2e.24", 400
	older.updateID = pick(rID.String(), "older", window, 0, window)
	newer := v2
	newer.name, newer.version, newer.sequence = "newer", "1.1.2-e2e.25", 500
	newer.updateID = pick(rID.String(), "newer", window, 50*time.Second, 90*time.Second)
	feed.publish(t, older, key)
	feed.publish(t, newer, key)
	sign("older-newer", 0)
	// The older release is not in the index (its manifest and carrier stay
	// on the feed), so the feed check never offers it: only its signal can
	// install it. (A feed check whose newest candidate fails goes on to the
	// next one it lists, by design.)
	feed.withdrawIndexEntry(t, older.updateID)
	r.configure(t, func(cfg *config.Config) {
		cfg.Update.RolloutWindowSeconds = int(window / time.Second)
		cfg.Update.CheckDelaySeconds = 3
		cfg.Update.CheckIntervalSeconds = 3600
	})
	r.start(t, boot)
	var pNewer *update.Pending
	waitPush(t, time.Minute, "r's feed check pending the newer release", func() bool {
		p, err := update.ReadPending(r.paths)
		if err == nil && p != nil && p.UpdateID == newer.updateID && p.Trigger == "feed-check" {
			pNewer = p
		}
		return pNewer != nil
	})
	var newerCarrier atomic.Int64
	newerPath := "/updates" + feed.lane() + "/" + newer.version + "/update.wasm"
	feed.mu.Lock()
	feed.requestHook = func(w http.ResponseWriter, req *http.Request) bool {
		if req.URL.Path == newerPath {
			newerCarrier.Add(1)
			http.Error(w, "test feed: carrier unavailable", http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	feed.mu.Unlock()
	sender.connect(t, ctx, r)
	send(signalOf(older))
	waitPush(t, time.Until(pNewer.Due())+time.Minute, "r's attempt at the newer release", func() bool { return newerCarrier.Load() > 0 })
	time.Sleep(45 * time.Second)
	r.trackHelper()
	if ledgerHasIfPresent(r.paths.Root, older.updateID, "stop-requested") || !pushIdentityMatches(r.adminBase, v1) {
		t.Errorf("after the newer release's attempt failed, r installed the older release whose signal arrived during the wait: %v", ledgerActions(t, r.paths.Root))
	}
	feed.mu.Lock()
	feed.requestHook = nil
	feed.mu.Unlock()
	t.Logf("the older release's signal during the feed check's wait for the newer one: not installed after the newer one's attempt failed (%d carrier requests)", newerCarrier.Load())
	r.stop(t)

	// 4. The wall clock moves back 30 minutes as the wait starts: r6 still
	// installs within the window of the wait's start.
	r6 := newPushDaemon(t, filepath.Join(work, "r6"), v16, feed, roots, health)
	r6.start(t, boot)
	r6ID := pushPeer(t, r6.adminBase).ID
	clock := v2
	clock.name, clock.version, clock.sequence = "clock", "1.1.2-e2e.26", 600
	clock.updateID = pick(r6ID.String(), "clock", time.Minute, 20*time.Second, 40*time.Second)
	feed.publish(t, clock, key)
	sign("clock", 60)
	sender.connect(t, ctx, r6)
	send(signalOf(clock))
	var p6 *update.Pending
	waitPush(t, 30*time.Second, "r6 pending", func() bool {
		p, err := update.ReadPending(r6.paths)
		if err == nil && p != nil && p.UpdateID == clock.updateID {
			p6 = p
		}
		return p6 != nil
	})
	accepted, err := time.Parse(time.RFC3339, p6.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	// The install starts by the end of the window measured from the wait's
	// start; without the bound it would start 30 minutes later.
	waitPush(t, time.Until(accepted.Add(90*time.Second)), "r6 starting its install within the window", func() bool {
		r6.trackHelper()
		return ledgerHasIfPresent(r6.paths.Root, clock.updateID, "stop-requested")
	})
	r6.waitUpgrade(t, clock, boot+2*time.Duration(health)*time.Second)
	if !strings.Contains(r6.logs(), "lane clock moved back 30 minutes") || !strings.Contains(r6.logs(), "the wall clock moved back during its rollout wait") {
		t.Errorf("r6 does not show the clock step and the bounded wait:\n%s", tailString(r6.logs(), 30))
	}
	at := installStartedAt(t, r6.logs(), clock.updateID, p6.Due())
	if at.After(accepted.Add(time.Minute + 15*time.Second)) {
		t.Errorf("r6 started its install at %s, more than the window (60 s) after it accepted the update at %s", at, accepted)
	}
	t.Logf("v16, its clock moved back 30 minutes during the wait: installed at %s, %s after it accepted the update", at.Format(time.RFC3339), at.Sub(accepted).Round(time.Second))
	// r and r6 dial no peer.
	for _, log := range append(append([]string(nil), r.logPaths...), r6.logPaths...) {
		requireIsolated(t, log)
	}
}
