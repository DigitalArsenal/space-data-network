package protocol

import (
	"bytes"
	"context"
	"errors"
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

// heldContent is an IPFS repository that holds the named content.
type heldContent map[string][]byte

func (h heldContent) Held(cid string) (int64, bool) {
	b, ok := h[cid]
	return int64(len(b)), ok
}

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func (h heldContent) Open(cid string) (io.ReadSeekCloser, int64, error) {
	b, ok := h[cid]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	return nopSeekCloser{bytes.NewReader(b)}, int64(len(b)), nil
}

// A follower's retention removes the files of a batch it no longer keeps as
// current, while its IPFS repository still holds their content: host-01 on
// 2026-11-21 listed four of host-02's five GP batches "shard_missing" while it
// pinned and served them over Bitswap, and a reader that drops unavailable
// rows had no earlier batch to fall back to. Held content in full counts as
// the file: the row lists available, and read-shard and read-index serve the
// content's bytes. Content of another size, or none, still does not.
func TestPublishedAvailabilityFromHeldContent(t *testing.T) {
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
	tags := storage.SourceTags{ProviderID: "held-e2e", SourceName: "fixture", BatchID: "batch", ContentKeyID: "public"}
	record := sds.NewOMMBuilder().WithNoradCatID(999997).WithObjectName("Held content fixture").Build()
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
	list := func(state, reason string) {
		t.Helper()
		q, err := syncwire.ParseQRP(request("list-published", syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 1}))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := syncwire.Segments(q)
		if err != nil || len(rows) != 1 {
			t.Fatalf("list-published: %d rows, %v", len(rows), err)
		}
		if rows[0].Availability != state || rows[0].AvailabilityReason != reason {
			t.Fatalf("availability %q (%q), want %q (%q)", rows[0].Availability, rows[0].AvailabilityReason, state, reason)
		}
	}
	// read reads a whole asset with read-shard or read-index; nil when refused.
	read := func(op, cid string, size int64) []byte {
		t.Helper()
		r := bytes.NewReader(request(op, syncwire.QRPFields{SchemaName: "OMM.fbs", CID: cid, MaxBytes: uint64(size)}))
		if _, _, err := syncwire.ReadPage(r); err != nil {
			var refused *syncwire.RemoteError
			if errors.As(err, &refused) {
				return nil
			}
			t.Fatalf("%s: %v", op, err)
		}
		rest, _ := io.ReadAll(r)
		return rest
	}

	shardPath, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatal(err)
	}
	indexPath, err := store.DatasetPublicationIndexPath(pub)
	if err != nil {
		t.Fatal(err)
	}
	shard, err := os.ReadFile(shardPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(shardPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	list("unavailable", "shard_missing")
	if read("read-shard", pub.ShardCID, int64(len(shard))) != nil {
		t.Fatal("a shard neither on disk nor held was served")
	}

	// The repository holds the shard but not the index.
	content := heldContent{pub.ShardCID: shard}
	store.SetPublicationContent(content)
	list("unavailable", "index_missing")

	// Both held: available, and both served from the repository, byte for byte.
	content[pub.IndexCID] = index
	list("available", "")
	if got := read("read-shard", pub.ShardCID, int64(len(shard))); !bytes.Equal(got, shard) {
		t.Fatalf("read-shard from held content: %d bytes, want the shard's %d", len(got), len(shard))
	}
	if got := read("read-index", pub.IndexCID, int64(len(index))); !bytes.Equal(got, index) {
		t.Fatalf("read-index from held content: %d bytes, want the index's %d", len(got), len(index))
	}

	// Held content of another size is not the shard the row names.
	content[pub.ShardCID] = shard[:len(shard)-1]
	list("unavailable", "shard_missing")
	if read("read-shard", pub.ShardCID, int64(len(shard))) != nil {
		t.Fatal("held content of the wrong size was served as the shard")
	}

	// Without a repository only the files count.
	store.SetPublicationContent(nil)
	list("unavailable", "shard_missing")
}
