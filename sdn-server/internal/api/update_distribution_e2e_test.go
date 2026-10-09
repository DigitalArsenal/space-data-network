package api

// A release goes out only when an administrator approves it in the updater
// module's page (owner 2026-10-09: "the new way to be able to use the node key
// by default, or upload a new one"). By default the coordinator signs with its
// own node key on approval; or the page loads an uploaded key into the built
// updater module, which signs. Either signature is accepted only when it
// verifies against the node's update roots and covers the submitted release,
// and the signal is published only once it too is signed.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
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
	Error     string `json:"error"`
}

// uploadKey loads a PKCS#8 PEM key file, as the page does with an uploaded one.
func (u *updaterModule) uploadKey(priv ed25519.PrivateKey) keyReport {
	u.t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		u.t.Fatal(err)
	}
	file := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	var report keyReport
	if err := json.Unmarshal(u.call("openKey", map[string]string{"key_file": string(file)}), &report); err != nil {
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

func spkiBase64(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// coordinator is the node holding releases for approval, with its own node key
// and the given update roots.
type coordinator struct {
	t         *testing.T
	mux       *http.ServeMux
	h         *CoreAPIHandler
	published *topicRecorder
	feed      string
	rootsPath string
}

func newCoordinator(t *testing.T, nodeKey ed25519.PrivateKey) *coordinator {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(update.TrustRootsEnv, filepath.Join(dir, "update-roots.json"))
	feed := filepath.Join(dir, "feed")
	t.Setenv(updateFeedDirEnv, feed)
	h, _ := newTestCoreAPIHandler(t)
	signer, err := updatesign.NewSigner(nodeKey, updatesign.NewAuditLog(filepath.Join(dir, "audit", "update-signing.log")))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &topicRecorder{}
	h.publisher, h.updateSigner, h.distributions = recorder, signer, newDistributionStore()
	mux := http.NewServeMux()
	mux.HandleFunc(UpdateDistributionsRoute, h.handleDistributions)
	mux.HandleFunc(UpdateDistributionsRoute+"/{id}", h.handleDistribution)
	mux.HandleFunc(UpdateDistributionsRoute+"/{id}/{document}", h.handleDistributionSignature)
	mux.HandleFunc(UpdateSignalRoute, h.handleUpdateSignal)
	c := &coordinator{t: t, mux: mux, h: h, published: recorder, feed: feed, rootsPath: filepath.Join(dir, "update-roots.json")}
	c.trust(update.TrustedRoots{signer.KeyID(): signer.PublicKeyB64()})
	return c
}

func (c *coordinator) trust(roots update.TrustedRoots) {
	c.t.Helper()
	raw, _ := json.Marshal(roots)
	if err := os.WriteFile(c.rootsPath, raw, 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func (c *coordinator) post(path string, body []byte) (int, distribution, string) {
	c.t.Helper()
	rec := httptest.NewRecorder()
	c.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	var d distribution
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return rec.Code, d, rec.Body.String()
}

func (c *coordinator) submit(release []byte) distribution {
	c.t.Helper()
	code, d, body := c.post(UpdateDistributionsRoute, release)
	if code != http.StatusCreated || d.State != DistributionAwaitingManifest {
		c.t.Fatalf("submit: %d %s", code, body)
	}
	return d
}

// serve writes the feed index the signal is built from, as the publish script
// does after the files are uploaded, and asks for the signal.
func (c *coordinator) serveAndAskForSignal(id string, m *update.Manifest) distribution {
	c.t.Helper()
	lane := filepath.Join(c.feed, m.Target.Kind, m.Channel, m.Target.Platform, m.Target.Arch)
	if err := os.MkdirAll(lane, 0o755); err != nil {
		c.t.Fatal(err)
	}
	base := "https://updates.example/" + m.Version
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
	selector, _ := json.Marshal(map[string]string{"channel": m.Channel, "platform": m.Target.Platform, "arch": m.Target.Arch, "distribution": id})
	code, held, body := c.post(UpdateSignalRoute, selector)
	if code != http.StatusAccepted || held.State != DistributionAwaitingSignal || c.published.data != nil {
		c.t.Fatalf("the signal must wait for approval: %d %s", code, body)
	}
	return held
}

func (c *coordinator) assertPublishedSignalVerifies(roots update.TrustedRoots) {
	c.t.Helper()
	signal, err := update.ParseSignal(c.published.data)
	if err != nil {
		c.t.Fatalf("published signal: %v", err)
	}
	if err := signal.Verify(update.SignalVerifyOptions{TrustedRoots: roots, Now: time.Now()}); err != nil || c.published.topic != update.SignalTopic("beta") {
		c.t.Fatalf("the published signal on %q does not verify: %v", c.published.topic, err)
	}
}

func unsignedRelease(t *testing.T, version string) []byte {
	t.Helper()
	sequence := int64(1791500000)
	doc, err := json.Marshal(update.Manifest{
		Schema: update.ManifestSchema, UpdateID: "sdn-cli-bundle-" + version, Version: version,
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

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestApprovedReleaseIsSignedWithTheNodeKeyByDefault(t *testing.T) {
	c := newCoordinator(t, newKey(t))
	release := unsignedRelease(t, "1.0.6-updatelane.feedface0")
	d := c.submit(release)
	approve := UpdateDistributionsRoute + "/" + d.ID + "/approve"

	code, signed, body := c.post(approve, []byte(`{"document":"manifest"}`))
	if code != http.StatusOK || signed.State != DistributionManifestSigned || signed.KeyID != c.h.updateSigner.KeyID() {
		t.Fatalf("approve the manifest: %d %s", code, body)
	}
	manifest, err := update.ParseManifest(signed.SignedManifest)
	if err != nil {
		t.Fatal(err)
	}
	roots := update.TrustedRoots{c.h.updateSigner.KeyID(): c.h.updateSigner.PublicKeyB64()}
	if err := manifest.VerifySignature(roots); err != nil {
		t.Fatalf("the approved manifest does not verify the way a node verifies it: %v", err)
	}
	if code, _, _ := c.post(approve, []byte(`{"document":"manifest"}`)); code != http.StatusBadRequest {
		t.Fatalf("a second approval of the same manifest: %d, want refused", code)
	}

	c.serveAndAskForSignal(d.ID, manifest)
	if code, done, body := c.post(approve, []byte(`{"document":"signal"}`)); code != http.StatusOK || done.State != DistributionSignalled {
		t.Fatalf("approve the signal: %d %s", code, body)
	}
	c.assertPublishedSignalVerifies(roots)
}

func TestUploadedKeySignsOnlyOnceItIsAnUpdateRoot(t *testing.T) {
	c := newCoordinator(t, newKey(t))
	updater := loadUpdaterModule(t)
	uploaded := newKey(t)
	key := updater.uploadKey(uploaded)
	if want := update.RootKeyID(uploaded.Public().(ed25519.PublicKey)); key.KeyID != want || key.PublicKey != spkiBase64(t, uploaded.Public().(ed25519.PublicKey)) {
		t.Fatalf("the module reports key %s %s, want the uploaded key %s", key.KeyID, key.PublicKey, want)
	}
	release := unsignedRelease(t, "1.0.6-updatelane.feedface1")
	d := c.submit(release)
	manifestPath := UpdateDistributionsRoute + "/" + d.ID + "/manifest"

	if code, _, body := c.post(manifestPath, updater.sign("manifest", release)); code != http.StatusBadRequest || !strings.Contains(body, "SIGNATURE_REFUSED") {
		t.Fatalf("a key that is not an update root: %d %s", code, body)
	}

	roots := update.TrustedRoots{c.h.updateSigner.KeyID(): c.h.updateSigner.PublicKeyB64(), key.KeyID: key.PublicKey}
	c.trust(roots)
	changed := bytes.Replace(release, []byte(`"tar.gz"`), []byte(`"zip"`), 1)
	if code, _, body := c.post(manifestPath, updater.sign("manifest", changed)); code != http.StatusBadRequest || !strings.Contains(body, "not the release that was submitted") {
		t.Fatalf("a changed release: %d %s", code, body)
	}
	signedManifest := updater.sign("manifest", release)
	if code, signed, body := c.post(manifestPath, signedManifest); code != http.StatusOK || signed.KeyID != key.KeyID {
		t.Fatalf("the uploaded key's signature: %d %s", code, body)
	}
	manifest, err := update.ParseManifest(signedManifest)
	if err != nil {
		t.Fatal(err)
	}
	held := c.serveAndAskForSignal(d.ID, manifest)

	// Wiped: nothing signs until a key is uploaded again.
	updater.call("closeKey", map[string]any{})
	request, _ := json.Marshal(map[string]any{"kind": "signal", "document": held.Signal})
	if out, err := updater.mod.InvokeMethod(context.Background(), "signRelease", request); err == nil && !strings.Contains(string(out), `"error"`) {
		t.Fatalf("signRelease after closeKey signed: %s", out)
	}
	updater.uploadKey(uploaded)
	if code, done, body := c.post(UpdateDistributionsRoute+"/"+d.ID+"/signal", updater.sign("signal", held.Signal)); code != http.StatusOK || done.State != DistributionSignalled {
		t.Fatalf("the uploaded key's signal: %d %s", code, body)
	}
	c.assertPublishedSignalVerifies(roots)
}
