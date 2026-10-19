package api

// FlatBuffer-only record scan (owner 2026-10-18: "we are flatbuffer only").
//
//	POST /api/v1/data/scan
//	  request:  application/vnd.sdn.flatbuffers.stream, exactly one
//	            size-prefixed $QRP{KIND=Request} frame
//	  response: application/vnd.sdn.flatbuffers.stream, one or more runs of
//	            [$QRP{KIND=Page} header][RECORD_COUNT verbatim record frames]
//
// A run groups consecutive records of one lane; its header names the lane
// (PROVIDER_ID, SOURCE_NAME, BATCH_ID, PRODUCER_PEER_ID, ORIGIN_ID = storing
// peer) and repeats the page fields (CURSOR, NEXT_CURSOR, TOTAL_COUNT, LIMIT,
// ETAG = page chunk hash). An empty page is one header with RECORD_COUNT 0.
// Record frames are the stored bytes behind a u32le length; a reader derives
// each record's cid as the sha256 hex of those bytes. Every error is one
// $QRP{KIND=Error} frame. JSON is refused.

import (
	"bufio"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// scanRequestMaxBytes bounds the one $QRP request frame.
const scanRequestMaxBytes int64 = 64 << 10

// scanWriteBuffer batches small record frames into large socket writes.
const scanWriteBuffer = 256 << 10

func (h *DataQueryHandler) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "scan takes POST with one $QRP request frame", 0)
		return
	}
	if h.store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "storage_unavailable", "local storage unavailable in edge mode", 0)
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != StreamContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"scan takes one size-prefixed $QRP request frame as "+StreamContentType, 0)
		return
	}
	req, err := readScanRequest(r)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
		return
	}

	page, records, err := datasync.ScanPage(h.store, req, datasync.MaxSyncChunkLimit)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrEngineRebuilding):
			writeStatusFrame(w, http.StatusServiceUnavailable, QRPStatusBusy, "engine_rebuilding", "The record engine is rebuilding; retry shortly.", 30*time.Second)
		case errors.Is(err, storage.ErrSearchIndexBuilding):
			writeStatusFrame(w, http.StatusServiceUnavailable, QRPStatusCold, "search_index_building", err.Error(), 2*time.Second)
		default:
			WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
		}
		return
	}
	writeScanPage(w, h.store, page, records)
}

// readScanRequest decodes the one $QRP{KIND=Request} frame into the sync
// query. A continuation cursor carries its snapshot, so the next page does
// not count again.
func readScanRequest(r *http.Request) (datasync.QueryRequest, error) {
	frames, err := ReadFrames(r.Body, scanRequestMaxBytes)
	if err != nil {
		return datasync.QueryRequest{}, err
	}
	if len(frames) != 1 {
		return datasync.QueryRequest{}, errors.New("scan takes exactly one $QRP request frame")
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		return datasync.QueryRequest{}, err
	}
	if int8(q.KIND()) != QRPKindRequest {
		return datasync.QueryRequest{}, errors.New("scan takes a $QRP frame of KIND Request")
	}
	req := datasync.QueryRequest{
		Schema:         normalizeRecordIndexSchema(string(q.SCHEMA_NAME())),
		ProviderID:     string(q.PROVIDER_ID()),
		SourceName:     string(q.SOURCE_NAME()),
		BatchID:        string(q.BATCH_ID()),
		ProducerPeerID: string(q.PRODUCER_PEER_ID()),
		PeerID:         string(q.ORIGIN_ID()),
		Search:         strings.TrimSpace(string(q.SEARCH())),
		Cursor:         string(q.CURSOR()),
		Limit:          int(q.LIMIT()),
		IncludeData:    true,
	}
	if req.Schema == "" {
		return datasync.QueryRequest{}, errors.New("SCHEMA_NAME is required")
	}
	if snapshotID, total, ok := datasync.CursorSnapshot(req.Cursor); ok {
		req.SnapshotID, req.Head, req.TotalCount = snapshotID, snapshotID, total
	}
	return req, nil
}

// scanLane is the provenance a run header carries.
type scanLane struct {
	providerID, sourceName, batchID, producerPeerID, originID string
}

func laneOf(rec *storage.Record) scanLane {
	return scanLane{rec.SourceTags.ProviderID, rec.SourceTags.SourceName, rec.SourceTags.BatchID, rec.SourceTags.ProducerPeerID, rec.PeerID}
}

// writeScanPage streams the page runs straight to the client.
func writeScanPage(w http.ResponseWriter, store *storage.FlatSQLStore, page *datasync.Page, records []*storage.Record) {
	h := w.Header()
	h.Set("Content-Type", StreamContentType)
	h.Set(StreamFormatHeader, StreamFormat)
	h.Set(StreamSchemaHeader, page.Schema)
	h.Set("Cache-Control", "no-store")
	if page.NextCursor != "" {
		h.Set(StreamNextCursorHeader, page.NextCursor)
	}
	w.WriteHeader(http.StatusOK)

	fileIdentifier := ""
	if len(records) > 0 && len(records[0].Data) >= frameIdentifierOffset {
		fileIdentifier = string(records[0].Data[framePrefixLength:frameIdentifierOffset])
	}
	header := func(lane scanLane, count int) []byte {
		return BuildQRP(QRPFields{
			Kind:           QRPKindPage,
			Status:         QRPStatusOk,
			SchemaName:     page.Schema,
			FileIdentifier: fileIdentifier,
			ProviderID:     lane.providerID,
			SourceName:     lane.sourceName,
			BatchID:        lane.batchID,
			ProducerPeerID: lane.producerPeerID,
			OriginID:       lane.originID,
			Limit:          uint32(page.Limit),
			Cursor:         page.Cursor,
			NextCursor:     page.NextCursor,
			RecordCount:    uint32(count),
			TotalCount:     uint64(page.TotalCount),
			Etag:           page.ChunkHash,
			GeneratedAtMs:  time.Now().UnixMilli(),
		})
	}

	out := bufio.NewWriterSize(w, scanWriteBuffer)
	defer out.Flush()
	if len(records) == 0 {
		_, _ = out.Write(header(scanLane{}, 0))
		return
	}
	for start := 0; start < len(records); {
		lane := laneOf(records[start])
		end := start + 1
		for end < len(records) && laneOf(records[end]) == lane {
			end++
		}
		if _, err := out.Write(header(lane, end-start)); err != nil {
			return
		}
		if err := store.WriteRawRecordFrames(out, records[start:end]); err != nil {
			return
		}
		start = end
	}
}
