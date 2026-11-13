package protocol

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

func TestPublishedCompletion(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "store")
	store, err := storage.NewFlatSQLStore(root, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	tags := storage.SourceTags{ProviderID: "completion-e2e", SourceName: "fixture", BatchID: "batch", ContentKeyID: "public"}
	for i := 0; i < 2; i++ {
		record := sds.NewOMMBuilder().WithNoradCatID(uint32(999998 + i)).WithObjectName("Completion fixture").Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "fixture", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	lane := storage.DatasetPublicationLane{SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName}
	parts := []storage.DatasetShardPublication{}
	series := storage.DatasetPublicationSeries{SchemaName: lane.SchemaName, ProviderID: lane.ProviderID, SourceName: lane.SourceName, BatchID: tags.BatchID, RecordCount: 2, PublishedAt: time.Now()}
	for offset := 0; offset < 2; offset++ {
		export, err := store.ExportDatasetWindow(filepath.Join(store.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, Offset: offset, Limit: 1, AllowLargeResultSet: true})
		if err != nil {
			t.Fatal(err)
		}
		pub := storage.DatasetShardPublication{SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, QueryProfile: storage.DatasetPublicationQueryProfile, Offset: offset, Limit: 1, RecordCount: export.RecordCount, ByteCount: export.ShardBytes, ShardCID: export.ShardCID, IndexCID: export.IndexCID, ShardSHA256: export.ShardSHA256, IndexSHA256: export.IndexSHA256, QuerySHA256: export.QuerySHA256, ResultSHA256: export.ResultSHA256, PublishedAt: time.Now()}
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, pub)
		series.Members = append(series.Members, storage.DatasetPublicationSeriesMember{CID: pub.ShardCID, Role: storage.PinLedgerRoleShard}, storage.DatasetPublicationSeriesMember{CID: pub.IndexCID, Role: storage.PinLedgerRoleIndex})
	}
	series.SeriesID = storage.DatasetPublicationSeriesID(lane, tags.BatchID, parts)
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	reader, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	NewFlatSQLSyncHandler(store).Register(h)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := reader.Connect(ctx, peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}); err != nil {
		t.Fatal(err)
	}
	list := func(complete bool) []byte {
		s, err := reader.NewStream(ctx, h.ID(), libprotocol.ID(syncwire.ProtocolID("list-published")))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))
		if err := syncwire.Request(s, syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 128}); err != nil {
			t.Fatal(err)
		}
		q, data, err := syncwire.ReadPage(s)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := syncwire.Segments(q)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("want two exact-size shards, got %d", len(rows))
		}
		for _, row := range rows {
			if row.RecordCount != 1 || row.Limit != 1 {
				t.Fatal("fixture must have no short final shard")
			}
			if complete {
				if row.BatchCompletion != "complete" || row.BatchRevision != series.SeriesID || row.BatchTotalRecords != 2 {
					t.Fatalf("missing authoritative completion: %+v", row)
				}
			} else if row.BatchCompletion != "" || row.BatchTotalRecords != 0 || row.BatchRevision != "" {
				t.Fatal("unrecorded or adopted legacy data claimed completion")
			}
		}
		return data
	}
	list(false)
	if _, err := store.AdoptDatasetPublicationSeries(lane, h.ID().String()); err != nil {
		t.Fatal(err)
	}
	list(false)
	if err := store.RecordDatasetPublicationSeries(series, h.ID().String()); err != nil {
		t.Fatal(err)
	}
	list(true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewFlatSQLStore(root, validator)
	if err != nil {
		t.Fatal(err)
	}
	NewFlatSQLSyncHandler(store).Register(h)
	data := list(true)
	if path := os.Getenv("SDN_COMPLETION_GOLDEN"); path != "" {
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
