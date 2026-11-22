package caps

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// indexWindowPage is storage.index_page's window form: a QRP Request with
// FROM_EPOCH_MS and TO_EPOCH_MS reads the records of one schema and lane whose
// epoch lies in [FROM, TO) through the epoch index, oldest first, or newest
// first with SORT_DESC. SORT_FIELD, when set, is EPOCH. FILTERS narrow it to
// one object: NORAD_CAT_ID or ENTITY_ID, equal. The answer is one page: the
// first LIMIT records of that order, TOTAL_COUNT the records the window
// matches, FROM_EPOCH_MS and TO_EPOCH_MS the window, no cursor. A window that
// matches more than LIMIT records, or more than MAX_BYTES, is TRUNCATED.
//
// The catalog builder reads each object's element-set history this way
// (stack #1020).
func (s *storageCapAdapter) indexWindowPage(req *QRP.QRP, schema string) []byte {
	// These fields belong to other query operations; ignoring them would broaden a read.
	if req.SQL() != nil || req.SEARCH() != nil || req.PARAMETERSLength() != 0 || req.PAGE() > 1 ||
		req.CID() != nil || req.PRODUCER_PEER_ID() != nil || req.RESUME_CURSOR() != nil || req.CURSOR() != nil {
		return errCapJSON("unsupported window page field; use schema, provider, source, batch, FILTERS, FROM/TO_EPOCH_MS, SORT_FIELD EPOCH, SORT_DESC and LIMIT")
	}
	if profile := req.TIME_PROFILE(); profile != 0 && profile != QRP.EnumValuesqrpTimeProfile["All"] {
		return errCapJSON("a window page takes no TIME_PROFILE but All")
	}
	if field := strings.ToUpper(strings.TrimSpace(string(req.SORT_FIELD()))); field != "" && field != "EPOCH" {
		return errCapJSON("a window page sorts by EPOCH only")
	}
	from, to := req.FROM_EPOCH_MS(), req.TO_EPOCH_MS()
	if from == 0 || to == 0 || from >= to {
		return errCapJSON("a window page takes FROM_EPOCH_MS before TO_EPOCH_MS")
	}
	if _, ok := storage.EpochProfileForSchema(schema); !ok {
		return errCapJSON("no epoch index for " + schema)
	}
	limit := int(req.LIMIT())
	if limit == 0 {
		limit = datasync.DefaultLimit
	}
	if limit > datasync.MaxQueryLimit {
		return errCapJSON("page limit exceeds 1000")
	}
	maxBytes := req.MAX_BYTES()
	if maxBytes == 0 {
		maxBytes = indexPageMaxBytes
	}
	if maxBytes > indexPageMaxBytes {
		return errCapJSON("page byte limit exceeds 8388608")
	}
	fromT, toT := time.UnixMilli(from).UTC(), time.UnixMilli(to).UTC()
	q := storage.EpochRecordQuery{
		SchemaName: schema, Profile: storage.EpochProfileWindow, From: &fromT, To: &toT,
		ProviderID: string(req.PROVIDER_ID()), SourceName: string(req.SOURCE_NAME()), BatchID: string(req.BATCH_ID()),
	}
	for _, value := range []string{q.ProviderID, q.SourceName, q.BatchID} {
		if strings.ContainsRune(value, 0) {
			return errCapJSON("filter contains NUL")
		}
	}
	var filter QRP.QRPFilter
	for i := 0; i < req.FILTERSLength(); i++ {
		req.FILTERS(&filter, i)
		field, value := strings.ToUpper(strings.TrimSpace(string(filter.FIELD()))), strings.TrimSpace(string(filter.VALUE()))
		if filter.OP() != QRP.EnumValuesqrpFilterOp["Eq"] {
			return errCapJSON("a window page filter takes OP Eq")
		}
		switch field {
		case "NORAD_CAT_ID":
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return errCapJSON("invalid NORAD_CAT_ID filter")
			}
			id := uint32(n)
			q.NoradCatID = &id
		case "ENTITY_ID":
			if strings.ContainsRune(value, 0) {
				return errCapJSON("filter contains NUL")
			}
			q.EntityID = value
		default:
			return errCapJSON("a window page filters by NORAD_CAT_ID or ENTITY_ID")
		}
	}
	if s.store == nil {
		return errCapJSON("storage unavailable")
	}
	if err := s.bridge.InvocationContext().Err(); err != nil {
		return errCapJSON(err.Error())
	}
	records, total, err := s.store.QueryEpochWindowSorted(q, req.SORT_DESC(), 0, limit)
	if err != nil {
		if errors.Is(err, storage.ErrEngineRebuilding) {
			return errCapJSON("the record engine is rebuilding; retry shortly")
		}
		return errCapJSON("window page failed: " + err.Error())
	}
	header := func(count int, truncated bool) []byte {
		return syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindPage, Status: syncwire.QRPStatusOk,
			SchemaName: schema, ProviderID: q.ProviderID, SourceName: q.SourceName, BatchID: q.BatchID,
			FromEpochMs: from, ToEpochMs: to, Limit: uint32(limit), MaxBytes: maxBytes,
			TotalCount: uint64(total), RecordCount: uint32(count), Truncated: truncated, HTTPStatus: 200})
	}
	var body []byte
	count := 0
	for _, rec := range records {
		if len(rec.Data) == 0 {
			return errCapJSON(fmt.Sprintf("window record %s unavailable", rec.CID))
		}
		framed := indexRecordFrame(rec.Data)
		if uint64(len(header(count+1, true))+len(body)+len(framed)) > maxBytes {
			if count == 0 {
				return errCapJSON("one record and page metadata exceed MAX_BYTES")
			}
			break
		}
		body = append(body, framed...)
		count++
	}
	stream := append(header(count, int64(count) < total), body...)
	return s.streamResult(&flatsqlrt.RawStream{Bytes: stream, Rows: count, FrameCount: count + 1}, "")
}
