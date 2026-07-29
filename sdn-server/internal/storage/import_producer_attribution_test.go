package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// A receipt that is real must be visible on the surface built to show receipts.
// Records that arrive by dataset-shard import — the lane a producer's pubsub
// publication actually travels — must book the same per-producer summary row
// that ProducerSourceProgress reads, attributed to the PEER, not to the
// provider name.
func TestImportDatasetShardBooksProducerAttributedSourceRow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-import-producer-attribution-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	producerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "producer-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore producer failed: %v", err)
	}
	defer producerStore.Close()
	consumerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "consumer-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore consumer failed: %v", err)
	}
	defer consumerStore.Close()

	const producerPeer = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"

	// The producer stamps its own peer id, exactly as a flow ingest does.
	tags := SourceTags{
		ProviderID:     "space-data-network-02",
		SourceName:     "celestrak-gp",
		SourceURL:      "https://fixture.test/gp.csv",
		BatchID:        "batch-producer-attribution",
		ContentKeyID:   "public",
		ProducerPeerID: producerPeer,
	}
	for i := 0; i < 4; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(70000 + i)).
			WithObjectName("ATTRIBUTION").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := producerStore.StoreWithSourceTags("CAT.fbs", record, producerPeer, nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}

	export, err := producerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	imported, _, err := consumerStore.ImportDatasetShard(shardBytes, indexBytes, producerPeer)
	if err != nil {
		t.Fatalf("ImportDatasetShard failed: %v", err)
	}
	if imported != 4 {
		t.Fatalf("imported=%d, want 4", imported)
	}

	rows, err := consumerStore.ProducerSourceProgress()
	if err != nil {
		t.Fatalf("ProducerSourceProgress failed: %v", err)
	}
	var found *ProducerSourceProgress
	for i := range rows {
		if rows[i].ProducerPeerID == producerPeer && rows[i].SchemaName == "CAT.fbs" {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no producer row for %s; rows=%+v", producerPeer, rows)
	}
	if found.ProviderID != tags.ProviderID || found.SourceName != tags.SourceName {
		t.Fatalf("producer row lane = %s/%s, want %s/%s",
			found.ProviderID, found.SourceName, tags.ProviderID, tags.SourceName)
	}
	if found.Count != 4 {
		t.Fatalf("producer row count = %d, want 4", found.Count)
	}
	if found.TotalBytes <= 0 {
		t.Fatalf("producer row total bytes = %d, want > 0", found.TotalBytes)
	}
	if found.LastBatchID != tags.BatchID {
		t.Fatalf("producer row last batch = %q, want %q", found.LastBatchID, tags.BatchID)
	}
	if found.FirstSeenUnix <= 0 {
		t.Fatalf("producer row first_seen = %d, want > 0", found.FirstSeenUnix)
	}
	if found.LastSeenUnix <= 0 {
		t.Fatalf("producer row last_seen = %d, want > 0", found.LastSeenUnix)
	}
}

// A shard whose records carry no producer peer id at all must still be
// attributed to the peer that served it. Otherwise normalizeSourceTags
// back-fills the PROVIDER name into producer_peer_id and the producer feed
// drops the row on purpose, leaving a filling node looking idle.
func TestImportDatasetShardAttributesUntaggedRecordsToTheServingPeer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-import-untagged-producer-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	producerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "producer-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore producer failed: %v", err)
	}
	defer producerStore.Close()
	consumerStore, err := NewFlatSQLStore(filepath.Join(tmpDir, "consumer-db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore consumer failed: %v", err)
	}
	defer consumerStore.Close()

	const servingPeer = "16Uiu2HAmUntaggedShardServingPeerIdentityForTest0000000"

	// No ProducerPeerID: an older producer's export.
	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-satcat",
		SourceURL:    "https://fixture.test/satcat.csv",
		BatchID:      "batch-untagged",
		ContentKeyID: "public",
	}
	for i := 0; i < 2; i++ {
		record := sds.NewCATBuilder().
			WithNoradCatID(uint32(71000 + i)).
			WithObjectName("UNTAGGED").
			WithObjectType("PAYLOAD").
			WithOpsStatus("OPERATIONAL").
			Build()
		if _, err := producerStore.StoreWithSourceTags("CAT.fbs", record, "legacy-producer", nil, tags); err != nil {
			t.Fatalf("store record %d failed: %v", i, err)
		}
	}
	export, err := producerStore.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard: %v", err)
	}
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if _, _, err := consumerStore.ImportDatasetShard(shardBytes, indexBytes, servingPeer); err != nil {
		t.Fatalf("ImportDatasetShard failed: %v", err)
	}

	rows, err := consumerStore.ProducerSourceProgress()
	if err != nil {
		t.Fatalf("ProducerSourceProgress failed: %v", err)
	}
	for _, row := range rows {
		if row.ProducerPeerID == servingPeer && row.SchemaName == "CAT.fbs" {
			return
		}
	}
	t.Fatalf("no producer row attributed to the serving peer %s; rows=%+v", servingPeer, rows)
}
