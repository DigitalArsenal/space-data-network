package protocol

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
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

func TestPublishedAvailability(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tags := storage.SourceTags{ProviderID: "availability-e2e", SourceName: "fixture", BatchID: "batch", ContentKeyID: "public"}
	record := sds.NewOMMBuilder().WithNoradCatID(999999).WithObjectName("Availability fixture").Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", record, "fixture", nil, tags); err != nil {
		t.Fatal(err)
	}
	export, err := store.ExportDatasetWindow(filepath.Join(store.DatasetPublicationOutputDir(), "OMM"), storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, Limit: 10, AllowLargeResultSet: true})
	if err != nil {
		t.Fatal(err)
	}
	pub := storage.DatasetShardPublication{SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 10, RecordCount: export.RecordCount, ByteCount: export.ShardBytes, ShardCID: export.ShardCID, IndexCID: export.IndexCID, ShardSHA256: export.ShardSHA256, IndexSHA256: export.IndexSHA256, QuerySHA256: export.QuerySHA256, ResultSHA256: export.ResultSHA256, PublishedAt: time.Now()}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatal(err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := reader.Connect(ctx, peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}); err != nil {
		t.Fatal(err)
	}
	request := func(op string, f syncwire.QRPFields) []byte {
		s, err := reader.NewStream(ctx, h.ID(), libprotocol.ID(syncwire.ProtocolID(op)))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))
		if err := syncwire.Request(s, f); err != nil {
			t.Fatal(err)
		}
		_ = s.CloseWrite()
		data, err := io.ReadAll(io.LimitReader(s, 2<<20))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	list := func(state, reason string) []byte {
		data := request("list-published", syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 1})
		q, err := syncwire.ParseQRP(data)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := syncwire.Segments(q)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || q.TOTAL_COUNT() != 1 {
			t.Fatalf("publication disappeared: %d rows, kind %d", len(rows), q.KIND())
		}
		row := rows[0]
		if row.Availability != state || row.AvailabilityReason != reason || row.AvailabilityCheckedAtMs <= 0 || row.ShardCID != pub.ShardCID || row.RecordCount != int64(pub.RecordCount) {
			t.Fatalf("wrong availability: %+v", row)
		}
		if bytes.Contains(data, []byte(store.DatasetPublicationOutputDir())) {
			t.Fatal("private filesystem path leaked")
		}
		return data
	}
	list("available", "")
	shard, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(shard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	missing := list("unavailable", "shard_missing")
	if path := os.Getenv("SDN_AVAILABILITY_GOLDEN"); path != "" {
		if err := os.WriteFile(path, missing, 0644); err != nil {
			t.Fatal(err)
		}
	}
	reply := request("read-shard", syncwire.QRPFields{SchemaName: "OMM.fbs", CID: pub.ShardCID, MaxBytes: 1})
	q, err := syncwire.ParseQRP(reply)
	if err != nil {
		t.Fatal(err)
	}
	if int8(q.KIND()) != syncwire.QRPKindError {
		t.Fatal("missing shard was served")
	}
	if err := os.WriteFile(shard, original, 0644); err != nil {
		t.Fatal(err)
	}
	list("available", "")
	index, err := store.DatasetPublicationIndexPath(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	list("unavailable", "index_missing")
}
