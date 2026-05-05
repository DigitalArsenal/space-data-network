package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFlatSQLStoreExportDatasetWindowWritesShardAndIndex(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-export-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}

	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-satcat-csv",
		SourceURL:    "https://celestrak.org/satcat/records.php?GROUP=active&FORMAT=CSV",
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	recordB := sds.NewCATBuilder().
		WithNoradCatID(40909).
		WithObjectName("STARLINK-1001").
		WithObjectID("2015-049A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()

	if _, err := store.StoreWithSourceTags("CAT.fbs", recordA, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordB, "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:         "CAT.fbs",
		ProviderID:         "space-data-network-02",
		SourceName:         "celestrak-satcat-csv",
		BatchID:            "source-sha-001",
		CAReadyResidentSet: true,
		From:               &from,
		To:                 &to,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow failed: %v", err)
	}
	if export.RecordCount != 2 {
		t.Fatalf("RecordCount = %d, want 2", export.RecordCount)
	}
	if export.ShardPath == "" || export.IndexPath == "" {
		t.Fatalf("export paths must be set: %+v", export)
	}
	if export.ShardSHA256 == "" || export.IndexSHA256 == "" || export.QuerySHA256 == "" || export.ResultSHA256 == "" {
		t.Fatalf("export hashes must be set: %+v", export)
	}

	shardBytes, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatalf("read shard failed: %v", err)
	}
	reader := bytes.NewReader(shardBytes)
	var recordLengths []uint32
	for reader.Len() > 0 {
		var length uint32
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			t.Fatalf("read length prefix failed: %v", err)
		}
		payload := make([]byte, length)
		if _, err := reader.Read(payload); err != nil {
			t.Fatalf("read record payload failed: %v", err)
		}
		recordLengths = append(recordLengths, length)
	}
	if len(recordLengths) != 2 {
		t.Fatalf("shard contains %d records, want 2", len(recordLengths))
	}

	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read index failed: %v", err)
	}
	var index DatasetExportIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatalf("unmarshal index failed: %v", err)
	}
	if index.SchemaName != "CAT.fbs" {
		t.Fatalf("SchemaName = %q, want CAT.fbs", index.SchemaName)
	}
	if index.ProviderID != "space-data-network-02" || index.SourceName != "celestrak-satcat-csv" || index.BatchID != "source-sha-001" {
		t.Fatalf("source tags not preserved in index: %+v", index)
	}
	if len(index.Records) != 2 {
		t.Fatalf("index contains %d records, want 2", len(index.Records))
	}
	for i, record := range index.Records {
		if record.CID == "" || record.NoradCatID == nil || record.ObjectType != "PAYLOAD" || record.OpsStatusCode != "OPERATIONAL" {
			t.Fatalf("record %d missing query metadata: %+v", i, record)
		}
		if record.Offset < 0 || record.Length <= 0 {
			t.Fatalf("record %d has invalid byte range: %+v", i, record)
		}
	}
}
