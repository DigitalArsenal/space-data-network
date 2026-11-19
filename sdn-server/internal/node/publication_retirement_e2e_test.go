package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// Real producer -> subscriber -> requester: retirement survives reopen, leaves
// held files and records intact, and cannot resurrect a superseded publication.
// A node imports a lane only through its subscription, and a subscriber keeps
// the records of a publication, not its files or its publication row, so the
// publication rows and files retirement acts on are the producer's own. The
// subscriber imports the producer's signed $PNM publications through the
// subscription gate and reads the producer's listing and shards over the wire.
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
	producerPath := filepath.Join(root, "producer", "data")
	producer := open(producerPath)
	defer func() { producer.Close() }()
	subscriber := open(filepath.Join(root, "subscriber", "data"))
	defer subscriber.Close()

	priv, _, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	rawPriv, err := priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := libp2p.New(libp2p.Identity(priv), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	consumer, err := libp2p.New(libp2p.NoListenAddrs, libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := consumer.Connect(ctx, peer.AddrInfo{ID: provider.ID(), Addrs: provider.Addrs()}); err != nil {
		t.Fatal(err)
	}
	protocol.NewFlatSQLSyncHandler(producer).Register(provider)

	// The producer's IPFS API: it serves each publication's manifest, and its
	// shard and index from the producer's files as they are at request time.
	var mu sync.Mutex
	requests := map[string]int{}
	objects := map[string]func() ([]byte, error){}
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cidValue := r.URL.Query().Get("arg")
		mu.Lock()
		requests[cidValue]++
		read, ok := objects[cidValue]
		mu.Unlock()
		if r.URL.Path != "/api/v0/cat" || !ok {
			http.Error(w, "not served: "+cidValue, http.StatusNotFound)
			return
		}
		data, err := read()
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	}))
	defer ipfs.Close()
	requestsFor := func(cid string) int {
		mu.Lock()
		defer mu.Unlock()
		return requests[cid]
	}

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: provider.ID(), TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	nodeCtx, stopNode := context.WithCancel(ctx)
	n := &Node{host: consumer, store: subscriber, validator: validator, peerRegistry: registry, ctx: nodeCtx, cancel: stopNode,
		config: &config.Config{Storage: config.StorageConfig{Path: filepath.Join(root, "subscriber")},
			Admin: config.AdminConfig{IPFSAPIURL: ipfs.URL}},
		datasetMaterializedPNMs: make(map[string]time.Time)}
	// Background work a materialization starts (the storage quota pass)
	// finishes before the stores close.
	defer func() {
		stopNode()
		n.wg.Wait()
	}()
	// The subscriber subscribes to the lane from the producer: the only gate
	// through which it imports the lane's publications.
	subscribeForTest(t, n, "OMM.fbs", "retirement-e2e", "fixture", provider.ID())

	pubs := make([]storage.DatasetShardPublication, 4)
	pnms := make([][]byte, 4)
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
		shardPath, err := producer.DatasetPublicationShardPath(pub)
		if err != nil {
			t.Fatal(err)
		}
		if shardPath != export.ShardPath {
			t.Fatalf("export shard %s is not the publication's shard %s", export.ShardPath, shardPath)
		}
		publishedAt := time.Unix(1700000000+int64(i), 0).UTC()
		manifest, err := storage.BuildSignedDatasetPublicationManifest(filepath.Join(root, "publish", batch), storage.DatasetPublicationManifestOptions{
			Export: export, DatasetID: "omm-retirement-" + batch, UpdateID: batch, ProviderPeerID: provider.ID().String(),
			ProviderEPMCID: "bafy-provider-epm", PublishedAt: publishedAt, SigningKey: ed25519.PrivateKey(rawPriv), SchemaHash: "omm-schema-hash"})
		if err != nil {
			t.Fatal(err)
		}
		pnms[i], err = storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
			PublishedAt: publishedAt, SigningKey: ed25519.PrivateKey(rawPriv)})
		if err != nil {
			t.Fatal(err)
		}
		manifestBytes, indexPath := manifest.Bytes, export.IndexPath
		mu.Lock()
		objects[manifest.CID] = func() ([]byte, error) { return manifestBytes, nil }
		objects[export.ShardCID] = func() ([]byte, error) { return os.ReadFile(shardPath) }
		objects[export.IndexCID] = func() ([]byte, error) { return os.ReadFile(indexPath) }
		mu.Unlock()
		if batch == "never-fetched" {
			if err := os.Remove(export.ShardPath); err != nil {
				t.Fatal(err)
			}
			if ok, err := n.materializeDatasetPublicationPNM(ctx, "OMM.fbs", pnms[i], provider.ID()); err == nil || ok {
				t.Fatalf("missing provider file was accepted: %v, %v", ok, err)
			}
			continue
		}
		if ok, err := n.materializeDatasetPublicationPNM(ctx, "OMM.fbs", pnms[i], provider.ID()); err != nil || !ok {
			t.Fatalf("import %s: %v, %v", batch, ok, err)
		}
		// The subscriber holds the $PNM, as gossip stores it, for the
		// stored-$PNM catch-up below.
		if _, err := subscriber.Store("PNM.fbs", pnms[i], provider.ID().String(), nil); err != nil {
			t.Fatal(err)
		}
	}
	countRecords := func(s *storage.FlatSQLStore) int {
		records, err := s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "retirement-e2e", SourceName: "fixture", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return len(records)
	}
	if got := countRecords(subscriber); got != 3 {
		t.Fatalf("subscriber imported %d records, want 3", got)
	}
	list := func() ([]syncwire.Segment, uint64) {
		stream, response, err := syncwire.Call(ctx, consumer, provider.ID(), syncwire.OpListPublished, syncwire.QRPFields{SchemaName: "OMM.fbs", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		stream.Close()
		rows, err := syncwire.Segments(response)
		if err != nil {
			t.Fatal(err)
		}
		return rows, response.TOTAL_COUNT()
	}
	// The producer lists its unavailable history as unavailable.
	rows, total := list()
	available := map[string]bool{}
	for _, row := range rows {
		if row.Availability == "available" {
			available[row.BatchID] = true
		} else if row.BatchID != "never-fetched" {
			t.Fatalf("unexpected listing before retirement: %+v", row)
		}
	}
	if len(rows) != 4 || total != 4 || len(available) != 3 || !available["held"] || !available["lost"] || !available["recovered"] {
		t.Fatalf("provider unavailable history: %d rows, total %d, available %v", len(rows), total, available)
	}

	lost, err := producer.DatasetPublicationShardPath(pubs[1])
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := producer.DatasetPublicationIndexPath(pubs[2])
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
		got, err := producer.RetireUnavailableDatasetPublications(at)
		if err != nil || got != want {
			t.Fatalf("retired %d, want %d: %v", got, want, err)
		}
	}
	reopen := func() {
		if err := producer.Close(); err != nil {
			t.Fatal(err)
		}
		producer = open(producerPath)
		protocol.NewFlatSQLSyncHandler(producer).Register(provider)
	}
	retire(observed, 0) // Old publication time is not evidence of long absence.
	retire(observed.Add(23*time.Hour), 0)
	reopen()
	if err := os.WriteFile(recovered, index, 0600); err != nil {
		t.Fatal(err)
	}
	// The lost shard and the shard the producer lost before anyone fetched it
	// are both its own unavailable publications a day after first observed.
	retire(observed.Add(24*time.Hour), 2)
	reopen()

	rows, total = list()
	if len(rows) != 2 || total != 2 {
		t.Fatalf("listed %d rows, total %d", len(rows), total)
	}
	for _, row := range rows {
		if (row.BatchID != "held" && row.BatchID != "recovered") || row.Availability != "available" {
			t.Fatalf("unexpected listing: %+v", row)
		}
	}

	// A replay of the old publication cannot refill the serving index, and
	// its files are not fetched or written again.
	lostRequests := map[string]int{}
	for _, cid := range []string{pubs[1].ShardCID, pubs[1].IndexCID} {
		lostRequests[cid] = requestsFor(cid)
	}
	if ok, err := n.materializeDatasetPublicationPNM(ctx, "OMM.fbs", pnms[1], provider.ID()); err != nil || ok {
		t.Fatalf("retired publication replay: %v, %v", ok, err)
	}
	if got, err := n.materializeStoredDatasetPublicationPNMs(ctx, 0); err != nil || got != 0 {
		t.Fatalf("stored $PNM catch-up after retirement: %d, %v", got, err)
	}
	for cid, before := range lostRequests {
		if after := requestsFor(cid); after != before {
			t.Fatalf("retired publication asset %s was fetched again: %d requests, was %d", cid, after, before)
		}
	}
	if state, found, err := producer.DatasetPublicationReplayState(storage.DatasetPublicationRetirementKey(pubs[1])); err != nil || !found || state.State != "retired" {
		t.Fatalf("retirement dedup: %+v, %v, %v", state, found, err)
	}
	if _, err := producer.RetireUnavailableDatasetPublications(observed.Add(48 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows, total = list(); len(rows) != 2 || total != 2 {
		t.Fatalf("retired publication listed again: %d rows, total %d", len(rows), total)
	}
	if _, err := os.Stat(lost); !os.IsNotExist(err) {
		t.Fatalf("retired file was fetched again: %v", err)
	}

	held, err := producer.DatasetPublicationShardPath(pubs[0])
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(held)
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := syncwire.Call(ctx, consumer, provider.ID(), syncwire.OpReadShard, syncwire.QRPFields{SchemaName: "OMM.fbs", CID: pubs[0].ShardCID})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(stream)
	stream.Close()
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("held shard changed: %v", err)
	}
	if got := countRecords(producer); got != 4 {
		t.Fatalf("retirement changed stored records: %d", got)
	}
	if got := countRecords(subscriber); got != 3 {
		t.Fatalf("subscriber records changed: %d", got)
	}
}
