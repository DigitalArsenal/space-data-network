package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const FlatSQLSyncProtocolID = datasync.ProtocolID

const (
	defaultWireSpeedProbeBytes    int64 = 64 * 1024 * 1024
	maxWireSpeedProbeBytes        int64 = 512 * 1024 * 1024
	wireSpeedProbeChunkBytes            = 1024 * 1024
	publishedShardCopyBufferBytes       = 1024 * 1024
)

// FlatSQLSyncHandler serves chunked, resumable FlatSQL raw-record streams over
// any libp2p stream transport, including WebSocket and WebRTC.
type FlatSQLSyncHandler struct {
	store *storage.FlatSQLStore
}

type flatSQLSyncRequest struct {
	Op                     string               `json:"op"`
	DatastoreKey           string               `json:"datastore_key"`
	Schema                 string               `json:"schema"`
	SchemaName             string               `json:"schema_name"`
	ProviderID             string               `json:"provider_id"`
	ProviderId             string               `json:"providerId"`
	SourceName             string               `json:"source_name"`
	BatchID                string               `json:"batch_id"`
	BatchId                string               `json:"batchId"`
	ProducerPeerID         string               `json:"producer_peer_id"`
	ProducerPeerId         string               `json:"producerPeerId"`
	ProducerPublicKey      string               `json:"producer_public_key"`
	ProducerPublicKeyCamel string               `json:"producerPublicKey"`
	PeerID                 string               `json:"peer_id"`
	PeerId                 string               `json:"peerId"`
	Cursor                 string               `json:"cursor"`
	SnapshotID             string               `json:"snapshot_id"`
	Head                   string               `json:"head"`
	QueryProfile           string               `json:"query_profile"`
	SyncFilter             string               `json:"sync_filter"`
	Limit                  int                  `json:"limit"`
	Offset                 int                  `json:"offset"`
	ScanHash               string               `json:"scan_hash"`
	ChunkHash              string               `json:"chunk_hash"`
	NextCursor             string               `json:"next_cursor"`
	TotalCount             int64                `json:"total_count"`
	HighWaterMark          string               `json:"high_water_mark"`
	Records                []datasync.RecordRef `json:"records"`
	LocalRows              int64                `json:"local_rows"`
	CachedBytes            int64                `json:"cached_bytes"`
	PinnedBytes            int64                `json:"pinned_bytes"`
	VerifiedChunks         []string             `json:"verified_chunks"`
	ProbeBytes             int64                `json:"probe_bytes"`
	CID                    string               `json:"cid"`
	CIDs                   []string             `json:"cids"`
	AssetRole              string               `json:"role"`
	AssetRoleSnake         string               `json:"asset_role"`
	ByteOffset             int64                `json:"byte_offset"`
	ByteLength             int64                `json:"byte_length"`
	PublicationOffset      int                  `json:"publication_offset"`
	PublicationLimit       int                  `json:"publication_limit"`
}

// NewFlatSQLSyncHandler creates a FlatSQL sync stream handler.
func NewFlatSQLSyncHandler(store *storage.FlatSQLStore) *FlatSQLSyncHandler {
	return &FlatSQLSyncHandler{store: store}
}

// HandleStream handles one FlatSQL sync request. The record data plane is a
// native FlatSQL little-endian size-prefixed FlatBuffer stream after the
// response header for read_chunk.
func (h *FlatSQLSyncHandler) HandleStream(s network.Stream) {
	defer s.Close()

	var req flatSQLSyncRequest
	if err := readFlatSQLSyncJSONFrame(s, datasync.StreamRequestMaxBytes, &req); err != nil {
		_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		return
	}

	switch strings.TrimSpace(req.Op) {
	case "", "read_chunk":
		if err := h.handleReadChunk(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "scan", "open_snapshot":
		if err := h.handleScan(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "open_manifest":
		if err := h.handleOpenManifest(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "list_datastores":
		if err := h.handleListDatastores(s); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "ack_progress":
		if err := h.handleAckProgress(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "wire_speed_probe":
		if err := h.handleWireSpeedProbe(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "read_published_shard":
		if err := h.handleReadPublishedShard(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "read_published_shard_batch":
		if err := h.handleReadPublishedShardBatch(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "read_published_asset":
		if err := h.handleReadPublishedAsset(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	case "list_published_shards":
		if err := h.handleListPublishedShards(s, req); err != nil {
			_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(err))
		}
	default:
		_ = writeFlatSQLSyncJSONFrame(s, flatSQLSyncErrorResponse(fmt.Errorf("unsupported sync op %q", req.Op)))
	}
}

func (h *FlatSQLSyncHandler) handleReadChunk(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	if len(req.Records) > 0 {
		streamReq := req.streamRequest()
		chunkHash, records, err := datasync.ResolveStreamRecords(activeStore, streamReq)
		if err != nil {
			return err
		}
		response := map[string]interface{}{
			"schema":          datasync.NormalizeSchema(streamReq.QueryRequest),
			"count":           len(records),
			"scan_hash":       chunkHash,
			"chunk_hash":      chunkHash,
			"snapshot_id":     req.SnapshotID,
			"head":            req.Head,
			"cursor":          req.Cursor,
			"next_cursor":     req.NextCursor,
			"total_count":     req.TotalCount,
			"high_water_mark": req.HighWaterMark,
			"query_profile":   datasync.NormalizeQueryProfile(req.QueryProfile),
			"sync_protocol":   FlatSQLSyncProtocolID,
			"max_chunk_size":  datasync.MaxSyncChunkLimit,
			"transports":      datasync.SupportedTransports,
		}
		if err := writeFlatSQLSyncJSONFrame(writer, response); err != nil {
			return err
		}
		return activeStore.WriteRawRecordFrames(writer, records)
	}

	response, records, err := datasync.Scan(activeStore, req.queryRequest(), datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	if err := writeFlatSQLSyncJSONFrame(writer, response); err != nil {
		return err
	}
	return activeStore.WriteRawRecordFrames(writer, records)
}

func (h *FlatSQLSyncHandler) handleScan(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	response, _, err := datasync.Scan(activeStore, req.queryRequest(), datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	return writeFlatSQLSyncJSONFrame(writer, response)
}

func (h *FlatSQLSyncHandler) handleOpenManifest(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	response, err := datasync.OpenManifest(activeStore, req.queryRequest(), datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	return writeFlatSQLSyncJSONFrame(writer, response)
}

func (h *FlatSQLSyncHandler) handleReadPublishedShard(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()

	cid := strings.TrimSpace(req.CID)
	if cid == "" {
		return fmt.Errorf("published shard cid is required")
	}
	item, err := h.publishedShardStreamItem(activeStore, req, cid)
	if err != nil {
		return err
	}
	byteOffset, byteLength, err := normalizePublishedShardByteRange(item.ByteCount, req.ByteOffset, req.ByteLength)
	if err != nil {
		return fmt.Errorf("published shard %s byte range: %w", cid, err)
	}
	item.Header["byte_offset"] = byteOffset
	item.Header["byte_length"] = byteLength
	item.Header["byte_count"] = byteLength
	item.Header["total_byte_count"] = item.ByteCount

	file, err := os.Open(item.Path)
	if err != nil {
		return fmt.Errorf("open published shard %s: %w", cid, err)
	}
	defer file.Close()
	if byteOffset > 0 {
		if _, err := file.Seek(byteOffset, io.SeekStart); err != nil {
			return fmt.Errorf("seek published shard %s to %d: %w", cid, byteOffset, err)
		}
	}
	if err := writeFlatSQLSyncJSONFrame(writer, item.Header); err != nil {
		return err
	}
	_, err = io.CopyBuffer(writer, io.LimitReader(file, byteLength), make([]byte, publishedShardCopyBufferBytes))
	return err
}

func (h *FlatSQLSyncHandler) handleReadPublishedShardBatch(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()

	cids := cleanPublishedShardCIDs(req.CIDs)
	if len(cids) == 0 && strings.TrimSpace(req.CID) != "" {
		cids = []string{strings.TrimSpace(req.CID)}
	}
	if len(cids) == 0 {
		return fmt.Errorf("published shard batch cids are required")
	}
	items := make([]publishedShardStreamItem, 0, len(cids))
	shards := make([]map[string]interface{}, 0, len(cids))
	var totalBytes int64
	for _, cid := range cids {
		item, err := h.publishedShardStreamItem(activeStore, req, cid)
		if err != nil {
			return err
		}
		items = append(items, item)
		shards = append(shards, item.Header)
		totalBytes += item.ByteCount
	}
	schema := strings.TrimSpace(firstNonEmptyProtocolString(req.Schema, req.SchemaName))
	queryProfile := strings.TrimSpace(req.QueryProfile)
	if queryProfile == "" {
		queryProfile = storage.DatasetPublicationQueryProfile
	}
	if err := writeFlatSQLSyncJSONFrame(writer, map[string]interface{}{
		"op":              "read_published_shard_batch",
		"status":          "ok",
		"schema":          schema,
		"provider_id":     firstNonEmptyProtocolString(req.ProviderID, req.ProviderId),
		"source_name":     req.SourceName,
		"batch_id":        firstNonEmptyProtocolString(req.BatchID, req.BatchId),
		"query_profile":   queryProfile,
		"sync_protocol":   FlatSQLSyncProtocolID,
		"transports":      datasync.SupportedTransports,
		"payload_format":  "concatenated-flatsql-size-prefixed-flatbuffers",
		"byte_count":      totalBytes,
		"shards":          shards,
		"immutable_bytes": true,
	}); err != nil {
		return err
	}
	buffer := make([]byte, publishedShardCopyBufferBytes)
	for _, item := range items {
		file, err := os.Open(item.Path)
		if err != nil {
			return fmt.Errorf("open published shard %s: %w", item.Pub.ShardCID, err)
		}
		_, copyErr := io.CopyBuffer(writer, file, buffer)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (h *FlatSQLSyncHandler) handleReadPublishedAsset(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()

	cid := strings.TrimSpace(req.CID)
	if cid == "" {
		return fmt.Errorf("published asset cid is required")
	}
	item, err := h.publishedAssetStreamItem(activeStore, req, cid)
	if err != nil {
		return err
	}
	byteOffset, byteLength, err := normalizePublishedShardByteRange(item.ByteCount, req.ByteOffset, req.ByteLength)
	if err != nil {
		return fmt.Errorf("published asset %s byte range: %w", cid, err)
	}
	item.Header["byte_offset"] = byteOffset
	item.Header["byte_length"] = byteLength
	item.Header["byte_count"] = byteLength
	item.Header["total_byte_count"] = item.ByteCount

	file, err := os.Open(item.Path)
	if err != nil {
		return fmt.Errorf("open published %s asset %s: %w", item.Role, cid, err)
	}
	defer file.Close()
	if byteOffset > 0 {
		if _, err := file.Seek(byteOffset, io.SeekStart); err != nil {
			return fmt.Errorf("seek published %s asset %s to %d: %w", item.Role, cid, byteOffset, err)
		}
	}
	if err := writeFlatSQLSyncJSONFrame(writer, item.Header); err != nil {
		return err
	}
	_, err = io.CopyBuffer(writer, io.LimitReader(file, byteLength), make([]byte, publishedShardCopyBufferBytes))
	return err
}

func (h *FlatSQLSyncHandler) handleListPublishedShards(writer io.Writer, req flatSQLSyncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()

	schema := strings.TrimSpace(firstNonEmptyProtocolString(req.Schema, req.SchemaName))
	if schema == "" {
		return fmt.Errorf("published shard schema is required")
	}
	queryProfile := strings.TrimSpace(req.QueryProfile)
	if queryProfile == "" {
		queryProfile = storage.DatasetPublicationQueryProfile
	}
	publications, err := activeStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   firstNonEmptyProtocolString(req.ProviderID, req.ProviderId),
		SourceName:   req.SourceName,
		BatchID:      firstNonEmptyProtocolString(req.BatchID, req.BatchId),
		QueryProfile: queryProfile,
	})
	if err != nil {
		return err
	}
	servable := publications[:0]
	for _, pub := range publications {
		if datasetShardPublicationAssetsAvailable(activeStore, pub) {
			servable = append(servable, pub)
		}
	}
	publications = servable

	offset := req.PublicationOffset
	if offset < 0 {
		offset = 0
	}
	if offset > len(publications) {
		offset = len(publications)
	}
	limit := req.PublicationLimit
	if limit <= 0 || offset+limit > len(publications) {
		limit = len(publications) - offset
	}
	selected := publications[offset : offset+limit]
	items := make([]map[string]interface{}, 0, len(selected))
	for _, pub := range selected {
		items = append(items, datasetShardPublicationListItem(pub))
	}
	return writeFlatSQLSyncJSONFrame(writer, map[string]interface{}{
		"op":                      "list_published_shards",
		"status":                  "ok",
		"schema":                  schema,
		"provider_id":             firstNonEmptyProtocolString(req.ProviderID, req.ProviderId),
		"source_name":             req.SourceName,
		"batch_id":                firstNonEmptyProtocolString(req.BatchID, req.BatchId),
		"query_profile":           queryProfile,
		"sync_protocol":           FlatSQLSyncProtocolID,
		"transports":              datasync.SupportedTransports,
		"publication_offset":      offset,
		"publication_count":       len(items),
		"total_publication_count": len(publications),
		"publications":            items,
	})
}

func datasetShardPublicationAssetsAvailable(store *storage.FlatSQLStore, pub storage.DatasetShardPublication) bool {
	if store == nil {
		return false
	}
	shardPath, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		return false
	}
	indexPath, err := store.DatasetPublicationIndexPath(pub)
	if err != nil {
		return false
	}
	return regularFileExists(shardPath) && regularFileExists(indexPath)
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func datasetShardPublicationListItem(pub storage.DatasetShardPublication) map[string]interface{} {
	return map[string]interface{}{
		"schema":        pub.SchemaName,
		"provider_id":   pub.ProviderID,
		"source_name":   pub.SourceName,
		"batch_id":      pub.BatchID,
		"query_profile": pub.QueryProfile,
		"offset":        pub.Offset,
		"limit":         pub.Limit,
		"record_count":  pub.RecordCount,
		"byte_count":    pub.ByteCount,
		"shard_cid":     pub.ShardCID,
		"index_cid":     pub.IndexCID,
		"manifest_cid":  pub.ManifestCID,
		"pnm_cid":       pub.PNMCID,
		"shard_sha256":  pub.ShardSHA256,
		"index_sha256":  pub.IndexSHA256,
		"query_sha256":  pub.QuerySHA256,
		"result_sha256": pub.ResultSHA256,
		"feed_sequence": pub.FeedSequence,
		"previous_head": pub.PreviousHead,
		"feed_head":     pub.FeedHead,
		"published_at":  pub.PublishedAt,
	}
}

type publishedShardStreamItem struct {
	Pub       storage.DatasetShardPublication
	Path      string
	ByteCount int64
	Header    map[string]interface{}
}

type publishedAssetStreamItem struct {
	Pub       storage.DatasetShardPublication
	Role      string
	Path      string
	ByteCount int64
	Header    map[string]interface{}
}

func (h *FlatSQLSyncHandler) publishedShardStreamItem(activeStore *storage.FlatSQLStore, req flatSQLSyncRequest, cid string) (publishedShardStreamItem, error) {
	schema := strings.TrimSpace(firstNonEmptyProtocolString(req.Schema, req.SchemaName))
	if schema == "" {
		return publishedShardStreamItem{}, fmt.Errorf("published shard schema is required")
	}
	queryProfile := strings.TrimSpace(req.QueryProfile)
	if queryProfile == "" {
		queryProfile = storage.DatasetPublicationQueryProfile
	}
	pub, found, err := activeStore.FindDatasetShardPublicationByCID(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   firstNonEmptyProtocolString(req.ProviderID, req.ProviderId),
		SourceName:   req.SourceName,
		BatchID:      firstNonEmptyProtocolString(req.BatchID, req.BatchId),
		QueryProfile: queryProfile,
	}, cid)
	if err != nil {
		return publishedShardStreamItem{}, err
	}
	if !found {
		return publishedShardStreamItem{}, fmt.Errorf("published shard was not found for %s", cid)
	}
	shardPath, err := activeStore.DatasetPublicationShardPath(pub)
	if err != nil {
		return publishedShardStreamItem{}, err
	}
	info, err := os.Stat(shardPath)
	if err != nil {
		return publishedShardStreamItem{}, fmt.Errorf("stat published shard %s: %w", cid, err)
	}
	if pub.ByteCount > 0 && info.Size() != pub.ByteCount {
		return publishedShardStreamItem{}, fmt.Errorf("published shard %s size = %d, want %d", cid, info.Size(), pub.ByteCount)
	}
	return publishedShardStreamItem{
		Pub:       pub,
		Path:      shardPath,
		ByteCount: info.Size(),
		Header: map[string]interface{}{
			"op":              "read_published_shard",
			"status":          "ok",
			"schema":          pub.SchemaName,
			"provider_id":     pub.ProviderID,
			"source_name":     pub.SourceName,
			"batch_id":        pub.BatchID,
			"query_profile":   pub.QueryProfile,
			"cid":             pub.ShardCID,
			"index_cid":       pub.IndexCID,
			"manifest_cid":    pub.ManifestCID,
			"pnm_cid":         pub.PNMCID,
			"row_count":       pub.RecordCount,
			"byte_count":      info.Size(),
			"shard_sha256":    pub.ShardSHA256,
			"index_sha256":    pub.IndexSHA256,
			"query_sha256":    pub.QuerySHA256,
			"result_sha256":   pub.ResultSHA256,
			"feed_sequence":   pub.FeedSequence,
			"previous_head":   pub.PreviousHead,
			"head":            pub.FeedHead,
			"sync_protocol":   FlatSQLSyncProtocolID,
			"transports":      datasync.SupportedTransports,
			"max_chunk_size":  datasync.MaxSyncChunkLimit,
			"payload_format":  "flatsql-size-prefixed-flatbuffers",
			"immutable_bytes": true,
		},
	}, nil
}

func (h *FlatSQLSyncHandler) publishedAssetStreamItem(activeStore *storage.FlatSQLStore, req flatSQLSyncRequest, cid string) (publishedAssetStreamItem, error) {
	pub, role, err := h.publishedAssetPublication(activeStore, req, cid)
	if err != nil {
		return publishedAssetStreamItem{}, err
	}
	path := ""
	sha := ""
	payloadFormat := ""
	switch role {
	case "shard":
		path, err = activeStore.DatasetPublicationShardPath(pub)
		sha = pub.ShardSHA256
		payloadFormat = "flatsql-size-prefixed-flatbuffers"
	case "index":
		path, err = activeStore.DatasetPublicationIndexPath(pub)
		sha = pub.IndexSHA256
		payloadFormat = "flatsql-index-json"
	default:
		err = fmt.Errorf("unsupported published asset role %q", role)
	}
	if err != nil {
		return publishedAssetStreamItem{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return publishedAssetStreamItem{}, fmt.Errorf("stat published %s asset %s: %w", role, cid, err)
	}
	return publishedAssetStreamItem{
		Pub:       pub,
		Role:      role,
		Path:      path,
		ByteCount: info.Size(),
		Header: map[string]interface{}{
			"op":              "read_published_asset",
			"status":          "ok",
			"schema":          pub.SchemaName,
			"provider_id":     pub.ProviderID,
			"source_name":     pub.SourceName,
			"batch_id":        pub.BatchID,
			"query_profile":   pub.QueryProfile,
			"role":            role,
			"cid":             cid,
			"shard_cid":       pub.ShardCID,
			"index_cid":       pub.IndexCID,
			"manifest_cid":    pub.ManifestCID,
			"pnm_cid":         pub.PNMCID,
			"row_count":       pub.RecordCount,
			"sha256":          sha,
			"query_sha256":    pub.QuerySHA256,
			"result_sha256":   pub.ResultSHA256,
			"feed_sequence":   pub.FeedSequence,
			"previous_head":   pub.PreviousHead,
			"head":            pub.FeedHead,
			"sync_protocol":   FlatSQLSyncProtocolID,
			"transports":      datasync.SupportedTransports,
			"payload_format":  payloadFormat,
			"immutable_bytes": true,
		},
	}, nil
}

func (h *FlatSQLSyncHandler) publishedAssetPublication(activeStore *storage.FlatSQLStore, req flatSQLSyncRequest, cid string) (storage.DatasetShardPublication, string, error) {
	schema := strings.TrimSpace(firstNonEmptyProtocolString(req.Schema, req.SchemaName))
	if schema == "" {
		return storage.DatasetShardPublication{}, "", fmt.Errorf("published asset schema is required")
	}
	queryProfile := strings.TrimSpace(req.QueryProfile)
	if queryProfile == "" {
		queryProfile = storage.DatasetPublicationQueryProfile
	}
	requestedRole := strings.TrimSpace(firstNonEmptyProtocolString(req.AssetRole, req.AssetRoleSnake))
	publications, err := activeStore.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   firstNonEmptyProtocolString(req.ProviderID, req.ProviderId),
		SourceName:   req.SourceName,
		BatchID:      firstNonEmptyProtocolString(req.BatchID, req.BatchId),
		QueryProfile: queryProfile,
	})
	if err != nil {
		return storage.DatasetShardPublication{}, "", err
	}
	for _, pub := range publications {
		role := ""
		switch cid {
		case pub.ShardCID:
			role = "shard"
		case pub.IndexCID:
			role = "index"
		}
		if role == "" {
			continue
		}
		if requestedRole != "" && requestedRole != role {
			return storage.DatasetShardPublication{}, "", fmt.Errorf("published asset %s is a %s, not %s", cid, role, requestedRole)
		}
		return pub, role, nil
	}
	return storage.DatasetShardPublication{}, "", fmt.Errorf("published asset was not found for %s", cid)
}

func cleanPublishedShardCIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		cid := strings.TrimSpace(value)
		if cid == "" {
			continue
		}
		if _, exists := seen[cid]; exists {
			continue
		}
		seen[cid] = struct{}{}
		out = append(out, cid)
	}
	return out
}

func normalizePublishedShardByteRange(totalBytes int64, requestedOffset int64, requestedLength int64) (int64, int64, error) {
	if totalBytes < 0 {
		return 0, 0, fmt.Errorf("total byte count is negative")
	}
	if requestedOffset < 0 {
		return 0, 0, fmt.Errorf("offset must be non-negative")
	}
	if requestedLength < 0 {
		return 0, 0, fmt.Errorf("length must be non-negative")
	}
	if requestedOffset > totalBytes {
		return 0, 0, fmt.Errorf("offset %d exceeds shard size %d", requestedOffset, totalBytes)
	}
	length := requestedLength
	if length == 0 {
		length = totalBytes - requestedOffset
	}
	if requestedOffset+length > totalBytes {
		return 0, 0, fmt.Errorf("range %d+%d exceeds shard size %d", requestedOffset, length, totalBytes)
	}
	return requestedOffset, length, nil
}

func (h *FlatSQLSyncHandler) handleListDatastores(writer io.Writer) error {
	if h.store == nil {
		return fmt.Errorf("FlatSQL store is unavailable")
	}
	entries, err := h.store.ListDatastoreIdentities()
	if err != nil {
		return err
	}
	results := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		results = append(results, map[string]interface{}{
			"key":        entry.Key,
			"identity":   entry.Identity,
			"updated_at": entry.UpdatedAt,
		})
	}
	return writeFlatSQLSyncJSONFrame(writer, map[string]interface{}{
		"count":   len(results),
		"results": results,
	})
}

func (h *FlatSQLSyncHandler) storeForRequest(req flatSQLSyncRequest) (*storage.FlatSQLStore, func(), error) {
	if h.store == nil {
		return nil, func() {}, fmt.Errorf("FlatSQL store is unavailable")
	}
	datastoreKey := strings.TrimSpace(req.DatastoreKey)
	if datastoreKey == "" {
		return h.store, func() {}, nil
	}
	store, err := h.store.OpenRegisteredDatastore(datastoreKey)
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() { _ = store.Close() }, nil
}

func (h *FlatSQLSyncHandler) handleAckProgress(writer io.Writer, req flatSQLSyncRequest) error {
	return writeFlatSQLSyncJSONFrame(writer, map[string]interface{}{
		"status":          "acknowledged",
		"sync_protocol":   FlatSQLSyncProtocolID,
		"schema":          strings.TrimSpace(firstNonEmptyProtocolString(req.Schema, req.SchemaName)),
		"snapshot_id":     req.SnapshotID,
		"head":            req.Head,
		"next_cursor":     req.NextCursor,
		"chunk_hash":      req.ChunkHash,
		"local_rows":      req.LocalRows,
		"cached_bytes":    req.CachedBytes,
		"pinned_bytes":    req.PinnedBytes,
		"verified_chunks": append([]string(nil), req.VerifiedChunks...),
	})
}

func (h *FlatSQLSyncHandler) handleWireSpeedProbe(writer io.Writer, req flatSQLSyncRequest) error {
	probeBytes := normalizeWireSpeedProbeBytes(req.ProbeBytes)
	if err := writeFlatSQLSyncJSONFrame(writer, map[string]interface{}{
		"op":              "wire_speed_probe",
		"status":          "ok",
		"sync_protocol":   FlatSQLSyncProtocolID,
		"probe_bytes":     probeBytes,
		"payload_bytes":   probeBytes,
		"max_probe_bytes": maxWireSpeedProbeBytes,
	}); err != nil {
		return err
	}
	return writeWireSpeedProbePayload(writer, probeBytes)
}

func normalizeWireSpeedProbeBytes(requested int64) int64 {
	if requested <= 0 {
		return defaultWireSpeedProbeBytes
	}
	if requested > maxWireSpeedProbeBytes {
		return maxWireSpeedProbeBytes
	}
	return requested
}

func writeWireSpeedProbePayload(writer io.Writer, totalBytes int64) error {
	chunk := make([]byte, wireSpeedProbeChunkBytes)
	var state uint32 = 0x9e3779b9
	for index := range chunk {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		chunk[index] = byte(state)
	}
	remaining := totalBytes
	for remaining > 0 {
		size := int64(len(chunk))
		if remaining < size {
			size = remaining
		}
		if _, err := writer.Write(chunk[:size]); err != nil {
			return err
		}
		remaining -= size
	}
	return nil
}

func (req flatSQLSyncRequest) queryRequest() datasync.QueryRequest {
	return datasync.QueryRequest{
		Op:                     req.Op,
		DatastoreKey:           req.DatastoreKey,
		Schema:                 req.Schema,
		SchemaName:             req.SchemaName,
		ProviderID:             req.ProviderID,
		ProviderId:             req.ProviderId,
		SourceName:             req.SourceName,
		BatchID:                req.BatchID,
		BatchId:                req.BatchId,
		ProducerPeerID:         req.ProducerPeerID,
		ProducerPeerId:         req.ProducerPeerId,
		ProducerPublicKey:      req.ProducerPublicKey,
		ProducerPublicKeyCamel: req.ProducerPublicKeyCamel,
		PeerID:                 req.PeerID,
		PeerId:                 req.PeerId,
		Cursor:                 req.Cursor,
		SnapshotID:             req.SnapshotID,
		Head:                   req.Head,
		HighWaterMark:          req.HighWaterMark,
		QueryProfile:           req.QueryProfile,
		SyncFilter:             req.SyncFilter,
		TotalCount:             req.TotalCount,
		Limit:                  req.Limit,
		Offset:                 req.Offset,
	}
}

func (req flatSQLSyncRequest) streamRequest() datasync.StreamRequest {
	return datasync.StreamRequest{
		QueryRequest:  req.queryRequest(),
		ScanHash:      req.ScanHash,
		ChunkHash:     req.ChunkHash,
		NextCursor:    req.NextCursor,
		TotalCount:    req.TotalCount,
		HighWaterMark: req.HighWaterMark,
		Records:       append([]datasync.RecordRef(nil), req.Records...),
	}
}

func readFlatSQLSyncJSONFrame(reader io.Reader, maxBytes int, target interface{}) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return fmt.Errorf("read sync frame header: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return fmt.Errorf("empty sync frame")
	}
	if maxBytes > 0 && int64(length) > int64(maxBytes) {
		return fmt.Errorf("sync frame exceeds %d bytes", maxBytes)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return fmt.Errorf("read sync frame payload: %w", err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode sync frame: %w", err)
	}
	return nil
}

func ReadFlatSQLSyncJSONFrame(reader io.Reader, maxBytes int, target interface{}) error {
	return readFlatSQLSyncJSONFrame(reader, maxBytes, target)
}

func writeFlatSQLSyncJSONFrame(writer io.Writer, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > int(^uint32(0)) {
		return fmt.Errorf("sync frame exceeds uint32 length")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func WriteFlatSQLSyncJSONFrame(writer io.Writer, payload interface{}) error {
	return writeFlatSQLSyncJSONFrame(writer, payload)
}

func flatSQLSyncErrorResponse(err error) map[string]interface{} {
	return map[string]interface{}{
		"status":        "error",
		"sync_protocol": FlatSQLSyncProtocolID,
		"error": map[string]interface{}{
			"message": err.Error(),
		},
	}
}

func firstNonEmptyProtocolString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
