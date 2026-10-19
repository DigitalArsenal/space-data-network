// Package syncwire is the one FlatBuffer wire kit of the node's paged reads:
// the $QRP request / page / error frame (SDS 1.257.0), size-prefixed frame
// I/O over any byte stream, the SEGMENT_COLUMNS projection row that carries a
// dataset publication, and the flatsql-sync 2.0.0 protocol IDs. HTTP routes
// (internal/api) and libp2p streams (internal/protocol, internal/node) share
// it, so a page means the same bytes on every transport. Nothing here
// projects a record to JSON.
//
// Only the PUBLISHED lib/go module is used (published-deps law); the generated
// enum types are unexported, so values travel here as the untyped constants
// the schema declares and cross into the generated types through enumOf.
package syncwire

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	// FramePrefixLength is the u32le length prefix of every frame.
	FramePrefixLength = 4
	// FrameIdentifierOffset is where the file identifier sits inside a
	// prefixed frame: prefix (4) + root uoffset (4).
	FrameIdentifierOffset = 8
	// FrameIdentifierLength is the FlatBuffers file identifier width.
	FrameIdentifierLength = 4
)

// $QRP KIND values (qrpKind).
const (
	QRPKindRequest int8 = 0
	QRPKindPage    int8 = 1
	QRPKindError   int8 = 2
)

// $QRP STATUS values (qrpStatus).
const (
	QRPStatusOk      int8 = 0
	QRPStatusPartial int8 = 1
	QRPStatusBusy    int8 = 2
	QRPStatusCold    int8 = 3
	QRPStatusError   int8 = 4
)

// $QRP filter OP values (qrpFilterOp).
const (
	QRPFilterEq int8 = 0
	QRPFilterIn int8 = 7
)

// $QRP QRPValue KIND values (qrpValueKind).
const (
	qrpValueKindNull  int8 = 0
	qrpValueKindInt   int8 = 2
	qrpValueKindText  int8 = 4
	qrpValueKindBytes int8 = 5
)

// enumOf converts a schema ordinal into the generated (unexported) enum type
// named by one of the published EnumValues* maps.
func enumOf[T ~int8](_ map[string]T, n int8) T { return T(n) }

// QRPFields is every $QRP field the Go lanes write. Zero values are omitted
// from the buffer (FlatBuffers defaults), so a Request, a Page header and an
// Error frame share one builder.
type QRPFields struct {
	Kind           int8
	Status         int8
	ErrorCode      string
	Message        string
	RetryAfterMs   uint32
	SchemaName     string
	FileIdentifier string
	Search         string
	Filters        []QRPFilterField
	ProviderID     string
	SourceName     string
	BatchID        string
	ProducerPeerID string
	OriginID       string
	FromEpochMs    int64
	ToEpochMs      int64
	Page           uint32
	Limit          uint32
	Cursor         string
	NextCursor     string
	ResumeCursor   string
	RecordCount    uint32
	TotalCount     uint64
	Scanned        uint64
	Stored         uint64
	Partial        bool
	Truncated      bool
	AsOfMs         int64
	Stale          bool
	ElapsedMs      uint32
	GeneratedAtMs  int64
	Etag           string
	CID            string
	ArchiveID      string
	MaxBytes       uint64
	// HTTPStatus is the transport status an Error frame answers with.
	HTTPStatus uint16
	// Columns and Rows carry a projection page (typed cells in column
	// order) instead of record frames.
	Columns []QRPColumnField
	Rows    [][]QRPCell
	// Tables answers a request for the query surface (one entry per topic,
	// datastore or table).
	Tables []QRPTableField
}

// QRPColumnField is one $QRP COLUMNS descriptor.
type QRPColumnField struct {
	Name string
	Type string
}

// QRPFilterField is one $QRP FILTERS entry; Op is a qrpFilterOp ordinal.
type QRPFilterField struct {
	Field string
	Op    int8
	Value string
}

// QRPTableField is one $QRP TABLES entry.
type QRPTableField struct {
	Name    string
	Kind    string
	Source  string
	Records int64
}

// QRPCell is one QRPValue of a projection row: Text when IsText, Int when
// IsInt, Bytes when IsBytes, Null otherwise.
type QRPCell struct {
	IsText  bool
	Text    string
	IsInt   bool
	Int     int64
	IsBytes bool
	Bytes   []byte
}

// TextCell is a Text cell, or Null for "".
func TextCell(s string) QRPCell {
	if s == "" {
		return QRPCell{}
	}
	return QRPCell{IsText: true, Text: s}
}

// IntCell is an Int cell.
func IntCell(n int64) QRPCell { return QRPCell{IsInt: true, Int: n} }

// BytesCell is a Bytes cell, or Null for an empty slice.
func BytesCell(b []byte) QRPCell {
	if len(b) == 0 {
		return QRPCell{}
	}
	return QRPCell{IsBytes: true, Bytes: b}
}

func buildQRPColumns(b *flatbuffers.Builder, columns []QRPColumnField) flatbuffers.UOffsetT {
	if len(columns) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, len(columns))
	for i, column := range columns {
		name, typ := b.CreateString(column.Name), b.CreateString(column.Type)
		QRP.QRPColumnStart(b)
		QRP.QRPColumnAddNAME(b, name)
		QRP.QRPColumnAddTYPE(b, typ)
		offsets[i] = QRP.QRPColumnEnd(b)
	}
	QRP.QRPStartCOLUMNSVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets))
}

func buildQRPFilters(b *flatbuffers.Builder, filters []QRPFilterField) flatbuffers.UOffsetT {
	if len(filters) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, len(filters))
	for i, filter := range filters {
		field, value := b.CreateString(filter.Field), b.CreateString(filter.Value)
		QRP.QRPFilterStart(b)
		QRP.QRPFilterAddFIELD(b, field)
		QRP.QRPFilterAddOP(b, enumOf(QRP.EnumValuesqrpFilterOp, filter.Op))
		QRP.QRPFilterAddVALUE(b, value)
		offsets[i] = QRP.QRPFilterEnd(b)
	}
	QRP.QRPStartFILTERSVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets))
}

func buildQRPTables(b *flatbuffers.Builder, tables []QRPTableField) flatbuffers.UOffsetT {
	if len(tables) == 0 {
		return 0
	}
	offsets := make([]flatbuffers.UOffsetT, len(tables))
	for i, table := range tables {
		name, kind, source := b.CreateString(table.Name), b.CreateString(table.Kind), b.CreateString(table.Source)
		QRP.QRPTableStart(b)
		QRP.QRPTableAddNAME(b, name)
		QRP.QRPTableAddKIND(b, kind)
		QRP.QRPTableAddSOURCE(b, source)
		QRP.QRPTableAddRECORDS(b, table.Records)
		offsets[i] = QRP.QRPTableEnd(b)
	}
	QRP.QRPStartTABLESVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets))
}

func buildQRPRows(b *flatbuffers.Builder, rows [][]QRPCell) flatbuffers.UOffsetT {
	if len(rows) == 0 {
		return 0
	}
	rowOffsets := make([]flatbuffers.UOffsetT, len(rows))
	for r, row := range rows {
		cells := make([]flatbuffers.UOffsetT, len(row))
		for c, cell := range row {
			var text, bytes flatbuffers.UOffsetT
			switch {
			case cell.IsText:
				text = b.CreateString(cell.Text)
			case cell.IsBytes:
				bytes = b.CreateByteVector(cell.Bytes)
			}
			QRP.QRPValueStart(b)
			switch {
			case cell.IsText:
				QRP.QRPValueAddKIND(b, enumOf(QRP.EnumValuesqrpValueKind, qrpValueKindText))
				QRP.QRPValueAddTEXT_VALUE(b, text)
			case cell.IsInt:
				QRP.QRPValueAddKIND(b, enumOf(QRP.EnumValuesqrpValueKind, qrpValueKindInt))
				QRP.QRPValueAddINT_VALUE(b, cell.Int)
			case cell.IsBytes:
				QRP.QRPValueAddKIND(b, enumOf(QRP.EnumValuesqrpValueKind, qrpValueKindBytes))
				QRP.QRPValueAddBYTES_VALUE(b, bytes)
			default:
				QRP.QRPValueAddKIND(b, enumOf(QRP.EnumValuesqrpValueKind, qrpValueKindNull))
			}
			cells[c] = QRP.QRPValueEnd(b)
		}
		QRP.QRPRowStartCELLSVector(b, len(cells))
		for i := len(cells) - 1; i >= 0; i-- {
			b.PrependUOffsetT(cells[i])
		}
		cellVector := b.EndVector(len(cells))
		QRP.QRPRowStart(b)
		QRP.QRPRowAddCELLS(b, cellVector)
		rowOffsets[r] = QRP.QRPRowEnd(b)
	}
	QRP.QRPStartROWSVector(b, len(rowOffsets))
	for i := len(rowOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(rowOffsets[i])
	}
	return b.EndVector(len(rowOffsets))
}

// BuildQRP serializes one size-prefixed $QRP frame. The same fields always
// produce the same bytes (the canonical form a signature covers).
func BuildQRP(f QRPFields) []byte {
	b := flatbuffers.NewBuilder(512)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	errorCode := str(f.ErrorCode)
	message := str(f.Message)
	schemaName := str(f.SchemaName)
	fileIdentifier := str(f.FileIdentifier)
	search := str(f.Search)
	providerID := str(f.ProviderID)
	sourceName := str(f.SourceName)
	batchID := str(f.BatchID)
	producerPeerID := str(f.ProducerPeerID)
	originID := str(f.OriginID)
	cursor := str(f.Cursor)
	nextCursor := str(f.NextCursor)
	resumeCursor := str(f.ResumeCursor)
	etag := str(f.Etag)
	cid := str(f.CID)
	archiveID := str(f.ArchiveID)
	filters := buildQRPFilters(b, f.Filters)
	columns := buildQRPColumns(b, f.Columns)
	rows := buildQRPRows(b, f.Rows)
	tables := buildQRPTables(b, f.Tables)

	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, enumOf(QRP.EnumValuesqrpKind, f.Kind))
	QRP.QRPAddSTATUS(b, enumOf(QRP.EnumValuesqrpStatus, f.Status))
	if errorCode != 0 {
		QRP.QRPAddERROR_CODE(b, errorCode)
	}
	if message != 0 {
		QRP.QRPAddMESSAGE(b, message)
	}
	QRP.QRPAddRETRY_AFTER_MS(b, f.RetryAfterMs)
	if schemaName != 0 {
		QRP.QRPAddSCHEMA_NAME(b, schemaName)
	}
	if fileIdentifier != 0 {
		QRP.QRPAddFILE_IDENTIFIER(b, fileIdentifier)
	}
	if search != 0 {
		QRP.QRPAddSEARCH(b, search)
	}
	if filters != 0 {
		QRP.QRPAddFILTERS(b, filters)
	}
	if providerID != 0 {
		QRP.QRPAddPROVIDER_ID(b, providerID)
	}
	if sourceName != 0 {
		QRP.QRPAddSOURCE_NAME(b, sourceName)
	}
	if batchID != 0 {
		QRP.QRPAddBATCH_ID(b, batchID)
	}
	if producerPeerID != 0 {
		QRP.QRPAddPRODUCER_PEER_ID(b, producerPeerID)
	}
	if originID != 0 {
		QRP.QRPAddORIGIN_ID(b, originID)
	}
	QRP.QRPAddFROM_EPOCH_MS(b, f.FromEpochMs)
	QRP.QRPAddTO_EPOCH_MS(b, f.ToEpochMs)
	QRP.QRPAddPAGE(b, f.Page)
	QRP.QRPAddLIMIT(b, f.Limit)
	if cursor != 0 {
		QRP.QRPAddCURSOR(b, cursor)
	}
	if nextCursor != 0 {
		QRP.QRPAddNEXT_CURSOR(b, nextCursor)
	}
	QRP.QRPAddRECORD_COUNT(b, f.RecordCount)
	QRP.QRPAddTOTAL_COUNT(b, f.TotalCount)
	QRP.QRPAddSCANNED(b, f.Scanned)
	QRP.QRPAddSTORED(b, f.Stored)
	QRP.QRPAddPARTIAL(b, f.Partial)
	QRP.QRPAddTRUNCATED(b, f.Truncated)
	QRP.QRPAddAS_OF_MS(b, f.AsOfMs)
	QRP.QRPAddSTALE(b, f.Stale)
	QRP.QRPAddELAPSED_MS(b, f.ElapsedMs)
	QRP.QRPAddGENERATED_AT_MS(b, f.GeneratedAtMs)
	if etag != 0 {
		QRP.QRPAddETAG(b, etag)
	}
	if cid != 0 {
		QRP.QRPAddCID(b, cid)
	}
	if archiveID != 0 {
		QRP.QRPAddARCHIVE_ID(b, archiveID)
	}
	if columns != 0 {
		QRP.QRPAddCOLUMNS(b, columns)
	}
	if rows != 0 {
		QRP.QRPAddROWS(b, rows)
	}
	if tables != 0 {
		QRP.QRPAddTABLES(b, tables)
	}
	QRP.QRPAddMAX_BYTES(b, f.MaxBytes)
	QRP.QRPAddHTTP_STATUS(b, f.HTTPStatus)
	if resumeCursor != 0 {
		QRP.QRPAddRESUME_CURSOR(b, resumeCursor)
	}
	root := QRP.QRPEnd(b)
	QRP.FinishSizePrefixedQRPBuffer(b, root)
	return b.FinishedBytes()
}

// ParseQRP decodes a $QRP frame. A size-prefixed frame is the wire form; a
// bare finished buffer is tolerated for producers that call Finish directly.
func ParseQRP(frame []byte) (*QRP.QRP, error) {
	switch {
	case len(frame) >= FrameIdentifierOffset+FrameIdentifierLength && QRP.SizePrefixedQRPBufferHasIdentifier(frame):
		if int64(binary.LittleEndian.Uint32(frame[:FramePrefixLength]))+FramePrefixLength > int64(len(frame)) {
			return nil, errors.New("$QRP frame is shorter than its length prefix")
		}
		return QRP.GetSizePrefixedRootAsQRP(frame, 0), nil
	case len(frame) >= FramePrefixLength+FrameIdentifierLength && QRP.QRPBufferHasIdentifier(frame):
		return QRP.GetRootAsQRP(frame, 0), nil
	}
	return nil, fmt.Errorf("frame is not a $QRP buffer (identifier %q)", FrameIdentifier(frame))
}

// FrameIdentifier returns the file identifier of a size-prefixed frame
// (bytes 8..12), or "" when the frame is too short to carry one.
func FrameIdentifier(frame []byte) string {
	if len(frame) < FrameIdentifierOffset+FrameIdentifierLength {
		return ""
	}
	return string(frame[FrameIdentifierOffset : FrameIdentifierOffset+FrameIdentifierLength])
}

// Filter returns the VALUE of the first FILTERS entry naming field with op,
// and whether one exists.
func Filter(q *QRP.QRP, field string, op int8) (string, bool) {
	var filter QRP.QRPFilter
	for i := 0; i < q.FILTERSLength(); i++ {
		if q.FILTERS(&filter, i) && string(filter.FIELD()) == field && int8(filter.OP()) == op {
			return string(filter.VALUE()), true
		}
	}
	return "", false
}

// FilterFields lists the FIELD of every FILTERS entry, so a reader can refuse
// a filter it does not apply instead of ignoring it.
func FilterFields(q *QRP.QRP) []string {
	var filter QRP.QRPFilter
	out := make([]string, 0, q.FILTERSLength())
	for i := 0; i < q.FILTERSLength(); i++ {
		if q.FILTERS(&filter, i) {
			out = append(out, string(filter.FIELD()))
		}
	}
	return out
}

// Rows decodes a projection page: column name -> index, and every row's
// cells in column order.
func Rows(q *QRP.QRP) (map[string]int, [][]QRPCell) {
	columns := make(map[string]int, q.COLUMNSLength())
	var column QRP.QRPColumn
	for i := 0; i < q.COLUMNSLength(); i++ {
		if q.COLUMNS(&column, i) {
			columns[string(column.NAME())] = i
		}
	}
	rows := make([][]QRPCell, 0, q.ROWSLength())
	var row QRP.QRPRow
	var value QRP.QRPValue
	for r := 0; r < q.ROWSLength(); r++ {
		if !q.ROWS(&row, r) {
			continue
		}
		cells := make([]QRPCell, row.CELLSLength())
		for c := range cells {
			if !row.CELLS(&value, c) {
				continue
			}
			switch int8(value.KIND()) {
			case qrpValueKindText:
				cells[c] = QRPCell{IsText: true, Text: string(value.TEXT_VALUE())}
			case qrpValueKindInt:
				cells[c] = QRPCell{IsInt: true, Int: value.INT_VALUE()}
			case qrpValueKindBytes:
				cells[c] = QRPCell{IsBytes: true, Bytes: append([]byte(nil), value.BYTES_VALUEBytes()...)}
			}
		}
		rows = append(rows, cells)
	}
	return columns, rows
}

// Tables decodes the TABLES of a query-surface page.
func Tables(q *QRP.QRP) []QRPTableField {
	out := make([]QRPTableField, 0, q.TABLESLength())
	var table QRP.QRPTable
	for i := 0; i < q.TABLESLength(); i++ {
		if q.TABLES(&table, i) {
			out = append(out, QRPTableField{Name: string(table.NAME()), Kind: string(table.KIND()), Source: string(table.SOURCE()), Records: table.RECORDS()})
		}
	}
	return out
}
