package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// DatasetExport describes a deterministic local dataset export.
type DatasetExport struct {
	SchemaName       string
	RecordCount      int
	CanonicalQuery   string
	QuerySHA256      string
	ResultSHA256     string
	ShardPath        string
	ShardSHA256      string
	ShardCID         string
	ShardBytes       int64
	IndexPath        string
	IndexSHA256      string
	IndexCID         string
	IndexBytes       int64
	SourceBatches    []DatasetExportSourceBatch
	ContentKeyID     string
	EncryptionPolicy string
}

// DatasetExportRecord is one already-materialized FlatBuffer record to include
// in a dataset export without first inserting it into a FlatSQL store.
type DatasetExportRecord struct {
	CID        string
	Data       []byte
	SourceTags SourceTags
}

// DatasetExportIndex is the replayable query/index sidecar for a shard.
type DatasetExportIndex struct {
	Version      int                          `json:"version"`
	SchemaName   string                       `json:"schemaName"`
	ProviderID   string                       `json:"providerId,omitempty"`
	SourceName   string                       `json:"sourceName,omitempty"`
	BatchID      string                       `json:"batchId,omitempty"`
	QuerySHA256  string                       `json:"querySha256"`
	ResultSHA256 string                       `json:"resultSha256"`
	ShardSHA256  string                       `json:"shardSha256"`
	ShardCID     string                       `json:"shardCid"`
	ShardFile    string                       `json:"shardFile"`
	RecordCount  int                          `json:"recordCount"`
	Records      []DatasetExportIndexRecord   `json:"records"`
	Indexes      DatasetExportMaterializedMap `json:"indexes"`
}

// DatasetExportSourceBatch summarizes one source batch used by an export.
type DatasetExportSourceBatch struct {
	ProviderID       string `json:"providerId,omitempty"`
	SourceName       string `json:"sourceName,omitempty"`
	SourceURL        string `json:"sourceUrl,omitempty"`
	SourceSHA256     string `json:"sourceSha256,omitempty"`
	HTTPETag         string `json:"httpEtag,omitempty"`
	HTTPLastModified string `json:"httpLastModified,omitempty"`
	RetrievedAt      string `json:"retrievedAt,omitempty"`
	ParserVersion    string `json:"parserVersion,omitempty"`
	ContentKeyID     string `json:"contentKeyId,omitempty"`
	RecordCount      uint64 `json:"recordCount"`
}

// DatasetExportIndexRecord records byte offsets and indexed fields for one shard entry.
type DatasetExportIndexRecord struct {
	CID           string     `json:"cid"`
	Offset        int64      `json:"offset"`
	Length        int64      `json:"length"`
	NoradCatID    *uint32    `json:"noradCatId,omitempty"`
	EntityID      string     `json:"entityId,omitempty"`
	ObjectType    string     `json:"objectType,omitempty"`
	OpsStatusCode string     `json:"opsStatusCode,omitempty"`
	EpochUnix     *int64     `json:"epochUnix,omitempty"`
	EpochDay      string     `json:"epochDay,omitempty"`
	SourceTags    SourceTags `json:"sourceTags"`
}

// DatasetExportMaterializedMap stores compact query indexes for shard consumers.
type DatasetExportMaterializedMap struct {
	ByNORAD         map[string][]int `json:"byNorad,omitempty"`
	ByEntityID      map[string][]int `json:"byEntityId,omitempty"`
	ByObjectType    map[string][]int `json:"byObjectType,omitempty"`
	ByOpsStatusCode map[string][]int `json:"byOpsStatusCode,omitempty"`
	CAReadyResident []int            `json:"caReadyResident,omitempty"`
	ActivePayloads  []int            `json:"activePayloads,omitempty"`
}

// ExportDatasetWindow writes a native FlatSQL size-prefixed FlatBuffer shard
// and a deterministic materialized index for an indexed FlatSQL query.
func (s *FlatSQLStore) ExportDatasetWindow(outputDir string, filter IndexedRecordQuery) (*DatasetExport, error) {
	if outputDir == "" {
		return nil, fmt.Errorf("output dir is required")
	}
	if filter.SchemaName == "" {
		return nil, fmt.Errorf("schema name is required")
	}

	records, err := s.QueryIndexedRecords(filter)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no records match export query")
	}

	cids := make([]string, 0, len(records))
	for _, record := range records {
		cids = append(cids, record.CID)
	}
	sourceTags, err := s.sourceTagsForCIDs(filter.SchemaName, cids)
	if err != nil {
		return nil, fmt.Errorf("load source tags: %w", err)
	}

	exportRecords := make([]DatasetExportRecord, 0, len(records))
	for _, record := range records {
		exportRecords = append(exportRecords, DatasetExportRecord{
			CID:        record.CID,
			Data:       record.Data,
			SourceTags: sourceTags[record.CID],
		})
	}
	return ExportDatasetRecords(outputDir, filter, exportRecords)
}

// ExportDatasetRecords writes a native FlatSQL size-prefixed FlatBuffer shard
// and deterministic materialized index from records supplied by an SDN-owned
// source such as a legacy SQLite archive. It does not insert the records into
// the provider's FlatSQL store.
func ExportDatasetRecords(outputDir string, filter IndexedRecordQuery, records []DatasetExportRecord) (*DatasetExport, error) {
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

func summarizeExportSourceBatches(records []DatasetExportIndexRecord) []DatasetExportSourceBatch {
	type key struct {
		providerID string
		sourceName string
		sourceURL  string
		batchID    string
		contentKey string
	}
	ordered := make([]key, 0)
	counts := map[key]uint64{}
	for _, record := range records {
		k := key{
			providerID: record.SourceTags.ProviderID,
			sourceName: record.SourceTags.SourceName,
			sourceURL:  record.SourceTags.SourceURL,
			batchID:    record.SourceTags.BatchID,
			contentKey: record.SourceTags.ContentKeyID,
		}
		if _, ok := counts[k]; !ok {
			ordered = append(ordered, k)
		}
		counts[k]++
	}
	batches := make([]DatasetExportSourceBatch, 0, len(ordered))
	for _, k := range ordered {
		batches = append(batches, DatasetExportSourceBatch{
			ProviderID:   k.providerID,
			SourceName:   k.sourceName,
			SourceURL:    k.sourceURL,
			SourceSHA256: k.batchID,
			ContentKeyID: k.contentKey,
			RecordCount:  counts[k],
		})
	}
	return batches
}

func buildDatasetExportIndexes(records []DatasetExportIndexRecord) DatasetExportMaterializedMap {
	indexes := DatasetExportMaterializedMap{
		ByNORAD:         map[string][]int{},
		ByEntityID:      map[string][]int{},
		ByObjectType:    map[string][]int{},
		ByOpsStatusCode: map[string][]int{},
	}
	for i, record := range records {
		if record.NoradCatID != nil {
			key := fmt.Sprintf("%d", *record.NoradCatID)
			indexes.ByNORAD[key] = append(indexes.ByNORAD[key], i)
		}
		if record.EntityID != "" {
			indexes.ByEntityID[record.EntityID] = append(indexes.ByEntityID[record.EntityID], i)
		}
		if record.ObjectType != "" {
			indexes.ByObjectType[record.ObjectType] = append(indexes.ByObjectType[record.ObjectType], i)
		}
		if record.OpsStatusCode != "" {
			indexes.ByOpsStatusCode[record.OpsStatusCode] = append(indexes.ByOpsStatusCode[record.OpsStatusCode], i)
		}
		if record.NoradCatID != nil && record.ObjectType == "PAYLOAD" {
			indexes.CAReadyResident = append(indexes.CAReadyResident, i)
		}
		if record.ObjectType == "PAYLOAD" && isActiveCatalogOpsStatus(record.OpsStatusCode) {
			indexes.ActivePayloads = append(indexes.ActivePayloads, i)
		}
	}
	return indexes
}

func isActiveCatalogOpsStatus(status string) bool {
	switch status {
	case "OPERATIONAL", "PARTIALLY_OPERATIONAL", "BACKUP_STANDBY", "SPARE", "EXTENDED_MISSION", "UNKNOWN", "":
		return true
	default:
		return false
	}
}

func canonicalQueryJSON(filter IndexedRecordQuery) ([]byte, error) {
	payload := struct {
		SchemaName          string  `json:"schemaName"`
		Day                 string  `json:"day,omitempty"`
		NoradCatID          *uint32 `json:"noradCatId,omitempty"`
		EntityID            string  `json:"entityId,omitempty"`
		ObjectType          string  `json:"objectType,omitempty"`
		OpsStatusCode       string  `json:"opsStatusCode,omitempty"`
		ActivePayloads      bool    `json:"activePayloads,omitempty"`
		CAReadyResidentSet  bool    `json:"caReadyResidentSet,omitempty"`
		From                string  `json:"from,omitempty"`
		To                  string  `json:"to,omitempty"`
		ProviderID          string  `json:"providerId,omitempty"`
		SourceName          string  `json:"sourceName,omitempty"`
		BatchID             string  `json:"batchId,omitempty"`
		Limit               int     `json:"limit,omitempty"`
		Offset              int     `json:"offset,omitempty"`
		AllowLargeResultSet bool    `json:"allowLargeResultSet,omitempty"`
		OrderByCID          bool    `json:"orderByCid,omitempty"`
	}{
		SchemaName:          filter.SchemaName,
		Day:                 filter.Day,
		NoradCatID:          filter.NoradCatID,
		EntityID:            filter.EntityID,
		ObjectType:          filter.ObjectType,
		OpsStatusCode:       filter.OpsStatusCode,
		ActivePayloads:      filter.ActivePayloads,
		CAReadyResidentSet:  filter.CAReadyResidentSet,
		ProviderID:          filter.ProviderID,
		SourceName:          filter.SourceName,
		BatchID:             filter.BatchID,
		Limit:               filter.Limit,
		Offset:              filter.Offset,
		AllowLargeResultSet: filter.AllowLargeResultSet,
		OrderByCID:          filter.OrderByCID,
	}
	if filter.From != nil {
		payload.From = filter.From.UTC().Format(time.RFC3339Nano)
	}
	if filter.To != nil {
		payload.To = filter.To.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(payload)
}

func hashCanonicalQuery(filter IndexedRecordQuery) (string, error) {
	data, err := canonicalQueryJSON(filter)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func writeImmutableExportFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("refusing to overwrite immutable export file %s with different bytes", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing export file %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create export temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write export temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close export temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit export file: %w", err)
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cidV1RawSHA256(data []byte) (string, error) {
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return "", err
	}
	return cid.NewCidV1(cid.Raw, hash).String(), nil
}
