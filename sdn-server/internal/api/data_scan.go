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
// The last page (NEXT_CURSOR empty) carries RESUME_CURSOR: a Request with it
// returns only the records stored since, and its own last page carries a
// fresh one. A resume cursor of another query is refused (400
// resume_cursor_query_mismatch); after a rebuild it expires (410
// resume_cursor_expired: rescan from the first page).
// Record frames are the stored bytes behind a u32le length; a reader derives
// each record's cid as the sha256 hex of those bytes. Every error is one
// $QRP{KIND=Error} frame. JSON is refused.

import (
	"errors"
	"mime"
	"net/http"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// scanRequestMaxBytes bounds the one $QRP request frame.
const scanRequestMaxBytes int64 = 64 << 10

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
		writeFailure(w, err)
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", StreamContentType)
	hdr.Set(StreamFormatHeader, StreamFormat)
	hdr.Set(StreamSchemaHeader, page.Schema)
	hdr.Set("Cache-Control", "no-store")
	if page.NextCursor != "" {
		hdr.Set(StreamNextCursorHeader, page.NextCursor)
	}
	w.WriteHeader(http.StatusOK)
	_ = syncwire.WriteScanPage(w, h.store, page, records)
}

// readScanRequest decodes the one $QRP{KIND=Request} frame into the sync
// query (syncwire.ScanQuery, shared with the libp2p read-chunk op).
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
	return syncwire.ScanQuery(q)
}

// scanWriteBuffer batches small record frames into large socket writes
// (the /data/query record runs, data_query_qrp.go).
const scanWriteBuffer = 256 << 10

// scanLane is the provenance a run header carries.
type scanLane struct {
	providerID, sourceName, batchID, producerPeerID, originID string
}

func laneOf(rec *storage.Record) scanLane {
	return scanLane{rec.SourceTags.ProviderID, rec.SourceTags.SourceName, rec.SourceTags.BatchID, rec.SourceTags.ProducerPeerID, rec.PeerID}
}
