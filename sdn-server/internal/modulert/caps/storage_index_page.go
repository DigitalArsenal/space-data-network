package caps

import (
	"encoding/binary"
	"fmt"
	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const indexPageMaxBytes = 8 << 20
const indexPageMaxRequest = 16 << 10

// indexPage resolves a bounded durable sequence page, independent of SQL's
// relation window. It uses storage.flatsql_query_stream's storage_query grant
// and excludes the same field-sealed schemas. It exposes older retained records
// beyond the SQL window, without adding decryption or schema/source grants.
// A missing record fails the entire page without advancing its cursor: retry
// the same request and cursor to read the surviving records of that snapshot.
type indexPageReader interface {
	RecordIndexSeqPage(storage.RecordIndexSeqQuery) (storage.RecordIndexSeqPage, error)
	Get(string, string) ([]byte, error)
}

func (s *storageCapAdapter) indexPage(payload []byte) []byte {
	return s.indexPageWithReader(payload, s.store)
}

func (s *storageCapAdapter) indexPageWithReader(payload []byte, reader indexPageReader) (answer []byte) {
	defer func() {
		if value := recover(); value != nil {
			log.Errorf("storage.index_page panic: %v\n%s", value, debug.Stack())
			answer = errCapJSON("invalid QRP page request")
		}
	}()
	if s.bridge == nil || !s.bridge.HasCapability("storage_query") {
		return refuseCapJSON("storage.index_page", "requires the storage_query capability grant")
	}
	if len(payload) > indexPageMaxRequest {
		return errCapJSON("page request exceeds 16384 bytes")
	}
	req, err := syncwire.ParseQRP(payload)
	if err != nil || int8(req.KIND()) != syncwire.QRPKindRequest {
		return errCapJSON("expected a QRP Request")
	}
	schema := sds.NormalizeSchemaFileName(string(req.SCHEMA_NAME()))
	if schema == "" {
		return errCapJSON("missing or invalid schema")
	}
	table, err := sds.SchemaNameToTable(schema)
	if err != nil {
		return errCapJSON("missing or invalid schema")
	}
	if encfield.HasEncryptedFields(table) {
		return refuseCapJSON("storage.index_page", "field-sealed schemas are excluded from storage queries")
	}
	// These fields belong to other query operations; ignoring them would broaden a read.
	if req.SQL() != nil || req.SEARCH() != nil || req.FILTERSLength() != 0 || req.PARAMETERSLength() != 0 ||
		req.TIME_PROFILE() != 0 || req.FROM_EPOCH_MS() != 0 || req.TO_EPOCH_MS() != 0 || req.PAGE() > 1 ||
		req.CID() != nil || req.PRODUCER_PEER_ID() != nil || req.RESUME_CURSOR() != nil || req.SORT_FIELD() != nil {
		return errCapJSON("unsupported index page filter; use schema, provider, source, batch and cursor")
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
	q := storage.RecordIndexSeqQuery{RecordIndexPageQuery: storage.RecordIndexPageQuery{
		SchemaName: schema, ProviderID: string(req.PROVIDER_ID()), SourceName: string(req.SOURCE_NAME()),
		BatchID: string(req.BATCH_ID()), Limit: limit + 1}}
	for _, value := range []string{q.ProviderID, q.SourceName, q.BatchID} {
		if strings.ContainsRune(value, 0) {
			return errCapJSON("filter contains NUL")
		}
	}
	hash := datasync.RecordIndexQueryHash(q.RecordIndexPageQuery)
	if raw := string(req.CURSOR()); raw != "" {
		cursor, ok, e := datasync.ParseRawRecordCursor(raw)
		if e != nil || !ok || cursor.MaxRowID <= 0 || cursor.QueryHash != hash {
			return errCapJSON("cursor is not a NEXT_CURSOR of this index query; restart from the first page")
		}
		q.AfterSeq, q.ThroughSeq = cursor.AfterRowID, cursor.MaxRowID
	}
	if s.store == nil {
		return errCapJSON("storage unavailable")
	}
	if err := s.bridge.InvocationContext().Err(); err != nil {
		return errCapJSON(err.Error())
	}
	page, err := reader.RecordIndexSeqPage(q)
	if err != nil {
		return errCapJSON("index page failed: " + err.Error())
	}
	encode := func(after int64) string {
		return datasync.EncodeRawRecordCursor(after, page.Through, "index-"+strconv.FormatInt(page.Through, 10), hash)
	}
	header := func(count int, after int64, more bool) []byte {
		next := ""
		if more {
			next = encode(after)
		}
		return syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindPage, Status: syncwire.QRPStatusOk,
			SchemaName: schema, ProviderID: q.ProviderID, SourceName: q.SourceName, BatchID: q.BatchID,
			Limit: uint32(limit), MaxBytes: maxBytes, Cursor: encode(q.AfterSeq), NextCursor: next,
			TotalCount: uint64(page.Total), RecordCount: uint32(count), HTTPStatus: 200})
	}
	var records []byte
	count, after := 0, q.AfterSeq
	for i, row := range page.Rows {
		if i == limit {
			break
		}
		if err := s.bridge.InvocationContext().Err(); err != nil {
			return errCapJSON(err.Error())
		}
		raw, e := reader.Get(schema, row.CID)
		if e != nil || len(raw) == 0 {
			return errCapJSON(fmt.Sprintf("snapshot record unavailable: %v", e))
		}
		framed := indexRecordFrame(raw)
		h := header(count+1, row.Seq, true)
		if uint64(len(h)+len(records)+len(framed)) > maxBytes {
			if count == 0 {
				return errCapJSON("one record and page metadata exceed MAX_BYTES")
			}
			break
		}
		records = append(records, framed...)
		count++
		after = row.Seq
	}
	h := header(count, after, count < len(page.Rows))
	if uint64(len(h)+len(records)) > maxBytes {
		return errCapJSON("page metadata exceed MAX_BYTES")
	}
	stream := append(h, records...)
	return s.streamResult(&flatsqlrt.RawStream{Bytes: stream, Rows: count, FrameCount: count + 1}, "")
}

func indexRecordFrame(raw []byte) []byte {
	if len(raw) >= 12 && raw[8] == '$' && int(binary.LittleEndian.Uint32(raw))+4 == len(raw) {
		return raw
	}
	out := make([]byte, 4+len(raw))
	binary.LittleEndian.PutUint32(out, uint32(len(raw)))
	copy(out[4:], raw)
	return out
}
