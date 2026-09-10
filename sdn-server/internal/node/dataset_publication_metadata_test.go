package node

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func metadataPublication(t *testing.T, h host.Host) (*storage.DatasetPublicationManifest, []byte) {
	t.Helper()
	raw, err := h.Peerstore().PrivKey(h.ID()).Raw()
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.PrivateKey(raw)
	export, err := storage.ExportDatasetRecords(t.TempDir(), storage.IndexedRecordQuery{
		SchemaName: "OMM.fbs", ProviderID: "provider", SourceName: "orbits", Limit: 10,
	}, []storage.DatasetExportRecord{{Data: sds.NewOMMBuilder().WithNoradCatID(25544).Build(), SourceTags: storage.SourceTags{ProviderID: "provider", SourceName: "orbits", BatchID: "current", ContentKeyID: "public"}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest, err := storage.BuildSignedDatasetPublicationManifest(t.TempDir(), storage.DatasetPublicationManifestOptions{
		Export: export, DatasetID: "provider", UpdateID: "current", FileID: "provider:OMM.fbs:current", ProviderPeerID: h.ID().String(), PublishedAt: now, SigningKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	pnm, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{PublishedAt: now, SigningKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, pnm
}

func TestDatasetMetadataCrossesRelayWithoutGrantingTrustOrFetchingRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relay, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	publisher := connectPeer(t, ctx, relay)
	defer publisher.Close()
	receiver := connectPeer(t, ctx, relay)
	defer receiver.Close()
	manifest, pnm := metadataPublication(t, publisher)
	var fetches, unexpected atomic.Int32
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("arg") != manifest.CID {
			unexpected.Add(1)
			http.Error(w, "unrequested block", 400)
			return
		}
		switch r.URL.Path {
		case "/api/v0/block/stat":
			fmt.Fprintf(w, `{"Size":%d}`, len(manifest.Bytes))
		case "/api/v0/cat":
			fetches.Add(1)
			w.Write(manifest.Bytes)
		default:
			unexpected.Add(1)
			http.Error(w, "no raw data or pins allowed", 400)
		}
	}))
	defer ipfs.Close()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := peers.NewRegistry(true, nil)
	n := &Node{host: receiver, store: store, peerRegistry: registry, config: &config.Config{Admin: config.AdminConfig{IPFSAPIURL: ipfs.URL}}, ctx: ctx, protocol: protocol.NewSDSExchangeHandler(store, validator)}
	n.protocol.SetPubSubPNMHandler(n.handleDatasetPublicationPNM)
	// Wrong publisher and a hard distrust veto reject before any IPFS fetch.
	if err := n.cacheDatasetPublicationMetadata(ctx, "PNM.fbs", pnm, relay.ID()); err == nil {
		t.Fatal("accepted relay key as publisher")
	}
	if err := registry.AddPeer(&peers.TrustedPeer{ID: publisher.ID(), TrustLevel: peers.Never}); err != nil {
		t.Fatal(err)
	}
	if err := n.cacheDatasetPublicationMetadata(ctx, "PNM.fbs", pnm, publisher.ID()); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 0 {
		t.Fatal("fetched a rejected announcement")
	}
	if err := registry.RemovePeer(publisher.ID()); err != nil {
		t.Fatal(err)
	}

	const topicName = "dataset-catalog-relay-test"
	relayPS, err := newGossipSub(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	relayTopic, err := relayPS.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	relaySub, err := relayTopic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer relaySub.Cancel()
	receiverPS, err := newGossipSub(ctx, receiver)
	if err != nil {
		t.Fatal(err)
	}
	receiverTopic, err := receiverPS.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := receiverTopic.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	publisherPS, err := newGossipSub(ctx, publisher)
	if err != nil {
		t.Fatal(err)
	}
	publisherTopic, err := publisherPS.Join(topicName)
	if err != nil {
		t.Fatal(err)
	}
	waitForTopicPeer(t, publisherTopic, relay.ID())
	waitForTopicPeer(t, relayTopic, receiver.ID())
	waitForDatasetMetadataRelay(t, ctx, publisherTopic, sub)
	n.wg.Add(1)
	go n.handleSubscription(sub, "PNM.fbs")
	defer func() { cancel(); n.wg.Wait() }()
	if err := publisherTopic.Publish(ctx, pnm); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := channels.ReadDatasetCatalog(store, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 1 {
			if entries[0].PeerID != publisher.ID().String() || entries[0].Rows != 1 {
				t.Fatalf("wrong publisher metadata: %+v", entries)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relayed publication never reached catalog")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := n.cacheDatasetPublicationMetadata(ctx, "OMM.fbs", pnm, publisher.ID()); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 1 || unexpected.Load() != 0 {
		t.Fatalf("fetches=%d unexpected=%d", fetches.Load(), unexpected.Load())
	}
	if registry.IsTrusted(publisher.ID()) {
		t.Fatal("metadata granted trust")
	}
	records, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil || len(records) != 0 {
		t.Fatalf("remote records materialized: %d %v", len(records), err)
	}
	pins, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{})
	if err != nil || len(pins) != 0 {
		t.Fatalf("remote data pinned: %v %v", pins, err)
	}
	// Both end hosts only have a direct connection to the relay.
	for _, connection := range receiver.Network().ConnsToPeer(peer.ID(publisher.ID())) {
		t.Fatalf("test unexpectedly connected publisher directly: %v", connection)
	}
}

// Subscription announcements can arrive before the relay's forwarding mesh is
// ready. Establish both hops with non-record traffic before publishing the
// single signed PNM whose handling this test measures.
func waitForDatasetMetadataRelay(t *testing.T, ctx context.Context, topic *pubsub.Topic, sub *pubsub.Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const probe = "dataset-catalog-relay-ready"
	ready := make(chan error, 1)
	go func() {
		for {
			message, err := sub.Next(ctx)
			if err != nil {
				ready <- err
				return
			}
			if string(message.Data) == probe {
				ready <- nil
				return
			}
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := topic.Publish(ctx, []byte(probe)); err != nil {
			t.Fatalf("relay readiness publish: %v", err)
		}
		select {
		case err := <-ready:
			if err != nil {
				t.Fatalf("relay readiness receive: %v", err)
			}
			return
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("relay forwarding mesh never became ready")
		}
	}
}

func TestDatasetMetadataReplaysSkippedAnnouncementAfterRestartWithoutTrust(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	publisher, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	manifest, pnm := metadataPublication(t, publisher)
	var fetches, unexpected atomic.Int32
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("arg") != manifest.CID {
			unexpected.Add(1)
			http.Error(w, "only the signed manifest may be fetched", 400)
			return
		}
		switch r.URL.Path {
		case "/api/v0/block/stat":
			fmt.Fprintf(w, `{"Size":%d}`, len(manifest.Bytes))
		case "/api/v0/cat":
			fetches.Add(1)
			w.Write(manifest.Bytes)
		default:
			unexpected.Add(1)
			http.Error(w, "raw data and pins are forbidden", 400)
		}
	}))
	defer ipfs.Close()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	storePath := t.TempDir()
	store, err := storage.NewFlatSQLStore(storePath, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	registry := peers.NewRegistry(true, nil)
	n := &Node{store: store, peerRegistry: registry, config: &config.Config{Admin: config.AdminConfig{IPFSAPIURL: ipfs.URL}}, ctx: ctx, protocol: protocol.NewSDSExchangeHandler(store, validator)}
	n.protocol.SetPubSubPNMHandler(n.handleDatasetPublicationPNM)
	// Another metadata fetch is in flight. The real protocol must preserve
	// this announcement even though the immediate cache callback skips it.
	n.datasetCatalogMu.Lock()
	err = n.protocol.HandlePubSubMessage("PNM.fbs", pnm, publisher.ID())
	n.datasetCatalogMu.Unlock()
	if err != nil || fetches.Load() != 0 {
		t.Fatalf("skipped live announcement: fetches=%d err=%v", fetches.Load(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewFlatSQLStore(storePath, validator)
	if err != nil {
		t.Fatal(err)
	}
	n.store = store
	if err := registry.AddPeer(&peers.TrustedPeer{ID: publisher.ID(), TrustLevel: peers.Never}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.materializeStoredDatasetPublicationPNMs(ctx, 10); err != nil || fetches.Load() != 0 {
		t.Fatalf("hard distrust must prevent catch-up fetch: %v", err)
	}
	if err := registry.RemovePeer(publisher.ID()); err != nil {
		t.Fatal(err)
	}
	if materialized, err := n.materializeStoredDatasetPublicationPNMs(ctx, 10); err != nil || materialized != 0 {
		t.Fatalf("metadata replay materialized records: %d %v", materialized, err)
	}
	entries, err := channels.ReadDatasetCatalog(store, time.Now())
	if err != nil || len(entries) != 1 || entries[0].PeerID != publisher.ID().String() {
		t.Fatalf("received announcement was not recovered after reopen: %+v %v", entries, err)
	}
	if _, err := n.materializeStoredDatasetPublicationPNMs(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if fetches.Load() != 1 || unexpected.Load() != 0 || registry.IsTrusted(publisher.ID()) {
		t.Fatalf("unexpected fetch/trust: fetches=%d unexpected=%d", fetches.Load(), unexpected.Load())
	}
	records, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil || len(records) != 0 {
		t.Fatalf("metadata replay fetched raw records: %v %v", records, err)
	}
	pins, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{})
	if err != nil || len(pins) != 0 {
		t.Fatalf("metadata replay pinned data: %v %v", pins, err)
	}
}

func TestDatasetMetadataReplayRotatesWithinBoundsAndHonorsCancellation(t *testing.T) {
	n := &Node{}
	records := make([]*storage.Record, 300)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.cacheStoredDatasetCatalogs(ctx, records)
	if n.datasetCatalogReplayAt != 0 {
		t.Fatal("cancelled catch-up consumed work")
	}
	n.cacheStoredDatasetCatalogs(context.Background(), records)
	if n.datasetCatalogReplayAt != 128 {
		t.Fatalf("first bounded pass stopped at %d", n.datasetCatalogReplayAt)
	}
	n.cacheStoredDatasetCatalogs(context.Background(), records)
	if n.datasetCatalogReplayAt != 256 {
		t.Fatalf("second pass repeated the newest records: %d", n.datasetCatalogReplayAt)
	}
	n.cacheStoredDatasetCatalogs(context.Background(), records)
	if n.datasetCatalogReplayAt != 84 {
		t.Fatalf("replay failed to wrap the bounded snapshot: %d", n.datasetCatalogReplayAt)
	}
}
