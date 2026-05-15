package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestDatasetPublicationFileIDSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fileID string
		want   string
	}{
		{
			name:   "series file id",
			fileID: "celestrak-gp:OMM.fbs:source-sha:part-000001",
			want:   "OMM.fbs",
		},
		{
			name:   "plain dataset schema",
			fileID: "CAT.fbs",
			want:   "CAT.fbs",
		},
		{
			name:   "no schema",
			fileID: "celestrak-provider-update",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := datasetPublicationFileIDSchema(tt.fileID); got != tt.want {
				t.Fatalf("datasetPublicationFileIDSchema(%q) = %q, want %q", tt.fileID, got, tt.want)
			}
		})
	}
}

func TestMaterializeDatasetFeedHeadAnnouncementImportsShardOverFlatSQLSync(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("provider store: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("subscriber store: %v", err)
	}
	defer subscriberStore.Close()

	priv, pub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x62}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	providerID, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}
	rawPriv, err := priv.Raw()
	if err != nil || len(rawPriv) == 0 {
		t.Fatalf("raw provider private key failed: %v", err)
	}

	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "feed-head-batch",
		ContentKeyID: "public",
	}
	record := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectID("1998-067A").
		WithObjectName("ISS").
		Build()
	if _, err := providerStore.StoreWithSourceTags("OMM.fbs", record, providerID.String(), nil, tags); err != nil {
		t.Fatalf("store provider OMM: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(providerStore.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{
		SchemaName:          "OMM.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	pubMeta := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        10,
		RecordCount:  export.RecordCount,
		ByteCount:    export.ShardBytes,
		ShardCID:     export.ShardCID,
		IndexCID:     export.IndexCID,
		ManifestCID:  "bafyfeedmanifest",
		ShardSHA256:  export.ShardSHA256,
		IndexSHA256:  export.IndexSHA256,
		QuerySHA256:  export.QuerySHA256,
		ResultSHA256: export.ResultSHA256,
		PublishedAt:  time.Unix(1700003333, 0).UTC(),
	}
	if err := providerStore.UpsertDatasetShardPublication(pubMeta); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}
	published, found, err := providerStore.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   pubMeta.SchemaName,
		ProviderID:   pubMeta.ProviderID,
		SourceName:   pubMeta.SourceName,
		BatchID:      pubMeta.BatchID,
		QueryProfile: pubMeta.QueryProfile,
		Offset:       pubMeta.Offset,
		Limit:        pubMeta.Limit,
	})
	if err != nil || !found {
		t.Fatalf("FindDatasetShardPublication failed: found=%v err=%v", found, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	providerHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.Identity(priv))
	if err != nil {
		t.Fatalf("provider host: %v", err)
	}
	defer providerHost.Close()
	providerHost.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(providerStore).HandleStream)
	subscriberHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("subscriber host: %v", err)
	}
	defer subscriberHost.Close()
	subscriberHost.Peerstore().AddAddrs(providerHost.ID(), providerHost.Addrs(), peerstore.PermanentAddrTTL)
	if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: providerHost.ID(), Addrs: providerHost.Addrs()}); err != nil {
		t.Fatalf("connect provider: %v", err)
	}

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: providerHost.ID(), TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add trusted provider: %v", err)
	}
	n := &Node{
		host:         subscriberHost,
		store:        subscriberStore,
		peerRegistry: registry,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "subscriber-storage")},
		},
		ctx: ctx,
	}
	shardBytes := mustReadNodeTestFile(t, export.ShardPath)
	indexBytes := mustReadNodeTestFile(t, export.IndexPath)
	shardSum := sha256.Sum256(shardBytes)
	indexSum := sha256.Sum256(indexBytes)

	imported, err := n.materializeDatasetFeedHeadAnnouncement(ctx, sdnpubsub.DatasetFeedHeadAnnouncement{
		MessageType:  sdnpubsub.DatasetFeedHeadMessageType,
		Schema:       published.SchemaName,
		ProviderID:   published.ProviderID,
		SourceName:   published.SourceName,
		BatchID:      published.BatchID,
		QueryProfile: published.QueryProfile,
		Offset:       published.Offset,
		Limit:        published.Limit,
		FeedSequence: published.FeedSequence,
		FeedHead:     published.FeedHead,
		RecordCount:  published.RecordCount,
		ByteCount:    published.ByteCount,
		ShardCID:     published.ShardCID,
		IndexCID:     published.IndexCID,
		ManifestCID:  published.ManifestCID,
		PublishedAt:  published.PublishedAt,
		PreviousHead: published.PreviousHead,
	}, providerHost.ID())
	if err != nil {
		t.Fatalf("materializeDatasetFeedHeadAnnouncement failed: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}

	records, err := subscriberStore.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query subscriber records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("subscriber records = %d, want 1", len(records))
	}
	if hex.EncodeToString(shardSum[:]) != published.ShardSHA256 || hex.EncodeToString(indexSum[:]) != published.IndexSHA256 {
		t.Fatalf("test publication hashes were not bound")
	}
	replicaPub, found, err := subscriberStore.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   published.SchemaName,
		ProviderID:   published.ProviderID,
		SourceName:   published.SourceName,
		BatchID:      published.BatchID,
		QueryProfile: published.QueryProfile,
		Offset:       published.Offset,
		Limit:        published.Limit,
	})
	if err != nil || !found {
		t.Fatalf("replica publication metadata missing: found=%v err=%v", found, err)
	}
	if replicaPub.ShardCID != published.ShardCID || replicaPub.IndexCID != published.IndexCID || replicaPub.FeedHead != published.FeedHead {
		t.Fatalf("replica publication = %+v, want shard/index/feed head from %+v", replicaPub, published)
	}
	replicaShardPath, err := subscriberStore.DatasetPublicationShardPath(replicaPub)
	if err != nil {
		t.Fatalf("DatasetPublicationShardPath replica failed: %v", err)
	}
	replicaIndexPath, err := subscriberStore.DatasetPublicationIndexPath(replicaPub)
	if err != nil {
		t.Fatalf("DatasetPublicationIndexPath replica failed: %v", err)
	}
	if _, err := os.Stat(replicaShardPath); err != nil {
		t.Fatalf("replica shard file missing: %v", err)
	}
	if _, err := os.Stat(replicaIndexPath); err != nil {
		t.Fatalf("replica index file missing: %v", err)
	}
}

func TestCatchUpDatasetShardPublicationsListsRemoteShardIndex(t *testing.T) {
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatalf("provider store: %v", err)
	}
	defer providerStore.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatalf("subscriber store: %v", err)
	}
	defer subscriberStore.Close()

	priv, pub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x73}, 128)))
	if err != nil {
		t.Fatalf("GenerateEd25519Key failed: %v", err)
	}
	providerID, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatalf("IDFromPublicKey failed: %v", err)
	}
	tags := storage.SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "direct-list-batch",
		ContentKeyID: "public",
	}
	record := sds.NewOMMBuilder().
		WithNoradCatID(33591).
		WithObjectID("2009-005A").
		WithObjectName("NOAA 19").
		Build()
	if _, err := providerStore.StoreWithSourceTags("OMM.fbs", record, providerID.String(), nil, tags); err != nil {
		t.Fatalf("store provider OMM: %v", err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(providerStore.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{
		SchemaName:          "OMM.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	published := storage.DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        10,
		RecordCount:  export.RecordCount,
		ByteCount:    export.ShardBytes,
		ShardCID:     export.ShardCID,
		IndexCID:     export.IndexCID,
		ManifestCID:  "bafydirectmanifest",
		ShardSHA256:  export.ShardSHA256,
		IndexSHA256:  export.IndexSHA256,
		QuerySHA256:  export.QuerySHA256,
		ResultSHA256: export.ResultSHA256,
		PublishedAt:  time.Unix(1700004444, 0).UTC(),
	}
	if err := providerStore.UpsertDatasetShardPublication(published); err != nil {
		t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	providerHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.Identity(priv))
	if err != nil {
		t.Fatalf("provider host: %v", err)
	}
	defer providerHost.Close()
	providerHost.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(providerStore).HandleStream)
	subscriberHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("subscriber host: %v", err)
	}
	defer subscriberHost.Close()
	subscriberHost.Peerstore().AddAddrs(providerHost.ID(), providerHost.Addrs(), peerstore.PermanentAddrTTL)
	if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: providerHost.ID(), Addrs: providerHost.Addrs()}); err != nil {
		t.Fatalf("connect provider: %v", err)
	}

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: providerHost.ID(), TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add trusted provider: %v", err)
	}
	n := &Node{
		host:         subscriberHost,
		validator:    validator,
		store:        subscriberStore,
		peerRegistry: registry,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "subscriber-storage")},
		},
		ctx: ctx,
	}

	materialized, err := n.catchUpDatasetShardPublicationsFromPeer(ctx, providerHost.ID())
	if err != nil {
		t.Fatalf("catchUpDatasetShardPublicationsFromPeer failed: %v", err)
	}
	if materialized != 1 {
		t.Fatalf("materialized = %d, want 1", materialized)
	}
	records, err := subscriberStore.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "OMM.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("query subscriber records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("subscriber records = %d, want 1", len(records))
	}
	replicaPub, found, err := subscriberStore.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   published.SchemaName,
		ProviderID:   published.ProviderID,
		SourceName:   published.SourceName,
		BatchID:      published.BatchID,
		QueryProfile: published.QueryProfile,
		Offset:       published.Offset,
		Limit:        published.Limit,
	})
	if err != nil || !found {
		t.Fatalf("replica publication metadata missing: found=%v err=%v", found, err)
	}
	if replicaPub.ShardCID != published.ShardCID || replicaPub.IndexCID != published.IndexCID {
		t.Fatalf("replica publication = %+v, want %+v", replicaPub, published)
	}
}

func mustReadNodeTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s failed: %v", path, err)
	}
	return data
}
