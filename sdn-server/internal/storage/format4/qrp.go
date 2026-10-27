package format4

// Binary transport only: the query and storage semantics execute in FlatSQL.
import (
	"encoding/binary"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

func encodeSQLRequest(req SQLRequest) ([]byte, error) {
	b := flatbuffers.NewBuilder(512)
	sql := b.CreateString(req.SQL)
	values := make([]flatbuffers.UOffsetT, len(req.Params))
	for i, c := range req.Params {
		var data flatbuffers.UOffsetT
		if c.Type == format2.CellText {
			data = b.CreateString(string(c.B))
		}
		if c.Type == format2.CellBlob {
			data = b.CreateByteVector(c.B)
		}
		QRP.QRPValueStart(b)
		switch c.Type {
		case format2.CellNull:
		case format2.CellInt:
			QRP.QRPValueAddKIND(b, QRP.EnumValuesqrpValueKind["Int"])
			QRP.QRPValueAddINT_VALUE(b, c.I)
		case format2.CellReal:
			QRP.QRPValueAddKIND(b, QRP.EnumValuesqrpValueKind["Float"])
			QRP.QRPValueAddFLOAT_VALUE(b, c.F)
		case format2.CellText:
			QRP.QRPValueAddKIND(b, QRP.EnumValuesqrpValueKind["Text"])
			QRP.QRPValueAddTEXT_VALUE(b, data)
		case format2.CellBlob:
			QRP.QRPValueAddKIND(b, QRP.EnumValuesqrpValueKind["Bytes"])
			QRP.QRPValueAddBYTES_VALUE(b, data)
		default:
			return nil, fmt.Errorf("unsupported SQL parameter kind %d", c.Type)
		}
		values[i] = QRP.QRPValueEnd(b)
	}
	QRP.QRPStartPARAMETERSVector(b, len(values))
	for i := len(values) - 1; i >= 0; i-- {
		b.PrependUOffsetT(values[i])
	}
	params := b.EndVector(len(values))
	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, QRP.EnumValuesqrpKind["Request"])
	QRP.QRPAddREPRESENTATION(b, QRP.EnumValuesqrpRepresentation["FlatBuffer"])
	QRP.QRPAddSQL(b, sql)
	QRP.QRPAddPARAMETERS(b, params)
	QRP.FinishSizePrefixedQRPBuffer(b, QRP.QRPEnd(b))
	return b.FinishedBytes(), nil
}

// QRPCompletion is the engine's terminal page. I/O byte accounting is in the
// mailbox completion record rather than being assigned to an unrelated QRP field.
type QRPCompletion struct {
	Status             int32
	Rows, RowsExamined uint64
}

// QRPDecoder accepts arbitrary chunks of size-prefixed SDS QRP pages. OnRow
// consumes rows incrementally; without it the caller collects the result.
type QRPDecoder struct {
	Names   []string
	Rows    [][]format2.Cell
	End     *QRPCompletion
	OnRow   func([]format2.Cell) error
	pending []byte
}

func (d *QRPDecoder) Done() bool { return d.End != nil && len(d.pending) == 0 }
func (d *QRPDecoder) Finish() error {
	if !d.Done() {
		return fmt.Errorf("incomplete QRP response")
	}
	if d.End.Status != 0 {
		return &StatusError{Op: "SQL", Status: d.End.Status}
	}
	return nil
}
func (d *QRPDecoder) Feed(data []byte) error {
	if d.End != nil && len(data) > 0 {
		return fmt.Errorf("QRP bytes after terminal page")
	}
	d.pending = append(d.pending, data...)
	for len(d.pending) >= 4 {
		n := int(binary.LittleEndian.Uint32(d.pending))
		if n < 8 || n > 256<<20 {
			return fmt.Errorf("invalid QRP page length %d", n)
		}
		if n > len(d.pending)-4 {
			break
		}
		if err := d.page(d.pending[:n+4]); err != nil {
			return err
		}
		d.pending = d.pending[n+4:]
		if d.End != nil && len(d.pending) > 0 {
			return fmt.Errorf("QRP bytes after terminal page")
		}
	}
	if len(d.pending) == 0 {
		d.pending = nil
	}
	return nil
}
func (d *QRPDecoder) page(frame []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("malformed QRP page")
		}
	}()
	if !QRP.SizePrefixedQRPBufferHasIdentifier(frame) {
		return fmt.Errorf("expected SDS QRP page")
	}
	q := QRP.GetSizePrefixedRootAsQRP(frame, 0)
	if q.KIND() != QRP.EnumValuesqrpKind["Page"] && q.KIND() != QRP.EnumValuesqrpKind["Error"] {
		return fmt.Errorf("unexpected QRP kind")
	}
	if q.REPRESENTATION() != QRP.EnumValuesqrpRepresentation["FlatBuffer"] {
		return fmt.Errorf("unexpected QRP representation")
	}
	if q.COLUMNSLength() > len(frame)/4 || q.ROWSLength() > len(frame)/4 {
		return fmt.Errorf("QRP vector exceeds page bounds")
	}
	if q.COLUMNSLength() > 0 {
		names := make([]string, q.COLUMNSLength())
		var col QRP.QRPColumn
		for i := range names {
			if !q.COLUMNS(&col, i) {
				return fmt.Errorf("missing QRP column")
			}
			names[i] = string(col.NAME())
		}
		if d.Names != nil && !sameStrings(d.Names, names) {
			return fmt.Errorf("QRP columns changed")
		}
		d.Names = names
	}
	var row QRP.QRPRow
	var value QRP.QRPValue
	for i := 0; i < q.ROWSLength(); i++ {
		if !q.ROWS(&row, i) || row.CELLSLength() != len(d.Names) {
			return fmt.Errorf("QRP row width mismatch")
		}
		cells := make([]format2.Cell, len(d.Names))
		for j := range cells {
			if !row.CELLS(&value, j) {
				return fmt.Errorf("missing QRP value")
			}
			switch value.KIND() {
			case QRP.EnumValuesqrpValueKind["Null"]:
			case QRP.EnumValuesqrpValueKind["Bool"]:
				if value.BOOL_VALUE() {
					cells[j] = format2.Int(1)
				} else {
					cells[j] = format2.Int(0)
				}
			case QRP.EnumValuesqrpValueKind["Int"]:
				cells[j] = format2.Int(value.INT_VALUE())
			case QRP.EnumValuesqrpValueKind["Float"]:
				cells[j] = format2.Real(value.FLOAT_VALUE())
			case QRP.EnumValuesqrpValueKind["Text"]:
				cells[j] = format2.Text(string(value.TEXT_VALUE()))
			case QRP.EnumValuesqrpValueKind["Bytes"]:
				cells[j] = format2.Blob(append([]byte(nil), value.BYTES_VALUEBytes()...))
			default:
				return fmt.Errorf("unknown QRP value kind")
			}
		}
		if d.OnRow != nil {
			if err := d.OnRow(cells); err != nil {
				return err
			}
		} else {
			d.Rows = append(d.Rows, cells)
		}
	}
	if !q.PARTIAL() || q.KIND() == QRP.EnumValuesqrpKind["Error"] {
		codes := []string{"", "ARG", "NOMEM", "IO", "CORRUPT", "FORMAT", "BUSY", "CANCELLED", "BUDGET", "UNSUPPORTED", "NOTYPE", "STOPPED", "NOSPACE", "SQL", "INTERNAL"}
		status := int32(-14)
		for i, code := range codes {
			if code == string(q.ERROR_CODE()) {
				status = -int32(i)
				break
			}
		}
		if q.KIND() == QRP.EnumValuesqrpKind["Error"] && status == 0 {
			return fmt.Errorf("QRP error without error code")
		}
		d.End = &QRPCompletion{Status: status, Rows: q.TOTAL_COUNT(), RowsExamined: q.SCANNED()}
	}
	return nil
}
