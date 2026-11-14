package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func exportOrdinaryDatasetRecords(outputDir string, filter IndexedRecordQuery, records []DatasetExportRecord) (*DatasetExport, error) {
	if outputDir == "" {
		return nil, fmt.Errorf("output dir is required")
	}
	if filter.SchemaName == "" {
		return nil, fmt.Errorf("schema name is required")
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no records match export query")
	}

	queryJSON, err := canonicalQueryJSON(filter)
	if err != nil {
		return nil, err
	}
	querySHA := sha256Hex(queryJSON)

	shard := bytes.Buffer{}
	indexRecords := make([]DatasetExportIndexRecord, 0, len(records))
	for _, record := range records {
		if len(record.Data) > int(^uint32(0)) {
			return nil, fmt.Errorf("record %s exceeds uint32 shard frame length", record.CID)
		}
		recordCID := strings.TrimSpace(record.CID)
		if recordCID == "" {
			recordCID = computeCID(record.Data)
		}
		offset := int64(shard.Len())
		if err := binary.Write(&shard, binary.LittleEndian, uint32(len(record.Data))); err != nil {
			return nil, fmt.Errorf("write shard length: %w", err)
		}
		if _, err := shard.Write(record.Data); err != nil {
			return nil, fmt.Errorf("write shard record: %w", err)
		}

		fields, err := extractIndexedFields(filter.SchemaName, record.Data)
		if err != nil {
			return nil, fmt.Errorf("extract index fields for %s: %w", record.CID, err)
		}
		indexRecords = append(indexRecords, DatasetExportIndexRecord{
			CID:           recordCID,
			Offset:        offset,
			Length:        int64(len(record.Data)),
			NoradCatID:    fields.noradCatID,
			EntityID:      fields.entityID,
			ObjectType:    fields.objectType,
			OpsStatusCode: fields.opsStatusCode,
			EpochUnix:     fields.epochUnix,
			EpochDay:      fields.epochDay,
			SourceTags:    record.SourceTags,
		})
	}

	shardBytes := shard.Bytes()
	shardSHA := sha256Hex(shardBytes)
	shardCID, err := cidV1RawSHA256(shardBytes)
	if err != nil {
		return nil, fmt.Errorf("compute shard CID: %w", err)
	}
	index := DatasetExportIndex{
		Version:      1,
		SchemaName:   filter.SchemaName,
		ProviderID:   filter.ProviderID,
		SourceName:   filter.SourceName,
		BatchID:      filter.BatchID,
		QuerySHA256:  querySHA,
		ResultSHA256: shardSHA,
		ShardSHA256:  shardSHA,
		ShardCID:     shardCID,
		ShardFile:    fmt.Sprintf("%s-%s.fbshard", querySHA[:16], shardSHA[:16]),
		RecordCount:  len(indexRecords),
		Records:      indexRecords,
		Indexes:      buildDatasetExportIndexes(indexRecords),
	}
	indexBytes, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal dataset export index: %w", err)
	}
	indexBytes = append(indexBytes, '\n')
	indexSHA := sha256Hex(indexBytes)
	indexCID, err := cidV1RawSHA256(indexBytes)
	if err != nil {
		return nil, fmt.Errorf("compute index CID: %w", err)
	}

	shardPath := filepath.Join(outputDir, "shards", index.ShardFile)
	indexPath := filepath.Join(outputDir, "indexes", fmt.Sprintf("%s-%s.index.json", querySHA[:16], indexSHA[:16]))
	if err := writeImmutableExportFile(shardPath, shardBytes); err != nil {
		return nil, err
	}
	if err := writeImmutableExportFile(indexPath, indexBytes); err != nil {
		return nil, err
	}

	return &DatasetExport{
		SchemaName:     filter.SchemaName,
		RecordCount:    len(records),
		CanonicalQuery: string(queryJSON),
		QuerySHA256:    querySHA,
		ResultSHA256:   shardSHA,
		ShardPath:      shardPath,
		ShardSHA256:    shardSHA,
		ShardCID:       shardCID,
		ShardBytes:     int64(len(shardBytes)),
		IndexPath:      indexPath,
		IndexSHA256:    indexSHA,
		IndexCID:       indexCID,
		IndexBytes:     int64(len(indexBytes)),
		SourceBatches:  summarizeExportSourceBatches(indexRecords),
	}, nil
}
