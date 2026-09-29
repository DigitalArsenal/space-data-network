package storage

// The dataset-publication replay (MaterializeDatasetPublication): its fetch
// budget bounds the fetch and not the import, a shard the store already
// recorded as a replicated publication is not fetched again, and every
// attempt removes its work files (sdn-replay-import-deadline-20260929).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

type replayWorkFixture struct {
	consumer  *FlatSQLStore
	export    *DatasetExport
	tags      SourceTags
	pnm       []byte
	publicKey crypto.PubKey
	objects   map[string][]byte
	records   int
}

// newReplayWorkFixture publishes n OMM records from a producer store as one
// signed shard (manifest, shard, index, PNM) and opens an empty consumer.
func newReplayWorkFixture(t *testing.T, n int) *replayWorkFixture {
	t.Helper()
	tmp := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	producer := openBootStore(t, filepath.Join(tmp, "producer"), validator)
	t.Cleanup(func() { producer.Close() })
	consumer := openBootStore(t, filepath.Join(tmp, "consumer"), validator)
	t.Cleanup(func() { consumer.Close() })

	tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "replay-work"}
	records := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, buildEngineOMM(t, uint32(96000+i), "R", 1_700_000_000+int64(i)))
	}
	if _, err := producer.StoreBatchWithSourceTags("OMM.fbs", records, "peer", nil, tags); err != nil {
		t.Fatal(err)
	}
	export, err := producer.ExportDatasetWindow(filepath.Join(tmp, "export"), IndexedRecordQuery{
		SchemaName: "OMM.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID,
		Limit: n, AllowLargeResultSet: true, OrderByCID: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if export.RecordCount != n {
		t.Fatalf("export holds %d records, want %d", export.RecordCount, n)
	}
	publicKey, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 128)))
	if err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Unix(1700004321, 0).UTC()
	manifest, err := BuildSignedDatasetPublicationManifest(filepath.Join(tmp, "publish"), DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "omm-replay-work",
		UpdateID:       tags.BatchID,
		ProviderPeerID: "peer",
		ProviderEPMCID: "bafy-provider-epm",
		PublishedAt:    publishedAt,
		SigningKey:     signingKey,
		SchemaHash:     "omm-schema-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	pnm, err := BuildDatasetPublicationPNM(manifest, DatasetPublicationPNMOptions{PublishedAt: publishedAt, SigningKey: signingKey})
	if err != nil {
		t.Fatal(err)
	}
	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	return &replayWorkFixture{
		consumer:  consumer,
		export:    export,
		tags:      tags,
		pnm:       pnm,
		publicKey: mustLibp2pEd25519(t, publicKey),
		objects: map[string][]byte{
			manifest.CID:    manifest.Bytes,
			export.ShardCID: shardBytes,
			export.IndexCID: indexBytes,
		},
		records: n,
	}
}

func (f *replayWorkFixture) fetchBytes(_ context.Context, cid string) ([]byte, error) {
	data, ok := f.objects[cid]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (f *replayWorkFixture) writeObject(cid, path string) error {
	data, ok := f.objects[cid]
	if !ok {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// feedHeadRow is the publication row the feed-head path records after its
// import of this shard completes.
func (f *replayWorkFixture) feedHeadRow(indexCID string) DatasetShardPublication {
	return DatasetShardPublication{
		SchemaName:   "OMM.fbs",
		ProviderID:   f.tags.ProviderID,
		SourceName:   f.tags.SourceName,
		BatchID:      f.tags.BatchID,
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        f.records,
		RecordCount:  f.records,
		ByteCount:    f.export.ShardBytes,
		ShardCID:     f.export.ShardCID,
		IndexCID:     indexCID,
		ShardSHA256:  f.export.ShardSHA256,
		IndexSHA256:  f.export.IndexSHA256,
		QuerySHA256:  f.export.QuerySHA256,
		ResultSHA256: f.export.ResultSHA256,
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
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

// A shard whose import takes longer than the fetch budget lands whole in one
// attempt; no store lock is held across the fetch, and a reader that arrives
// during the import is served between chunks, not after the import.
func TestMaterializeDatasetPublicationImportOutlivesItsFetchBudget(t *testing.T) {
	const chunks = 6
	const chunkHold = 50 * time.Millisecond
	const budget = 100 * time.Millisecond // well under chunks*chunkHold
	f := newReplayWorkFixture(t, chunks*storeWriteChunkSize)
	workRoot := filepath.Join(t.TempDir(), "dataset-publication-replay")

	var chunksRun atomic.Int32
	readerSawChunk := make(chan int32, 1)
	hook := func() {
		if chunksRun.Add(1) == 1 {
			go func() {
				// Queues behind the first chunk's lock hold.
				_, _, _ = f.consumer.DatasetPublicationReplayState("reader-probe")
				readerSawChunk <- chunksRun.Load()
			}()
		}
		time.Sleep(chunkHold)
	}
	importDatasetShardChunkHook.Store(&hook)
	defer importDatasetShardChunkHook.Store(nil)

	lockFreeDuringFetch := 0
	started := time.Now()
	result, err := MaterializeDatasetPublication(context.Background(), f.consumer, DatasetPublicationReplayOptions{
		PNM:               f.pnm,
		ProviderPublicKey: f.publicKey,
		FetchByCID:        f.fetchBytes,
		FetchByCIDToFile: func(_ context.Context, cid, path string) error {
			probe := make(chan struct{})
			go func() {
				release := f.consumer.lockWrite("test: store lock during the fetch")
				release()
				close(probe)
			}()
			select {
			case <-probe:
				lockFreeDuringFetch++
			case <-time.After(5 * time.Second):
				t.Errorf("the store lock was held across the fetch of %s", cid)
			}
			return f.writeObject(cid, path)
		},
		FetchBudget: budget,
		WorkDir:     workRoot,
	})
	took := time.Since(started)
	if err != nil {
		t.Fatalf("materialize with a %s fetch budget: %v", budget, err)
	}
	if took <= budget {
		t.Fatalf("the import took %s, not longer than the %s budget: the test proves nothing", took, budget)
	}
	if lockFreeDuringFetch != 2 {
		t.Fatalf("store lock free during %d of the 2 fetches", lockFreeDuringFetch)
	}
	if result.AlreadyImported || result.Imported != f.records || result.RecordCount != f.records {
		t.Fatalf("result = %+v, want %d records imported", result, f.records)
	}
	if n := indexCount(t, f.consumer, "OMM.fbs"); n != int64(f.records) {
		t.Fatalf("the consumer holds %d records, want %d", n, f.records)
	}
	if got := chunksRun.Load(); got != chunks {
		t.Fatalf("%d chunks ran, want %d", got, chunks)
	}
	select {
	case at := <-readerSawChunk:
		if at >= chunks {
			t.Fatalf("the reader waited until chunk %d of %d: it was held for the whole import", at, chunks)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never returned")
	}
	if left := dirEntries(t, workRoot); len(left) != 0 {
		t.Fatalf("the attempt left %v in the work root", left)
	}
	t.Logf("%d records imported in %s against a %s fetch budget", f.records, took.Round(time.Millisecond), budget)
}

// Cancelling the import's own context (the node context, at shutdown) still
// stops it before the next chunk: the drain d988d443d protects is intact.
func TestMaterializeDatasetPublicationImportStopsWhenItsContextEnds(t *testing.T) {
	const chunks = 4
	f := newReplayWorkFixture(t, chunks*storeWriteChunkSize)
	workRoot := filepath.Join(t.TempDir(), "dataset-publication-replay")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var chunksRun atomic.Int32
	hook := func() {
		if chunksRun.Add(1) == 2 {
			cancel()
		}
		time.Sleep(20 * time.Millisecond)
	}
	importDatasetShardChunkHook.Store(&hook)
	defer importDatasetShardChunkHook.Store(nil)

	_, err := MaterializeDatasetPublication(ctx, f.consumer, DatasetPublicationReplayOptions{
		PNM:               f.pnm,
		ProviderPublicKey: f.publicKey,
		FetchByCID:        f.fetchBytes,
		FetchByCIDToFile: func(_ context.Context, cid, path string) error {
			return f.writeObject(cid, path)
		},
		FetchBudget: time.Minute,
		WorkDir:     workRoot,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("an import whose context ended returned %v, want context.Canceled", err)
	}
	// The chunk running at the cancel is abandoned and finishes on its own;
	// no chunk after it starts.
	time.Sleep(200 * time.Millisecond)
	if got := chunksRun.Load(); got >= chunks {
		t.Fatalf("%d of %d chunks ran after the context ended", got, chunks)
	}
	if left := dirEntries(t, workRoot); len(left) != 0 {
		t.Fatalf("the stopped attempt left %v in the work root", left)
	}
}

// The budget still bounds the fetch: a shard fetch that hangs ends at the
// budget, nothing is imported, and the attempt's files are gone.
func TestMaterializeDatasetPublicationFetchStaysWithinItsBudget(t *testing.T) {
	f := newReplayWorkFixture(t, 3)
	workRoot := filepath.Join(t.TempDir(), "dataset-publication-replay")
	const budget = 150 * time.Millisecond
	started := time.Now()
	_, err := MaterializeDatasetPublication(context.Background(), f.consumer, DatasetPublicationReplayOptions{
		PNM:               f.pnm,
		ProviderPublicKey: f.publicKey,
		FetchByCID:        f.fetchBytes,
		FetchByCIDToFile: func(ctx context.Context, cid, path string) error {
			if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
		FetchBudget: budget,
		WorkDir:     workRoot,
	})
	took := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a hung fetch returned %v, want the budget's deadline", err)
	}
	if took > 5*time.Second {
		t.Fatalf("a hung fetch returned after %s against a %s budget", took, budget)
	}
	if n := indexCount(t, f.consumer, "OMM.fbs"); n != 0 {
		t.Fatalf("%d records imported from a fetch that never finished", n)
	}
	if left := dirEntries(t, workRoot); len(left) != 0 {
		t.Fatalf("the failed attempt left %v in the work root", left)
	}
}

// A shard the store recorded as a replicated publication (what the feed-head
// path writes once its import completes) is materialized from the manifest
// alone. A row naming another index does not count.
func TestMaterializeDatasetPublicationSkipsAShardTheFeedHeadPathImported(t *testing.T) {
	f := newReplayWorkFixture(t, 5)
	workRoot := filepath.Join(t.TempDir(), "dataset-publication-replay")
	if imported, _, err := f.consumer.ImportDatasetShardFromFiles(f.export.ShardPath, f.export.IndexPath, "peer"); err != nil || imported != f.records {
		t.Fatalf("feed-head import: %d records, %v", imported, err)
	}
	fileFetches := map[string]int{}
	materialize := func() *DatasetPublicationReplayResult {
		t.Helper()
		result, err := MaterializeDatasetPublication(context.Background(), f.consumer, DatasetPublicationReplayOptions{
			PNM:               f.pnm,
			ProviderPublicKey: f.publicKey,
			FetchByCID:        f.fetchBytes,
			FetchByCIDToFile: func(_ context.Context, cid, path string) error {
				fileFetches[cid]++
				return f.writeObject(cid, path)
			},
			FetchBudget: time.Minute,
			WorkDir:     workRoot,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if err := f.consumer.UpsertDatasetShardPublication(f.feedHeadRow("bafkreiotherindex")); err != nil {
		t.Fatal(err)
	}
	if result := materialize(); result.AlreadyImported || fileFetches[f.export.ShardCID] != 1 {
		t.Fatalf("a row naming another index skipped the fetch: result=%+v fetches=%v", result, fileFetches)
	}

	if err := f.consumer.UpsertDatasetShardPublication(f.feedHeadRow(f.export.IndexCID)); err != nil {
		t.Fatal(err)
	}
	result := materialize()
	if !result.AlreadyImported || result.Imported != 0 {
		t.Fatalf("result = %+v, want the recorded shard reported as already imported", result)
	}
	if fileFetches[f.export.ShardCID] != 1 || fileFetches[f.export.IndexCID] != 1 {
		t.Fatalf("the recorded shard was fetched again: %v", fileFetches)
	}
	if result.SchemaName != "OMM.fbs" || result.RecordCount != f.records || result.ShardCID != f.export.ShardCID ||
		result.ProviderID != f.tags.ProviderID || result.SourceName != f.tags.SourceName || result.BatchID != f.tags.BatchID {
		t.Fatalf("result = %+v does not describe the recorded shard", result)
	}
	if n := indexCount(t, f.consumer, "OMM.fbs"); n != int64(f.records) {
		t.Fatalf("the consumer holds %d records, want %d", n, f.records)
	}
	if left := dirEntries(t, workRoot); len(left) != 0 {
		t.Fatalf("attempts left %v in the work root", left)
	}
}

// Stale work is pruned by age: what predates the cutoff goes, whatever its
// shape (the replay's old flat shard files, an interrupted attempt's
// directory), and what is newer stays.
func TestPruneStaleDatasetPublicationWorkRemovesOnlyWhatPredatesTheCutoff(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dataset-publication-replay")
	if err := os.MkdirAll(filepath.Join(root, "attempt-old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "attempt-new"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, size int) {
		t.Helper()
		if err := os.WriteFile(path, bytes.Repeat([]byte{1}, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "bafybeiold.fbshard"), 4096)
	write(filepath.Join(root, "bafkreiold.index.json"), 100)
	write(filepath.Join(root, "attempt-old", "shard.fbshard"), 2048)
	write(filepath.Join(root, "attempt-new", "shard.fbshard"), 512)
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"bafybeiold.fbshard", "bafkreiold.index.json", "attempt-old"} {
		if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
			t.Fatal(err)
		}
	}

	removed, removedBytes, err := PruneStaleDatasetPublicationWork(root, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 || removedBytes != 4096+100+2048 {
		t.Fatalf("pruned %d entries, %d bytes; want 3 entries, %d bytes", removed, removedBytes, 4096+100+2048)
	}
	if left := dirEntries(t, root); len(left) != 1 || left[0] != "attempt-new" {
		t.Fatalf("left %v, want only attempt-new", left)
	}
	if n, b, err := PruneStaleDatasetPublicationWork(filepath.Join(root, "absent"), time.Now()); n != 0 || b != 0 || err != nil {
		t.Fatalf("a missing root: %d, %d, %v", n, b, err)
	}
}
