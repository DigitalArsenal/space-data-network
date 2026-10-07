package node

// A UI package reaches a running node and becomes what it serves, with no
// helper and no restart (owner 2026-10-07: "package the updates and send them
// through the update channel, digitally signed and encrypted, and have the
// running servers update in situ WITHOUT needing to republish the binaries").

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
)

const uiTestBuild = "1.0.6-updatelane.uitest"

// uiNode is a node's sealed-transport key, as its /api/node/info advertises it.
type uiNode struct {
	priv        []byte
	fingerprint string
	recipient   update.EnvelopeRecipient
}

func newUINode(t *testing.T) uiNode {
	t.Helper()
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pub := key.PubKey().SerializeCompressed()
	fp := walletderive.Fingerprint(pub)
	return uiNode{
		priv:        key.Serialize(),
		fingerprint: fp,
		recipient:   update.EnvelopeRecipient{KeyID: []byte(fp), PublicKey: pub, KeyExchange: ecies.Secp256k1},
	}
}

type uiPackage struct {
	updateID  string
	version   string
	sequence  int64
	manifest  []byte
	carrier   []byte
	bundle    []byte
	dashboard []byte
}

func hexSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// buildUIPackage makes a signed UI package sealed for recipients, the way
// publish-ui-update.mjs and `update seal` make one.
func buildUIPackage(t *testing.T, h *signalHarness, sequence int64, builds []string, recipients ...update.EnvelopeRecipient) uiPackage {
	t.Helper()
	version := fmt.Sprintf("ui.%d.test", sequence)
	files := map[string][]byte{
		"ui/dashboard.html":           []byte("<!doctype html><title>dashboard " + version + "</title>"),
		"ui/dashboard.csp":            []byte("default-src 'self'"),
		"ui/homepage.html":            []byte("<!doctype html><title>homepage " + version + "</title>"),
		"ui/homepage.csp":             []byte("default-src 'self'"),
		"ui/media/explainer-0a1b.mp4": []byte("video " + version),
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var artifacts, modules []map[string]string
	for _, name := range names {
		sum := hexSum(files[name])
		artifacts = append(artifacts, map[string]string{"path": name, "sha256": sum})
		modules = append(modules, map[string]string{"id": strings.TrimPrefix(name, "ui/"), "hash": sum, "path": name})
	}
	bundleManifest, err := json.Marshal(map[string]any{
		"schema": "org.spacedatanetwork.bundle.v1", "version": version, "channel": "beta", "artifacts": artifacts,
	})
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

	now := time.Now().UTC()
	unsigned, err := json.Marshal(map[string]any{
		"schema":     update.ManifestSchema,
		"update_id":  "sdn-ui-bundle-" + version,
		"version":    version,
		"sequence":   sequence,
		"channel":    "beta",
		"created_at": now.Format(time.RFC3339),
		"expires_at": now.Add(24 * time.Hour).Format(time.RFC3339),
		"target":     map[string]any{"platform": update.TargetAny, "arch": update.TargetAny, "kind": update.TargetKindUIBundle},
		"bundle":     map[string]any{"hash": hexSum(bundle), "size": len(bundle), "format": "tar.gz"},
		"wasm":       map[string]any{"hash": strings.Repeat("0", 64)},
		"signing": map[string]any{
			"key_id": h.keyID, "algorithm": "Ed25519", "statement_domain": sigdomain.DomainUpdateManifestV1,
		},
		"modules":       modules,
		"compatibility": map[string]any{"bundle_versions": builds},
	})
	if err != nil {
		t.Fatal(err)
	}
	// As `update seal` reads it: a generic document, numbers kept exact.
	decoder := json.NewDecoder(bytes.NewReader(unsigned))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	carrier, err := update.SealPayload(doc, bundle, recipients)
	if err != nil {
		t.Fatalf("seal: %v", err)
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
	doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(h.priv, statement))
	signed, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return uiPackage{
		updateID: "sdn-ui-bundle-" + version, version: version, sequence: sequence,
		manifest: signed, carrier: carrier, bundle: bundle, dashboard: files["ui/dashboard.html"],
	}
}

// uiFeed serves one package the way the public feed does.
type uiFeed struct {
	srv            *httptest.Server
	pkg            atomic.Pointer[uiPackage]
	tamper         atomic.Bool
	carrierFetches atomic.Int64
}

func newUIFeed(t *testing.T) *uiFeed {
	t.Helper()
	f := &uiFeed{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pkg := f.pkg.Load()
		switch r.URL.Path {
		case "/manifest.json":
			_, _ = w.Write(pkg.manifest)
		case "/update.wasm":
			f.carrierFetches.Add(1)
			body := append([]byte(nil), pkg.carrier...)
			if f.tamper.Load() {
				body[len(body)-1] ^= 0xff
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// signal is the pointer the publisher pushes for pkg.
func (f *uiFeed) signal(t *testing.T, h *signalHarness, pkg uiPackage) []byte {
	t.Helper()
	f.pkg.Store(&pkg)
	return h.sign(t, func(s *update.Signal) {
		s.UpdateID = pkg.updateID
		s.Version = pkg.version
		s.Sequence = pkg.sequence
		s.Target = update.ManifestTarget{Platform: update.TargetAny, Arch: update.TargetAny, Kind: update.TargetKindUIBundle}
		s.FeedBaseURL = f.srv.URL
		s.ManifestURL = f.srv.URL + "/manifest.json"
		s.CarrierURL = f.srv.URL + "/update.wasm"
		s.BundleHash = hexSum(pkg.bundle)
		s.BundleSize = int64(len(pkg.bundle))
		s.WasmHash = hexSum(pkg.carrier)
		s.WasmSize = int64(len(pkg.carrier))
	})
}

// uiSubscriber is a running node on build uiTestBuild holding key self.
func uiSubscriber(t *testing.T, h *signalHarness, f *uiFeed, self uiNode, launches *atomic.Int64) *UpdateSignalSubscriber {
	t.Helper()
	sub, err := NewUpdateSignalSubscriber(UpdateSignalSubscriberDeps{
		Subscriber:    fakeSubscriber{sub: &fakeSubscription{}},
		Topic:         "/sdn/updates/v1/beta",
		Paths:         h.paths,
		TrustedRoots:  h.roots,
		Channel:       "beta",
		Kind:          "cli-bundle",
		AdminURL:      "https://127.0.0.1:5001/",
		HealthTimeout: time.Minute,
		MinInterval:   time.Hour,
		Client:        f.srv.Client(),
		Launch: func(update.Paths, update.SelfUpgradeOptions) (*update.SelfUpgradeLaunch, error) {
			launches.Add(1)
			return &update.SelfUpgradeLaunch{Mode: "test"}, nil
		},
		BundleVersion: uiTestBuild,
		EnvelopeKey: func() (*update.EnvelopeKey, error) {
			return &update.EnvelopeKey{KeyID: []byte(self.fingerprint), Private: self.priv}, nil
		},
	})
	if err != nil {
		t.Fatalf("construct subscriber: %v", err)
	}
	return sub
}

func servedDashboard(t *testing.T, paths update.Paths) (*update.UIPackage, []byte) {
	t.Helper()
	pkg, err := update.ActiveUIPackage(paths)
	if err != nil {
		t.Fatalf("active UI package: %v", err)
	}
	if pkg == nil {
		return nil, nil
	}
	data, err := pkg.ReadFile(paths, "dashboard.html")
	if err != nil {
		t.Fatalf("read the installed dashboard: %v", err)
	}
	return pkg, data
}

func TestUIPackageInstallsWhileTheNodeServes(t *testing.T) {
	h := newSignalHarness(t)
	f := newUIFeed(t)
	self, other := newUINode(t), newUINode(t)
	var launches atomic.Int64
	sub := uiSubscriber(t, h, f, self, &launches)
	ctx := context.Background()

	first := buildUIPackage(t, h, 2000, []string{uiTestBuild}, self.recipient, other.recipient)
	sub.handle(ctx, f.signal(t, h, first))
	pkg, dashboard := servedDashboard(t, h.paths)
	if pkg == nil || pkg.Version != first.version || !bytes.Equal(dashboard, first.dashboard) {
		t.Fatalf("after the signal the node serves %v, want %s", pkg, first.version)
	}
	if !pkg.ServesOn(uiTestBuild) {
		t.Fatalf("the installed package does not name this build: %v", pkg.BundleVersions)
	}
	if launches.Load() != 0 {
		t.Fatalf("a UI package launched the stop-and-swap helper %d times", launches.Load())
	}
	ledger, err := os.ReadFile(filepath.Join(h.paths.Root, "deploy-ledger.jsonl"))
	if err != nil || !bytes.Contains(ledger, []byte(`"action":"ui-install"`)) || !bytes.Contains(ledger, []byte(first.updateID)) {
		t.Fatalf("the install is not in the deploy ledger: %v %s", err, ledger)
	}

	second := buildUIPackage(t, h, 3000, []string{uiTestBuild}, self.recipient)
	sub.handle(ctx, f.signal(t, h, second))
	if pkg, dashboard = servedDashboard(t, h.paths); pkg == nil || pkg.Version != second.version || !bytes.Equal(dashboard, second.dashboard) {
		t.Fatalf("the next package did not replace the first: serving %v", pkg)
	}

	// A replay of the older package is not news.
	replay := buildUIPackage(t, h, 2500, []string{uiTestBuild}, self.recipient)
	sub.handle(ctx, f.signal(t, h, replay))
	if pkg, _ = servedDashboard(t, h.paths); pkg.Version != second.version {
		t.Fatalf("an older package replaced a newer one: serving %s", pkg.Version)
	}
	if launches.Load() != 0 {
		t.Fatalf("helper launched %d times", launches.Load())
	}
}

func TestUIPackageRefusals(t *testing.T) {
	for _, tc := range []struct {
		name          string
		builds        []string
		sealedForSelf bool
		tamper        bool
		wantFetches   int64
	}{
		// Refused from the signed manifest alone, before the payload is fetched.
		{name: "sealed for other nodes only", builds: []string{uiTestBuild}, sealedForSelf: false, wantFetches: 0},
		{name: "made for another build", builds: []string{"1.0.6-updatelane.other"}, sealedForSelf: true, wantFetches: 0},
		// Refused once the carrier does not hash to what was signed.
		{name: "carrier altered on the feed", builds: []string{uiTestBuild}, sealedForSelf: true, tamper: true, wantFetches: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSignalHarness(t)
			f := newUIFeed(t)
			self, other := newUINode(t), newUINode(t)
			var launches atomic.Int64
			sub := uiSubscriber(t, h, f, self, &launches)
			recipients := []update.EnvelopeRecipient{other.recipient}
			if tc.sealedForSelf {
				recipients = append(recipients, self.recipient)
			}
			pkg := buildUIPackage(t, h, 2000, tc.builds, recipients...)
			f.tamper.Store(tc.tamper)
			sub.handle(context.Background(), f.signal(t, h, pkg))
			if served, _ := servedDashboard(t, h.paths); served != nil {
				t.Fatalf("installed %s", served.Version)
			}
			if got := f.carrierFetches.Load(); got != tc.wantFetches {
				t.Fatalf("carrier fetched %d times, want %d", got, tc.wantFetches)
			}
			if launches.Load() != 0 {
				t.Fatalf("helper launched %d times", launches.Load())
			}
		})
	}
}
