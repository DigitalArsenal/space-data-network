// Package datasync contains the shared SDN FlatSQL sync contract used by HTTP
// compatibility routes and libp2p stream handlers.
package datasync

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	ProtocolID               = "/space-data-network/flatsql-sync/1.0.0"
	RawFlatBufferStreamType  = "application/vnd.sdn.flatbuffers.stream"
	DefaultLimit             = 100
	MaxQueryLimit            = 1000
	MaxSyncChunkLimit        = 50000
	StreamRequestMaxBytes    = 32 * 1024 * 1024
	DefaultQueryProfile      = "ordered-offset-v1"
	HTTPTransport            = "http"
	Libp2pWebSocketTransport = "libp2p-websocket"
	Libp2pWebRTCTransport    = "libp2p-webrtc"
	Libp2pWebTransport       = "libp2p-webtransport"
)

var SupportedTransports = []string{
	HTTPTransport,
	Libp2pWebSocketTransport,
	Libp2pWebRTCTransport,
	Libp2pWebTransport,
}

type QueryRequest struct {
	Op                     string `json:"op,omitempty"`
	DatastoreKey           string `json:"datastore_key,omitempty"`
	Schema                 string `json:"schema"`
	SchemaName             string `json:"schema_name"`
	ProviderID             string `json:"provider_id"`
	ProviderId             string `json:"providerId"`
	SourceName             string `json:"source_name"`
	BatchID                string `json:"batch_id"`
	BatchId                string `json:"batchId"`
	ProducerPeerID         string `json:"producer_peer_id"`
	ProducerPeerId         string `json:"producerPeerId"`
	ProducerPublicKey      string `json:"producer_public_key"`
	ProducerPublicKeyCamel string `json:"producerPublicKey"`
	PeerID                 string `json:"peer_id"`
	PeerId                 string `json:"peerId"`
	Cursor                 string `json:"cursor"`
	SnapshotID             string `json:"snapshot_id"`
	Head                   string `json:"head"`
	HighWaterMark          string `json:"high_water_mark"`
	QueryProfile           string `json:"query_profile"`
	SyncFilter             string `json:"sync_filter"`
	TotalCount             int64  `json:"total_count"`
	Limit                  int    `json:"limit"`
	Offset                 int    `json:"offset"`
	IncludeData            bool   `json:"include_data"`
}

type StreamRequest struct {
	QueryRequest
	ScanHash      string      `json:"scan_hash"`
	ChunkHash     string      `json:"chunk_hash"`
	NextCursor    string      `json:"next_cursor"`
	TotalCount    int64       `json:"total_count"`
	HighWaterMark string      `json:"high_water_mark"`
	Records       []RecordRef `json:"records"`
}

type RecordRef struct {
	Schema                 string `json:"schema"`
	SchemaName             string `json:"schema_name"`
	CID                    string `json:"cid"`
	ID                     string `json:"id"`
	ProviderID             string `json:"provider_id"`
	ProviderId             string `json:"providerId"`
	SourceName             string `json:"source_name"`
	BatchID                string `json:"batch_id"`
	BatchId                string `json:"batchId"`
	ProducerPeerID         string `json:"producer_peer_id"`
	ProducerPeerId         string `json:"producerPeerId"`
	ProducerPublicKey      string `json:"producer_public_key"`
	ProducerPublicKeyCamel string `json:"producerPublicKey"`
	PeerID                 string `json:"peer_id"`
	PeerId                 string `json:"peerId"`
}

type ScanResponse struct {
	Schema        string                   `json:"schema"`
	TotalCount    int64                    `json:"total_count"`
	Count         int                      `json:"count"`
	Limit         int                      `json:"limit"`
	Offset        int                      `json:"offset"`
	Cursor        string                   `json:"cursor"`
	NextCursor    string                   `json:"next_cursor"`
	SnapshotID    string                   `json:"snapshot_id"`
	Head          string                   `json:"head"`
	HighWaterMark string                   `json:"high_water_mark"`
	ScanHash      string                   `json:"scan_hash"`
	ChunkHash     string                   `json:"chunk_hash"`
	QueryProfile  string                   `json:"query_profile"`
	SyncProtocol  string                   `json:"sync_protocol"`
	MaxChunkSize  int                      `json:"max_chunk_size"`
	Transports    []string                 `json:"transports"`
	Results       []map[string]interface{} `json:"results"`
}

type ManifestResponse struct {
	ManifestID        string            `json:"manifest_id"`
	Schema            string            `json:"schema"`
	ProviderID        string            `json:"provider_id,omitempty"`
	SourceName        string            `json:"source_name,omitempty"`
	BatchID           string            `json:"batch_id,omitempty"`
	ProducerPeerID    string            `json:"producer_peer_id,omitempty"`
	ProducerPublicKey string            `json:"producer_public_key,omitempty"`
	TotalCount        int64             `json:"total_count"`
	TotalBytes        int64             `json:"total_bytes"`
	SnapshotID        string            `json:"snapshot_id"`
	Head              string            `json:"head"`
	HighWaterMark     string            `json:"high_water_mark"`
	QueryProfile      string            `json:"query_profile"`
	SyncProtocol      string            `json:"sync_protocol"`
	MaxChunkSize      int               `json:"max_chunk_size"`
	Transports        []string          `json:"transports"`
	ArtifactBundles   []ArtifactBundle  `json:"artifact_bundles,omitempty"`
	Segments          []ManifestSegment `json:"segments"`
}

type ArtifactBundle struct {
	Role         string `json:"role"`
	CID          string `json:"cid"`
	ByteCount    int64  `json:"byte_count,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Format       string `json:"format,omitempty"`
	SegmentStart int    `json:"segment_start"`
	SegmentCount int    `json:"segment_count"`
}

type ManifestSegment struct {
	Index        int    `json:"index"`
	Cursor       string `json:"cursor"`
	NextCursor   string `json:"next_cursor"`
	RowCount     int    `json:"row_count"`
	ByteCount    int64  `json:"byte_count"`
	ChunkHash    string `json:"chunk_hash"`
	CID          string `json:"cid,omitempty"`
	IndexCID     string `json:"index_cid,omitempty"`
	ManifestCID  string `json:"manifest_cid,omitempty"`
	PNMCID       string `json:"pnm_cid,omitempty"`
	ShardSHA256  string `json:"shard_sha256,omitempty"`
	IndexSHA256  string `json:"index_sha256,omitempty"`
	QuerySHA256  string `json:"query_sha256,omitempty"`
	FeedSequence int64  `json:"feed_sequence,omitempty"`
	PreviousHead string `json:"previous_head,omitempty"`
	FeedHead     string `json:"feed_head,omitempty"`
}

type Snapshot struct {
	SnapshotID    string
	Head          string
	HighWaterMark string
	MaxRowID      int64
}

func NormalizeSchema(req QueryRequest) string {
	return strings.TrimSpace(FirstNonEmpty(req.Schema, req.SchemaName))
}

func NormalizeQueryProfile(value string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return DefaultQueryProfile
}

func FilterFromRequest(req QueryRequest, limit, offset int) storage.RawRecordQuery {
	return storage.RawRecordQuery{
		SchemaName:        NormalizeSchema(req),
		ProviderID:        FirstNonEmpty(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           FirstNonEmpty(req.BatchID, req.BatchId),
		ProducerPeerID:    FirstNonEmpty(req.ProducerPeerID, req.ProducerPeerId),
		ProducerPublicKey: FirstNonEmpty(req.ProducerPublicKey, req.ProducerPublicKeyCamel),
		PeerID:            FirstNonEmpty(req.PeerID, req.PeerId),
		SyncFilter:        req.SyncFilter,
		Limit:             limit,
		Offset:            offset,
	}
}

func Scan(store *storage.FlatSQLStore, req QueryRequest, maxLimit int) (*ScanResponse, []*storage.Record, error) {
	if store == nil {
		return nil, nil, fmt.Errorf("FlatSQL store is unavailable")
	}
	schemaName := NormalizeSchema(req)
	if schemaName == "" {
		return nil, nil, fmt.Errorf("schema is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if maxLimit <= 0 || maxLimit > MaxSyncChunkLimit {
		maxLimit = MaxSyncChunkLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	hasProvidedSnapshot := req.TotalCount > 0 && strings.TrimSpace(req.SnapshotID) != "" && strings.TrimSpace(req.Head) != ""
	offset := req.Offset
	useRowIDCursor := !hasProvidedSnapshot && req.Offset <= 0
	var cursor rawRecordCursor
	if strings.TrimSpace(req.Cursor) != "" {
		parsedCursor, ok, err := ParseRawRecordCursor(req.Cursor)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			cursor = parsedCursor
			useRowIDCursor = true
		} else {
			parsedOffset, err := ParseCursor(req.Cursor)
			if err != nil {
				return nil, nil, err
			}
			offset = parsedOffset
			useRowIDCursor = false
		}
	}
	if offset < 0 {
		return nil, nil, fmt.Errorf("offset must be non-negative")
	}

	filter := FilterFromRequest(req, limit, offset)
	filter.UseRowIDCursor = useRowIDCursor
	if useRowIDCursor {
		filter.AfterRowID = cursor.AfterRowID
		filter.MaxRowID = cursor.MaxRowID
	}
	var totalCount int64
	var snapshot Snapshot
	if hasProvidedSnapshot {
		totalCount = req.TotalCount
		snapshot = Snapshot{
			SnapshotID:    strings.TrimSpace(req.SnapshotID),
			Head:          strings.TrimSpace(req.Head),
			HighWaterMark: strings.TrimSpace(req.HighWaterMark),
			MaxRowID:      cursor.MaxRowID,
		}
		if useRowIDCursor && filter.MaxRowID <= 0 {
			return nil, nil, fmt.Errorf("row cursor is missing snapshot row boundary")
		}
	} else {
		var err error
		totalCount, err = store.CountRawRecords(filter)
		if err != nil {
			return nil, nil, err
		}
		snapshot, err = SnapshotForFilter(store, filter, totalCount, NormalizeQueryProfile(req.QueryProfile))
		if err != nil {
			return nil, nil, err
		}
		if useRowIDCursor {
			filter.MaxRowID = snapshot.MaxRowID
		}
	}
	records, err := store.QueryRawRecordRefs(filter)
	if err != nil {
		return nil, nil, err
	}
	results := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		results = append(results, RecordRow(schemaName, rec, false))
	}
	nextCursor := ""
	responseCursor := EncodeCursor(offset)
	if useRowIDCursor {
		responseCursor = EncodeRawRecordCursor(filter.AfterRowID, filter.MaxRowID, snapshot.SnapshotID)
		if len(records) > 0 {
			lastRowID := records[len(records)-1].RowID
			if len(records) >= limit && filter.MaxRowID > 0 && lastRowID < filter.MaxRowID {
				nextCursor = EncodeRawRecordCursor(lastRowID, filter.MaxRowID, snapshot.SnapshotID)
			}
		}
	} else if int64(offset+len(records)) < totalCount {
		nextCursor = EncodeCursor(offset + len(records))
	}
	chunkHash := ScanHash(schemaName, records)
	response := &ScanResponse{
		Schema:        schemaName,
		TotalCount:    totalCount,
		Count:         len(results),
		Limit:         limit,
		Offset:        offset,
		Cursor:        responseCursor,
		NextCursor:    nextCursor,
		SnapshotID:    snapshot.SnapshotID,
		Head:          snapshot.Head,
		HighWaterMark: snapshot.HighWaterMark,
		ScanHash:      chunkHash,
		ChunkHash:     chunkHash,
		QueryProfile:  NormalizeQueryProfile(req.QueryProfile),
		SyncProtocol:  ProtocolID,
		MaxChunkSize:  MaxSyncChunkLimit,
		Transports:    append([]string(nil), SupportedTransports...),
		Results:       results,
	}
	return response, records, nil
}

func OpenManifest(store *storage.FlatSQLStore, req QueryRequest, maxLimit int) (*ManifestResponse, error) {
	if store == nil {
		return nil, fmt.Errorf("FlatSQL store is unavailable")
	}
	schemaName := NormalizeSchema(req)
	if schemaName == "" {
		return nil, fmt.Errorf("schema is required")
	}
	segmentLimit := req.Limit
	if segmentLimit <= 0 {
		segmentLimit = MaxSyncChunkLimit
	}
	if maxLimit <= 0 || maxLimit > MaxSyncChunkLimit {
		maxLimit = MaxSyncChunkLimit
	}
	if segmentLimit > maxLimit {
		segmentLimit = maxLimit
	}
	queryProfile := NormalizeQueryProfile(req.QueryProfile)
	hasSyncFilter := strings.TrimSpace(req.SyncFilter) != ""
	if queryProfile == storage.DatasetPublicationQueryProfile && !hasSyncFilter {
		publishedManifest, err := OpenPublishedManifest(store, req, queryProfile, maxLimit)
		if err != nil {
			return nil, err
		}
		if publishedManifest != nil {
			return publishedManifest, nil
		}

		publishedLimit, found, err := store.FindLargestDatasetShardPublicationLimit(storage.DatasetShardPublicationQuery{
			SchemaName:   schemaName,
			ProviderID:   FirstNonEmpty(req.ProviderID, req.ProviderId),
			SourceName:   req.SourceName,
			BatchID:      FirstNonEmpty(req.BatchID, req.BatchId),
			QueryProfile: queryProfile,
		})
		if err != nil {
			return nil, err
		}
		if found && publishedLimit > 0 && publishedLimit <= maxLimit {
			segmentLimit = publishedLimit
		}
	}

	filter := FilterFromRequest(req, segmentLimit, 0)
	totalCount, err := store.CountRawRecords(filter)
	if err != nil {
		return nil, err
	}
	snapshot, err := SnapshotForFilter(store, filter, totalCount, NormalizeQueryProfile(req.QueryProfile))
	if err != nil {
		return nil, err
	}

	segments := make([]ManifestSegment, 0)
	var totalBytes int64
	for offset, index := 0, 0; int64(offset) < totalCount || (totalCount == 0 && index == 0); offset, index = offset+segmentLimit, index+1 {
		segmentReq := req
		segmentReq.Limit = segmentLimit
		segmentReq.Offset = offset
		segmentReq.Cursor = ""
		segmentReq.TotalCount = totalCount
		segmentReq.SnapshotID = snapshot.SnapshotID
		segmentReq.Head = snapshot.Head
		segmentReq.HighWaterMark = snapshot.HighWaterMark
		scan, records, err := Scan(store, segmentReq, maxLimit)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			break
		}
		byteCount := int64(0)
		for _, record := range records {
			byteCount += RecordSizeBytes(record)
		}
		segment := ManifestSegment{
			Index:      index,
			Cursor:     scan.Cursor,
			NextCursor: scan.NextCursor,
			RowCount:   len(records),
			ByteCount:  byteCount,
			ChunkHash:  scan.ChunkHash,
		}
		if !hasSyncFilter {
			if publication, found, err := findDatasetShardPublicationForSegment(store, storage.DatasetShardPublicationQuery{
				SchemaName:   schemaName,
				ProviderID:   FirstNonEmpty(req.ProviderID, req.ProviderId),
				SourceName:   req.SourceName,
				BatchID:      FirstNonEmpty(req.BatchID, req.BatchId),
				QueryProfile: queryProfile,
				Offset:       offset,
				Limit:        segmentLimit,
				RecordCount:  len(records),
			}); err != nil {
				return nil, err
			} else if found {
				segment.CID = publication.ShardCID
				segment.IndexCID = publication.IndexCID
				segment.ManifestCID = publication.ManifestCID
				segment.PNMCID = publication.PNMCID
				segment.ShardSHA256 = publication.ShardSHA256
				segment.IndexSHA256 = publication.IndexSHA256
				segment.QuerySHA256 = publication.QuerySHA256
				if publication.ResultSHA256 != "" {
					segment.ChunkHash = publication.ResultSHA256
				}
				if publication.ByteCount > 0 {
					segment.ByteCount = publication.ByteCount
				}
			}
		}
		totalBytes += segment.ByteCount
		segments = append(segments, segment)
		if scan.NextCursor == "" {
			break
		}
	}

	manifest := &ManifestResponse{
		Schema:            schemaName,
		ProviderID:        FirstNonEmpty(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           FirstNonEmpty(req.BatchID, req.BatchId),
		ProducerPeerID:    FirstNonEmpty(req.ProducerPeerID, req.ProducerPeerId),
		ProducerPublicKey: FirstNonEmpty(req.ProducerPublicKey, req.ProducerPublicKeyCamel),
		TotalCount:        totalCount,
		TotalBytes:        totalBytes,
		SnapshotID:        snapshot.SnapshotID,
		Head:              snapshot.Head,
		HighWaterMark:     snapshot.HighWaterMark,
		QueryProfile:      queryProfile,
		SyncProtocol:      ProtocolID,
		MaxChunkSize:      maxLimit,
		Transports:        append([]string(nil), SupportedTransports...),
		Segments:          segments,
	}
	manifest.ManifestID = ManifestHash(manifest)
	return manifest, nil
}

func OpenPublishedManifest(store *storage.FlatSQLStore, req QueryRequest, queryProfile string, maxLimit int) (*ManifestResponse, error) {
	schemaName := NormalizeSchema(req)
	publications, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schemaName,
		ProviderID:   FirstNonEmpty(req.ProviderID, req.ProviderId),
		SourceName:   req.SourceName,
		BatchID:      FirstNonEmpty(req.BatchID, req.BatchId),
		QueryProfile: queryProfile,
	})
	if err != nil {
		return nil, err
	}
	if len(publications) == 0 {
		return nil, nil
	}

	var totalCount int64
	var totalBytes int64
	for _, publication := range publications {
		totalCount += int64(publication.RecordCount)
		totalBytes += publication.ByteCount
	}

	segments := make([]ManifestSegment, 0, len(publications))
	for index, publication := range publications {
		chunkHash := publication.ResultSHA256
		if chunkHash == "" {
			chunkHash = publication.ShardSHA256
		}
		nextCursor := ""
		if index+1 < len(publications) {
			nextCursor = EncodeCursor(publications[index+1].Offset)
		}
		segments = append(segments, ManifestSegment{
			Index:        index,
			Cursor:       EncodeCursor(publication.Offset),
			NextCursor:   nextCursor,
			RowCount:     publication.RecordCount,
			ByteCount:    publication.ByteCount,
			ChunkHash:    chunkHash,
			CID:          publication.ShardCID,
			IndexCID:     publication.IndexCID,
			ManifestCID:  publication.ManifestCID,
			PNMCID:       publication.PNMCID,
			ShardSHA256:  publication.ShardSHA256,
			IndexSHA256:  publication.IndexSHA256,
			QuerySHA256:  publication.QuerySHA256,
			FeedSequence: publication.FeedSequence,
			PreviousHead: publication.PreviousHead,
			FeedHead:     publication.FeedHead,
		})
	}

	head := PublishedFeedHead(schemaName, FirstNonEmpty(req.ProviderID, req.ProviderId), req.SourceName, FirstNonEmpty(req.BatchID, req.BatchId), queryProfile, publications)
	highWater := PublishedFeedHighWaterMark(publications, totalCount, totalBytes)
	artifactBundles, err := publishedManifestArtifactBundles(store, req, queryProfile, head, highWater, len(segments), totalCount)
	if err != nil {
		return nil, err
	}
	manifest := &ManifestResponse{
		Schema:            schemaName,
		ProviderID:        FirstNonEmpty(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           FirstNonEmpty(req.BatchID, req.BatchId),
		ProducerPeerID:    FirstNonEmpty(req.ProducerPeerID, req.ProducerPeerId),
		ProducerPublicKey: FirstNonEmpty(req.ProducerPublicKey, req.ProducerPublicKeyCamel),
		TotalCount:        totalCount,
		TotalBytes:        totalBytes,
		SnapshotID:        head,
		Head:              head,
		HighWaterMark:     highWater,
		QueryProfile:      queryProfile,
		SyncProtocol:      ProtocolID,
		MaxChunkSize:      maxLimit,
		Transports:        append([]string(nil), SupportedTransports...),
		ArtifactBundles:   artifactBundles,
		Segments:          segments,
	}
	manifest.ManifestID = ManifestHash(manifest)
	return manifest, nil
}

func publishedManifestArtifactBundles(store *storage.FlatSQLStore, req QueryRequest, queryProfile, feedHead, highWaterMark string, segmentCount int, totalCount int64) ([]ArtifactBundle, error) {
	requestedBatchID := FirstNonEmpty(req.BatchID, req.BatchId)
	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:        NormalizeSchema(req),
		ProviderID:        FirstNonEmpty(req.ProviderID, req.ProviderId),
		SourceName:        req.SourceName,
		BatchID:           requestedBatchID,
		QueryProfile:      queryProfile,
		Role:              "shard-group-car",
		VerificationState: "verified",
	})
	if err != nil {
		return nil, fmt.Errorf("list published shard CAR bundles: %w", err)
	}
	seen := map[string]bool{}
	bundles := make([]ArtifactBundle, 0, len(entries))
	for _, entry := range entries {
		cidValue := strings.TrimSpace(entry.CID)
		if cidValue == "" || seen[cidValue] {
			continue
		}
		if feedHead != "" && entry.Head != feedHead && entry.SnapshotID != feedHead &&
			(entry.SegmentCount <= 0 || entry.HighWaterMark == "" || entry.HighWaterMark != highWaterMark) {
			continue
		}
		segmentStart := entry.SegmentStart
		bundleSegmentCount := entry.SegmentCount
		if bundleSegmentCount <= 0 {
			if totalCount <= 0 || entry.RowCount < totalCount {
				continue
			}
			segmentStart = 0
			bundleSegmentCount = segmentCount
		}
		if segmentStart < 0 || segmentStart >= segmentCount || bundleSegmentCount <= 0 {
			continue
		}
		if segmentStart+bundleSegmentCount > segmentCount {
			bundleSegmentCount = segmentCount - segmentStart
		}
		seen[cidValue] = true
		bundles = append(bundles, ArtifactBundle{
			Role:         "shard-group-car",
			CID:          cidValue,
			ByteCount:    entry.ByteCount,
			SHA256:       entry.ByteHash,
			Format:       "car-v1",
			SegmentStart: segmentStart,
			SegmentCount: bundleSegmentCount,
		})
	}
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].SegmentStart != bundles[j].SegmentStart {
			return bundles[i].SegmentStart < bundles[j].SegmentStart
		}
		return bundles[i].CID < bundles[j].CID
	})
	return bundles, nil
}

func findDatasetShardPublicationForSegment(store *storage.FlatSQLStore, query storage.DatasetShardPublicationQuery) (storage.DatasetShardPublication, bool, error) {
	limits := []int{query.Limit}
	if query.RecordCount > 0 && query.RecordCount < query.Limit {
		limits = append(limits, query.RecordCount)
	}
	for _, limit := range limits {
		nextQuery := query
		nextQuery.Limit = limit
		publication, found, err := store.FindDatasetShardPublication(nextQuery)
		if err != nil || found {
			return publication, found, err
		}
	}
	return storage.DatasetShardPublication{}, false, nil
}

func ResolveStreamRecords(store *storage.FlatSQLStore, req StreamRequest) (string, []*storage.Record, error) {
	schemaName := NormalizeSchema(req.QueryRequest)
	if schemaName == "" {
		return "", nil, fmt.Errorf("schema is required")
	}
	if len(req.Records) == 0 {
		return "", nil, fmt.Errorf("records are required")
	}
	if len(req.Records) > MaxSyncChunkLimit {
		return "", nil, fmt.Errorf("records limit is %d", MaxSyncChunkLimit)
	}
	refs := make([]storage.RawRecordRef, 0, len(req.Records))
	for _, ref := range req.Records {
		cid := strings.TrimSpace(FirstNonEmpty(ref.CID, ref.ID))
		if cid == "" {
			return "", nil, fmt.Errorf("record cid is required")
		}
		refSchema := strings.TrimSpace(FirstNonEmpty(ref.Schema, ref.SchemaName))
		if refSchema != "" && refSchema != schemaName {
			return "", nil, fmt.Errorf("record schema does not match stream schema")
		}
		refs = append(refs, storage.RawRecordRef{
			CID:               cid,
			ProviderID:        FirstNonEmpty(ref.ProviderID, ref.ProviderId),
			SourceName:        ref.SourceName,
			BatchID:           FirstNonEmpty(ref.BatchID, ref.BatchId),
			ProducerPeerID:    FirstNonEmpty(ref.ProducerPeerID, ref.ProducerPeerId),
			ProducerPublicKey: FirstNonEmpty(ref.ProducerPublicKey, ref.ProducerPublicKeyCamel),
			PeerID:            FirstNonEmpty(ref.PeerID, ref.PeerId),
		})
	}
	records, err := store.QueryRawRecordRefsByRefs(schemaName, refs)
	if err != nil {
		return "", nil, err
	}
	computedHash := ScanHash(schemaName, records)
	expectedHash := strings.TrimSpace(FirstNonEmpty(req.ChunkHash, req.ScanHash))
	if expectedHash != "" && expectedHash != computedHash {
		return "", nil, fmt.Errorf("scan hash does not match requested record refs")
	}
	return computedHash, records, nil
}

func SnapshotForFilter(store *storage.FlatSQLStore, filter storage.RawRecordQuery, totalCount int64, queryProfile string) (Snapshot, error) {
	head, err := store.RawRecordHead(filter)
	if err != nil {
		return Snapshot{}, err
	}
	queryProfile = NormalizeQueryProfile(queryProfile)
	highWater := fmt.Sprintf("%d:%d:%d:%d", head.MaxRecordTimestampUnix, head.MaxSourceUpdatedAtUnix, head.MaxCreatedAtUnix, totalCount)
	hash := sha256.New()
	_, _ = fmt.Fprintf(
		hash,
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d",
		filter.SchemaName,
		filter.ProviderID,
		filter.SourceName,
		filter.BatchID,
		filter.ProducerPeerID,
		filter.ProducerPublicKey,
		filter.PeerID,
		queryProfile,
		totalCount,
		head.TotalBytes,
		head.MaxRecordTimestampUnix,
		head.MaxSourceUpdatedAtUnix,
		head.MaxCreatedAtUnix,
		head.MaxRowID,
	)
	id := hex.EncodeToString(hash.Sum(nil))
	return Snapshot{
		SnapshotID:    id,
		Head:          id,
		HighWaterMark: highWater,
		MaxRowID:      head.MaxRowID,
	}, nil
}

func RecordRow(schemaName string, rec *storage.Record, includeData bool) map[string]interface{} {
	row := map[string]interface{}{
		"schema_name":    schemaName,
		"cid":            rec.CID,
		"peer_id":        rec.PeerID,
		"timestamp":      rec.Timestamp.UTC().Format(time.RFC3339),
		"size_bytes":     RecordSizeBytes(rec),
		"flatbuffer_uri": fmt.Sprintf("/api/v1/data/records/%s/%s", url.PathEscape(schemaName), url.PathEscape(rec.CID)),
	}
	if includeData && len(rec.Data) > 0 {
		row["data_base64"] = base64.StdEncoding.EncodeToString(rec.Data)
	}
	if len(rec.Signature) > 0 {
		row["signature_base64"] = base64.StdEncoding.EncodeToString(rec.Signature)
	}
	if !rec.MaterializedAt.IsZero() {
		row["materialized_at"] = rec.MaterializedAt.UTC().Format(time.RFC3339)
	}
	if rec.SourceTags.ProviderID != "" {
		row["provider_id"] = rec.SourceTags.ProviderID
	}
	if rec.SourceTags.SourceName != "" {
		row["source_name"] = rec.SourceTags.SourceName
	}
	if rec.SourceTags.SourceURL != "" {
		row["source_url"] = rec.SourceTags.SourceURL
	}
	if rec.SourceTags.BatchID != "" {
		row["batch_id"] = rec.SourceTags.BatchID
	}
	if rec.SourceTags.ContentKeyID != "" {
		row["content_key_id"] = rec.SourceTags.ContentKeyID
	}
	if rec.SourceTags.ProducerPeerID != "" {
		row["producer_peer_id"] = rec.SourceTags.ProducerPeerID
	}
	if rec.SourceTags.ProducerPublicKey != "" {
		row["producer_public_key"] = rec.SourceTags.ProducerPublicKey
	}
	return row
}

func ScanHash(schemaName string, records []*storage.Record) string {
	hash := sha256.New()
	for _, rec := range records {
		_, _ = fmt.Fprintf(
			hash,
			"%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\n",
			schemaName,
			rec.CID,
			rec.PeerID,
			RecordSizeBytes(rec),
			rec.SourceTags.ProviderID,
			rec.SourceTags.SourceName,
			rec.SourceTags.BatchID,
			rec.SourceTags.ProducerPeerID,
			rec.SourceTags.ProducerPublicKey,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func ManifestHash(manifest *ManifestResponse) string {
	hash := sha256.New()
	if manifest == nil {
		return hex.EncodeToString(hash.Sum(nil))
	}
	_, _ = fmt.Fprintf(
		hash,
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\n",
		manifest.Schema,
		manifest.ProviderID,
		manifest.SourceName,
		manifest.BatchID,
		manifest.ProducerPeerID,
		manifest.ProducerPublicKey,
		manifest.QueryProfile,
		manifest.TotalCount,
		manifest.TotalBytes,
		manifest.SnapshotID,
		manifest.Head,
		manifest.HighWaterMark,
		manifest.SyncProtocol,
	)
	for _, segment := range manifest.Segments {
		_, _ = fmt.Fprintf(
			hash,
			"%d\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\n",
			segment.Index,
			segment.Cursor,
			segment.NextCursor,
			segment.RowCount,
			segment.ByteCount,
			segment.ChunkHash,
			segment.CID,
			segment.IndexCID,
			segment.ManifestCID,
			segment.PNMCID,
			segment.ShardSHA256,
			segment.IndexSHA256,
			segment.QuerySHA256,
		)
	}
	for _, bundle := range manifest.ArtifactBundles {
		_, _ = fmt.Fprintf(
			hash,
			"%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%d\n",
			bundle.Role,
			bundle.CID,
			bundle.ByteCount,
			bundle.SHA256,
			bundle.Format,
			bundle.SegmentStart,
			bundle.SegmentCount,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func PublishedFeedHead(schemaName, providerID, sourceName, batchID, queryProfile string, publications []storage.DatasetShardPublication) string {
	if len(publications) > 0 {
		if head := strings.TrimSpace(publications[len(publications)-1].FeedHead); head != "" {
			return head
		}
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "sdn-dataset-feed-log-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\n", schemaName, providerID, sourceName, batchID, queryProfile, len(publications))
	for _, publication := range publications {
		_, _ = fmt.Fprintf(
			hash,
			"%d\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%d\n",
			publication.Offset,
			publication.Limit,
			publication.RecordCount,
			publication.ByteCount,
			publication.ShardCID,
			publication.IndexCID,
			publication.ManifestCID,
			publication.PNMCID,
			publication.ShardSHA256,
			publication.IndexSHA256,
			publication.QuerySHA256,
			publication.ResultSHA256,
			publication.PublishedAt.UTC().Unix(),
			publication.PreviousHead,
			publication.FeedHead,
			publication.FeedSequence,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func PublishedFeedHighWaterMark(publications []storage.DatasetShardPublication, totalCount, totalBytes int64) string {
	var newest int64
	for _, publication := range publications {
		if ts := publication.PublishedAt.UTC().Unix(); ts > newest {
			newest = ts
		}
	}
	return fmt.Sprintf("published-feed-v1:%d:%d:%d:%d", newest, len(publications), totalCount, totalBytes)
}

func RecordSizeBytes(rec *storage.Record) int64 {
	if len(rec.Data) > 0 {
		return int64(len(rec.Data))
	}
	if rec.RecordLength > 0 {
		return rec.RecordLength
	}
	return 0
}

func EncodeCursor(offset int) string {
	if offset < 0 {
		offset = 0
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

type rawRecordCursor struct {
	Version    int    `json:"v"`
	Mode       string `json:"mode"`
	AfterRowID int64  `json:"after_row_id"`
	MaxRowID   int64  `json:"max_row_id"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

func EncodeRawRecordCursor(afterRowID, maxRowID int64, snapshotID string) string {
	if afterRowID < 0 {
		afterRowID = 0
	}
	cursor := rawRecordCursor{
		Version:    1,
		Mode:       "rowid-snapshot",
		AfterRowID: afterRowID,
		MaxRowID:   maxRowID,
		SnapshotID: strings.TrimSpace(snapshotID),
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func ParseRawRecordCursor(cursor string) (rawRecordCursor, bool, error) {
	trimmed := strings.TrimSpace(cursor)
	if trimmed == "" {
		return rawRecordCursor{}, false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return rawRecordCursor{}, false, nil
	}
	if len(raw) == 0 || raw[0] != '{' {
		return rawRecordCursor{}, false, nil
	}
	var parsed rawRecordCursor
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return rawRecordCursor{}, false, fmt.Errorf("invalid cursor")
	}
	if parsed.Version != 1 || parsed.Mode != "rowid-snapshot" || parsed.AfterRowID < 0 || parsed.MaxRowID < 0 {
		return rawRecordCursor{}, false, fmt.Errorf("invalid cursor")
	}
	if parsed.MaxRowID > 0 && parsed.AfterRowID > parsed.MaxRowID {
		return rawRecordCursor{}, false, fmt.Errorf("invalid cursor")
	}
	return parsed, true, nil
}

func ParseCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return offset, nil
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
