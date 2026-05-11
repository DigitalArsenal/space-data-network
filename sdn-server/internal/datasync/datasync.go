// Package datasync contains the shared SDN FlatSQL sync contract used by HTTP
// compatibility routes and libp2p stream handlers.
package datasync

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
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
	Segments          []ManifestSegment `json:"segments"`
}

type ManifestSegment struct {
	Index      int    `json:"index"`
	Cursor     string `json:"cursor"`
	NextCursor string `json:"next_cursor"`
	RowCount   int    `json:"row_count"`
	ByteCount  int64  `json:"byte_count"`
	ChunkHash  string `json:"chunk_hash"`
	CID        string `json:"cid,omitempty"`
}

type Snapshot struct {
	SnapshotID    string
	Head          string
	HighWaterMark string
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
	offset := req.Offset
	if strings.TrimSpace(req.Cursor) != "" {
		parsedOffset, err := ParseCursor(req.Cursor)
		if err != nil {
			return nil, nil, err
		}
		offset = parsedOffset
	}
	if offset < 0 {
		return nil, nil, fmt.Errorf("offset must be non-negative")
	}

	filter := FilterFromRequest(req, limit, offset)
	var totalCount int64
	var snapshot Snapshot
	hasProvidedSnapshot := req.TotalCount > 0 && strings.TrimSpace(req.SnapshotID) != "" && strings.TrimSpace(req.Head) != ""
	if hasProvidedSnapshot {
		totalCount = req.TotalCount
		snapshot = Snapshot{
			SnapshotID:    strings.TrimSpace(req.SnapshotID),
			Head:          strings.TrimSpace(req.Head),
			HighWaterMark: strings.TrimSpace(req.HighWaterMark),
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
	if int64(offset+len(records)) < totalCount {
		nextCursor = EncodeCursor(offset + len(records))
	}
	chunkHash := ScanHash(schemaName, records)
	response := &ScanResponse{
		Schema:        schemaName,
		TotalCount:    totalCount,
		Count:         len(results),
		Limit:         limit,
		Offset:        offset,
		Cursor:        EncodeCursor(offset),
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
		totalBytes += byteCount
		segments = append(segments, ManifestSegment{
			Index:      index,
			Cursor:     scan.Cursor,
			NextCursor: scan.NextCursor,
			RowCount:   len(records),
			ByteCount:  byteCount,
			ChunkHash:  scan.ChunkHash,
		})
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
		QueryProfile:      NormalizeQueryProfile(req.QueryProfile),
		SyncProtocol:      ProtocolID,
		MaxChunkSize:      maxLimit,
		Transports:        append([]string(nil), SupportedTransports...),
		Segments:          segments,
	}
	manifest.ManifestID = ManifestHash(manifest)
	return manifest, nil
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
			"%d\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s\n",
			segment.Index,
			segment.Cursor,
			segment.NextCursor,
			segment.RowCount,
			segment.ByteCount,
			segment.ChunkHash,
			segment.CID,
		)
	}
	return hex.EncodeToString(hash.Sum(nil))
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
