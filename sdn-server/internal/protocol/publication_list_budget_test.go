package protocol

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/readbudget"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// One E2E for stack #643: the console's conservative first-minute workload,
// abusive reads and recovery, then more real peers than either budget retains.
func TestListingReadBudget(t *testing.T) {
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
	tags := storage.SourceTags{ProviderID: "read-budget-e2e", SourceName: "fixture", BatchID: "batch", ContentKeyID: "public"}
	record := sds.NewOMMBuilder().WithNoradCatID(999999).WithObjectName("Read budget fixture").Build()
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
	server, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	NewFlatSQLSyncHandler(store).Register(server)
	epm.RegisterPeerDirectory(server, peers.NewRegistry(false, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	newReader := func() host.Host {
		r, err := libp2p.New(libp2p.NoListenAddrs, libp2p.DisableRelay())
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
			_ = r.Close()
			t.Fatal(err)
		}
		return r
	}
	reader := newReader()
	defer reader.Close()
	listID := libprotocol.ID(syncwire.ProtocolID(syncwire.OpListPublished))
	read := func(r host.Host, id libprotocol.ID) error {
		t.Helper()
		s, err := r.NewStream(ctx, server.ID(), id)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(10 * time.Second))
		if err := syncwire.Request(s, syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 128}); err != nil {
			t.Fatal(err)
		}
		page, data, err := syncwire.ReadPage(s)
		if err != nil {
			return err
		}
		if len(data) > publicationListBytes {
			t.Fatal("listing exceeded the unchanged byte cap")
		}
		if id == listID {
			rows, err := syncwire.Segments(page)
			if err != nil || len(rows) != 1 || page.TOTAL_COUNT() != 1 || rows[0].ShardCID != pub.ShardCID || rows[0].RecordCount != 1 || rows[0].Availability != "available" {
				t.Fatalf("publication answer changed: %+v %v", rows, err)
			}
		} else {
			if string(page.PRODUCER_PEER_ID()) != server.ID().String() {
				t.Fatal("directory lost the producer identity")
			}
			if page.RECORD_COUNT() != 0 || page.TOTAL_COUNT() != 0 {
				t.Fatal("loopback-only readers leaked into the public directory")
			}
		}
		return nil
	}
	// 33 lane reads + 48 paginated poll reads + 5 catalog retry/discovery reads.
	const consoleLoadReads = 86
	for i := 0; i < consoleLoadReads; i++ {
		if err := read(reader, listID); err != nil {
			t.Fatalf("console read %d: %v", i+1, err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := read(reader, epm.PeerDirectoryProtocolID); err != nil {
			t.Fatalf("console directory read %d: %v", i+1, err)
		}
	}
	t.Log("all 86 publication and 4 directory startup reads returned the expected pages")
	for _, id := range []libprotocol.ID{listID, epm.PeerDirectoryProtocolID} {
		var refusal *syncwire.RemoteError
		for i := 0; i < 4096; i++ {
			err := read(reader, id)
			if err == nil {
				continue
			}
			if !errors.As(err, &refusal) || refusal.Code != "rate_limited" || refusal.HTTPStatus != 429 || refusal.RetryAfterMs == 0 {
				t.Fatalf("expected typed rate refusal from %s, got %v", id, err)
			}
			break
		}
		if refusal == nil {
			t.Fatalf("%s did not refuse excessive reads", id)
		}
		// Follow the server's retry advice; assert the response, not elapsed time.
		time.Sleep(time.Duration(refusal.RetryAfterMs) * time.Millisecond)
		if err := read(reader, id); err != nil {
			t.Fatalf("%s did not recover after its retry advice: %v", id, err)
		}
		t.Logf("%s refused excessive reads with 429 and served the same peer after retry", id)
	}
	// Connect one reader at a time: distinct authenticated identities without
	// retaining thousands of live hosts or opening any non-loopback connection.
	seen := map[peer.ID]bool{reader.ID(): true}
	for i := 0; i < readbudget.MaxPeers+1; i++ {
		next := newReader()
		if seen[next.ID()] {
			_ = next.Close()
			t.Fatal("peer churn reused an identity")
		}
		seen[next.ID()] = true
		for _, id := range []libprotocol.ID{listID, epm.PeerDirectoryProtocolID} {
			if err := read(next, id); err != nil {
				_ = next.Close()
				t.Fatalf("new peer %d refused by %s: %v", i+1, id, err)
			}
		}
		if err := next.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// The least recently seen original peer also gets a fresh burst after eviction.
	for i := 0; i < consoleLoadReads; i++ {
		if err := read(reader, listID); err != nil {
			t.Fatalf("returning console read %d: %v", i+1, err)
		}
	}
	if err := read(reader, epm.PeerDirectoryProtocolID); err != nil {
		t.Fatal(err)
	}
	t.Logf("both protocols served %d distinct peers and the returning console", len(seen))
}
