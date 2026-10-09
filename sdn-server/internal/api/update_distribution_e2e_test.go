package api

// A release is distributed only with the distribution key entered in the
// updater module (owner 2026-10-09). The module is the built updater WASM: it
// derives the key from a recovery phrase exactly as the node's own wallet
// module does, signs the release's manifest and signal, and the coordinator
// accepts each only when it verifies against the node's update roots and is
// the release that was submitted. The signal is published only after it too is
// signed.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

const (
	distributionPhrase = "legal winner thank year wave sausage worth useful legal winner thank yellow"
	otherPhrase        = "letter advice cage absurd amount doctor acoustic avoid letter advice cage above"
)

type topicRecorder struct {
	topic string
	data  []byte
}

func (r *topicRecorder) Publish(string, []byte) error { return nil }
func (r *topicRecorder) PublishToTopic(_ context.Context, topic string, data []byte) error {
	r.topic, r.data = topic, append([]byte(nil), data...)
	return nil
}

type updaterModule struct {
	t   *testing.T
	mod *modulert.Module
}

func loadUpdaterModule(t *testing.T) *updaterModule {
	t.Helper()
	bytes, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "sdn-updater-module", "dist", "isomorphic", "module.wasm"))
	if err != nil {
		t.Fatalf("the updater module is not built: %v", err)
	}
	mod, err := modulert.NewModule(bytes, modulert.NewCapabilityRegistry(), nil)
	if err != nil {
		t.Fatalf("load the updater module: %v", err)
	}
	t.Cleanup(func() { mod.Close() })
	return &updaterModule{t: t, mod: mod}
}

func (u *updaterModule) call(method string, request any) []byte {
	u.t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		u.t.Fatal(err)
	}
	out, err := u.mod.InvokeMethod(context.Background(), method, payload)
	if err != nil {
		u.t.Fatalf("%s: %v", method, err)
	}
	return out
}

type keyReport struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Path      string `json:"path"`
	Error     string `json:"error"`
}

func (u *updaterModule) openKey(phrase string) keyReport {
	u.t.Helper()
	var report keyReport
	if err := json.Unmarshal(u.call("openKey", map[string]string{"phrase": phrase}), &report); err != nil {
		u.t.Fatal(err)
	}
	if report.Error != "" {
		u.t.Fatalf("openKey: %s", report.Error)
	}
	return report
}

func (u *updaterModule) sign(kind string, document json.RawMessage) []byte {
	u.t.Helper()
	return u.call("signRelease", map[string]any{"kind": kind, "document": document})
}

// coordinator is the node holding releases for the distribution key: its update
// roots trust only the key at rootsKeyID.
type coordinator struct {
	t         *testing.T
	mux       *http.ServeMux
	h         *CoreAPIHandler
	published *topicRecorder
	feed      string
}

func newCoordinator(t *testing.T, root keyReport) *coordinator {
	t.Helper()
	dir := t.TempDir()
	roots, _ := json.Marshal(update.TrustedRoots{root.KeyID: root.PublicKey})
	rootsPath := filepath.Join(dir, "update-roots.json")
	if err := os.WriteFile(rootsPath, roots, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(update.TrustRootsEnv, rootsPath)
	feed := filepath.Join(dir, "feed")
	t.Setenv(updateFeedDirEnv, feed)
	h, _ := newTestCoreAPIHandler(t)
	recorder := &topicRecorder{}
	h.publisher = recorder
	h.distributions = newDistributionStore()
	mux := http.NewServeMux()
	mux.HandleFunc(UpdateDistributionsRoute, h.handleDistributions)
	mux.HandleFunc(UpdateDistributionsRoute+"/{id}", h.handleDistribution)
	mux.HandleFunc(UpdateDistributionsRoute+"/{id}/{document}", h.handleDistributionSignature)
	mux.HandleFunc(UpdateSignalRoute, h.handleUpdateSignal)
	return &coordinator{t: t, mux: mux, h: h, published: recorder, feed: feed}
}

func (c *coordinator) post(path string, body []byte) (int, distribution, string) {
	c.t.Helper()
	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	var d distribution
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d, rec.Body.String()
}

// serve writes the feed index the signal is built from, as the publish script
// does after the files are uploaded.
func (c *coordinator) serve(m *update.Manifest) {
	c.t.Helper()
	lane := filepath.Join(c.feed, m.Target.Kind, m.Channel, m.Target.Platform, m.Target.Arch)
	if err := os.MkdirAll(lane, 0o755); err != nil {
		c.t.Fatal(err)
	}
	base := "https://updates.example/" + m.Target.Kind + "/" + m.Channel + "/" + m.Target.Platform + "/" + m.Target.Arch + "/" + m.Version
	index, _ := json.Marshal(update.ProviderFeed{
		Schema: update.ProviderFeedSchema, GeneratedAt: time.Now().UTC().Format(time.RFC3339), FeedBaseURL: "https://updates.example",
		Updates: []update.ProviderFeedUpdate{{
			UpdateID: m.UpdateID, Version: m.Version, Sequence: *m.Sequence, Channel: m.Channel, Target: m.Target,
			ExpiresAt: m.ExpiresAt, ManifestURL: base + "/manifest.json", CarrierURL: base + "/update.wasm",
			BundleHash: m.Bundle.Hash, BundleSize: m.Bundle.Size, WasmHash: m.Wasm.Hash,
		}},
	})
	if err := os.WriteFile(filepath.Join(lane, "index.json"), index, 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func unsignedRelease(t *testing.T) []byte {
	t.Helper()
	sequence := int64(1791500000)
	doc, err := json.Marshal(update.Manifest{
		Schema: update.ManifestSchema, UpdateID: "sdn-cli-bundle-1.0.6-updatelane.feedface0", Version: "1.0.6-updatelane.feedface0",
		Sequence: &sequence, Channel: "beta", CreatedAt: "2026-10-09T00:00:00Z", ExpiresAt: "2030-01-01T00:00:00Z",
		Target:  update.ManifestTarget{Platform: "linux", Arch: "amd64", Kind: "cli-bundle"},
		Bundle:  update.ManifestBundle{Hash: strings.Repeat("ab", 32), Size: 1234, Format: "tar.gz"},
		Wasm:    update.ManifestWasm{Hash: strings.Repeat("cd", 32)},
		Signing: update.ManifestSigning{Algorithm: "Ed25519", StatementDomain: sigdomain.DomainUpdateManifestV1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestReleaseIsDistributedOnlyWithTheEnteredDistributionKey(t *testing.T) {
	updater := loadUpdaterModule(t)
	key := updater.openKey(distributionPhrase)

	// The same key the node's wallet module derives at the distribution path:
	// "the same as any other key".
	hw, err := wasm.NewHDWalletModuleFromBytes(context.Background(), wasm.EmbeddedHDWalletWasm())
	if err != nil {
		t.Fatal(err)
	}
	seed, err := hw.MnemonicToSeed(context.Background(), distributionPhrase, "")
	if err != nil {
		t.Fatal(err)
	}
	derived, err := hw.DeriveEd25519Key(context.Background(), seed, "m/44'/0'/0'/3'/0'")
	if err != nil {
		t.Fatal(err)
	}
	walletPub := ed25519.NewKeyFromSeed(derived.PrivateKey).Public().(ed25519.PublicKey)
	spki, _ := base64.StdEncoding.DecodeString(key.PublicKey)
	if key.Path != "m/44'/0'/0'/3'/0'" || !bytes.Equal(spki[len(spki)-32:], walletPub) || key.KeyID != update.RootKeyID(walletPub) {
		t.Fatalf("the module's distribution key %s (%s) is not the wallet's key at m/44'/0'/0'/3'/0'", key.KeyID, key.Path)
	}

	c := newCoordinator(t, key)
	release := unsignedRelease(t)
	code, d, body := c.post(UpdateDistributionsRoute, release)
	if code != http.StatusCreated || d.State != DistributionAwaitingManifest {
		t.Fatalf("submit: %d %s", code, body)
	}
	manifestPath := UpdateDistributionsRoute + "/" + d.ID + "/manifest"

	t.Run("a release signed with another key is refused", func(t *testing.T) {
		other := loadUpdaterModule(t)
		other.openKey(otherPhrase)
		if code, _, body := c.post(manifestPath, other.sign("manifest", release)); code != http.StatusBadRequest || !strings.Contains(body, "SIGNATURE_REFUSED") {
			t.Fatalf("another key's signature: %d %s", code, body)
		}
	})

	t.Run("a changed release is refused", func(t *testing.T) {
		changed := bytes.Replace(release, []byte(`"tar.gz"`), []byte(`"zip"`), 1)
		if code, _, body := c.post(manifestPath, updater.sign("manifest", changed)); code != http.StatusBadRequest || !strings.Contains(body, "not the release that was submitted") {
			t.Fatalf("a changed release: %d %s", code, body)
		}
	})

	signedManifest := updater.sign("manifest", release)
	if code, d, body := c.post(manifestPath, signedManifest); code != http.StatusOK || d.State != DistributionManifestSigned || d.KeyID != key.KeyID {
		t.Fatalf("the distribution key's signature: %d %s", code, body)
	}
	manifest, err := update.ParseManifest(signedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature(update.TrustedRoots{key.KeyID: key.PublicKey}); err != nil {
		t.Fatalf("the signed manifest does not verify the way a node verifies it: %v", err)
	}

	// The files are served; the publish script asks for the signal.
	c.serve(manifest)
	selector, _ := json.Marshal(map[string]string{"channel": "beta", "platform": "linux", "arch": "amd64", "distribution": d.ID})
	code, held, body := c.post(UpdateSignalRoute, selector)
	if code != http.StatusAccepted || held.State != DistributionAwaitingSignal || c.published.data != nil {
		t.Fatalf("the signal must wait for the distribution key: %d %s", code, body)
	}

	// The key is wiped: nothing signs until it is entered again.
	var closed struct {
		Error string `json:"error"`
	}
	updater.call("closeKey", map[string]any{})
	request, _ := json.Marshal(map[string]any{"kind": "signal", "document": held.Signal})
	if out, err := updater.mod.InvokeMethod(context.Background(), "signRelease", request); err == nil {
		if json.Unmarshal(out, &closed) != nil || closed.Error == "" {
			t.Fatalf("signRelease after closeKey signed: %s", out)
		}
	}
	updater.openKey(distributionPhrase)

	if code, d, body := c.post(UpdateDistributionsRoute+"/"+d.ID+"/signal", updater.sign("signal", held.Signal)); code != http.StatusOK || d.State != DistributionSignalled {
		t.Fatalf("the signed signal: %d %s", code, body)
	}
	signal, err := update.ParseSignal(c.published.data)
	if err != nil {
		t.Fatalf("published signal: %v", err)
	}
	if err := signal.Verify(update.SignalVerifyOptions{TrustedRoots: update.TrustedRoots{key.KeyID: key.PublicKey}, Now: time.Now()}); err != nil || c.published.topic != update.SignalTopic("beta") {
		t.Fatalf("the published signal on %q does not verify against the distribution key: %v", c.published.topic, err)
	}
}
