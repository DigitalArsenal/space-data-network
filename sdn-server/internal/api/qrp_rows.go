package api

// Typed $QRP projection pages: one $QRP{KIND=Page} frame whose COLUMNS
// (NAME, TYPE, UNIT) describe the table and whose ROWS carry QRPValue cells
// (Null, Bool, Int, Float, Text, Bytes). Bytes stay bytes; nothing here
// renders a value as JSON or base64.

import (
	"net/http"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
)

// QRPColumn TYPE names.
const (
	colString = "string"
	colInt64  = "int64"
	colDouble = "double"
	colBool   = "bool"
	colBytes  = "bytes"
)

// $QRP QRPValue KIND values the projection pages add to wire.go's.
const (
	qrpValueKindBool  int8 = 1
	qrpValueKindFloat int8 = 3
	qrpValueKindBytes int8 = 5
)

// qrpCol is one COLUMNS descriptor.
type qrpCol struct {
	Name, Type, Unit string
}

// qrpVal is one typed cell; Kind is a qrpValueKind ordinal.
type qrpVal struct {
	Kind  int8
	Bool  bool
	Int   int64
	Float float64
	Text  string
	Bytes []byte
}

func nullVal() qrpVal              { return qrpVal{Kind: qrpValueKindNull} }
func textVal(s string) qrpVal      { return qrpVal{Kind: qrpValueKindText, Text: s} }
func intVal(n int64) qrpVal        { return qrpVal{Kind: qrpValueKindInt, Int: n} }
func floatVal(f float64) qrpVal    { return qrpVal{Kind: qrpValueKindFloat, Float: f} }
func boolVal(v bool) qrpVal        { return qrpVal{Kind: qrpValueKindBool, Bool: v} }
func bytesVal(b []byte) qrpVal     { return qrpVal{Kind: qrpValueKindBytes, Bytes: b} }

func timeMsOrNull(t time.Time) qrpVal {
	if t.IsZero() {
		return nullVal()
	}
	return intVal(t.UnixMilli())
}

// qrpTable is one projection page.
type qrpTable struct {
	Status      int8
	SchemaName  string
	Columns     []qrpCol
	Rows        [][]qrpVal
	TotalCount  uint64
	Page, Limit uint32
	Truncated   bool
	AsOfMs      int64
}

func buildTypedColumns(b *flatbuffers.Builder, cols []qrpCol) flatbuffers.UOffsetT {
	if len(cols) == 0 {
		return 0
	}
	offs := make([]flatbuffers.UOffsetT, len(cols))
	for i, c := range cols {
		name, typ := b.CreateString(c.Name), b.CreateString(c.Type)
		var unit flatbuffers.UOffsetT
		if c.Unit != "" {
			unit = b.CreateString(c.Unit)
		}
		QRP.QRPColumnStart(b)
		QRP.QRPColumnAddNAME(b, name)
		QRP.QRPColumnAddTYPE(b, typ)
		if unit != 0 {
			QRP.QRPColumnAddUNIT(b, unit)
		}
		offs[i] = QRP.QRPColumnEnd(b)
	}
	QRP.QRPStartCOLUMNSVector(b, len(offs))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	return b.EndVector(len(offs))
}

func buildTypedValue(b *flatbuffers.Builder, v qrpVal) flatbuffers.UOffsetT {
	var text, blob flatbuffers.UOffsetT
	switch v.Kind {
	case qrpValueKindText:
		text = b.CreateString(v.Text)
	case qrpValueKindBytes:
		blob = b.CreateByteVector(v.Bytes)
	}
	QRP.QRPValueStart(b)
	QRP.QRPValueAddKIND(b, enumOf(QRP.EnumValuesqrpValueKind, v.Kind))
	switch v.Kind {
	case qrpValueKindBool:
		QRP.QRPValueAddBOOL_VALUE(b, v.Bool)
	case qrpValueKindInt:
		QRP.QRPValueAddINT_VALUE(b, v.Int)
	case qrpValueKindFloat:
		QRP.QRPValueAddFLOAT_VALUE(b, v.Float)
	case qrpValueKindText:
		QRP.QRPValueAddTEXT_VALUE(b, text)
	case qrpValueKindBytes:
		QRP.QRPValueAddBYTES_VALUE(b, blob)
	}
	return QRP.QRPValueEnd(b)
}

func buildTypedRows(b *flatbuffers.Builder, rows [][]qrpVal) flatbuffers.UOffsetT {
	if len(rows) == 0 {
		return 0
	}
	rowOffs := make([]flatbuffers.UOffsetT, len(rows))
	for r, row := range rows {
		cells := make([]flatbuffers.UOffsetT, len(row))
		for c, v := range row {
			cells[c] = buildTypedValue(b, v)
		}
		QRP.QRPRowStartCELLSVector(b, len(cells))
		for i := len(cells) - 1; i >= 0; i-- {
			b.PrependUOffsetT(cells[i])
		}
		vec := b.EndVector(len(cells))
		QRP.QRPRowStart(b)
		QRP.QRPRowAddCELLS(b, vec)
		rowOffs[r] = QRP.QRPRowEnd(b)
	}
	QRP.QRPStartROWSVector(b, len(rowOffs))
	for i := len(rowOffs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(rowOffs[i])
	}
	return b.EndVector(len(rowOffs))
}

// buildTablePage serializes one size-prefixed $QRP{KIND=Page} projection.
func buildTablePage(t qrpTable) []byte {
	b := flatbuffers.NewBuilder(1024)
	var schema flatbuffers.UOffsetT
	if t.SchemaName != "" {
		schema = b.CreateString(t.SchemaName)
	}
	cols := buildTypedColumns(b, t.Columns)
	rows := buildTypedRows(b, t.Rows)
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, enumOf(QRP.EnumValuesqrpKind, QRPKindPage))
	QRP.QRPAddSTATUS(b, enumOf(QRP.EnumValuesqrpStatus, t.Status))
	if schema != 0 {
		QRP.QRPAddSCHEMA_NAME(b, schema)
	}
	QRP.QRPAddPAGE(b, t.Page)
	QRP.QRPAddLIMIT(b, t.Limit)
	QRP.QRPAddRECORD_COUNT(b, uint32(len(t.Rows)))
	QRP.QRPAddTOTAL_COUNT(b, t.TotalCount)
	QRP.QRPAddTRUNCATED(b, t.Truncated)
	QRP.QRPAddAS_OF_MS(b, t.AsOfMs)
	QRP.QRPAddGENERATED_AT_MS(b, time.Now().UnixMilli())
	if cols != 0 {
		QRP.QRPAddCOLUMNS(b, cols)
	}
	if rows != 0 {
		QRP.QRPAddROWS(b, rows)
	}
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes()
}

// writeTablePage answers 200 with exactly one projection page.
func writeTablePage(w http.ResponseWriter, t qrpTable) {
	WriteFrameStream(w, http.StatusOK, [][]byte{buildTablePage(t)}, map[string]string{
		"Cache-Control":    "no-store",
		StreamSchemaHeader: t.SchemaName,
	})
}

// qrpParamValue decodes one PARAMETERS cell into an SQL bind value.
func qrpParamValue(v *QRP.QRPValue) interface{} {
	switch int8(v.KIND()) {
	case qrpValueKindBool:
		return v.BOOL_VALUE()
	case qrpValueKindInt:
		return v.INT_VALUE()
	case qrpValueKindFloat:
		return v.FLOAT_VALUE()
	case qrpValueKindText:
		return string(v.TEXT_VALUE())
	case qrpValueKindBytes:
		return append([]byte(nil), v.BYTES_VALUEBytes()...)
	default:
		return nil
	}
}
