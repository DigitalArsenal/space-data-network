package node

// Binary releases are sealed per node like UI packages (owner 2026-10-07:
// "updates even the binary need to be encrypted per node since they go over
// noise protocol"). A node stages a release sealed for it by opening the
// carrier with its own key, and what it staged re-verifies from disk the way
// the swap helper reads it; a release sealed only for other nodes stops before
// its carrier is downloaded.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// sealedRelease is the harness's cli-bundle release sealed for recipients, as
// publish-fleet-update.mjs makes one with `update seal` and `sign-manifest`.
func (h *signalHarness) sealedRelease(t *testing.T, recipients ...update.EnvelopeRecipient) (manifest, carrier, bundle []byte) {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("runtime/sdn/spacedatanetwork")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("test bundle executable")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	bundle = archive.Bytes()
	sequence := int64(999)
	unsigned, err := json.Marshal(update.Manifest{
		Schema: update.ManifestSchema, UpdateID: "sdn-cli-bundle-9.9.9", Version: "9.9.9", Sequence: &sequence, Channel: "beta",
		CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2030-01-01T00:00:00Z",
		Target:  update.ManifestTarget{Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: "cli-bundle"},
		Bundle:  update.ManifestBundle{Hash: hexSum(bundle), Size: int64(len(bundle)), Format: "zip"},
		Signing: update.ManifestSigning{KeyID: h.keyID, Algorithm: "Ed25519", StatementDomain: sigdomain.DomainUpdateManifestV1},
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
	carrier, err = update.SealPayload(doc, bundle, recipients)
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
	if manifest, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	return manifest, carrier, bundle
}

func TestSealedBinaryReleaseStagesOnlyOnItsNodes(t *testing.T) {
	self, other := newUINode(t), newUINode(t)
	for _, tc := range []struct {
		name       string
		sealedFor  []update.EnvelopeRecipient
		wantStaged bool
	}{
		{name: "sealed for this node", sealedFor: []update.EnvelopeRecipient{other.recipient, self.recipient}, wantStaged: true},
		{name: "sealed for other nodes", sealedFor: []update.EnvelopeRecipient{other.recipient}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSignalHarness(t)
			manifest, carrier, bundle := h.sealedRelease(t, tc.sealedFor...)
			if bytes.Contains(carrier, bundle) {
				t.Fatal("the sealed carrier carries the bundle in the clear")
			}
			h.sub.deps.EnvelopeKey = func() (*update.EnvelopeKey, error) {
				return &update.EnvelopeKey{KeyID: []byte(self.fingerprint), Private: self.priv}, nil
			}
			carrierFetches := 0
			h.sub.deps.Client = &http.Client{Transport: updateFixtureTransport(func(req *http.Request) (*http.Response, error) {
				body := manifest
				if strings.HasSuffix(req.URL.Path, ".wasm") {
					carrierFetches++
					body = carrier
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), ContentLength: int64(len(body)), Request: req}, nil
			})}
			h.sub.handle(context.Background(), h.sign(t, func(s *update.Signal) {
				s.BundleHash, s.BundleSize = hexSum(bundle), int64(len(bundle))
				s.WasmHash, s.WasmSize = hexSum(carrier), int64(len(carrier))
			}))

			if !tc.wantStaged {
				if carrierFetches != 0 || h.launches.Load() != 0 {
					t.Fatalf("a release sealed for other nodes: %d carrier fetches, %d launches; want none", carrierFetches, h.launches.Load())
				}
				return
			}
			if h.launches.Load() != 1 {
				t.Fatalf("launches = %d, want the swap handed to the helper once", h.launches.Load())
			}
			// What the helper reads back: the staged release verifies from disk,
			// its bundle is the plaintext, and its carrier stays the ciphertext.
			staged, err := update.ScanStaged(h.paths, update.HostVerifyOptions(h.roots, 0, time.Now()))
			if err != nil || len(staged) != 1 || staged[0].Err != nil || staged[0].UpdateID != "sdn-cli-bundle-9.9.9" {
				t.Fatalf("staged = %+v (%v), want the release verified from disk", staged, err)
			}
			if got, err := os.ReadFile(staged[0].BundleFile); err != nil || !bytes.Equal(got, bundle) {
				t.Fatalf("the staged bundle is not the release's plaintext bundle (%v)", err)
			}
		})
	}
}
