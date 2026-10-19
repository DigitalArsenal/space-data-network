package api

// POST /api/v1/data/query: one $QRP{KIND=Request} frame in, the scan shape
// out: runs of [$QRP{KIND=Page} header][RECORD_COUNT verbatim record frames].
// TIME_PROFILE (with PROFILE_EPOCH_MS) or FROM/TO_EPOCH_MS selects through the
// epoch index; FILTERS carry its keys (NORAD_CAT_ID, ENTITY_ID,
// MAX_DELTA_SECONDS). SQL is the sync-filter clause, small-body field filters
// included (sbd_query.go). Every error is one $QRP Error frame.
//
// Order and paging. A raw-lane page (no TIME_PROFILE) is the scan's order:
// store sequence ascending, each record once (datasync.ScanPage). A page
// without CURSOR pins a snapshot at the lane's sequence head: its records are
// the snapshot's [(PAGE-1)*LIMIT, PAGE*LIMIT) in sequence order and
// TOTAL_COUNT counts the snapshot. Its header carries CURSOR and, unless it is
// the snapshot's last page, NEXT_CURSOR; a request with CURSOR reads the next
// LIMIT records of that same snapshot (PAGE is not used), so a cursor walk
// returns every record of the snapshot exactly once under concurrent ingest
// and expiry. A PAGE walk without CURSOR pins a fresh snapshot per page;
// sequences only grow, so it is exact under append, not under expiry.
// SORT_FIELD and SORT_DESC are refused: sequence order is the one order.
// A small-body (SBD) page is sequence order with PAGE offsets; a TIME_PROFILE
// page is the epoch index's profile order. Neither takes CURSOR.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	dataQueryDefaultLimit = 100
	// dataQueryMaxLimit caps one page; a browser engine fills its window in
	// batches of this order, under storage's rawRecordMaxQueryLimit.
	dataQueryMaxLimit = 20000
	// dataQueryMaxPageDepth caps PAGE*LIMIT on the epoch path, whose index
	// read has no offset.
	dataQueryMaxPageDepth = 250000
)

// $QRP TIME_PROFILE values (qrpTimeProfile).
const (
	qrpProfileUnspecified int8 = 0
	qrpProfileAll         int8 = 1
	qrpProfileNearest     int8 = 2
	qrpProfileAsOf        int8 = 3
	qrpProfileForward     int8 = 4
	qrpProfileLatest      int8 = 5
)

// $QRP FILTERS OP values (qrpFilterOp) the epoch keys accept.
const (
	qrpFilterEq  int8 = 0
	qrpFilterLte int8 = 3
)

// dataQuery is one decoded request.
type dataQuery struct {
	raw       storage.RawRecordQuery
	archiveID string
	epoch     *storage.EpochRecordQuery
	page      int
	limit     int
	cursor    string
}

// queryPage is one answered page: its records, the total they were counted
// against and the cursors around them (raw-lane pages only).
type queryPage struct {
	records            []*storage.Record
	total              int64
	cursor, nextCursor string
}

func (h *DataQueryHandler) handleDataQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "query takes POST with one $QRP request frame", 0)
		return
	}
	if !h.ensureStore(w) {
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != StreamContentType {
		WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"query takes one size-prefixed $QRP request frame as "+StreamContentType, 0)
		return
	}
	q, err := readDataQuery(r)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
		return
	}
	store, closeStore, err := h.storeForDatastoreKey(q.archiveID)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
		return
	}
	defer closeStore()

	page, err := runDataQuery(r.Context(), store, q)
	if err != nil {
		writeDataReadError(w, err)
		return
	}
	writeQueryRuns(w, store, q, page)
}

// runDataQuery selects one page and its total.
func runDataQuery(ctx context.Context, store *storage.FlatSQLStore, q dataQuery) (queryPage, error) {
	if q.epoch != nil {
		total, err := store.CountEpochRecords(*q.epoch)
		if err != nil {
			return queryPage{}, err
		}
		matches, err := store.QueryEpochRecords(*q.epoch)
		if err != nil {
			return queryPage{}, err
		}
		offset := (q.page - 1) * q.limit
		records := make([]*storage.Record, 0, q.limit)
		for i := offset; i < len(matches) && len(records) < q.limit; i++ {
			if matches[i].Record != nil {
				records = append(records, matches[i].Record)
			}
		}
		return queryPage{records: records, total: total}, nil
	}
	sbd, isSBD, err := parseSBDFilter(q.raw.SchemaName, q.raw.SyncFilter)
	if isSBD {
		if err != nil {
			return queryPage{}, err
		}
		if q.cursor != "" {
			return queryPage{}, errors.New("CURSOR pages a raw lane; a small-body field filter pages by PAGE")
		}
		records, total, err := querySBD(ctx, store, q.raw, sbd, q.raw.Offset, q.raw.Limit)
		return queryPage{records: records, total: int64(total)}, err
	}
	return runRawLanePage(store, q)
}

// runRawLanePage is one raw-lane page of the snapshot scan (the package
// comment's order and paging). PAGE N without CURSOR walks the first N-1
// pages of a fresh snapshot as record refs (no record bytes), then reads
// page N under that snapshot.
func runRawLanePage(store *storage.FlatSQLStore, q dataQuery) (queryPage, error) {
	req := datasync.QueryRequest{
		Schema:         q.raw.SchemaName,
		ProviderID:     q.raw.ProviderID,
		SourceName:     q.raw.SourceName,
		BatchID:        q.raw.BatchID,
		ProducerPeerID: q.raw.ProducerPeerID,
		PeerID:         q.raw.PeerID,
		SyncFilter:     q.raw.SyncFilter,
		Search:         q.raw.Search,
		Limit:          q.limit,
	}
	continueAt := func(cursor string) {
		req.Cursor = cursor
		req.SnapshotID, req.Head, req.TotalCount = "", "", 0
		if snapshotID, total, ok := datasync.CursorSnapshot(cursor); ok {
			req.SnapshotID, req.Head, req.TotalCount = snapshotID, snapshotID, total
		}
	}
	continueAt(q.cursor)
	if q.cursor == "" {
		for skipped := 1; skipped < q.page; skipped++ {
			page, _, err := datasync.ScanPage(store, req, dataQueryMaxLimit)
			if err != nil {
				return queryPage{}, err
			}
			if page.NextCursor == "" {
				// The snapshot ends before page q.page.
				return queryPage{total: page.TotalCount}, nil
			}
			continueAt(page.NextCursor)
		}
	}
	req.IncludeData = true
	page, records, err := datasync.ScanPage(store, req, dataQueryMaxLimit)
	if err != nil {
		return queryPage{}, err
	}
	return queryPage{records: records, total: page.TotalCount, cursor: page.Cursor, nextCursor: page.NextCursor}, nil
}

// writeDataReadError maps a store read failure onto one $QRP Error frame.
func writeDataReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrEngineRebuilding):
		writeStatusFrame(w, http.StatusServiceUnavailable, QRPStatusBusy, "engine_rebuilding", "The record engine is rebuilding; retry shortly.", 30*time.Second)
	case errors.Is(err, storage.ErrSearchIndexBuilding):
		writeStatusFrame(w, http.StatusServiceUnavailable, QRPStatusCold, "search_index_building", err.Error(), 2*time.Second)
	case errors.Is(err, errSBDBusy):
		writeStatusFrame(w, http.StatusServiceUnavailable, QRPStatusBusy, "scan_busy", err.Error(), 5*time.Second)
	default:
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", err.Error(), 0)
	}
}

// readDataQuery decodes the one $QRP{KIND=Request} frame.
func readDataQuery(r *http.Request) (dataQuery, error) {
	frames, err := ReadFrames(r.Body, scanRequestMaxBytes)
	if err != nil {
		return dataQuery{}, err
	}
	if len(frames) != 1 {
		return dataQuery{}, errors.New("query takes exactly one $QRP request frame")
	}
	f, err := ParseQRP(frames[0])
	if err != nil {
		return dataQuery{}, err
	}
	if int8(f.KIND()) != QRPKindRequest {
		return dataQuery{}, errors.New("query takes a $QRP frame of KIND Request")
	}
	schema := normalizeRecordIndexSchema(string(f.SCHEMA_NAME()))
	if schema == "" {
		return dataQuery{}, errors.New("SCHEMA_NAME is required")
	}
	q := dataQuery{
		archiveID: strings.TrimSpace(string(f.ARCHIVE_ID())),
		page:      max(int(f.PAGE()), 1),
		limit:     int(f.LIMIT()),
		cursor:    strings.TrimSpace(string(f.CURSOR())),
	}
	if len(f.SORT_FIELD()) > 0 || f.SORT_DESC() {
		return dataQuery{}, errors.New("query pages are in store sequence order: SORT_FIELD and SORT_DESC are not supported")
	}
	if q.limit <= 0 {
		q.limit = dataQueryDefaultLimit
	}
	q.limit = min(q.limit, dataQueryMaxLimit)
	q.raw = storage.RawRecordQuery{
		SchemaName:     schema,
		ProviderID:     strings.TrimSpace(string(f.PROVIDER_ID())),
		SourceName:     strings.TrimSpace(string(f.SOURCE_NAME())),
		BatchID:        strings.TrimSpace(string(f.BATCH_ID())),
		ProducerPeerID: strings.TrimSpace(string(f.PRODUCER_PEER_ID())),
		PeerID:         strings.TrimSpace(string(f.ORIGIN_ID())),
		SyncFilter:     strings.TrimSpace(string(f.SQL())),
		Search:         strings.TrimSpace(string(f.SEARCH())),
		Limit:          q.limit,
		Offset:         (q.page - 1) * q.limit,
	}
	profile := int8(f.TIME_PROFILE())
	from, to := f.FROM_EPOCH_MS(), f.TO_EPOCH_MS()
	if profile == qrpProfileUnspecified && from == 0 && to == 0 {
		if f.FILTERSLength() > 0 {
			return dataQuery{}, errors.New("FILTERS select through the epoch index: set TIME_PROFILE or FROM/TO_EPOCH_MS")
		}
		return q, nil
	}
	if q.raw.ProducerPeerID != "" || q.raw.PeerID != "" || q.raw.SyncFilter != "" || q.raw.Search != "" || q.cursor != "" {
		return dataQuery{}, errors.New("a TIME_PROFILE query takes PROVIDER_ID, SOURCE_NAME, BATCH_ID and FILTERS, not PRODUCER_PEER_ID, ORIGIN_ID, SQL, SEARCH or CURSOR")
	}
	if q.page*q.limit > dataQueryMaxPageDepth {
		return dataQuery{}, fmt.Errorf("PAGE*LIMIT exceeds %d on a TIME_PROFILE query", dataQueryMaxPageDepth)
	}
	e := storage.EpochRecordQuery{
		SchemaName: schema,
		ProviderID: q.raw.ProviderID,
		SourceName: q.raw.SourceName,
		BatchID:    q.raw.BatchID,
		Limit:      q.page * q.limit,
	}
	at := f.PROFILE_EPOCH_MS()
	switch profile {
	case qrpProfileUnspecified, qrpProfileAll:
		if from == 0 || to == 0 {
			return dataQuery{}, errors.New("a window query takes FROM_EPOCH_MS and TO_EPOCH_MS")
		}
		fromT, toT := time.UnixMilli(from).UTC(), time.UnixMilli(to).UTC()
		e.Profile, e.From, e.To = storage.EpochProfileWindow, &fromT, &toT
	case qrpProfileNearest, qrpProfileAsOf, qrpProfileForward:
		if at == 0 {
			return dataQuery{}, errors.New("TIME_PROFILE " + QRP.EnumNamesqrpTimeProfile[f.TIME_PROFILE()] + " takes PROFILE_EPOCH_MS")
		}
		e.Profile = map[int8]string{qrpProfileNearest: storage.EpochProfileNearest, qrpProfileAsOf: storage.EpochProfileAsOf, qrpProfileForward: storage.EpochProfileForward}[profile]
		e.At = time.UnixMilli(at).UTC()
	case qrpProfileLatest:
		if at == 0 {
			at = time.Now().UnixMilli()
		}
		e.Profile, e.At = storage.EpochProfileAsOf, time.UnixMilli(at).UTC()
	default:
		return dataQuery{}, fmt.Errorf("unsupported TIME_PROFILE %d", profile)
	}
	var filter QRP.QRPFilter
	for i := 0; i < f.FILTERSLength(); i++ {
		f.FILTERS(&filter, i)
		field, op, value := strings.ToUpper(strings.TrimSpace(string(filter.FIELD()))), int8(filter.OP()), strings.TrimSpace(string(filter.VALUE()))
		switch {
		case field == "NORAD_CAT_ID" && op == qrpFilterEq:
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return dataQuery{}, errors.New("invalid NORAD_CAT_ID filter")
			}
			id := uint32(n)
			e.NoradCatID = &id
		case field == "ENTITY_ID" && op == qrpFilterEq:
			e.EntityID = value
		case field == "MAX_DELTA_SECONDS" && (op == qrpFilterLte || op == qrpFilterEq):
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return dataQuery{}, errors.New("invalid MAX_DELTA_SECONDS filter")
			}
			e.MaxDeltaSeconds = n
		default:
			return dataQuery{}, fmt.Errorf("unsupported filter %s %s", field, QRP.EnumNamesqrpFilterOp[filter.OP()])
		}
	}
	q.epoch = &e
	return q, nil
}

// writeQueryRuns streams one page as lane runs; an empty page is one header
// with RECORD_COUNT 0.
func writeQueryRuns(w http.ResponseWriter, store *storage.FlatSQLStore, q dataQuery, page queryPage) {
	records := page.records
	h := w.Header()
	h.Set("Content-Type", StreamContentType)
	h.Set(StreamFormatHeader, StreamFormat)
	h.Set(StreamSchemaHeader, q.raw.SchemaName)
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	fileIdentifier := ""
	if len(records) > 0 {
		fileIdentifier = recordFileIdentifier(records[0].Data)
	}
	search := ""
	if q.epoch == nil {
		search = q.raw.Search
	}
	generated := time.Now().UnixMilli()
	header := func(lane scanLane, count int) []byte {
		return buildRunHeader(runHeader{
			lane:           lane,
			schemaName:     q.raw.SchemaName,
			fileIdentifier: fileIdentifier,
			search:         search,
			page:           uint32(q.page),
			limit:          uint32(q.limit),
			recordCount:    uint32(count),
			totalCount:     uint64(max(page.total, 0)),
			cursor:         page.cursor,
			nextCursor:     page.nextCursor,
			generatedAtMs:  generated,
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

// runHeader is one query run's $QRP{KIND=Page} header.
type runHeader struct {
	lane                               scanLane
	schemaName, fileIdentifier, search string
	cursor, nextCursor                 string
	page, limit, recordCount           uint32
	totalCount                         uint64
	generatedAtMs                      int64
}

func buildRunHeader(f runHeader) []byte {
	b := flatbuffers.NewBuilder(256)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	schemaName, fileIdentifier, search := str(f.schemaName), str(f.fileIdentifier), str(f.search)
	providerID, sourceName, batchID := str(f.lane.providerID), str(f.lane.sourceName), str(f.lane.batchID)
	producerPeerID, originID := str(f.lane.producerPeerID), str(f.lane.originID)
	cursor, nextCursor := str(f.cursor), str(f.nextCursor)
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, enumOf(QRP.EnumValuesqrpKind, QRPKindPage))
	QRP.QRPAddSTATUS(b, enumOf(QRP.EnumValuesqrpStatus, QRPStatusOk))
	for _, field := range []struct {
		off flatbuffers.UOffsetT
		add func(*flatbuffers.Builder, flatbuffers.UOffsetT)
	}{
		{schemaName, QRP.QRPAddSCHEMA_NAME}, {fileIdentifier, QRP.QRPAddFILE_IDENTIFIER}, {search, QRP.QRPAddSEARCH},
		{providerID, QRP.QRPAddPROVIDER_ID}, {sourceName, QRP.QRPAddSOURCE_NAME}, {batchID, QRP.QRPAddBATCH_ID},
		{producerPeerID, QRP.QRPAddPRODUCER_PEER_ID}, {originID, QRP.QRPAddORIGIN_ID},
		{cursor, QRP.QRPAddCURSOR}, {nextCursor, QRP.QRPAddNEXT_CURSOR},
	} {
		if field.off != 0 {
			field.add(b, field.off)
		}
	}
	QRP.QRPAddPAGE(b, f.page)
	QRP.QRPAddLIMIT(b, f.limit)
	QRP.QRPAddRECORD_COUNT(b, f.recordCount)
	QRP.QRPAddTOTAL_COUNT(b, f.totalCount)
	QRP.QRPAddGENERATED_AT_MS(b, f.generatedAtMs)
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}

// recordFileIdentifier is a stored record's "$XXX" identifier, whether the
// producer finished it bare (bytes 4..8) or size-prefixed (bytes 8..12).
func recordFileIdentifier(data []byte) string {
	for _, at := range []int{framePrefixLength, frameIdentifierOffset} {
		if len(data) >= at+frameIdentifierLength && data[at] == '$' {
			return string(data[at : at+frameIdentifierLength])
		}
	}
	return ""
}
