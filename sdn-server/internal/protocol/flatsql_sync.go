package protocol

// flatsql-sync 2.0.0: chunked, resumable FlatSQL record and publication reads
// over any libp2p stream transport (TCP, WebSocket, WebRTC, WebTransport).
//
// One protocol ID per op (syncwire.ProtocolID). The request is exactly one
// size-prefixed $QRP{KIND=Request} frame; every reply begins with a $QRP
// Page or Error frame (u32 little-endian length prefix). read-chunk replies
// exactly as POST /api/v1/data/scan does: runs of [$QRP Page][RECORD_COUNT
// record frames]. Publication ops answer SEGMENT_COLUMNS rows; byte ops
// follow their header with the raw shard or index bytes it counts.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	defaultWireSpeedProbeBytes    int64 = 64 * 1024 * 1024
	maxWireSpeedProbeBytes        int64 = 512 * 1024 * 1024
	wireSpeedProbeChunkBytes            = 1024 * 1024
	publishedShardCopyBufferBytes       = 1024 * 1024
)

// FlatSQLSyncHandler serves the flatsql-sync 2.0.0 ops.
type FlatSQLSyncHandler struct {
	store *storage.FlatSQLStore
	// readAuthorizer admits a peer to schemas outside the anonymous read
	// surface, to other registered datastores, to the datastore listing and
	// to the wire-speed probe (NET-02, NET-06). Unset: nobody.
	readAuthorizer PeerAuthorizer
	probes         *wireSpeedProbeBudget
}

// syncRequest is one decoded $QRP request.
type syncRequest struct {
	op           string
	q            *QRP.QRP
	schema       string
	providerID   string
	sourceName   string
	batchID      string
	queryProfile string
	datastoreKey string
	cid          string
	cursor       string
	limit        int
	maxBytes     uint64
	shardCIDs    []string
}

// NewFlatSQLSyncHandler creates a FlatSQL sync stream handler.
//
// As constructed it serves only the anonymous read surface: the schemas
// sds.IsPublicReadSchema names, from the node's own datastore. Everything
// else needs SetPrivateReadAuthorizer.
func NewFlatSQLSyncHandler(store *storage.FlatSQLStore) *FlatSQLSyncHandler {
	return &FlatSQLSyncHandler{store: store, probes: newWireSpeedProbeBudget()}
}

// SetPrivateReadAuthorizer installs the check that admits a peer to private
// schemas, other datastores, list-datastores and wire-speed-probe. It returns
// h so registration can chain it.
func (h *FlatSQLSyncHandler) SetPrivateReadAuthorizer(fn PeerAuthorizer) *FlatSQLSyncHandler {
	h.readAuthorizer = fn
	return h
}

// StreamHandlerSetter is what Register needs of a libp2p host.
type StreamHandlerSetter interface {
	SetStreamHandler(pid libp2pprotocol.ID, handler network.StreamHandler)
}

// Register installs one stream handler per op on host.
func (h *FlatSQLSyncHandler) Register(host StreamHandlerSetter) {
	for _, op := range syncwire.Ops {
		host.SetStreamHandler(libp2pprotocol.ID(syncwire.ProtocolID(op)), h.streamHandler(op))
	}
}

func (h *FlatSQLSyncHandler) streamHandler(op string) network.StreamHandler {
	return func(s network.Stream) { h.HandleStream(op, s) }
}

// authorize applies the read surface to one request before any op runs. For
// a peer without private-read authority it rewrites the request's schema to
// the canonical public name it checked, so the name checked is the name read.
func (h *FlatSQLSyncHandler) authorize(peerID peer.ID, req *syncRequest) error {
	authorized := authorizedPeer(h.readAuthorizer, peerID)
	switch req.op {
	case syncwire.OpListDatastores, syncwire.OpWireSpeedProbe:
		if !authorized {
			return fmt.Errorf("%w: %s needs private-read authority", syncwire.ErrNotAuthorized, req.op)
		}
		return nil
	}
	if authorized {
		return nil
	}
	if req.datastoreKey != "" {
		return fmt.Errorf("%w: DATASTORE_KEY needs private-read authority", syncwire.ErrNotAuthorized)
	}
	if publishedLaneOps[req.op] {
		// The dataset-publication lane serves only what this node holds as a
		// dataset publication: shards and indexes announced on open topics,
		// whose manifests are pinned publicly. Its handlers look every byte
		// up in the publication index, never in a raw table, so it is open
		// to any peer for any well-formed schema name (trusted catch-up
		// replicates every standard over it). Raw tables stay behind the
		// public-read allow-list below.
		if req.schema == "" || sds.ValidateSchemaName(req.schema) != nil {
			return fmt.Errorf("%w (%s)", ErrReadNotAuthorized, strconv.QuoteToASCII(truncateName(req.schema)))
		}
		return nil
	}
	canonical, ok := publicReadSchema(req.schema)
	if !ok {
		return fmt.Errorf("%w (%s)", ErrReadNotAuthorized, strconv.QuoteToASCII(truncateName(req.schema)))
	}
	req.schema = canonical
	return nil
}

// publishedLaneOps are the flatsql-sync ops that read only the node's
// dataset publications (see FlatSQLSyncHandler.authorize).
var publishedLaneOps = map[string]bool{
	syncwire.OpListPublished:  true,
	syncwire.OpReadShard:      true,
	syncwire.OpReadIndex:      true,
	syncwire.OpReadShardBatch: true,
}

func truncateName(name string) string {
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

// HandleStream answers one request of op.
func (h *FlatSQLSyncHandler) HandleStream(op string, s network.Stream) {
	defer s.Close()
	defer recoverStream("flatsql-sync/"+op, s)
	_ = s.SetReadDeadline(time.Now().Add(DefaultReadTimeout))

	req, err := decodeSyncRequest(op, s)
	if err != nil {
		log.Warnf("flatsql-sync: refused %s request from %s: %s", op, streamPeer(s).ShortString(), boundedRefusalReason(err.Error()))
		_, _ = s.Write(syncwire.ErrorFrameFor(err))
		return
	}
	peerID := streamPeer(s)
	if err := h.authorize(peerID, &req); err != nil {
		log.Warnf("flatsql-sync: refused %s from %s: %s", op, peerID.ShortString(), boundedRefusalReason(err.Error()))
		_, _ = s.Write(syncwire.ErrorFrameFor(err))
		return
	}

	var requestErr error
	switch op {
	case syncwire.OpReadChunk:
		requestErr = h.handleReadChunk(s, req)
	case syncwire.OpOpenManifest:
		requestErr = h.handleOpenManifest(s, req)
	case syncwire.OpListPublished:
		requestErr = h.handleListPublished(s, req)
	case syncwire.OpReadShard, syncwire.OpReadIndex:
		requestErr = h.handleReadPublishedBytes(s, req)
	case syncwire.OpReadShardBatch:
		requestErr = h.handleReadShardBatch(s, req)
	case syncwire.OpWireSpeedProbe:
		requestErr = h.handleWireSpeedProbe(s, peerID, req)
	case syncwire.OpListDatastores:
		requestErr = h.handleListDatastores(s)
	default:
		requestErr = fmt.Errorf("unsupported sync op %q", truncateName(op))
	}
	if requestErr != nil {
		log.Debugf("flatsql-sync: %s from %s failed: %s", op, peerID.ShortString(), boundedRefusalReason(requestErr.Error()))
		_, _ = s.Write(syncwire.ErrorFrameFor(requestErr))
	}
}

// decodeSyncRequest reads the one $QRP request and its FILTERS. A filter the
// op does not apply is refused, never ignored.
func decodeSyncRequest(op string, r io.Reader) (syncRequest, error) {
	q, err := syncwire.ReadRequest(r)
	if err != nil {
		return syncRequest{}, err
	}
	req := syncRequest{
		op:         op,
		q:          q,
		schema:     syncwire.NormalizeSchema(string(q.SCHEMA_NAME())),
		providerID: strings.TrimSpace(string(q.PROVIDER_ID())),
		sourceName: strings.TrimSpace(string(q.SOURCE_NAME())),
		batchID:    strings.TrimSpace(string(q.BATCH_ID())),
		cid:        strings.TrimSpace(string(q.CID())),
		cursor:     strings.TrimSpace(string(q.CURSOR())),
		limit:      int(q.LIMIT()),
		maxBytes:   q.MAX_BYTES(),
	}
	for _, field := range syncwire.FilterFields(q) {
		switch field {
		case syncwire.FilterQueryProfile, syncwire.FilterDatastoreKey:
		case syncwire.FilterShardCID:
			if op != syncwire.OpReadShardBatch {
				return syncRequest{}, fmt.Errorf("%s takes no %s filter", op, field)
			}
		default:
			return syncRequest{}, fmt.Errorf("unsupported filter %q", truncateName(field))
		}
	}
	req.queryProfile, _ = syncwire.Filter(q, syncwire.FilterQueryProfile, syncwire.QRPFilterEq)
	req.queryProfile = strings.TrimSpace(req.queryProfile)
	req.datastoreKey, _ = syncwire.Filter(q, syncwire.FilterDatastoreKey, syncwire.QRPFilterEq)
	req.datastoreKey = strings.TrimSpace(req.datastoreKey)
	if cids, ok := syncwire.Filter(q, syncwire.FilterShardCID, syncwire.QRPFilterIn); ok {
		req.shardCIDs = cleanPublishedShardCIDs(strings.Split(cids, ","))
	}
	return req, nil
}

// publicationQueryProfile is the request's query profile, defaulting to the
// dataset-publication profile.
func (req syncRequest) publicationQueryProfile() string {
	if req.queryProfile != "" {
		return req.queryProfile
	}
	return storage.DatasetPublicationQueryProfile
}

func (req syncRequest) publicationQuery() storage.DatasetShardPublicationQuery {
	return storage.DatasetShardPublicationQuery{
		SchemaName:   req.schema,
		ProviderID:   req.providerID,
		SourceName:   req.sourceName,
		BatchID:      req.batchID,
		QueryProfile: req.publicationQueryProfile(),
	}
}

func (h *FlatSQLSyncHandler) handleReadChunk(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	query, err := syncwire.ScanQuery(req.q)
	if err != nil {
		return err
	}
	query.Schema = req.schema // the name authorize checked
	query.QueryProfile = req.queryProfile
	page, records, err := datasync.ScanPage(activeStore, query, datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	return syncwire.WriteScanPage(w, activeStore, page, records)
}

func (h *FlatSQLSyncHandler) handleOpenManifest(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	m, err := datasync.OpenManifest(activeStore, datasync.QueryRequest{
		Schema:         req.schema,
		ProviderID:     req.providerID,
		SourceName:     req.sourceName,
		BatchID:        req.batchID,
		ProducerPeerID: strings.TrimSpace(string(req.q.PRODUCER_PEER_ID())),
		PeerID:         strings.TrimSpace(string(req.q.ORIGIN_ID())),
		Search:         strings.TrimSpace(string(req.q.SEARCH())),
		QueryProfile:   req.queryProfile,
		Limit:          req.limit,
	}, datasync.MaxSyncChunkLimit)
	if err != nil {
		return err
	}
	segs := make([]syncwire.Segment, 0, len(m.Segments)+len(m.ArtifactBundles))
	for _, seg := range m.Segments {
		segs = append(segs, syncwire.Segment{
			SchemaName: m.Schema, ProviderID: m.ProviderID, SourceName: m.SourceName, BatchID: m.BatchID,
			QueryProfile: m.QueryProfile, Role: syncwire.RoleSegment,
			SegmentIndex: int64(seg.Index), Cursor: seg.Cursor, NextCursor: seg.NextCursor,
			RecordCount: int64(seg.RowCount), ByteCount: seg.ByteCount, ChunkHash: seg.ChunkHash,
			ShardCID: seg.CID, IndexCID: seg.IndexCID, ManifestCID: seg.ManifestCID, PNMCID: seg.PNMCID,
			ShardSHA256: seg.ShardSHA256, IndexSHA256: seg.IndexSHA256, QuerySHA256: seg.QuerySHA256,
			FeedSequence: seg.FeedSequence, PreviousHead: seg.PreviousHead, FeedHead: seg.FeedHead,
		})
	}
	for _, bundle := range m.ArtifactBundles {
		// A bundle row: SHARD_CID = the bundle CID, SHARD_SHA256 = its
		// bytes' hash, SEGMENT_INDEX = first segment covered, LIMIT =
		// segments covered.
		segs = append(segs, syncwire.Segment{
			SchemaName: m.Schema, ProviderID: m.ProviderID, SourceName: m.SourceName, BatchID: m.BatchID,
			QueryProfile: m.QueryProfile, Role: bundle.Role,
			SegmentIndex: int64(bundle.SegmentStart), Limit: int64(bundle.SegmentCount),
			ShardCID: bundle.CID, ByteCount: bundle.ByteCount, ShardSHA256: bundle.SHA256,
		})
	}
	_, err = w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:     m.Schema,
		ProviderID:     m.ProviderID,
		SourceName:     m.SourceName,
		BatchID:        m.BatchID,
		ProducerPeerID: m.ProducerPeerID,
		TotalCount:     uint64(max(m.TotalCount, 0)),
		Etag:           m.ManifestID,
		Cursor:         m.SnapshotID,
		GeneratedAtMs:  time.Now().UnixMilli(),
	}, segs))
	return err
}

func (h *FlatSQLSyncHandler) handleListPublished(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	if req.schema == "" {
		return fmt.Errorf("published shard schema is required")
	}
	publications, err := activeStore.ListDatasetShardPublications(req.publicationQuery())
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

	offset := 0
	if req.cursor != "" {
		if offset, err = strconv.Atoi(req.cursor); err != nil || offset < 0 {
			return fmt.Errorf("list-published CURSOR %q is not a node-issued offset", truncateName(req.cursor))
		}
	}
	offset = min(offset, len(publications))
	limit := req.limit
	if limit <= 0 || offset+limit > len(publications) {
		limit = len(publications) - offset
	}
	selected := publications[offset : offset+limit]
	segs := make([]syncwire.Segment, len(selected))
	for i, pub := range selected {
		segs[i] = syncwire.SegmentFromPublication(pub)
	}
	next := ""
	if offset+limit < len(publications) {
		next = strconv.Itoa(offset + limit)
	}
	_, err = w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:    req.schema,
		ProviderID:    req.providerID,
		SourceName:    req.sourceName,
		BatchID:       req.batchID,
		Limit:         uint32(limit),
		Cursor:        strconv.Itoa(offset),
		NextCursor:    next,
		TotalCount:    uint64(len(publications)),
		GeneratedAtMs: time.Now().UnixMilli(),
	}, segs))
	return err
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

// publishedAsset is one publication's shard or index file.
type publishedAsset struct {
	segment syncwire.Segment
	path    string
	size    int64
}

// publishedShard resolves a shard CID of the request's lane.
func publishedShard(store *storage.FlatSQLStore, req syncRequest, cid string) (publishedAsset, error) {
	if req.schema == "" {
		return publishedAsset{}, fmt.Errorf("published shard schema is required")
	}
	pub, found, err := store.FindDatasetShardPublicationByCID(req.publicationQuery(), cid)
	if err != nil {
		return publishedAsset{}, err
	}
	if !found {
		return publishedAsset{}, fmt.Errorf("published shard was not found for %s", cid)
	}
	path, err := store.DatasetPublicationShardPath(pub)
	if err != nil {
		return publishedAsset{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return publishedAsset{}, fmt.Errorf("stat published shard %s: %w", cid, err)
	}
	if pub.ByteCount > 0 && info.Size() != pub.ByteCount {
		return publishedAsset{}, fmt.Errorf("published shard %s size = %d, want %d", cid, info.Size(), pub.ByteCount)
	}
	seg := syncwire.SegmentFromPublication(pub)
	seg.Role, seg.ByteCount = syncwire.RoleShard, info.Size()
	return publishedAsset{segment: seg, path: path, size: info.Size()}, nil
}

// publishedIndex resolves an index CID of the request's lane.
func publishedIndex(store *storage.FlatSQLStore, req syncRequest, cid string) (publishedAsset, error) {
	if req.schema == "" {
		return publishedAsset{}, fmt.Errorf("published index schema is required")
	}
	publications, err := store.ListDatasetShardPublications(req.publicationQuery())
	if err != nil {
		return publishedAsset{}, err
	}
	for _, pub := range publications {
		if pub.IndexCID != cid {
			continue
		}
		path, err := store.DatasetPublicationIndexPath(pub)
		if err != nil {
			return publishedAsset{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return publishedAsset{}, fmt.Errorf("stat published index %s: %w", cid, err)
		}
		seg := syncwire.SegmentFromPublication(pub)
		seg.Role, seg.ByteCount = syncwire.RoleIndex, info.Size()
		return publishedAsset{segment: seg, path: path, size: info.Size()}, nil
	}
	return publishedAsset{}, fmt.Errorf("published index was not found for %s", cid)
}

// handleReadPublishedBytes serves read-shard and read-index: one Page with
// one row (BYTE_OFFSET, BYTE_LENGTH, BYTE_COUNT = whole file), then
// BYTE_LENGTH raw bytes from BYTE_OFFSET.
func (h *FlatSQLSyncHandler) handleReadPublishedBytes(w io.Writer, req syncRequest) error {
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	if req.cid == "" {
		return fmt.Errorf("%s CID is required", req.op)
	}
	resolve := publishedShard
	if req.op == syncwire.OpReadIndex {
		resolve = publishedIndex
	}
	asset, err := resolve(activeStore, req, req.cid)
	if err != nil {
		return err
	}
	var requestedOffset int64
	if req.cursor != "" {
		if requestedOffset, err = strconv.ParseInt(req.cursor, 10, 64); err != nil {
			return fmt.Errorf("%s CURSOR %q is not a byte offset", req.op, truncateName(req.cursor))
		}
	}
	if req.maxBytes > uint64(asset.size) {
		return fmt.Errorf("%s %s byte range: length %d exceeds size %d", req.op, req.cid, req.maxBytes, asset.size)
	}
	byteOffset, byteLength, err := normalizePublishedShardByteRange(asset.size, requestedOffset, int64(req.maxBytes))
	if err != nil {
		return fmt.Errorf("%s %s byte range: %w", req.op, req.cid, err)
	}
	asset.segment.ByteOffset, asset.segment.ByteLength = byteOffset, byteLength

	file, err := os.Open(asset.path)
	if err != nil {
		return fmt.Errorf("open %s %s: %w", asset.segment.Role, req.cid, err)
	}
	defer file.Close()
	if byteOffset > 0 {
		if _, err := file.Seek(byteOffset, io.SeekStart); err != nil {
			return fmt.Errorf("seek %s %s to %d: %w", asset.segment.Role, req.cid, byteOffset, err)
		}
	}
	if _, err := w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:    asset.segment.SchemaName,
		CID:           req.cid,
		GeneratedAtMs: time.Now().UnixMilli(),
	}, []syncwire.Segment{asset.segment})); err != nil {
		return err
	}
	_, err = io.CopyBuffer(w, io.LimitReader(file, byteLength), make([]byte, publishedShardCopyBufferBytes))
	return err
}

// handleReadShardBatch serves read-shard-batch: one Page with one row per
// shard in request order, then each shard's BYTE_COUNT bytes concatenated.
func (h *FlatSQLSyncHandler) handleReadShardBatch(w io.Writer, req syncRequest) error {
	if len(req.shardCIDs) > datasync.MaxSyncChunkLimit {
		return fmt.Errorf("published shard batch limit is %d", datasync.MaxSyncChunkLimit)
	}
	if len(req.shardCIDs) == 0 {
		return fmt.Errorf("read-shard-batch takes FILTERS [{SHARD_CID In \"a,b,c\"}]")
	}
	activeStore, cleanup, err := h.storeForRequest(req)
	if err != nil {
		return err
	}
	defer cleanup()
	assets := make([]publishedAsset, 0, len(req.shardCIDs))
	segs := make([]syncwire.Segment, 0, len(req.shardCIDs))
	var total int64
	for _, cid := range req.shardCIDs {
		asset, err := publishedShard(activeStore, req, cid)
		if err != nil {
			return err
		}
		asset.segment.ByteLength = asset.size
		assets = append(assets, asset)
		segs = append(segs, asset.segment)
		total += asset.size
	}
	if _, err := w.Write(syncwire.SegmentPage(syncwire.QRPFields{
		SchemaName:    req.schema,
		ProviderID:    req.providerID,
		SourceName:    req.sourceName,
		BatchID:       req.batchID,
		MaxBytes:      uint64(total),
		GeneratedAtMs: time.Now().UnixMilli(),
	}, segs)); err != nil {
		return err
	}
	buffer := make([]byte, publishedShardCopyBufferBytes)
	for _, asset := range assets {
		file, err := os.Open(asset.path)
		if err != nil {
			return fmt.Errorf("open published shard %s: %w", asset.segment.ShardCID, err)
		}
		_, copyErr := io.CopyBuffer(w, io.LimitReader(file, asset.size), buffer)
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

// DatastoreColumns is the COLUMNS of a list-datastores page.
var DatastoreColumns = []syncwire.QRPColumnField{
	{Name: "DATASTORE_KEY", Type: "string"},
	{Name: "SCHEMA_NAME", Type: "string"},
	{Name: "PROVIDER_ID", Type: "string"},
	{Name: "SOURCE_NAME", Type: "string"},
	{Name: "BATCH_ID", Type: "string"},
	{Name: "QUERY_PROFILE", Type: "string"},
	{Name: "PRODUCER_PEER_ID", Type: "string"},
	{Name: "PRODUCER_PUBLIC_KEY", Type: "string"},
	{Name: "UPDATED_AT_MS", Type: "int64"},
}

func (h *FlatSQLSyncHandler) handleListDatastores(w io.Writer) error {
	if h.store == nil {
		return fmt.Errorf("FlatSQL store is unavailable")
	}
	entries, err := h.store.ListDatastoreIdentities()
	if err != nil {
		return err
	}
	rows := make([][]syncwire.QRPCell, 0, len(entries))
	for _, entry := range entries {
		id := entry.Identity
		updated := entry.UpdatedAt * 1000 // registry stores Unix seconds
		rows = append(rows, []syncwire.QRPCell{
			syncwire.TextCell(entry.Key), syncwire.TextCell(id.SchemaName), syncwire.TextCell(id.ProviderID),
			syncwire.TextCell(id.SourceName), syncwire.TextCell(id.BatchHead), syncwire.TextCell(id.QueryProfile),
			syncwire.TextCell(id.SourcePeerID), syncwire.TextCell(id.SourcePublicKey), syncwire.IntCell(updated),
		})
	}
	_, err = w.Write(syncwire.BuildQRP(syncwire.QRPFields{
		Kind:          syncwire.QRPKindPage,
		Status:        syncwire.QRPStatusOk,
		TotalCount:    uint64(len(rows)),
		Columns:       DatastoreColumns,
		Rows:          rows,
		GeneratedAtMs: time.Now().UnixMilli(),
	}))
	return err
}

func (h *FlatSQLSyncHandler) storeForRequest(req syncRequest) (*storage.FlatSQLStore, func(), error) {
	if h.store == nil {
		return nil, func() {}, fmt.Errorf("FlatSQL store is unavailable")
	}
	if req.datastoreKey == "" {
		return h.store, func() {}, nil
	}
	store, err := h.store.OpenRegisteredDatastore(req.datastoreKey)
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() { _ = store.Close() }, nil
}

func (h *FlatSQLSyncHandler) handleWireSpeedProbe(w io.Writer, peerID peer.ID, req syncRequest) error {
	probeBytes := normalizeWireSpeedProbeBytes(int64(min(req.maxBytes, uint64(maxWireSpeedProbeBytes))))
	if h.probes == nil {
		return errors.New("wire-speed-probe: probe budget unavailable")
	}
	release, err := h.probes.acquire(peerID, probeBytes)
	if err != nil {
		return err
	}
	defer release()
	// A reader that stops consuming must not hold a node-wide probe slot:
	// every write (the header, then each chunk) must make progress within
	// DefaultHandlerTimeout. A slow but live reader keeps going.
	if err := extendProbeWriteDeadline(w); err != nil {
		return err
	}
	if _, err := w.Write(syncwire.BuildQRP(syncwire.QRPFields{
		Kind:          syncwire.QRPKindPage,
		Status:        syncwire.QRPStatusOk,
		MaxBytes:      uint64(probeBytes),
		GeneratedAtMs: time.Now().UnixMilli(),
	})); err != nil {
		return err
	}
	return writeWireSpeedProbePayload(w, probeBytes)
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

// extendProbeWriteDeadline gives the next probe write DefaultHandlerTimeout
// to complete, when the writer is a stream that supports deadlines.
func extendProbeWriteDeadline(writer io.Writer) error {
	stream, ok := writer.(interface{ SetWriteDeadline(time.Time) error })
	if !ok {
		return nil
	}
	if err := stream.SetWriteDeadline(time.Now().Add(DefaultHandlerTimeout)); err != nil {
		return fmt.Errorf("wire-speed-probe: set write deadline: %w", err)
	}
	return nil
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
		if err := extendProbeWriteDeadline(writer); err != nil {
			return err
		}
		if _, err := writer.Write(chunk[:size]); err != nil {
			return err
		}
		remaining -= size
	}
	return nil
}
