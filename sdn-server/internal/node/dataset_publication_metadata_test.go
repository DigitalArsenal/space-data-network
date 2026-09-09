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
