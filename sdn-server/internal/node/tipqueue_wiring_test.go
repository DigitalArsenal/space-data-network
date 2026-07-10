package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	ps "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// --- D1: TipQueue construction (startup wiring) -----------------------

func TestBuildTipQueueConstructsNonNilEngineWithSaneDefaults(t *testing.T) {
	n := &Node{
		config: &config.Config{
			Admin: config.AdminConfig{IPFSAPIURL: ""},
		},
	}

	tq := n.buildTipQueue()
	if tq == nil {
		t.Fatal("buildTipQueue returned nil; TipQueue must be constructed on the live startup path")
	}
	cfg := tq.Config()
	if cfg == nil {
		t.Fatal("TipQueue.Config() returned nil")
	}
	if !cfg.DefaultAutoFetch || !cfg.DefaultAutoPin {
		t.Fatalf("expected DefaultAutoFetch/DefaultAutoPin = true (trust is already gated at the forwarding boundary), got AutoFetch=%v AutoPin=%v", cfg.DefaultAutoFetch, cfg.DefaultAutoPin)
	}
}

// TestBuildTipQueueWiresRealIPFSFetcherAndPinnerWhenConfigured drives a tip
// through the TipQueue built by buildTipQueue against a fake Kubo RPC
// server, exercising the actual ipfsTipFetcher/ipfsTipPinner adapters (not
// test doubles) to prove "trusted peer's PNM arrives -> TipQueue drives
// DPM fetch -> pin (real CID)" is really wired end to end.
func TestBuildTipQueueWiresRealIPFSFetcherAndPinnerWhenConfigured(t *testing.T) {
	var mu sync.Mutex
	var sawCat, sawPinAdd bool
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v0/cat":
			sawCat = true
			_, _ = w.Write([]byte("fake-content"))
		case "/api/v0/pin/add":
			sawPinAdd = true
			_, _ = w.Write([]byte(`{"Pins":["bafyipfswired"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ipfs.Close()

	n := &Node{
		config: &config.Config{
			Admin: config.AdminConfig{IPFSAPIURL: ipfs.URL},
		},
	}
	tq := n.buildTipQueue()
	if tq == nil {
		t.Fatal("buildTipQueue returned nil")
	}

	pnmBytes := buildWiringTestPNM(t, "bafyipfswired", "unknown-schema")
	tq.HandleMessage(&ps.Message{
		Message:      &pb.Message{Data: pnmBytes},
		ReceivedFrom: mustTestPeerID(t, 0x70),
	})

	if !waitForCondition(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return sawCat && sawPinAdd
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("TipQueue did not drive real IPFS fetch/pin calls: sawCat=%v sawPinAdd=%v", sawCat, sawPinAdd)
	}
}

// --- D1: OnTip -> materializeDatasetPublicationPNM reuse ---------------

// buildTipMaterializationFixture builds a signed dataset-publication PNM
// for a CAT record (mirroring dataset_publication_catchup_test.go's
// TestMaterializeStoredDatasetPublicationPNMsReplaysTrustedProviderPNM
// fixture) and a Node wired with a store + an IPFS test server that can
// serve the manifest/shard/index CIDs, WITHOUT registering the provider as
// trusted — callers add trust as needed per test.
func buildTipMaterializationFixture(t *testing.T) (n *Node, pnmBytes []byte, providerID peer.ID) {
	t.Helper()

	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("provider store: %v", err)
	}
	t.Cleanup(func() { providerStore.Close() })
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("subscriber store: %v", err)
	}
	t.Cleanup(func() { subscriberStore.Close() })

	priv, pub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x61}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	rawPriv, err := priv.Raw()
	if err != nil {
		t.Fatalf("raw private key: %v", err)
	}
	providerID, err = peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}

	tags := storage.SourceTags{
		ProviderID:   "tipqueue-wiring.eth",
		SourceName:   "tipqueue-wiring-source",
		SourceURL:    "https://example.invalid/satcat.csv",
		BatchID:      "batch-tipqueue-001",
		ContentKeyID: "public",
	}
	record := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, providerID.String(), nil, tags); err != nil {
		t.Fatalf("store provider record: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), storage.IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	publishedAt := time.Unix(1700009999, 0).UTC()
	manifest, err := storage.BuildSignedDatasetPublicationManifest(filepath.Join(tmpDir, "publish"), storage.DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "cat-tipqueue-wiring",
		UpdateID:       tags.BatchID,
		ProviderPeerID: providerID.String(),
		ProviderEPMCID: "bafy-provider-epm",
		PublishedAt:    publishedAt,
		SigningKey:     ed25519.PrivateKey(rawPriv),
		SchemaHash:     "cat-schema-hash",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	pnmBytes, err = storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  ed25519.PrivateKey(rawPriv),
	})
	if err != nil {
		t.Fatalf("BuildDatasetPublicationPNM failed: %v", err)
	}

	shardBytes := mustReadTestFile(t, export.ShardPath)
	indexBytes := mustReadTestFile(t, export.IndexPath)
	objects := map[string][]byte{
		manifest.CID:    manifest.Bytes,
		export.ShardCID: shardBytes,
		export.IndexCID: indexBytes,
	}
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v0/cat" {
			http.Error(w, "unexpected path "+got, http.StatusNotFound)
			return
		}
		cidValue := r.URL.Query().Get("arg")
		data, ok := objects[cidValue]
		if !ok {
			http.Error(w, fmt.Sprintf("missing cid %s", cidValue), http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(ipfs.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n = &Node{
		store:        subscriberStore,
		peerRegistry: peers.NewRegistry(false, nil),
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "subscriber-storage")},
			Admin:   config.AdminConfig{IPFSAPIURL: ipfs.URL},
		},
		ctx:                     ctx,
		datasetMaterializedPNMs: make(map[string]time.Time),
	}
	n.tipQueue = n.buildTipQueue()
	return n, pnmBytes, providerID
}

func TestHandleTipQueueTipMaterializesTrustedSchemaPNM(t *testing.T) {
	t.Parallel()

	n, pnmBytes, providerID := buildTipMaterializationFixture(t)
	if err := n.peerRegistry.AddPeer(&peers.TrustedPeer{ID: providerID, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add trusted provider: %v", err)
	}

	tip := &sdnpubsub.Tip{
		PeerID:     providerID.String(),
		CID:        pnmCID(t, pnmBytes),
		SchemaType: pnmFileID(t, pnmBytes),
		RawPNM:     pnmBytes,
	}

	n.handleTipQueueTip(tip, sdnpubsub.ResolvedConfig{})

	query := storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "tipqueue-wiring.eth",
		SourceName: "tipqueue-wiring-source",
		BatchID:    "batch-tipqueue-001",
		Limit:      10,
	}
	records, err := n.store.QueryIndexedRecords(query)
	if err != nil {
		t.Fatalf("query CAT records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("CAT records = %d, want 1", len(records))
	}

	// Reuse (not duplicate): calling the OnTip handler again for the same
	// PNM must not duplicate rows — materializeDatasetPublicationPNM's own
	// replay-state dedupe is what TipQueue relies on.
	n.handleTipQueueTip(tip, sdnpubsub.ResolvedConfig{})
	records, err = n.store.QueryIndexedRecords(query)
	if err != nil {
		t.Fatalf("query CAT records (second call): %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("CAT records after duplicate tip = %d, want 1 (dedupe via replay state)", len(records))
	}
}

func TestHandleTipQueueTipUntrustedPeerDoesNothing(t *testing.T) {
	t.Parallel()

	n, pnmBytes, providerID := buildTipMaterializationFixture(t)
	// Deliberately do NOT add providerID to the registry: default
	// (non-strict) trust for an unknown peer is Standard, below Trusted.

	tip := &sdnpubsub.Tip{
		PeerID:     providerID.String(),
		CID:        pnmCID(t, pnmBytes),
		SchemaType: pnmFileID(t, pnmBytes),
		RawPNM:     pnmBytes,
	}

	n.handleTipQueueTip(tip, sdnpubsub.ResolvedConfig{})

	records, err := n.store.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: "tipqueue-wiring.eth",
		SourceName: "tipqueue-wiring-source",
		BatchID:    "batch-tipqueue-001",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query CAT records: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("CAT records = %d, want 0: untrusted peer's PNM must not materialize", len(records))
	}
}

// --- D2: trust-change hook ------------------------------------------

// TestHandleTrustLevelChangeOnlyActsOnFullBoundaryCrossing verifies the
// hook's dedupe guard directly and deterministically (no async registry
// dispatch involved): only a below-Full -> Full-or-above transition
// subscribes, only a Full-or-above -> below-Full transition unsubscribes,
// and a transition that stays on the same side of the Full boundary
// (e.g. Trusted -> Admin, or Untrusted -> Standard) does neither.
func TestHandleTrustLevelChangeOnlyActsOnFullBoundaryCrossing(t *testing.T) {
	newNode := func(t *testing.T) *Node {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		n := &Node{
			ctx:          ctx,
			peerRegistry: peers.NewRegistry(false, nil),
			config:       &config.Config{},
		}
		n.tipQueue = n.buildTipQueue()
		return n
	}
	id := mustTestPeerID(t, 0x71)

	t.Run("below to full subscribes", func(t *testing.T) {
		n := newNode(t)
		n.handleTrustLevelChange(id, peers.Standard, peers.Trusted)
		if !n.tipQueue.Config().IsTrusted(id.String()) {
			t.Fatal("promotion across the Full boundary should mark the source trusted in TipQueue config")
		}
	})

	t.Run("full to below unsubscribes", func(t *testing.T) {
		n := newNode(t)
		n.handleTrustLevelChange(id, peers.Standard, peers.Trusted) // get into the trusted state first
		n.handleTrustLevelChange(id, peers.Trusted, peers.Standard)
		if n.tipQueue.Config().IsTrusted(id.String()) {
			t.Fatal("demotion across the Full boundary should mark the source untrusted in TipQueue config")
		}
	})

	t.Run("full to full does not re-trigger subscribe", func(t *testing.T) {
		n := newNode(t)
		if n.tipQueue.Config().IsTrusted(id.String()) {
			t.Fatal("precondition: source should start untrusted in TipQueue config")
		}
		n.handleTrustLevelChange(id, peers.Trusted, peers.Admin) // both >= Full: no crossing
		if n.tipQueue.Config().IsTrusted(id.String()) {
			t.Fatal("a Full-to-Full transition must not call subscribeFullyTrustedPeer")
		}
	})

	t.Run("below to below does not trigger subscribe", func(t *testing.T) {
		n := newNode(t)
		n.handleTrustLevelChange(id, peers.Untrusted, peers.Standard) // both < Full
		if n.tipQueue.Config().IsTrusted(id.String()) {
			t.Fatal("a below-Full transition must not call subscribeFullyTrustedPeer")
		}
	})
}

// TestRegistryPromotionToFullTriggersNodeCatchupBackfill exercises the
// full D2 wiring: n.peerRegistry.OnTrustChange(n.handleTrustLevelChange)
// (as registered in init()), then a real SetTrustLevel promotion, ending
// in the SAME catch-up machinery (materializeStoredDatasetPublicationPNMs)
// the periodic loop uses actually materializing a previously-stored PNM
// for the newly-trusted provider. Demotion is then checked to unsubscribe.
func TestRegistryPromotionToFullTriggersNodeCatchupBackfill(t *testing.T) {
	n, pnmBytes, providerID := buildTipMaterializationFixture(t)
	if err := n.peerRegistry.AddPeer(&peers.TrustedPeer{ID: providerID, TrustLevel: peers.Standard}); err != nil {
		t.Fatalf("add standard-trust provider: %v", err)
	}
	n.peerRegistry.OnTrustChange(n.handleTrustLevelChange)

	relayID := mustTestPeerID(t, 0x72)
	if _, err := n.store.Store("PNM.fbs", pnmBytes, relayID.String(), nil); err != nil {
		t.Fatalf("store relay PNM: %v", err)
	}

	if err := n.peerRegistry.SetTrustLevel(providerID, peers.Trusted); err != nil {
		t.Fatalf("SetTrustLevel(Trusted) failed: %v", err)
	}

	promoted := waitForCondition(t, 3*time.Second, func() bool {
		records, err := n.store.QueryIndexedRecords(storage.IndexedRecordQuery{
			SchemaName: "CAT.fbs",
			ProviderID: "tipqueue-wiring.eth",
			SourceName: "tipqueue-wiring-source",
			BatchID:    "batch-tipqueue-001",
			Limit:      10,
		})
		return err == nil && len(records) == 1 && n.tipQueue.Config().IsTrusted(providerID.String())
	})
	if !promoted {
		t.Fatal("promotion to Full did not trigger catch-up backfill + TipQueue trust bookkeeping in time")
	}

	if err := n.peerRegistry.SetTrustLevel(providerID, peers.Standard); err != nil {
		t.Fatalf("SetTrustLevel(Standard) (demotion) failed: %v", err)
	}
	demoted := waitForCondition(t, 2*time.Second, func() bool {
		return !n.tipQueue.Config().IsTrusted(providerID.String())
	})
	if !demoted {
		t.Fatal("demotion below Full did not unsubscribe (TipQueue config still reports trusted)")
	}
}

// --- D4: resource caps on auto-ingest fetches --------------------------

// TestIPFSTipFetcherRejectsContentOverCapViaStatPreCheckWithoutDownloading
// drives the real ipfsTipFetcher (not a test double) against a fake Kubo
// RPC server that implements /api/v0/block/stat, proving the pre-check
// path rejects an oversize CID without ever calling /api/v0/cat -- i.e.
// without paying for the download at all -- while an undersize CID still
// fetches normally.
func TestIPFSTipFetcherRejectsContentOverCapViaStatPreCheckWithoutDownloading(t *testing.T) {
	const oversizeCID = "bafyoversizestat"
	const undersizeCID = "bafyundersizestat"

	var mu sync.Mutex
	sawCatForOversize := false
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cidValue := r.URL.Query().Get("arg")
		switch r.URL.Path {
		case "/api/v0/block/stat":
			if cidValue == oversizeCID {
				_, _ = w.Write([]byte(`{"Key":"` + cidValue + `","Size":1048576}`))
				return
			}
			http.NotFound(w, r)
		case "/api/v0/cat":
			mu.Lock()
			if cidValue == oversizeCID {
				sawCatForOversize = true
			}
			mu.Unlock()
			_, _ = w.Write([]byte("small-ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ipfs.Close()

	fetcher := newIPFSTipFetcher(ipfs.URL, 1024) // 1 KiB cap; stat reports 1 MiB for oversizeCID

	_, err := fetcher.Fetch(context.Background(), oversizeCID)
	if err == nil || !errors.Is(err, sdnpubsub.ErrFetchTooLarge) {
		t.Fatalf("Fetch(oversize) error = %v, want an error wrapping sdnpubsub.ErrFetchTooLarge", err)
	}
	mu.Lock()
	calledCat := sawCatForOversize
	mu.Unlock()
	if calledCat {
		t.Fatal("stat pre-check should have rejected the oversize CID before ever calling /api/v0/cat")
	}

	data, err := fetcher.Fetch(context.Background(), undersizeCID)
	if err != nil {
		t.Fatalf("Fetch(undersize) unexpected error: %v", err)
	}
	if string(data) != "small-ok" {
		t.Fatalf("Fetch(undersize) data = %q, want %q", data, "small-ok")
	}
}

// TestIPFSTipFetcherHardLimitsReadWhenStatUnavailable drives the real
// ipfsTipFetcher against a fake Kubo server that does NOT implement
// /api/v0/block/stat (as the D1 wiring test's fake server, and real Kubo
// for a chunked UnixFS CID whose single-block stat wouldn't reflect the
// file's cumulative size, both effectively behave) to prove the hard
// io.LimitReader-based read cap is what actually enforces the ceiling when
// no reliable pre-check size is available.
func TestIPFSTipFetcherHardLimitsReadWhenStatUnavailable(t *testing.T) {
	const oversizeCID = "bafyoversizeread"
	payload := bytes.Repeat([]byte{0x41}, 2048) // 2 KiB, over the cap below

	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/cat":
			_, _ = w.Write(payload)
		default:
			// No /api/v0/block/stat support -- forces the hard-limit
			// fallback path in ipfsTipFetcher.Fetch.
			http.NotFound(w, r)
		}
	}))
	defer ipfs.Close()

	fetcher := newIPFSTipFetcher(ipfs.URL, 1024) // 1 KiB cap, payload is 2 KiB

	_, err := fetcher.Fetch(context.Background(), oversizeCID)
	if err == nil || !errors.Is(err, sdnpubsub.ErrFetchTooLarge) {
		t.Fatalf("Fetch error = %v, want an error wrapping sdnpubsub.ErrFetchTooLarge", err)
	}
}

// TestIPFSTipFetcherAllowsContentExactlyAtCap is a boundary check for the
// off-by-one behavior of the maxBytes+1 LimitReader in ipfsTipFetcher.Fetch.
func TestIPFSTipFetcherAllowsContentExactlyAtCap(t *testing.T) {
	const cidValue = "bafyexactcap"
	payload := bytes.Repeat([]byte{0x42}, 1024)

	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/cat":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ipfs.Close()

	fetcher := newIPFSTipFetcher(ipfs.URL, int64(len(payload)))

	data, err := fetcher.Fetch(context.Background(), cidValue)
	if err != nil {
		t.Fatalf("Fetch at exactly the cap should succeed, got error: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("Fetch returned %d bytes, want %d", len(data), len(payload))
	}
}

// TestTipQueueRejectsOversizeIPFSFetchWithoutPinningEndToEnd exercises the
// full D1+D4 chain through the SAME real adapters buildTipQueue wires
// (ipfsTipFetcher, ipfsTipPinner) against a fake Kubo server, mirroring
// TestBuildTipQueueWiresRealIPFSFetcherAndPinnerWhenConfigured's pattern:
// an oversize tip must be fetched-and-rejected without ever reaching
// /api/v0/pin/add, while an undersize tip in the same queue still flows
// through to a real pin call.
func TestTipQueueRejectsOversizeIPFSFetchWithoutPinningEndToEnd(t *testing.T) {
	const oversizeCID = "bafyoversizeflow"
	const undersizeCID = "bafyunderflow"

	var mu sync.Mutex
	pinned := make(map[string]bool)
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cidValue := r.URL.Query().Get("arg")
		switch r.URL.Path {
		case "/api/v0/cat":
			if cidValue == oversizeCID {
				_, _ = w.Write(bytes.Repeat([]byte{0x43}, 4096))
				return
			}
			_, _ = w.Write([]byte("ok"))
		case "/api/v0/pin/add":
			mu.Lock()
			pinned[cidValue] = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"Pins":["` + cidValue + `"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ipfs.Close()

	tqConfig := sdnpubsub.NewTipQueueConfig()
	tqConfig.DefaultAutoFetch = true
	tqConfig.DefaultAutoPin = true
	tqConfig.MaxFetchBytes = 1024 // smaller than the oversize payload above

	tq := sdnpubsub.NewTipQueue(tqConfig)
	tq.SetFetcher(newIPFSTipFetcher(ipfs.URL, tqConfig.MaxFetchBytes))
	tq.SetPinner(newIPFSTipPinner(ipfs.URL))

	tq.HandleMessage(&ps.Message{
		Message:      &pb.Message{Data: buildWiringTestPNM(t, oversizeCID, "unknown-schema")},
		ReceivedFrom: mustTestPeerID(t, 0x73),
	})
	tq.HandleMessage(&ps.Message{
		Message:      &pb.Message{Data: buildWiringTestPNM(t, undersizeCID, "unknown-schema")},
		ReceivedFrom: mustTestPeerID(t, 0x74),
	})

	if !waitForCondition(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return pinned[undersizeCID] && tq.OversizeRejections() >= 1
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("expected undersize CID pinned and an oversize rejection counted: pinned=%v rejections=%d", pinned, tq.OversizeRejections())
	}

	mu.Lock()
	oversizePinned := pinned[oversizeCID]
	mu.Unlock()
	if oversizePinned {
		t.Fatal("oversize content must not have reached /api/v0/pin/add")
	}
}

// --- shared helpers ----------------------------------------------------

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func mustTestPeerID(t *testing.T, seed byte) peer.ID {
	t.Helper()
	_, pub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{seed}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}
	return id
}

func pnmFileID(t *testing.T, pnmBytes []byte) string {
	t.Helper()
	if !PNM.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		t.Fatalf("test PNM missing identifier")
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	return string(pnm.FILE_ID())
}

func pnmCID(t *testing.T, pnmBytes []byte) string {
	t.Helper()
	if !PNM.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		t.Fatalf("test PNM missing identifier")
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	return string(pnm.CID())
}

// buildWiringTestPNM builds a PNM buffer that clears the TipQueue default
// verifier's structural checks (SIGNATURE_TYPE == "Ed25519", SIGNATURE
// decodes to 64 bytes) without asserting anything about the signature's
// cryptographic validity.
func buildWiringTestPNM(t *testing.T, cid, fileID string) []byte {
	t.Helper()

	signature := make([]byte, 64)
	for i := range signature {
		signature[i] = byte(i + 1)
	}

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	sigTypeOffset := builder.CreateString("Ed25519")
	sigOffset := builder.CreateString(hex.EncodeToString(signature))

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, sigTypeOffset)
	PNM.PNMAddSIGNATURE(builder, sigOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}
