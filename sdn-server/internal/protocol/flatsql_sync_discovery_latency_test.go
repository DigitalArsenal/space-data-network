package protocol

// Regression gate for task sdn-flatsql-sync-discovery-latency-resets.
//
// Live failure this encodes: an anonymous browser's list_published_shards
// frame (the /beta catalogue DISCOVER hop) queued behind the FlatSQL store's
// write lock and answered in 22-45 s against the client's 20 s timeout —
// CATALOGUE UNAVAILABLE. The same 5,756-byte answer took 77 ms on an idle
// box: contention, not cost. Fleet measurements narrowed the hold to the
// ingest-side writers (host-02 under active ingest still answered in 21 s on
// the post-fsync-removal build).
//
// The contract under test: the publication DISCOVER hop and the published-
// shard payload hop answer in BOUNDED time while a writer holds the store,
// serving the last-known publication snapshot instead of queueing on
// s.mu.RLock. Before the discovery cache this test FAILS with probe answers
// in the seconds (each probe waits out a full writer hold).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	// discoveryContentionSeedRecords sizes the tagged-record population so one
	// RebuildDerivedState call (an exported, whole-operation write-lock hold,
	// same lock the live ingest writers take) holds the store for seconds on
	// current hardware — calibrated 2026-08-13: ~2.3 s at 6,000 tagged records.
	discoveryContentionSeedRecords = 6000

	// discoveryContentionControlFloor is how long a DIRECT store read must
	// take under the writer loop for the fixture to count as contended. If
	// hardware or storage changes shrink the hold below this, the test fails
	// loudly asking for recalibration instead of passing vacuously.
	discoveryContentionControlFloor = 500 * time.Millisecond

	// discoveryAnswerBound is the bounded-time contract for every discovery
	// and payload-hop answer while the writer churns. The cache path answers
	// in single-digit milliseconds; the pre-fix RLock path waits out a full
	// multi-second writer hold and fails this by an order of magnitude.
	discoveryAnswerBound = 250 * time.Millisecond
)

func TestFlatSQLSyncDiscoveryAnswersBoundedWhileWriterHoldsStore(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-second store-contention fixture; skipped in -short")
	}

	store := newFlatSQLSyncTestStore(t)

	// Two published shards with real on-disk shard+index files, the exact
	// fixture shape of the live OMM.fbs publication feed.
	pubs := seedDiscoveryLatencyPublications(t, store)

	// Tagged records are what make the writer holds long (source-summary and
	// engine-record rebuild cost scales with them).
	seedDiscoveryLatencyRecords(t, store, discoveryContentionSeedRecords)

	handler := NewFlatSQLSyncHandler(store)
	listReq := flatSQLSyncRequest{
		Op:           "list_published_shards",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "latency-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	}

	// Warm probe before any writer churn: fills the discovery snapshot and
	// proves the fixture serves both publications.
	var warmOut bytes.Buffer
	if err := handler.handleListPublishedShards(&warmOut, listReq); err != nil {
		t.Fatalf("warm list_published_shards failed: %v", err)
	}
	assertDiscoveryListAnswer(t, &warmOut, len(pubs))

	// Writer: back-to-back whole-operation write-lock holds, the shape of the
	// live ingest/publication writers ListDatasetShardPublications queued
	// behind. RebuildDerivedState holds s.mu.Lock() for its full duration.
	stopWriter := make(chan struct{})
	writerErr := make(chan error, 1)
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for {
			select {
			case <-stopWriter:
				return
			default:
			}
			if err := store.RebuildDerivedState(); err != nil {
				select {
				case writerErr <- err:
				default:
				}
				return
			}
		}
	}()
	defer func() {
		close(stopWriter)
		writerWG.Wait()
		select {
		case err := <-writerErr:
			t.Fatalf("contention writer failed: %v", err)
		default:
		}
	}()
	// Let the first hold start.
	time.Sleep(300 * time.Millisecond)

	// CONTROL: a direct store read must be queueing behind the writer, or the
	// fixture is not exercising the regression at all. Take the worst of
	// three so a lucky landing in the microsecond gap between holds cannot
	// fake a calibration failure.
	var control time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		if _, err := store.ListDatasetShardPublications(discoveryLatencyQuery()); err != nil {
			t.Fatalf("control read failed: %v", err)
		}
		if elapsed := time.Since(start); elapsed > control {
			control = elapsed
		}
	}
	if control < discoveryContentionControlFloor {
		t.Fatalf("contention fixture broke: direct store read answered in %v (< %v) under the writer loop; grow discoveryContentionSeedRecords so the write-lock hold is multi-second again", control, discoveryContentionControlFloor)
	}

	// THE REGRESSION: eight sequential DISCOVER probes (the shape of every
	// live measurement in the task) must each answer inside the bound while
	// the writer churns, and must still carry the full publication snapshot.
	worst := time.Duration(0)
	for probe := 0; probe < 8; probe++ {
		var out bytes.Buffer
		start := time.Now()
		err := handler.handleListPublishedShards(&out, listReq)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("probe %d list_published_shards failed: %v", probe, err)
		}
		assertDiscoveryListAnswer(t, &out, len(pubs))
		if elapsed > worst {
			worst = elapsed
		}
	}
	t.Logf("worst of 8 discovery probes under writer churn: %v (control direct read: %v)", worst, control)
	if worst > discoveryAnswerBound {
		t.Fatalf("worst list_published_shards answer was %v while a writer held the store (bound %v, control read %v): the DISCOVER hop queued behind the write lock again — this is the 22-45 s /beta CATALOGUE UNAVAILABLE regression", worst, discoveryAnswerBound, control)
	}

	// The payload hop must not stall either: read_published_shard resolves
	// its publication through the same snapshot.
	var shardOut bytes.Buffer
	start := time.Now()
	err := handler.handleReadPublishedShard(&shardOut, flatSQLSyncRequest{
		Op:           "read_published_shard",
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "latency-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
		CID:          pubs[0].ShardCID,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("read_published_shard under writer churn failed: %v", err)
	}
	var shardHeader struct {
		Status string `json:"status"`
		CID    string `json:"cid"`
	}
	readFlatSQLSyncTestJSONFrame(t, &shardOut, &shardHeader)
	if shardHeader.Status != "ok" || shardHeader.CID != pubs[0].ShardCID {
		t.Fatalf("unexpected read_published_shard header: %+v", shardHeader)
	}
	if elapsed > discoveryAnswerBound {
		t.Fatalf("read_published_shard answered in %v while a writer held the store (bound %v): the payload hop queued behind the write lock", elapsed, discoveryAnswerBound)
	}
}

func discoveryLatencyQuery() storage.DatasetShardPublicationQuery {
	return storage.DatasetShardPublicationQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "latency-batch",
		QueryProfile: storage.DatasetPublicationQueryProfile,
	}
}

func seedDiscoveryLatencyPublications(t *testing.T, store *storage.FlatSQLStore) []storage.DatasetShardPublication {
	t.Helper()
	var pubs []storage.DatasetShardPublication
	for index, cid := range []string{"bafklatencyshardone", "bafklatencyshardtwo"} {
		pub := storage.DatasetShardPublication{
			SchemaName:   "OMM.fbs",
			ProviderID:   "space-data-network-02",
			SourceName:   "celestrak-gp",
			BatchID:      "latency-batch",
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       index * 50000,
			Limit:        50000,
			RecordCount:  10 + index,
			ByteCount:    9,
			ShardCID:     cid,
			IndexCID:     cid + "-index",
			ManifestCID:  "bafklatencymanifest",
			ShardSHA256:  fmt.Sprintf("%064d", index+1),
			IndexSHA256:  fmt.Sprintf("%064d", index+3),
			QuerySHA256:  fmt.Sprintf("%064d", index+5),
			ResultSHA256: fmt.Sprintf("%064d", index+7),
			FeedSequence: int64(index + 1),
			PublishedAt:  time.Unix(1700000000+int64(index), 0).UTC(),
		}
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatalf("UpsertDatasetShardPublication failed: %v", err)
		}
		writeDiscoveryLatencyPublicationFiles(t, store, pub)
		pubs = append(pubs, pub)
	}
	return pubs
}

func writeDiscoveryLatencyPublicationFiles(t *testing.T, store *storage.FlatSQLStore, pub storage.DatasetShardPublication) {
	t.Helper()
	shardPath, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		t.Fatalf("DatasetPublicationShardPath failed: %v", err)
	}
	indexPath, err := store.DatasetPublicationIndexPath(pub)
	if err != nil {
		t.Fatalf("DatasetPublicationIndexPath failed: %v", err)
	}
	for path, payload := range map[string][]byte{
		shardPath: {0x05, 0x00, 0x00, 0x00, 'O', 'M', 'M', '-', 'x'},
		indexPath: []byte(`{"idx":true}`),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir for %s failed: %v", path, err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatalf("write %s failed: %v", path, err)
		}
	}
}

func seedDiscoveryLatencyRecords(t *testing.T, store *storage.FlatSQLStore, count int) {
	t.Helper()
	const chunk = 2000
	stored := 0
	for stored < count {
		size := chunk
		if count-stored < size {
			size = count - stored
		}
		batch := make([][]byte, 0, size)
		for i := 0; i < size; i++ {
			builder := flatbuffers.NewBuilder(256)
			name := builder.CreateString(fmt.Sprintf("LATENCY-OBJ-%d", stored+i))
			objectID := builder.CreateString(fmt.Sprintf("2026-%03dA", (stored+i)%999))
			epoch := builder.CreateString("2026-05-10T12:00:00Z")
			OMM.OMMStart(builder)
			OMM.OMMAddNORAD_CAT_ID(builder, uint32(700000+stored+i))
			OMM.OMMAddOBJECT_NAME(builder, name)
			OMM.OMMAddOBJECT_ID(builder, objectID)
			OMM.OMMAddEPOCH(builder, epoch)
			omm := OMM.OMMEnd(builder)
			OMM.FinishSizePrefixedOMMBuffer(builder, omm)
			batch = append(batch, append([]byte(nil), builder.FinishedBytes()...))
		}
		n, err := store.StoreBatchWithSourceTags("OMM.fbs", batch, "source:latency", nil, storage.SourceTags{
			ProviderID: "space-data-network-02",
			SourceName: "celestrak-gp",
			BatchID:    "latency-batch",
		})
		if err != nil {
			t.Fatalf("StoreBatchWithSourceTags failed at %d: %v", stored, err)
		}
		stored += n
	}
}

func assertDiscoveryListAnswer(t *testing.T, out *bytes.Buffer, wantPublications int) {
	t.Helper()
	var header struct {
		Op                    string `json:"op"`
		Status                string `json:"status"`
		Schema                string `json:"schema"`
		TotalPublicationCount int    `json:"total_publication_count"`
		Publications          []struct {
			ShardCID string `json:"shard_cid"`
			IndexCID string `json:"index_cid"`
		} `json:"publications"`
	}
	readFlatSQLSyncTestJSONFrame(t, out, &header)
	if header.Op != "list_published_shards" || header.Status != "ok" || header.Schema != "OMM.fbs" {
		t.Fatalf("unexpected list_published_shards header: %+v", header)
	}
	if header.TotalPublicationCount != wantPublications || len(header.Publications) != wantPublications {
		t.Fatalf("expected %d publications, got %+v", wantPublications, header)
	}
	for _, pub := range header.Publications {
		if pub.ShardCID == "" || pub.IndexCID == "" {
			t.Fatalf("publication entry missing CIDs: %+v", header.Publications)
		}
	}
}
