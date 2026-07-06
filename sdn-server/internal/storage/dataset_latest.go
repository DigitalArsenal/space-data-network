package storage

// dataset_latest.go — the provider-scoped "latest dataset" read surface
// (gateway loop G.4, docs/gateway-api.md §10).
//
// "The latest published dataset" is defined BATCH-SCOPED and honestly: the
// shard content of one publication batch, exactly the aligned size-prefixed
// FlatBuffer stream the provider exported, pinned to IPFS, and signed — NOT
// whatever rows happen to sit in the store's tables. The store keeps the
// deterministic shard files for every publication it recorded (its own
// exports and the CID/SHA-verified replicas cached by the feed-head
// materialization path), so serving = reading the batch's part files in
// window order and splicing them. Content is re-verified against the
// recorded SHA-256 at every materialization: what leaves this function is
// provably the published bytes.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// DatasetBatchPart is one publication window of a served batch.
type DatasetBatchPart struct {
	Offset      int
	Limit       int
	RecordCount int
	ByteCount   int64
	ShardCID    string
	PublishedAt time.Time
}

// DatasetBatchContent is one publication batch materialized for serving.
type DatasetBatchContent struct {
	SchemaName  string
	ProviderID  string
	SourceName  string
	BatchID     string
	RecordCount int
	// Bytes is the concatenated aligned size-prefixed record stream of the
	// batch's shard files in window order (nil when materialized with
	// IncludeBytes=false).
	Bytes []byte
	// FNV1a64 is the word-folded FNV-1a 64 content hash of Bytes (the
	// gateway entity-tag identity; only set when bytes were read).
	FNV1a64 uint64
	Parts   []DatasetBatchPart
	// PublishedAt is the newest part's publication timestamp.
	PublishedAt time.Time
}

// DatasetBatchOptions tunes MaterializedDatasetBatch.
type DatasetBatchOptions struct {
	// IncludeBytes reads and concatenates the shard files (serving). When
	// false only servability is checked (supersede evaluation): part rows
	// contiguous from offset 0 and every shard file present with the
	// recorded size.
	IncludeBytes bool
}

// MaterializedDatasetBatch resolves one publication batch to locally
// materialized dataset content. Returns ok=false (with a nil error) when the
// batch is not servable here: no publication rows, a non-contiguous part
// chain, a possibly-truncated tail (see below), or missing/corrupt shard
// files.
//
// Servability rules (honesty over availability):
//   - all rows of (schema, batch) must belong to ONE (provider, source);
//   - the window chain must be contiguous from offset 0
//     (offset[i+1] == offset[i] + limit[i]);
//   - the final part must carry record_count < window_limit. A batch whose
//     last known part exactly fills its window is indistinguishable from a
//     mid-import batch (the publisher only stops the series on a short
//     window), so it is NOT served rather than risking a silently truncated
//     dataset. Production full-catalog publications (~32K records vs 50K
//     windows) always end short.
func (s *FlatSQLStore) MaterializedDatasetBatch(schemaName, batchID string, opts DatasetBatchOptions) (*DatasetBatchContent, bool, error) {
	if s == nil {
		return nil, false, errors.New("store is required")
	}
	schemaName = strings.TrimSpace(schemaName)
	batchID = strings.TrimSpace(batchID)
	if schemaName == "" {
		return nil, false, errors.New("schema name is required")
	}
	if batchID == "" {
		return nil, false, errors.New("batch id is required")
	}

	publications, err := s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   schemaName,
		BatchID:      batchID,
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		return nil, false, err
	}
	if len(publications) == 0 {
		return nil, false, nil
	}

	// One batch = one (provider, source) publication group.
	providerID := publications[0].ProviderID
	sourceName := publications[0].SourceName
	for _, pub := range publications {
		if pub.ProviderID != providerID || pub.SourceName != sourceName {
			return nil, false, fmt.Errorf("dataset batch %s %s spans multiple provider/source groups", schemaName, batchID)
		}
	}

	// Contiguous window chain from offset 0, short final window.
	expectedOffset := 0
	for i, pub := range publications {
		if pub.Offset != expectedOffset {
			log.Debugf("dataset batch %s %s not servable: window %d offset=%d, want %d", schemaName, batchID, i, pub.Offset, expectedOffset)
			return nil, false, nil
		}
		if pub.Limit <= 0 || pub.RecordCount <= 0 || pub.RecordCount > pub.Limit {
			return nil, false, nil
		}
		expectedOffset += pub.Limit
	}
	if last := publications[len(publications)-1]; last.RecordCount >= last.Limit {
		log.Debugf("dataset batch %s %s not servable: final window is full (%d/%d) — possibly mid-import", schemaName, batchID, last.RecordCount, last.Limit)
		return nil, false, nil
	}

	content := &DatasetBatchContent{
		SchemaName: schemaName,
		ProviderID: providerID,
		SourceName: sourceName,
		BatchID:    batchID,
	}
	var stream []byte
	if opts.IncludeBytes {
		var total int64
		for _, pub := range publications {
			total += pub.ByteCount
		}
		if total > 0 {
			stream = make([]byte, 0, total)
		}
	}
	for _, pub := range publications {
		shardPath, err := s.DatasetPublicationShardPath(pub)
		if err != nil {
			log.Debugf("dataset batch %s %s not servable: shard path for offset %d: %v", schemaName, batchID, pub.Offset, err)
			return nil, false, nil
		}
		info, err := os.Stat(shardPath)
		if err != nil || info.IsDir() {
			log.Debugf("dataset batch %s %s not servable: shard file missing (offset %d)", schemaName, batchID, pub.Offset)
			return nil, false, nil
		}
		if pub.ByteCount > 0 && info.Size() != pub.ByteCount {
			log.Warnf("dataset batch %s %s: shard file size %d != recorded %d (offset %d) — refusing to serve", schemaName, batchID, info.Size(), pub.ByteCount, pub.Offset)
			return nil, false, nil
		}
		if opts.IncludeBytes {
			data, err := os.ReadFile(shardPath)
			if err != nil {
				return nil, false, fmt.Errorf("read dataset shard %s: %w", shardPath, err)
			}
			if pub.ShardSHA256 != "" && sha256Hex(data) != pub.ShardSHA256 {
				log.Warnf("dataset batch %s %s: shard SHA-256 mismatch (offset %d) — refusing to serve", schemaName, batchID, pub.Offset)
				return nil, false, nil
			}
			stream = append(stream, data...)
		}
		content.RecordCount += pub.RecordCount
		content.Parts = append(content.Parts, DatasetBatchPart{
			Offset:      pub.Offset,
			Limit:       pub.Limit,
			RecordCount: pub.RecordCount,
			ByteCount:   pub.ByteCount,
			ShardCID:    pub.ShardCID,
			PublishedAt: pub.PublishedAt,
		})
		if pub.PublishedAt.After(content.PublishedAt) {
			content.PublishedAt = pub.PublishedAt
		}
	}
	if opts.IncludeBytes {
		content.Bytes = stream
		content.FNV1a64 = flatsqlrt.FNV1a64WordFolded(stream)
	}
	return content, true, nil
}
