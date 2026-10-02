package storage

// record_backend_format2.go — store format 2 behind the record-backend seam
// (record_backend.go). Each method is the format-2 branch its FlatSQLStore
// method carried before the seam existed, unchanged: the work is the f2*
// functions' (format2_daemon_*.go).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// format2Backend is the format-2 partition store as the record backend.
type format2Backend struct{ s *FlatSQLStore }

var _ recordBackend = format2Backend{}

// ── writes ──────────────────────────────────────────────────────────────

func (b format2Backend) storeOne(schemaName string, data []byte, peerID string, signature []byte, tags *SourceTags) (string, error) {
	return b.s.f2StoreOne(schemaName, data, peerID, signature, tags)
}

// StoreWithSourceTags writes the record with its tag in one write: the
// engine keeps the tag on the record (A2).
func (b format2Backend) StoreWithSourceTags(schemaName string, data []byte, peerID string, signature []byte, tags SourceTags) (string, error) {
	return b.s.f2StoreOne(schemaName, data, peerID, signature, &tags)
}

func (b format2Backend) storeBatch(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags) (int, error) {
	n, _, err := b.s.f2StoreBatch(schemaName, records, peerID, signature, tags)
	return n, err
}

// StoreRoutedByProducer is keyed by the raw peer id: the engine derives the
// partition token from it (A3), exactly as this path named its table.
func (b format2Backend) StoreRoutedByProducer(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	return b.s.f2StoreOne(schemaName, data, peerID, signature, nil)
}

func (b format2Backend) importDatasetShardChunk(index *DatasetExportIndex, providerPeerID string, records []DatasetExportIndexRecord, readRecord datasetShardRecordReader) (int, error) {
	return b.s.f2ImportDatasetShardChunk(index, providerPeerID, records, readRecord)
}

func (b format2Backend) UpsertSourceTags(schemaName, cid string, tags SourceTags) error {
	return b.s.f2Retag(schemaName, cid, tags)
}

func (b format2Backend) Delete(schemaName, cid string) error {
	return b.s.f2Delete(schemaName, cid)
}

func (b format2Backend) reconcileSourceBatch(result SourceBatchReconcileResult) (SourceBatchReconcileResult, error) {
	return b.s.f2ReconcileSourceBatch(result)
}

// supersedeSourceBatches is RECONCILE(keep) in the lane's partitions (A2):
// the engine retires the tag instances and tombstones the records left
// without one, and keeps the lane counters itself.
func (b format2Backend) supersedeSourceBatches(result DatasetSupersedeResult, started time.Time) (DatasetSupersedeResult, error) {
	r, err := b.s.f2ReconcileSourceBatch(SourceBatchReconcileResult{SchemaName: result.SchemaName, ProviderID: result.ProviderID,
		SourceName: result.SourceName, KeepBatch: result.KeepBatch, Apply: true})
	if err != nil {
		return result, err
	}
	result.TagsDeleted, result.RecordsDeleted = r.Matched, r.Deleted
	if r.Matched > 0 {
		result.Chunks = 1
		log.Infof("Dataset supersede %s %s/%s keep %s: retired %d tag instances and %d records in %s (format 2)",
			result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch, r.Matched, r.Deleted,
			time.Since(started).Round(time.Millisecond))
	}
	return result, nil
}

func (b format2Backend) GarbageCollect(time.Duration) (int64, error) {
	return 0, f2Unsupported("age-based garbage collection (the engine evicts by quota in arrival order, §13)")
}

func (b format2Backend) GarbageCollectToQuota(maxBytes int64) (int64, error) {
	return b.s.f2GarbageCollectToQuota(maxBytes)
}

// RefreshSourceBatchSummary: the lane counters are maintained on append.
func (b format2Backend) RefreshSourceBatchSummary(string, string, string, string) error { return nil }

// RebuildSourceSummaries: the lane counters are the writer's.
func (b format2Backend) RebuildSourceSummaries() error { return nil }

// RebuildDerivedState: no hot window to hydrate.
func (b format2Backend) RebuildDerivedState() error { return nil }

// RebuildIndex: the partitions index on append.
func (b format2Backend) RebuildIndex() (map[string]int64, error) { return map[string]int64{}, nil }

// ── record reads ────────────────────────────────────────────────────────

func (b format2Backend) GetRecord(schemaName, cid string) (*Record, error) {
	return b.s.f2GetRecord(schemaName, cid)
}

func (b format2Backend) GetSourceTags(schemaName, cid string) (SourceTags, error) {
	return b.s.f2GetSourceTags(schemaName, cid)
}

func (b format2Backend) sourceTagsForCIDs(schemaName string, cids []string) (map[string]SourceTags, error) {
	return b.s.f2SourceTagsForCIDs(schemaName, cids, f2TagSpec{})
}

func (b format2Backend) exportSourceTags(filter IndexedRecordQuery, cids []string) (map[string]SourceTags, error) {
	return b.s.f2SourceTagsForCIDs(filter.SchemaName, cids, f2TagSpec{provider: strings.TrimSpace(filter.ProviderID),
		source: strings.TrimSpace(filter.SourceName), batch: strings.TrimSpace(filter.BatchID)})
}

func (b format2Backend) QueryRawRecordRefsByRefs(schemaName string, refs []RawRecordRef) ([]*Record, error) {
	return b.s.f2QueryRawRecordRefsByRefs(schemaName, refs)
}

func (b format2Backend) queryRawRecords(filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	return b.s.f2QueryRawRecords(filter, hydrate)
}

func (b format2Backend) QuerySourceTaggedRecords(query SourceTagQuery) ([]*Record, error) {
	return b.s.f2QuerySourceTaggedRecords(query)
}

func (b format2Backend) QueryRecentRecords(schemaName string, limit int) ([]*Record, error) {
	return b.s.f2QueryRecentRecords(schemaName, limit)
}

func (b format2Backend) FullTablePageWithCursor(query FullTablePageQuery) (FullTablePageResult, error) {
	return b.s.f2FullTablePage(query)
}

func (b format2Backend) Query(schemaName, whereClause string, _ ...interface{}) ([][]byte, error) {
	return b.s.f2QueryData(schemaName, whereClause, 0, 0)
}

func (b format2Backend) QueryAll(schemaName string, limit int) ([][]byte, error) {
	return b.s.f2QueryData(schemaName, "", min(max(limit, 1), 10000), 0)
}

func (b format2Backend) QueryAllBounded(schemaName string, limit int, maxTotalBytes int) ([][]byte, error) {
	return b.s.f2QueryData(schemaName, "", limit, maxTotalBytes)
}

func (b format2Backend) QueryRoutedByStandard(string, int) ([]RoutedRecord, error) {
	return nil, f2Unsupported("routed-table listings (the (producer, standard) tables are partitions)")
}

func (b format2Backend) QueryRoutedByProducer(string, int) ([]RoutedRecord, error) {
	return nil, f2Unsupported("routed-table listings (the (producer, standard) tables are partitions)")
}

func (b format2Backend) QueryRoutedAll(int) ([]RoutedRecord, error) {
	return nil, f2Unsupported("routed-table listings (the (producer, standard) tables are partitions)")
}

func (b format2Backend) QueryIndexedRecords(filter IndexedRecordQuery) ([]*Record, error) {
	return b.s.f2QueryIndexedRecords(filter)
}

func (b format2Backend) IndexedRecordWindowLimitForBytes(filter IndexedRecordQuery, maxBytes int64) (int, bool, error) {
	return b.s.f2IndexedRecordWindowLimitForBytes(filter, maxBytes)
}

func (b format2Backend) DatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID string) (string, int, error) {
	return b.s.f2PublicationSetFingerprint(schemaName, providerID, sourceName, batchID)
}

// QueryLogEntries is format 1's join, which cannot see the partition store's
// PLOG records: format 2 answers no entries, as it did before the seam.
func (b format2Backend) QueryLogEntries(publisherPeerID, schemaType string, sinceSequence uint64, limit int) ([][]byte, error) {
	return b.s.queryLogEntriesJoined(publisherPeerID, schemaType, sinceSequence, limit)
}

// ── counts, heads and index pages ───────────────────────────────────────

func (b format2Backend) CountRawRecords(filter RawRecordQuery) (int64, error) {
	return b.s.f2CountRawRecords(filter)
}

func (b format2Backend) RawRecordHead(filter RawRecordQuery) (RawRecordHead, error) {
	return b.s.f2RawRecordHead(filter)
}

// RawRecordSnapshot: the count and the head come from the same committed
// state: counters only move forward, and the head's MaxRowID bounds the
// pages.
func (b format2Backend) RawRecordSnapshot(filter RawRecordQuery) (int64, RawRecordHead, error) {
	head, err := b.s.f2RawRecordHead(filter)
	if err != nil {
		return 0, RawRecordHead{}, err
	}
	filter.MaxRowID = head.MaxRowID
	count, err := b.s.f2CountRawRecords(filter)
	return count, head, err
}

func (b format2Backend) Count(schemaName string) (int64, error) { return b.s.f2Count(schemaName) }

func (b format2Backend) EngineRecordCount(schemaName string) (int64, error) {
	return b.s.f2Count(schemaName)
}

func (b format2Backend) RecordIndexPage(q RecordIndexPageQuery) ([]RecordIndexRow, int64, error) {
	return b.s.f2RecordIndexPage(q)
}

// ── epoch profiles ──────────────────────────────────────────────────────

func (b format2Backend) queryEpochIndexedRecords(query EpochRecordQuery) ([]*Record, error) {
	return b.s.f2QueryEpochIndexedRecords(query)
}

func (b format2Backend) countEpochIndexedRows(query EpochRecordQuery) (int64, error) {
	return b.s.f2CountEpochIndexedRows(query)
}

func (b format2Backend) queryPointEpochRecords(query EpochRecordQuery) ([]EpochRecordMatch, error) {
	return b.s.f2QueryPointEpochRecords(query)
}

func (b format2Backend) countPointEpochEntities(query EpochRecordQuery) (int64, error) {
	return b.s.f2CountPointEpochEntities(query)
}

func (b format2Backend) QueryEpochCoverage(query EpochRecordQuery) ([]EpochCoverageBucket, error) {
	return b.s.f2QueryEpochCoverage(query)
}

// ── SQL surface ─────────────────────────────────────────────────────────

func (b format2Backend) QueryRawStream(sql string, params ...interface{}) (*flatsqlrt.RawStream, error) {
	return b.s.f2QueryRawStream(sql, params...)
}

func (b format2Backend) QuerySandboxedStream(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (*flatsqlrt.RawStream, error) {
	return b.s.f2QuerySandboxedStream(sql, caps, params...)
}

func (b format2Backend) QuerySandboxedJSON(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) ([]byte, int, int, error) {
	return b.s.f2QuerySandboxedJSON(sql, caps, params...)
}

func (b format2Backend) sandboxedSelect(ctx context.Context, stmt string, maxRows, maxBytes int, timeout time.Duration) (*SandboxSelectResult, error) {
	return b.s.f2SandboxedSelect(ctx, stmt, maxRows, maxBytes, timeout)
}

func (b format2Backend) PublicQuerySurface() ([]QuerySurfaceTable, error) {
	return b.s.f2PublicQuerySurface()
}

// ── summaries and accounting ────────────────────────────────────────────

func (b format2Backend) DataSummary() (*DataSummary, error) { return b.s.f2DataSummary() }

func (b format2Backend) SchemaDateRanges() ([]SchemaDateRange, error) {
	return b.s.f2SchemaDateRanges()
}

func (b format2Backend) LiveRecordBytes() (int64, error) { return b.s.f2LiveRecordBytes() }

// liveRecordBytesReconciled: the heads count every partition from its first
// append.
func (b format2Backend) liveRecordBytesReconciled() bool { return true }

func (b format2Backend) SourceRecordCounts() (map[string]int64, error) {
	return b.s.f2SourceRecordCounts()
}

func (b format2Backend) SourceBatchProgress() ([]SourceBatchProgress, error) {
	return b.s.f2SourceBatchProgress()
}

func (b format2Backend) ProducerSourceProgress() ([]ProducerSourceProgress, error) {
	return b.s.f2ProducerSourceProgress()
}

func (b format2Backend) laneBatchHoldsRecords(lane DatasetPublicationLane, batchID string) (bool, error) {
	batches, err := b.s.f2DistinctBatches(lane.SchemaName, lane.ProviderID, lane.SourceName)
	if err != nil {
		return false, fmt.Errorf("probe %s batch %s: %w", lane.SchemaName, batchID, err)
	}
	for _, id := range batches {
		if id == strings.TrimSpace(batchID) {
			return true, nil
		}
	}
	return false, nil
}

func (b format2Backend) laneHasOtherUnledgeredBatch(schemaName, providerID, sourceName string, known map[string]bool) (bool, error) {
	batches, err := b.s.f2DistinctBatches(strings.TrimSpace(schemaName), strings.TrimSpace(providerID), strings.TrimSpace(sourceName))
	if err != nil {
		return false, fmt.Errorf("list lane batches: %w", err)
	}
	for _, id := range batches {
		if !known[strings.TrimSpace(id)] {
			return true, nil
		}
	}
	return false, nil
}

func (b format2Backend) PeerStorageBytes(peerID string) (int64, error) {
	return b.s.f2PeerStorageBytes(peerID)
}

func (b format2Backend) DiskUsageBytes() (int64, error) { return b.s.f2DiskUsageBytes() }

// ── full-text search ────────────────────────────────────────────────────

func (b format2Backend) CheckFullTextSearch(schema, search string) error {
	return b.s.f2CheckFullTextSearch(schema, search)
}

func (b format2Backend) FullTextIndexState(schema string) string {
	return b.s.f2FullTextIndexState(schema)
}

func (b format2Backend) WarmFullTextIndexes() (scheduled, skipped []string, err error) {
	return b.s.f2WarmFullTextIndexes()
}

// ── engine hooks ────────────────────────────────────────────────────────

// recoverControlLocked replaces only the control instance; the partition
// store's instances are their own poison domains (§15).
func (b format2Backend) recoverControlLocked() (uint64, error) {
	return b.s.recoverControlInstanceLocked()
}

func (b format2Backend) close() error { return b.s.closeFormat2Locked() }
