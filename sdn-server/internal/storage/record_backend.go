package storage

// record_backend.go — the one seam between FlatSQLStore and a partitioned
// record store (contract §5.4; stack design
// docs/architecture/flatsql-sqlite-partitions.md §10).
//
// Format 1 keeps its records in the control database and serves them on the
// store's own paths. A partitioned store (format 2, and format 4 after it)
// keeps them in an engine of its own, and every record read and write of the
// node goes there instead: FlatSQLStore asks s.rb, once, at the top of each
// record method (or, where format 1 normalises its arguments first, right
// after that). s.rb is nil on format 1, so a format-1-only step (the hot
// window, the arena, the checkpoint of the record state, the supersede chunk
// loop) is guarded by s.rb == nil.
//
// The methods are the FlatSQLStore methods that dispatch, with their
// signatures; where a dispatch sits mid-method the method takes what the
// store already normalised. Nothing outside this package sees the interface.

import (
	"context"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// recordBackend is a partitioned record store behind FlatSQLStore.
type recordBackend interface {
	// ── writes ──────────────────────────────────────────────────────────
	storeOne(schemaName string, data []byte, peerID string, signature []byte, tags *SourceTags) (string, error)
	// StoreWithSourceTags runs after the batch licence is recorded.
	StoreWithSourceTags(schemaName string, data []byte, peerID string, signature []byte, tags SourceTags) (string, error)
	storeBatch(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags) (int, error)
	StoreRoutedByProducer(schemaName string, data []byte, peerID string, signature []byte) (string, error)
	importDatasetShardChunk(index *DatasetExportIndex, providerPeerID string, records []DatasetExportIndexRecord, readRecord datasetShardRecordReader) (int, error)
	// UpsertSourceTags runs after the writable and schema-name checks.
	UpsertSourceTags(schemaName, cid string, tags SourceTags) error
	Delete(schemaName, cid string) error
	// reconcileSourceBatch takes a validated request (count or apply).
	reconcileSourceBatch(result SourceBatchReconcileResult) (SourceBatchReconcileResult, error)
	// supersedeSourceBatches takes a validated request; the store removes the
	// superseded batches' shard files after it.
	supersedeSourceBatches(result DatasetSupersedeResult, started time.Time) (DatasetSupersedeResult, error)
	GarbageCollect(maxAge time.Duration) (int64, error)
	// GarbageCollectToQuota takes maxBytes > 0.
	GarbageCollectToQuota(maxBytes int64) (int64, error)
	RefreshSourceBatchSummary(schemaName, providerID, sourceName, batchID string) error
	RebuildSourceSummaries() error
	RebuildDerivedState() error
	RebuildIndex() (map[string]int64, error)

	// ── record reads ────────────────────────────────────────────────────
	GetRecord(schemaName, cid string) (*Record, error)
	GetSourceTags(schemaName, cid string) (SourceTags, error)
	sourceTagsForCIDs(schemaName string, cids []string) (map[string]SourceTags, error)
	// exportSourceTags is the tag of each exported record, preferring the
	// export's own lane.
	exportSourceTags(filter IndexedRecordQuery, cids []string) (map[string]SourceTags, error)
	QueryRawRecordRefsByRefs(schemaName string, refs []RawRecordRef) ([]*Record, error)
	queryRawRecords(filter RawRecordQuery, hydrate bool) ([]*Record, error)
	QuerySourceTaggedRecords(query SourceTagQuery) ([]*Record, error)
	QueryRecentRecords(schemaName string, limit int) ([]*Record, error)
	// FullTablePageWithCursor takes the normalised query.
	FullTablePageWithCursor(query FullTablePageQuery) (FullTablePageResult, error)
	Query(schemaName, whereClause string, args ...interface{}) ([][]byte, error)
	QueryAll(schemaName string, limit int) ([][]byte, error)
	// QueryAllBounded takes the clamped limit and byte budget.
	QueryAllBounded(schemaName string, limit int, maxTotalBytes int) ([][]byte, error)
	QueryRoutedByStandard(schemaName string, limit int) ([]RoutedRecord, error)
	QueryRoutedByProducer(producerID string, limit int) ([]RoutedRecord, error)
	QueryRoutedAll(limit int) ([]RoutedRecord, error)
	QueryIndexedRecords(filter IndexedRecordQuery) ([]*Record, error)
	// IndexedRecordWindowLimitForBytes takes maxBytes > 0.
	IndexedRecordWindowLimitForBytes(filter IndexedRecordQuery, maxBytes int64) (int, bool, error)
	DatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error)

	// ── counts, heads and index pages ───────────────────────────────────
	CountRawRecords(filter RawRecordQuery) (int64, error)
	RawRecordHead(filter RawRecordQuery) (RawRecordHead, error)
	RawRecordSnapshot(filter RawRecordQuery) (int64, RawRecordHead, error)
	Count(schemaName string) (int64, error)
	EngineRecordCount(schemaName string) (int64, error)
	RecordIndexPage(q RecordIndexPageQuery) ([]RecordIndexRow, int64, error)

	// ── epoch profiles ──────────────────────────────────────────────────
	queryEpochIndexedRecords(query EpochRecordQuery) ([]*Record, error)
	countEpochIndexedRows(query EpochRecordQuery) (int64, error)
	queryPointEpochRecords(query EpochRecordQuery) ([]EpochRecordMatch, error)
	countPointEpochEntities(query EpochRecordQuery) (int64, error)
	QueryEpochCoverage(query EpochRecordQuery) ([]EpochCoverageBucket, error)

	// ── SQL surface ─────────────────────────────────────────────────────
	QueryRawStream(sql string, params ...interface{}) (*flatsqlrt.RawStream, error)
	QuerySandboxedStream(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (*flatsqlrt.RawStream, error)
	QuerySandboxedJSON(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) ([]byte, int, int, error)
	// sandboxedSelect takes the checked statement and the clamped caps.
	sandboxedSelect(ctx context.Context, stmt string, maxRows, maxBytes int, timeout time.Duration) (*SandboxSelectResult, error)
	PublicQuerySurface() ([]QuerySurfaceTable, error)

	// ── summaries and accounting ────────────────────────────────────────
	DataSummary() (*DataSummary, error)
	SchemaDateRanges() ([]SchemaDateRange, error)
	LiveRecordBytes() (int64, error)
	liveRecordBytesReconciled() bool
	SourceRecordCounts() (map[string]int64, error)
	SourceBatchProgress() ([]SourceBatchProgress, error)
	ProducerSourceProgress() ([]ProducerSourceProgress, error)
	laneBatchHoldsRecords(lane DatasetPublicationLane, batchID string) (bool, error)
	// laneHasOtherUnledgeredBatch reports a lane batch id outside known.
	laneHasOtherUnledgeredBatch(schemaName, providerID, sourceName string, known map[string]bool) (bool, error)
	PeerStorageBytes(peerID string) (int64, error)
	DiskUsageBytes() (int64, error)

	// ── full-text search ────────────────────────────────────────────────
	CheckFullTextSearch(schema, search string) error
	FullTextIndexState(schema string) string
	WarmFullTextIndexes() (scheduled, skipped []string, err error)

	// ── engine hooks ────────────────────────────────────────────────────
	// recoverControlLocked replaces a poisoned control instance
	// (RecoverPoisonedEngine). Caller holds s.mu.
	recoverControlLocked() (uint64, error)
	// close stops the backend. Called by Close after the background loops
	// stopped, holding s.mu, before the control instance closes.
	close() error
}

// PartitionedRecords reports whether this store keeps its records in a
// partitioned record store (formats 2 and 4): reads never wait on a writer
// there, and format 1's maintenance passes (the index-key duplicate
// reconcile) do not exist.
func (s *FlatSQLStore) PartitionedRecords() bool { return s != nil && s.rb != nil }
