package node

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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// --- test doubles: in-memory topic subscriber ------------------------------

// fakeUpdateSubscription is an in-memory updateTopicSubscription: Next
// blocks on a channel until a message is published or ctx is done, mirroring
// how a real *pubsub.Subscription.Next behaves.
type fakeUpdateSubscription struct {
	ch chan []byte
}

func (f *fakeUpdateSubscription) Next(ctx context.Context) ([]byte, error) {
	select {
	case msg, ok := <-f.ch:
		if !ok {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fakeUpdateTopicSubscriber is an in-memory updateTopicSubscriber: Subscribe
// succeeds only for the configured topic (mirroring a real subscriber that
// is only wired to one topic) and returns a fakeUpdateSubscription that
// publish() feeds.
type fakeUpdateTopicSubscriber struct {
	topic string
	sub   *fakeUpdateSubscription
}

func newFakeUpdateTopicSubscriber(topic string) *fakeUpdateTopicSubscriber {
	return &fakeUpdateTopicSubscriber{topic: topic, sub: &fakeUpdateSubscription{ch: make(chan []byte, 8)}}
}

func (f *fakeUpdateTopicSubscriber) Subscribe(topic string) (updateTopicSubscription, error) {
	if topic != f.topic {
		return nil, fmt.Errorf("fakeUpdateTopicSubscriber: unexpected topic %q, want %q", topic, f.topic)
	}
	return f.sub, nil
}

func (f *fakeUpdateTopicSubscriber) publish(msg []byte) {
	f.sub.ch <- msg
}

// --- test doubles: a signed-manifest + G2-sealed-bundle announcement builder

// testUpdateSigner is a minimal Ed25519 release-signer double, mirroring
// internal/update's own test-only testSigner (update_test.go) since that
// type is unexported and unavailable across the package boundary.
type testUpdateSigner struct {
	keyID string
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
}

func newTestUpdateSigner(t *testing.T, keyID string) testUpdateSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testUpdateSigner{keyID: keyID, pub: pub, priv: priv}
}

func (s testUpdateSigner) roots(t *testing.T) update.TrustedRoots {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(s.pub)
	if err != nil {
		t.Fatal(err)
	}
	return update.TrustedRoots{s.keyID: base64.StdEncoding.EncodeToString(der)}
}

func testSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func marshalSortedJSON(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// signManifest builds and signs an org.spacedatanetwork.update.v1 manifest
// document, mirroring internal/update's own signedManifest test helper
// (update_test.go) byte-for-byte in shape so update.ParseManifest/Validate
// accept it. mutate, if non-nil, runs after the base document is built and
// before signing, so a test can corrupt a field the signature will then
// legitimately cover (e.g. bump the sequence) or corrupt the signature
// itself after this returns.
func (s testUpdateSigner) signManifest(t *testing.T, updateID, version string, sequence int64, bundleBytes, wasmBytes []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	doc := map[string]any{
		"schema":     "org.spacedatanetwork.update.v1",
		"update_id":  updateID,
		"version":    version,
		"sequence":   sequence,
		"channel":    "beta",
		"created_at": "2026-06-01T00:00:00Z",
		"expires_at": "2030-01-01T00:00:00Z",
		"target": map[string]any{
			"platform": runtime.GOOS,
			"arch":     runtime.GOARCH,
			"kind":     "cli-bundle",
		},
		"bundle": map[string]any{
			"hash":   testSHA256Hex(bundleBytes),
			"size":   int64(len(bundleBytes)),
			"format": "tar.gz",
		},
		"wasm": map[string]any{
			"hash": testSHA256Hex(wasmBytes),
		},
		"signing": map[string]any{
			"key_id":    s.keyID,
			"algorithm": "Ed25519",
		},
	}
	if mutate != nil {
		mutate(doc)
	}
	canonical, err := update.CanonicalManifestBytes(marshalSortedJSON(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(s.priv, canonical)
	doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(signature)
	return marshalSortedJSON(t, doc)
}

// corruptManifestSignature returns manifestBytes with the signing.signature
// field replaced by a well-formed but wrong signature, so the document
// remains valid JSON/shape but fails Ed25519 verification.
func corruptManifestSignature(t *testing.T, manifestBytes []byte) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(manifestBytes, &doc); err != nil {
		t.Fatal(err)
	}
	signing, ok := doc["signing"].(map[string]any)
	if !ok {
		t.Fatal("manifest document has no signing object")
	}
	signing["signature"] = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// newX25519TestRecipient generates a fresh synthetic X25519 keypair (never
// real seed/key material) and wraps it as an update.EnvelopeRecipient keyed
// by keyID, mirroring internal/channelkeys' KeyID-is-a-stable-member-id
// convention (channelkeys.go WrapForMembers) and internal/update's own
// envelope_test.go recipient double.
func newX25519TestRecipient(t *testing.T, keyID string) (update.EnvelopeRecipient, []byte) {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return update.EnvelopeRecipient{KeyID: []byte(keyID), PublicKey: pub, KeyExchange: ecies.X25519}, priv
}

// sealTestBundle G2-encrypts bundleBytes for recipients.
func sealTestBundle(t *testing.T, bundleBytes []byte, recipients []update.EnvelopeRecipient) *update.EncryptedBundle {
	t.Helper()
	enc, err := update.EncryptCarrierForRecipients(bundleBytes, recipients, "test-update-announcement")
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// marshalTestAnnouncement assembles and serializes the wire envelope
// UpdateSubscriber consumes, via the package's own NewUpdateAnnouncement.
func marshalTestAnnouncement(t *testing.T, manifestBytes []byte, bundle *update.EncryptedBundle) []byte {
	t.Helper()
	ann, err := NewUpdateAnnouncement(manifestBytes, bundle)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ann.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// makeTestBundleTarGz builds a real tar.gz update bundle archive (a single
// wrapper directory containing an org.spacedatanetwork.bundle.v1
// manifest.json plus the given files), mirroring internal/update's own
// makeBundleTarGz test helper (update_test.go) so update.Apply's real
// extraction/validation path (extractBundleArchive, validateIncomingBundle)
// accepts it.
func makeTestBundleTarGz(t *testing.T, version string, files map[string]string) []byte {
	t.Helper()
	wrapper := fmt.Sprintf("spacedatanetwork-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)

	type artifact struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	}
	var artifacts []artifact
	for path, contents := range files {
		artifacts = append(artifacts, artifact{Path: path, SHA256: testSHA256Hex([]byte(contents)), Size: len(contents)})
	}
	manifest := map[string]any{
		"schema":    "org.spacedatanetwork.bundle.v1",
		"version":   version,
		"channel":   "beta",
		"signature": "test-signature",
		"artifacts": artifacts,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name, contents string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range files {
		write(wrapper+"/"+path, contents)
	}
	write(wrapper+"/manifest.json", string(manifestBytes))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// setupTestBundleRoot creates a minimal real bundle root on disk (needed
// only by the auto-apply test: update.Apply loads trust roots and state
// straight off disk, independent of whatever UpdateSubscriberDeps.
// TrustedRoots a test also passes in-memory), mirroring internal/update's
// own setupBundleRoot test helper.
func setupTestBundleRoot(t *testing.T, roots update.TrustedRoots) (update.Paths, string) {
	t.Helper()
	root := t.TempDir()
	paths := update.PathsFor(root)
	for _, dir := range []string{"bin", "runtime"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "spacedatanetwork"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"schema":"org.spacedatanetwork.bundle.v1","version":"1.0.0","channel":"beta","signature":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Trust), 0o755); err != nil {
		t.Fatal(err)
	}
	rootsJSON, err := json.Marshal(roots)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Trust, rootsJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	return paths, root
}

const testUpdateTopic = "/sdn/updates/v1/beta"

func newTestUpdateSubscriber(t *testing.T, deps UpdateSubscriberDeps) *UpdateSubscriber {
	t.Helper()
	sub, err := NewUpdateSubscriber(deps)
	if err != nil {
		t.Fatalf("NewUpdateSubscriber returned error: %v", err)
	}
	return sub
}

// --- tests -------------------------------------------------------------

// TestUpdateSubscriberStagesOnlyWhenAutoApplyDisabled covers the default
// (AutoApply=false) posture: a correctly-signed, correctly-addressed
// announcement is verified, decrypted, and staged, but update.LoadState
// (i.e. the running bundle) is left completely untouched.
func TestUpdateSubscriberStagesOnlyWhenAutoApplyDisabled(t *testing.T) {
	signer := newTestUpdateSigner(t, "release-key-1")
	recipient, recipPriv := newX25519TestRecipient(t, "node-under-test")
	paths := update.PathsFor(t.TempDir())

	bundleBytes := []byte("plaintext bundle payload for staging-only test")
	manifestBytes := signer.signManifest(t, "upd-stage-only", "1.2.3", 10, bundleBytes, update.BuildCarrier(bundleBytes), nil)
	sealed := sealTestBundle(t, bundleBytes, []update.EnvelopeRecipient{recipient})
	data := marshalTestAnnouncement(t, manifestBytes, sealed)

	fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)
	sub := newTestUpdateSubscriber(t, UpdateSubscriberDeps{
		Subscriber:          fakeSub,
		Topic:               testUpdateTopic,
		Paths:               paths,
		TrustedRoots:        signer.roots(t),
		RecipientKeyID:      []byte("node-under-test"),
		RecipientPrivateKey: recipPriv,
		AutoApply:           false,
	})
	if sub.AutoApply() {
		t.Fatal("AutoApply() = true, want false (the required default)")
	}

	sub.handleMessage(data)

	staged, err := update.ScanStaged(paths, update.HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatalf("ScanStaged returned error: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("got %d staged updates, want 1: %+v", len(staged), staged)
	}
	if staged[0].Err != nil {
		t.Fatalf("staged update failed verification: %v", staged[0].Err)
	}
	if staged[0].UpdateID != "upd-stage-only" {
		t.Fatalf("staged update id = %q, want upd-stage-only", staged[0].UpdateID)
	}

	// Nothing must have been applied: state.json must not exist / must
	// remain the zero state, proving the running bundle was never touched.
	state, err := update.LoadState(paths)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	if state.Sequence != 0 || state.UpdateID != "" {
		t.Fatalf("update state = %+v, want untouched zero state (auto-apply is disabled)", state)
	}
}

// TestUpdateSubscriberAutoAppliesWhenEnabled covers AutoApply=true: the
// same verified/decrypted/staged update additionally drives a real
// update.Apply through UpdateSubscriber, and the bundle root's payload
// (bin/spacedatanetwork) is observed to have actually swapped in place.
func TestUpdateSubscriberAutoAppliesWhenEnabled(t *testing.T) {
	signer := newTestUpdateSigner(t, "release-key-1")
	recipient, recipPriv := newX25519TestRecipient(t, "node-under-test")
	paths, root := setupTestBundleRoot(t, signer.roots(t))

	bundleBytes := makeTestBundleTarGz(t, "9.9.9", map[string]string{
		"bin/spacedatanetwork": "new-binary",
	})
	manifestBytes := signer.signManifest(t, "upd-auto-apply", "9.9.9", 42, bundleBytes, update.BuildCarrier(bundleBytes), nil)
	sealed := sealTestBundle(t, bundleBytes, []update.EnvelopeRecipient{recipient})
	data := marshalTestAnnouncement(t, manifestBytes, sealed)

	fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)
	sub := newTestUpdateSubscriber(t, UpdateSubscriberDeps{
		Subscriber:          fakeSub,
		Topic:               testUpdateTopic,
		Paths:               paths,
		TrustedRoots:        signer.roots(t),
		RecipientKeyID:      []byte("node-under-test"),
		RecipientPrivateKey: recipPriv,
		AutoApply:           true,
	})

	sub.handleMessage(data)

	newBinary, err := os.ReadFile(filepath.Join(root, "bin", "spacedatanetwork"))
	if err != nil {
		t.Fatalf("read applied binary: %v", err)
	}
	if string(newBinary) != "new-binary" {
		t.Fatalf("bin/spacedatanetwork = %q, want new-binary (auto-apply should have swapped it in place)", newBinary)
	}

	state, err := update.LoadState(paths)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	if state.Sequence != 42 || state.UpdateID != "upd-auto-apply" || state.Version != "9.9.9" {
		t.Fatalf("update state = %+v, want sequence=42 update_id=upd-auto-apply version=9.9.9", state)
	}

	if entries, err := os.ReadDir(paths.Staged); err == nil && len(entries) != 0 {
		t.Fatalf("staged dir not cleaned up after apply: %v", entries)
	}
}

// TestUpdateSubscriberRejectsUnverifiedAnnouncements covers the pre-decrypt
// signature/trust gate: a forged signature and an untrusted signing key
// must both be dropped before anything is staged.
func TestUpdateSubscriberRejectsUnverifiedAnnouncements(t *testing.T) {
	cases := []struct {
		name       string
		buildAnn   func(t *testing.T, signer testUpdateSigner, bundleBytes []byte, recipient update.EnvelopeRecipient) []byte
		trustRoots func(t *testing.T, signer testUpdateSigner) update.TrustedRoots
	}{
		{
			name: "forged signature",
			buildAnn: func(t *testing.T, signer testUpdateSigner, bundleBytes []byte, recipient update.EnvelopeRecipient) []byte {
				manifestBytes := signer.signManifest(t, "upd-forged", "1.0.0", 1, bundleBytes, update.BuildCarrier(bundleBytes), nil)
				manifestBytes = corruptManifestSignature(t, manifestBytes)
				sealed := sealTestBundle(t, bundleBytes, []update.EnvelopeRecipient{recipient})
				return marshalTestAnnouncement(t, manifestBytes, sealed)
			},
			trustRoots: func(t *testing.T, signer testUpdateSigner) update.TrustedRoots { return signer.roots(t) },
		},
		{
			name: "untrusted signing key",
			buildAnn: func(t *testing.T, signer testUpdateSigner, bundleBytes []byte, recipient update.EnvelopeRecipient) []byte {
				manifestBytes := signer.signManifest(t, "upd-untrusted", "1.0.0", 1, bundleBytes, update.BuildCarrier(bundleBytes), nil)
				sealed := sealTestBundle(t, bundleBytes, []update.EnvelopeRecipient{recipient})
				return marshalTestAnnouncement(t, manifestBytes, sealed)
			},
			// A completely different signer's roots: signer's key id/signature
			// pair is simply absent, so it's untrusted rather than merely wrong.
			trustRoots: func(t *testing.T, _ testUpdateSigner) update.TrustedRoots {
				other := newTestUpdateSigner(t, "some-other-key")
				return other.roots(t)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signer := newTestUpdateSigner(t, "release-key-1")
			recipient, recipPriv := newX25519TestRecipient(t, "node-under-test")
			paths := update.PathsFor(t.TempDir())
			bundleBytes := []byte("plaintext bundle payload for rejection test: " + tc.name)

			data := tc.buildAnn(t, signer, bundleBytes, recipient)

			fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)
			sub := newTestUpdateSubscriber(t, UpdateSubscriberDeps{
				Subscriber:          fakeSub,
				Topic:               testUpdateTopic,
				Paths:               paths,
				TrustedRoots:        tc.trustRoots(t, signer),
				RecipientKeyID:      []byte("node-under-test"),
				RecipientPrivateKey: recipPriv,
				AutoApply:           true, // even with auto-apply on, an unverified announcement must never reach staging or apply.
			})

			sub.handleMessage(data)

			staged, err := update.ScanStaged(paths, update.HostVerifyOptions(signer.roots(t), 0, time.Now()))
			if err != nil {
				t.Fatalf("ScanStaged returned error: %v", err)
			}
			if len(staged) != 0 {
				t.Fatalf("got %d staged updates, want 0 (announcement should have been rejected): %+v", len(staged), staged)
			}
			state, err := update.LoadState(paths)
			if err != nil {
				t.Fatalf("LoadState returned error: %v", err)
			}
			if state.Sequence != 0 || state.UpdateID != "" {
				t.Fatalf("update state = %+v, want untouched zero state", state)
			}
		})
	}
}

// TestUpdateSubscriberDropsAnnouncementForDifferentRecipient covers the G2
// addressing check: an announcement sealed only for a different node's
// envelope must be dropped cleanly (ErrEnvelopeNotForRecipient), without
// ever attempting to stage anything, and even with auto-apply enabled.
func TestUpdateSubscriberDropsAnnouncementForDifferentRecipient(t *testing.T) {
	signer := newTestUpdateSigner(t, "release-key-1")
	otherRecipient, _ := newX25519TestRecipient(t, "some-other-node")
	_, thisNodePriv := newX25519TestRecipient(t, "node-under-test")
	paths := update.PathsFor(t.TempDir())

	bundleBytes := []byte("plaintext bundle payload addressed to someone else")
	manifestBytes := signer.signManifest(t, "upd-wrong-recipient", "1.0.0", 1, bundleBytes, update.BuildCarrier(bundleBytes), nil)
	sealed := sealTestBundle(t, bundleBytes, []update.EnvelopeRecipient{otherRecipient})
	data := marshalTestAnnouncement(t, manifestBytes, sealed)

	fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)
	sub := newTestUpdateSubscriber(t, UpdateSubscriberDeps{
		Subscriber:          fakeSub,
		Topic:               testUpdateTopic,
		Paths:               paths,
		TrustedRoots:        signer.roots(t),
		RecipientKeyID:      []byte("node-under-test"),
		RecipientPrivateKey: thisNodePriv,
		AutoApply:           true,
	})

	sub.handleMessage(data)

	staged, err := update.ScanStaged(paths, update.HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatalf("ScanStaged returned error: %v", err)
	}
	if len(staged) != 0 {
		t.Fatalf("got %d staged updates, want 0 (this node is not a recipient): %+v", len(staged), staged)
	}
}

// TestUpdateSubscriberRecoverPendingDelegatesToUpdatePackage proves
// RecoverPending is a real delegation to update.RecoverPendingApply rather
// than a stub: it plants a durable two-phase-apply crash marker in exactly
// the shape/schema that package expects and asserts it gets consumed.
func TestUpdateSubscriberRecoverPendingDelegatesToUpdatePackage(t *testing.T) {
	paths := update.PathsFor(t.TempDir())
	if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := map[string]any{
		"schema":       update.ApplyPhaseSchema,
		"update_id":    "upd-recover-1",
		"rollback_dir": filepath.Join(paths.Rollback, "upd-recover-1"),
		"phase":        "kubo-done",
	}
	markerBytes, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Phase, markerBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	signer := newTestUpdateSigner(t, "release-key-1")
	_, recipPriv := newX25519TestRecipient(t, "node-under-test")
	fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)
	sub := newTestUpdateSubscriber(t, UpdateSubscriberDeps{
		Subscriber:          fakeSub,
		Topic:               testUpdateTopic,
		Paths:               paths,
		TrustedRoots:        signer.roots(t),
		RecipientKeyID:      []byte("node-under-test"),
		RecipientPrivateKey: recipPriv,
	})

	recovered, err := sub.RecoverPending()
	if err != nil {
		t.Fatalf("RecoverPending returned error: %v", err)
	}
	if !recovered {
		t.Fatal("RecoverPending reported recovered=false, want true (a phase marker was present on disk)")
	}
	if _, err := os.Stat(paths.Phase); !os.IsNotExist(err) {
		t.Fatalf("phase marker file still present after RecoverPending (stat err=%v); RecoverPending did not really delegate to update.RecoverPendingApply", err)
	}

	// Calling again with no marker present must be a clean no-op, exactly
	// update.RecoverPendingApply's own documented contract.
	recovered, err = sub.RecoverPending()
	if err != nil {
		t.Fatalf("second RecoverPending returned error: %v", err)
	}
	if recovered {
		t.Fatal("second RecoverPending reported recovered=true, want false (no marker left)")
	}
}

// TestUpdateSubscriberRunProcessesFakeTopicMessages drives UpdateSubscriber
// through its real Run/Subscribe loop (not handleMessage directly) against
// an in-memory fake topic subscriber, proving the join+read-loop plumbing
// itself works end to end and that Run returns cleanly when ctx is
// cancelled.
func TestUpdateSubscriberRunProcessesFakeTopicMessages(t *testing.T) {
	signer := newTestUpdateSigner(t, "release-key-1")
	recipient, recipPriv := newX25519TestRecipient(t, "node-under-test")
	paths := update.PathsFor(t.TempDir())

	bundleBytes := []byte("plaintext bundle payload for run-loop test")
	manifestBytes := signer.signManifest(t, "upd-run-loop", "2.0.0", 5, bundleBytes, update.BuildCarrier(bundleBytes), nil)
	sealed := sealTestBundle(t, bundleBytes, []update.EnvelopeRecipient{recipient})
	data := marshalTestAnnouncement(t, manifestBytes, sealed)

	fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)
	sub := newTestUpdateSubscriber(t, UpdateSubscriberDeps{
		Subscriber:          fakeSub,
		Topic:               testUpdateTopic,
		Paths:               paths,
		TrustedRoots:        signer.roots(t),
		RecipientKeyID:      []byte("node-under-test"),
		RecipientPrivateKey: recipPriv,
		AutoApply:           false,
	})

	processed := make(chan struct{}, 1)
	sub.testAfterHandle = func() { processed <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- sub.Run(ctx) }()

	fakeSub.publish(data)

	select {
	case <-processed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to process the fake announcement")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to return after ctx cancel")
	}

	staged, err := update.ScanStaged(paths, update.HostVerifyOptions(signer.roots(t), 0, time.Now()))
	if err != nil {
		t.Fatalf("ScanStaged returned error: %v", err)
	}
	if len(staged) != 1 || staged[0].Err != nil || staged[0].UpdateID != "upd-run-loop" {
		t.Fatalf("staged = %+v, want exactly upd-run-loop verified", staged)
	}
}

// TestNewUpdateSubscriberRejectsIncompleteDeps covers constructor
// fail-closed validation: missing required wiring must be an error, not a
// subscriber that silently accepts everything or panics later.
func TestNewUpdateSubscriberRejectsIncompleteDeps(t *testing.T) {
	signer := newTestUpdateSigner(t, "release-key-1")
	_, recipPriv := newX25519TestRecipient(t, "node-under-test")
	paths := update.PathsFor(t.TempDir())
	fakeSub := newFakeUpdateTopicSubscriber(testUpdateTopic)

	base := UpdateSubscriberDeps{
		Subscriber:          fakeSub,
		Topic:               testUpdateTopic,
		Paths:               paths,
		TrustedRoots:        signer.roots(t),
		RecipientKeyID:      []byte("node-under-test"),
		RecipientPrivateKey: recipPriv,
	}

	if _, err := NewUpdateSubscriber(base); err != nil {
		t.Fatalf("baseline deps should construct cleanly, got error: %v", err)
	}

	withoutSubscriber := base
	withoutSubscriber.Subscriber = nil
	if _, err := NewUpdateSubscriber(withoutSubscriber); err == nil {
		t.Fatal("expected error for nil Subscriber")
	}

	withoutTopic := base
	withoutTopic.Topic = "  "
	if _, err := NewUpdateSubscriber(withoutTopic); err == nil {
		t.Fatal("expected error for blank Topic")
	}

	withoutRoots := base
	withoutRoots.TrustedRoots = nil
	if _, err := NewUpdateSubscriber(withoutRoots); err == nil {
		t.Fatal("expected error for empty TrustedRoots")
	}

	withoutKeyID := base
	withoutKeyID.RecipientKeyID = nil
	if _, err := NewUpdateSubscriber(withoutKeyID); err == nil {
		t.Fatal("expected error for empty RecipientKeyID")
	}

	withoutPriv := base
	withoutPriv.RecipientPrivateKey = nil
	if _, err := NewUpdateSubscriber(withoutPriv); err == nil {
		t.Fatal("expected error for empty RecipientPrivateKey")
	}
}
