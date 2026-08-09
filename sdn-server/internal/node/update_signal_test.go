package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// These tests pin the DECISION path — which signals cause this box to spend
// bandwidth and swap its own binary, and which are dropped before a single byte
// is fetched. Manifest and payload verification are covered exhaustively in
// internal/update; what is new here is that a broadcast can reach a running
// daemon at all, so the gates in front of the fetch are the security boundary.

type countingTransport struct {
	requests atomic.Int64
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.requests.Add(1)
	return nil, http.ErrServerClosed // any error: we only care that a fetch was attempted
}

type fakeSubscription struct {
	mu   sync.Mutex
	msgs [][]byte
}

func (f *fakeSubscription) Next(ctx context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	msg := f.msgs[0]
	f.msgs = f.msgs[1:]
	return msg, nil
}

type fakeSubscriber struct{ sub *fakeSubscription }

func (f fakeSubscriber) Subscribe(string) (updateTopicSubscription, error) { return f.sub, nil }

type signalHarness struct {
	paths     update.Paths
	roots     update.TrustedRoots
	priv      ed25519.PrivateKey
	keyID     string
	transport *countingTransport
	launches  atomic.Int64
	sub       *UpdateSignalSubscriber
}

func newSignalHarness(t *testing.T) *signalHarness {
	t.Helper()
	root := t.TempDir()
	paths := update.PathsFor(root)
	for _, dir := range []string{paths.Updates, paths.Staged, paths.Rollback, paths.Failed, paths.Incoming} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyID := "signaltestkey"
	h := &signalHarness{
		paths:     paths,
		roots:     update.TrustedRoots{keyID: base64.StdEncoding.EncodeToString(der)},
		priv:      priv,
		keyID:     keyID,
		transport: &countingTransport{},
	}
	sub, err := NewUpdateSignalSubscriber(UpdateSignalSubscriberDeps{
		Subscriber:    fakeSubscriber{sub: &fakeSubscription{}},
		Topic:         "/sdn/updates/v1/beta",
		Paths:         paths,
		TrustedRoots:  h.roots,
		Channel:       "beta",
		Kind:          "cli-bundle",
		AdminURL:      "https://127.0.0.1:5001/",
		HealthTimeout: time.Minute,
		MinInterval:   time.Minute,
		Client:        &http.Client{Transport: h.transport},
		Launch: func(update.Paths, update.SelfUpgradeOptions) (*update.SelfUpgradeLaunch, error) {
			h.launches.Add(1)
			return &update.SelfUpgradeLaunch{Mode: "test"}, nil
		},
	})
	if err != nil {
		t.Fatalf("construct subscriber: %v", err)
	}
	h.sub = sub
	return h
}

func (h *signalHarness) sign(t *testing.T, mutate func(*update.Signal)) []byte {
	t.Helper()
	signal := &update.Signal{
		Schema:   update.SignalSchema,
		UpdateID: "sdn-cli-bundle-9.9.9",
		Version:  "9.9.9",
		Sequence: 999,
		Channel:  "beta",
		// The subscriber matches against runtime.GOOS/GOARCH, so a fixture that
		// hardcoded linux/amd64 would pass on CI and vacuously "pass" on a
		// developer laptop by being refused for the wrong reason.
		Target:      update.ManifestTarget{Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: "cli-bundle"},
		FeedBaseURL: "https://feed.example/updates",
		ManifestURL: "https://feed.example/updates/m.json",
		CarrierURL:  "https://feed.example/updates/u.wasm",
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
		Signing: update.SignalSigning{
			KeyID:           h.keyID,
			Algorithm:       update.SignalSignatureAlgorithm,
			StatementDomain: sigdomain.DomainUpdateSignalV1,
		},
	}
	if mutate != nil {
		mutate(signal)
	}
	unsigned, err := json.Marshal(signal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	canonical, err := update.CanonicalSignedDocument(unsigned)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	sum := sha256.Sum256(canonical)
	statement, err := sigdomain.Statement(sigdomain.DomainUpdateSignalV1, sum[:])
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	signal.Signing.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(h.priv, statement))
	signed, err := json.Marshal(signal)
	if err != nil {
		t.Fatalf("marshal signed: %v", err)
	}
	return signed
}

// The happy path stops at the FETCH here (the transport always errors), which
// is exactly what we want to observe: the gates let the signal through and the
// box went to the feed.
func TestSignalAcceptedReachesTheFetch(t *testing.T) {
	h := newSignalHarness(t)
	h.sub.handle(context.Background(), h.sign(t, nil))
	if h.transport.requests.Load() == 0 {
		t.Fatal("a valid, relevant, trusted signal must cause the box to fetch the manifest")
	}
	if h.launches.Load() != 0 {
		t.Fatal("nothing may be launched when the fetch failed")
	}
}

// A DECLARED source-lineage rollback is never automatic. This is the gate that
// stops one bad publish from undoing every lane that landed since.
func TestSignalDeclaringRollbackNeverFetches(t *testing.T) {
	h := newSignalHarness(t)
	h.sub.handle(context.Background(), h.sign(t, func(s *update.Signal) { s.Rollback = true }))
	if h.transport.requests.Load() != 0 {
		t.Fatal("a rollback signal must be refused before any fetch: a broadcast is not an operator")
	}
}

// The quarantine is what makes a REPLAYED signal harmless. A box that reversed
// an update has a lower sequence again, so the sequence gate alone would let
// the replay reinstall the build it just judged unhealthy, forever.
func TestSignalForAnAlreadyFailedUpdateIsRefused(t *testing.T) {
	h := newSignalHarness(t)
	if err := os.MkdirAll(filepath.Join(h.paths.Failed, "sdn-cli-bundle-9.9.9"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h.sub.handle(context.Background(), h.sign(t, nil))
	if h.transport.requests.Load() != 0 {
		t.Fatal("a signal for an update this box already reversed must not be acted on again")
	}
}

// One signal, one attempt within the interval floor: gossipsub delivers
// duplicates, and a box that acted on each would fetch 20 MB per copy. A
// pre-swap failure re-opens the update for a LATER retry, but never an
// immediate one — otherwise a broken feed becomes a retry storm.
func TestDuplicateSignalsAreHandledOnce(t *testing.T) {
	h := newSignalHarness(t)
	raw := h.sign(t, nil)
	h.sub.handle(context.Background(), raw)
	first := h.transport.requests.Load()
	h.sub.handle(context.Background(), raw)
	if h.transport.requests.Load() != first {
		t.Fatal("a duplicate signal must not cause a second fetch")
	}
}

func TestUntrustedAndIrrelevantSignalsNeverFetch(t *testing.T) {
	cases := map[string]func(*update.Signal){
		"other platform": func(s *update.Signal) { s.Target.Platform = "plan9" },
		"other channel":  func(s *update.Signal) { s.Channel = "stable" },
		"stale sequence": func(s *update.Signal) { s.Sequence = 1 },
		"expired":        func(s *update.Signal) { s.PublishedAt = time.Now().Add(-90 * time.Hour).UTC().Format(time.RFC3339) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newSignalHarness(t)
			// Give the box a sequence so "stale" is actually stale.
			if err := update.SaveState(h.paths, &update.State{Schema: update.StateSchema, Sequence: 10}); err != nil {
				t.Fatalf("save state: %v", err)
			}
			h.sub.handle(context.Background(), h.sign(t, mutate))
			if h.transport.requests.Load() != 0 {
				t.Fatalf("%s: signal must be dropped before any fetch", name)
			}
		})
	}

	t.Run("forged", func(t *testing.T) {
		h := newSignalHarness(t)
		raw := h.sign(t, nil)
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		doc["carrier_url"] = "https://attacker.example/u.wasm"
		forged, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		h.sub.handle(context.Background(), forged)
		if h.transport.requests.Load() != 0 {
			t.Fatal("a signal whose carrier URL was rewritten must not cause a fetch")
		}
	})

	t.Run("not a signal at all", func(t *testing.T) {
		h := newSignalHarness(t)
		h.sub.handle(context.Background(), []byte(`{"schema":"org.spacedatanetwork.update.announcement.v1"}`))
		if h.transport.requests.Load() != 0 {
			t.Fatal("a message of another schema on the same topic must be ignored, not acted on")
		}
	})
}

// A subscriber with no trusted roots could verify nothing. Listening would be
// strictly worse than not listening, so construction refuses.
func TestSubscriberRefusesToStartWithoutTrustRoots(t *testing.T) {
	_, err := NewUpdateSignalSubscriber(UpdateSignalSubscriberDeps{
		Subscriber: fakeSubscriber{sub: &fakeSubscription{}},
		Topic:      "/sdn/updates/v1/beta",
		Paths:      update.PathsFor(t.TempDir()),
	})
	if err == nil {
		t.Fatal("a signal subscriber with no trust roots must refuse to exist")
	}
}
