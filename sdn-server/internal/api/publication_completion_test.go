package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

func TestPublishedCompletion(t *testing.T) {
	const publicationListBytes = 1 << 20
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
	result := &DatasetPublicationResult{Schema: "OMM.fbs", RecordCount: 2}
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
		result.Publications = append(result.Publications, DatasetPublicationResult{Schema: pub.SchemaName, RecordCount: pub.RecordCount, ShardCID: pub.ShardCID, IndexCID: pub.IndexCID})
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
	protocol.NewFlatSQLSyncHandler(store).Register(h)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := reader.Connect(ctx, peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}); err != nil {
		t.Fatal(err)
	}
	read := func(fields syncwire.QRPFields) []byte {
		s, err := reader.NewStream(ctx, h.ID(), libprotocol.ID(syncwire.ProtocolID("list-published")))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))
		if err := syncwire.Request(s, fields); err != nil {
			t.Fatal(err)
		}
		_, data, err := syncwire.ReadPage(s)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > publicationListBytes {
			t.Fatal("publication page exceeds byte cap")
		}
		return data
	}
	list := func(complete bool) []byte {
		data := read(syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 128})
		q, err := syncwire.ParseQRP(data)
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
	// Exercise the production producer finalizer without signing or announcing an artifact.
	service := &ConcreteDatasetPublicationService{store: store, providerPeerID: h.ID().String()}
	req := DatasetPublicationRequest{Schema: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID}
	if err := service.afterPublication(ctx, req, "OMM.fbs", result, nil, "", series.PublishedAt); err != nil {
		t.Fatal(err)
	}
	list(true)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.afterPublication(ctx, req, "OMM.fbs", result, nil, "", series.PublishedAt); err == nil {
		t.Fatal("producer reported completion after its persistence store closed")
	}
	store, err = storage.NewFlatSQLStore(root, validator)
	if err != nil {
		t.Fatal(err)
	}
	protocol.NewFlatSQLSyncHandler(store).Register(h)
	data := list(true)
	if path := os.Getenv("SDN_COMPLETION_GOLDEN"); path != "" {
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	// The revision/total survive pagination, not only a single full listing.
	cursor := ""
	for i := 0; i < 2; i++ {
		q, err := syncwire.ParseQRP(read(syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 1, Cursor: cursor}))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := syncwire.Segments(q)
		if err != nil || len(rows) != 1 || rows[0].Offset != int64(i) || rows[0].BatchRevision != series.SeriesID || q.TOTAL_COUNT() != 2 {
			t.Fatalf("wrong completed page: %+v %v", rows, err)
		}
		cursor = string(q.NEXT_CURSOR())
	}
	if cursor != "" {
		t.Fatal("last completion page has another cursor")
	}
	// A new manifest in the same batch is not a member of the completed revision.
	changed := parts[0]
	changed.ManifestCID = changed.ShardCID
	if err := store.UpsertDatasetShardPublication(changed); err != nil {
		t.Fatal(err)
	}
	q, err := syncwire.ParseQRP(read(syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 128}))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := syncwire.Segments(q)
	if err != nil || len(rows) != 2 || rows[0].BatchCompletion != "" || rows[1].BatchCompletion != "complete" {
		t.Fatalf("completion leaked to another revision: %+v %v", rows, err)
	}
	// Burst admission, typed refusal, recovery and peer churn are exercised
	// together over real nodes in protocol.TestListingReadBudget.
}
