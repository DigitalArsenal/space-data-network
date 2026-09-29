package node

// The PNM replay on a node (sdn-replay-import-deadline-20260929): a shard the
// feed-head path already imported is marked materialized without being
// fetched again, an attempt leaves nothing in the replay work root, and boot
// clears what earlier processes left in both shard work roots.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type replayNodeFixture struct {
	node        *Node
	providerID  peer.ID
	pnm         []byte
	export      *storage.DatasetExport
	manifestCID string
	tags        storage.SourceTags

	mu       sync.Mutex
	requests map[string]int
}

func (f *replayNodeFixture) requestsFor(cid string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[cid]
}

func (f *replayNodeFixture) replayRoot() string {
	return filepath.Join(f.node.config.Storage.Path, datasetPublicationReplayWorkRoot)
}

// newReplayNodeFixture publishes one CAT record as a signed shard from a
// trusted provider and wires a subscriber node to an IPFS API that serves the
// manifest, and the shard and index only when serveShard is set.
func newReplayNodeFixture(t *testing.T, serveShard bool) *replayNodeFixture {
	t.Helper()
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "provider-db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { providerStore.Close() })
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "subscriber-db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subscriberStore.Close() })

	priv, pub, err := libp2pcrypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x72}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	rawPriv, err := priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	providerID, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	tags := storage.SourceTags{
		ProviderID:   "replay-work.eth",
		SourceName:   "replay-work-satcat",
		SourceURL:    "https://example.invalid/satcat.csv",
		BatchID:      "batch-replay-work-001",
		ContentKeyID: "public",
	}
	record := sds.NewCATBuilder().WithNoradCatID(25544).WithObjectName("ISS").WithObjectType("PAYLOAD").WithOpsStatus("OPERATIONAL").Build()
	if _, err := providerStore.StoreWithSourceTags("CAT.fbs", record, providerID.String(), nil, tags); err != nil {
		t.Fatal(err)
	}
	export, err := providerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID,
		Limit: 10, AllowLargeResultSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Unix(1700007777, 0).UTC()
	manifest, err := storage.BuildSignedDatasetPublicationManifest(filepath.Join(tmpDir, "publish"), storage.DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "cat-replay-work",
		UpdateID:       tags.BatchID,
		ProviderPeerID: providerID.String(),
		ProviderEPMCID: "bafy-provider-epm",
		PublishedAt:    publishedAt,
		SigningKey:     ed25519.PrivateKey(rawPriv),
		SchemaHash:     "cat-schema-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	pnmBytes, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  ed25519.PrivateKey(rawPriv),
	})
	if err != nil {
		t.Fatal(err)
	}

	f := &replayNodeFixture{
		providerID:  providerID,
		pnm:         pnmBytes,
		export:      export,
		manifestCID: manifest.CID,
		tags:        tags,
		requests:    map[string]int{},
	}
	objects := map[string][]byte{manifest.CID: manifest.Bytes}
	if serveShard {
		objects[export.ShardCID] = mustReadTestFile(t, export.ShardPath)
		objects[export.IndexCID] = mustReadTestFile(t, export.IndexPath)
	}
	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cidValue := r.URL.Query().Get("arg")
		f.mu.Lock()
		f.requests[cidValue]++
		f.mu.Unlock()
		data, ok := objects[cidValue]
		if r.URL.Path != "/api/v0/cat" || !ok {
			http.Error(w, "not served: "+cidValue, http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(ipfs.Close)

	ctx, cancel := context.WithCancel(context.Background())
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: providerID, TrustLevel: peers.Trusted}); err != nil {
		t.Fatal(err)
	}
	f.node = &Node{
		store:        subscriberStore,
		peerRegistry: registry,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "subscriber-storage")},
			Admin:   config.AdminConfig{IPFSAPIURL: ipfs.URL},
		},
		ctx:                     ctx,
		cancel:                  cancel,
		datasetMaterializedPNMs: make(map[string]time.Time),
	}
	// Runs before the stores close: background work the materialization
	// started (the storage quota pass) finishes first.
	t.Cleanup(func() {
		cancel()
		f.node.wg.Wait()
	})
	return f
}

func replayWorkRootEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func (f *replayNodeFixture) replayState(t *testing.T) string {
	t.Helper()
	state, found, err := f.node.store.DatasetPublicationReplayState(pnmCID(t, f.pnm) + "\x00" + pnmFileID(t, f.pnm))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return ""
	}
	return state.State
}

// The feed-head path imported this shard and recorded it; the PNM replay of
// the same publication verifies the signed manifest, finds that record and
// marks the PNM materialized without fetching the shard or index again.
func TestMaterializeDatasetPublicationPNMMarksAFeedHeadImportedShardWithoutRefetching(t *testing.T) {
	t.Parallel()
	f := newReplayNodeFixture(t, false)
	store := f.node.store

	imported, index, err := store.ImportDatasetShardFromFiles(f.export.ShardPath, f.export.IndexPath, f.providerID.String())
	if err != nil || imported != 1 {
		t.Fatalf("feed-head import: %d records, %v", imported, err)
	}
	ann := sdnpubsub.DatasetFeedHeadAnnouncement{
		Schema:       "CAT.fbs",
		ProviderID:   f.tags.ProviderID,
		SourceName:   f.tags.SourceName,
		BatchID:      f.tags.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Limit:        10,
		RecordCount:  f.export.RecordCount,
		ByteCount:    f.export.ShardBytes,
		ShardCID:     f.export.ShardCID,
		IndexCID:     f.export.IndexCID,
		ManifestCID:  f.manifestCID,
		PublishedAt:  time.Unix(1700007777, 0).UTC(),
	}
	shardHeader := datasetFeedHeadAssetHeader{SHA256: f.export.ShardSHA256, ByteCount: f.export.ShardBytes}
	indexHeader := datasetFeedHeadAssetHeader{SHA256: f.export.IndexSHA256, ByteCount: f.export.IndexBytes}
	if err := store.UpsertDatasetShardPublication(datasetShardPublicationFromFeedHead(ann, shardHeader, indexHeader, index)); err != nil {
		t.Fatal(err)
	}

	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || !materialized {
		t.Fatalf("materialize = %v, %v; want the PNM materialized", materialized, err)
	}
	if shard, idx := f.requestsFor(f.export.ShardCID), f.requestsFor(f.export.IndexCID); shard != 0 || idx != 0 {
		t.Fatalf("the shard was fetched again: %d shard and %d index requests", shard, idx)
	}
	if f.requestsFor(f.manifestCID) != 1 {
		t.Fatalf("manifest requests = %d, want 1", f.requestsFor(f.manifestCID))
	}
	if state := f.replayState(t); state != storage.DatasetPublicationReplayStateMaterialized {
		t.Fatalf("replay state = %q, want %q", state, storage.DatasetPublicationReplayStateMaterialized)
	}
	if left := replayWorkRootEntries(t, f.replayRoot()); len(left) != 0 {
		t.Fatalf("the replay left %v in its work root", left)
	}
}

// A replay that fetches and imports leaves nothing in its work root.
func TestMaterializeDatasetPublicationPNMLeavesNoWorkFiles(t *testing.T) {
	t.Parallel()
	f := newReplayNodeFixture(t, true)

	materialized, err := f.node.materializeDatasetPublicationPNM(f.node.ctx, "CAT.fbs", f.pnm, f.providerID)
	if err != nil || !materialized {
		t.Fatalf("materialize = %v, %v; want the PNM materialized", materialized, err)
	}
	if f.requestsFor(f.export.ShardCID) != 1 {
		t.Fatalf("shard requests = %d, want 1", f.requestsFor(f.export.ShardCID))
	}
	records, err := f.node.store.QueryIndexedRecords(storage.IndexedRecordQuery{
		SchemaName: "CAT.fbs", ProviderID: f.tags.ProviderID, SourceName: f.tags.SourceName, BatchID: f.tags.BatchID, Limit: 10,
	})
	if err != nil || len(records) != 1 {
		t.Fatalf("imported CAT records = %d, %v; want 1", len(records), err)
	}
	if state := f.replayState(t); state != storage.DatasetPublicationReplayStateMaterialized {
		t.Fatalf("replay state = %q, want %q", state, storage.DatasetPublicationReplayStateMaterialized)
	}
	if left := replayWorkRootEntries(t, f.replayRoot()); len(left) != 0 {
		t.Fatalf("the replay left %v in its work root", left)
	}
}

// Boot clears both shard work roots of what predates this process and keeps
// what is newer; with no storage path configured it sweeps nothing.
func TestPruneStaleDatasetPublicationWorkSweepsBothWorkRoots(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	replayRoot := filepath.Join(base, datasetPublicationReplayWorkRoot)
	feedHeadRoot := filepath.Join(base, datasetFeedHeadSyncWorkRoot)
	for _, dir := range []string{
		replayRoot,
		filepath.Join(feedHeadRoot, "feed-head-1180675009"),
		filepath.Join(feedHeadRoot, "feed-head-2000000000"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, size := range map[string]int{
		filepath.Join(replayRoot, "bafybeiold.fbshard"):                      8192,
		filepath.Join(replayRoot, "bafkreiold.index.json"):                   64,
		filepath.Join(feedHeadRoot, "feed-head-1180675009", "shard.fbshard"): 4096,
		filepath.Join(feedHeadRoot, "feed-head-2000000000", "shard.fbshard"): 16,
	} {
		if err := os.WriteFile(path, bytes.Repeat([]byte{7}, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-72 * time.Hour)
	for _, path := range []string{
		filepath.Join(replayRoot, "bafybeiold.fbshard"),
		filepath.Join(replayRoot, "bafkreiold.index.json"),
		filepath.Join(feedHeadRoot, "feed-head-1180675009"),
	} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	n := &Node{config: &config.Config{Storage: config.StorageConfig{Path: base}}}
	n.pruneStaleDatasetPublicationWork(time.Now().Add(-time.Hour))

	if left := replayWorkRootEntries(t, replayRoot); len(left) != 0 {
		t.Fatalf("replay root still holds %v", left)
	}
	if left := replayWorkRootEntries(t, feedHeadRoot); len(left) != 1 || left[0] != "feed-head-2000000000" {
		t.Fatalf("feed-head root holds %v, want only the current process's feed-head-2000000000", left)
	}
	(&Node{config: &config.Config{}}).pruneStaleDatasetPublicationWork(time.Now())
}
