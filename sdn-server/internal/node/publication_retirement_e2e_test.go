package node

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// Real producer -> replica -> requester: retirement survives reopen, leaves
// held files and records intact, and cannot resurrect a superseded publication.
// Explicit observation dates exercise policy; no wall-clock speed is asserted.
func TestPublicationRetirement(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	open := func(path string) *storage.FlatSQLStore {
		s, err := storage.NewFlatSQLStore(path, validator)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	producer := open(filepath.Join(root, "producer", "data"))
	defer producer.Close()
	replicaPath := filepath.Join(root, "replica", "data")
	replica := open(replicaPath)
	defer func() { replica.Close() }()
	provider, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	consumer, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	reader, err := libp2p.New(libp2p.NoListenAddrs, libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := consumer.Connect(ctx, peer.AddrInfo{ID: provider.ID(), Addrs: provider.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Connect(ctx, peer.AddrInfo{ID: consumer.ID(), Addrs: consumer.Addrs()}); err != nil {
		t.Fatal(err)
	}
	protocol.NewFlatSQLSyncHandler(producer).Register(provider)
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: provider.ID(), TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	n := &Node{host: consumer, store: replica, validator: validator, peerRegistry: registry, ctx: ctx,
		config: &config.Config{Storage: config.StorageConfig{Path: replicaPath}, Peers: config.PeersConfig{
			TrustedImport: config.TrustedImportConfig{Allow: []config.LaneSelector{{Schema: "OMM.fbs", ProviderID: "retirement-e2e", SourceName: "fixture"}}}}}}
	pubs := make([]storage.DatasetShardPublication, 4)
	for i, batch := range []string{"held", "lost", "recovered", "never-fetched"} {
		tags := storage.SourceTags{ProviderID: "retirement-e2e", SourceName: "fixture", BatchID: batch, ContentKeyID: "public"}
		record := sds.NewOMMBuilder().WithNoradCatID(uint32(900000 + i)).WithObjectName(batch).Build()
		if _, err := producer.StoreWithSourceTags("OMM.fbs", record, provider.ID().String(), nil, tags); err != nil {
			t.Fatal(err)
		}
		export, err := producer.ExportDatasetWindow(filepath.Join(producer.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{
			SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: batch, Limit: 10, AllowLargeResultSet: true})
		if err != nil {
			t.Fatal(err)
		}
		pub := storage.DatasetShardPublication{SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: batch,
			QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 10, RecordCount: export.RecordCount, ByteCount: export.ShardBytes,
			ShardCID: export.ShardCID, IndexCID: export.IndexCID, ShardSHA256: export.ShardSHA256, IndexSHA256: export.IndexSHA256,
			QuerySHA256: export.QuerySHA256, ResultSHA256: export.ResultSHA256, PublishedAt: time.Unix(1700000000, 0)}
		if err := producer.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatal(err)
		}
		pubs[i] = pub
		if batch == "never-fetched" {
			if err := os.Remove(export.ShardPath); err != nil {
				t.Fatal(err)
			}
			if _, err := n.materializeDatasetFeedHeadAnnouncement(ctx, syncwire.SegmentFromPublication(pub), provider.ID()); err == nil {
				t.Fatal("missing provider file was accepted")
			}
			continue
		}
		if count, err := n.materializeDatasetFeedHeadAnnouncement(ctx, syncwire.SegmentFromPublication(pub), provider.ID()); err != nil || count != 1 {
			t.Fatalf("import %s: %d, %v", batch, count, err)
		}
	}
	remote, err := n.listRemoteDatasetShardPublications(ctx, provider.ID(), "OMM.fbs")
	if err != nil || len(remote) != 3 {
		t.Fatalf("provider unavailable history: %d, %v", len(remote), err)
	}
	lost, err := replica.DatasetPublicationShardPath(pubs[1])
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := replica.DatasetPublicationIndexPath(pubs[2])
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lost); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(recovered); err != nil {
		t.Fatal(err)
	}
	observed := time.Unix(1800000000, 0)
	retire := func(at time.Time, want int) {
		got, err := replica.RetireUnavailableDatasetPublications(at)
		if err != nil || got != want {
			t.Fatalf("retired %d, want %d: %v", got, want, err)
		}
	}
	retire(observed, 0) // Old publication time is not evidence of long absence.
	retire(observed.Add(23*time.Hour), 0)
	if err := replica.Close(); err != nil {
		t.Fatal(err)
	}
	replica = open(replicaPath)
	n.store = replica
	if err := os.WriteFile(recovered, index, 0600); err != nil {
		t.Fatal(err)
	}
	retire(observed.Add(24*time.Hour), 1)
	if err := replica.Close(); err != nil {
		t.Fatal(err)
	}
	replica = open(replicaPath)
	n.store = replica
	protocol.NewFlatSQLSyncHandler(replica).Register(consumer)
	stream, response, err := syncwire.Call(ctx, reader, consumer.ID(), syncwire.OpListPublished, syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	rows, err := syncwire.Segments(response)
	if err != nil || len(rows) != 2 || response.TOTAL_COUNT() != 2 {
		t.Fatalf("listed %d rows, total %d: %v", len(rows), response.TOTAL_COUNT(), err)
	}
	for _, row := range rows {
		if (row.BatchID != "held" && row.BatchID != "recovered") || row.Availability != "available" {
			t.Fatalf("unexpected listing: %+v", row)
		}
	}
	// A replay of the old announcement cannot refill the serving index.
	if count, err := n.materializeDatasetFeedHeadAnnouncement(ctx, syncwire.SegmentFromPublication(pubs[1]), provider.ID()); err != nil || count != 0 {
		t.Fatalf("retired feed replay: %d, %v", count, err)
	}
	if cached, err := n.datasetShardPublicationAlreadyCached(pubs[1]); err != nil || !cached {
		t.Fatalf("retirement dedup: %v, %v", cached, err)
	}
	if _, err := os.Stat(lost); !os.IsNotExist(err) {
		t.Fatalf("retired file was fetched again: %v", err)
	}
	held, err := replica.DatasetPublicationShardPath(pubs[0])
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(held)
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err = syncwire.Call(ctx, reader, consumer.ID(), syncwire.OpReadShard, syncwire.QRPFields{SchemaName: "OMM.fbs", CID: pubs[0].ShardCID})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(stream)
	stream.Close()
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("held shard changed: %v", err)
	}
	records, err := replica.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "retirement-e2e", SourceName: "fixture", Limit: 10})
	if err != nil || len(records) != 3 {
		t.Fatalf("retirement changed stored records: %d, %v", len(records), err)
	}
}
