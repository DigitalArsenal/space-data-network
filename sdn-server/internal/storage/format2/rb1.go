package format2

// RB1: the lanes' result stream (flatsql ps/result_block.h; design §9
// "Results"). Little-endian:
//
//	header 'RB1H' u32 | ncols u16 | rsv u16 | ncols x (u16 len, name bytes)
//	block  'RB1B' u32 | nrows u32 | bodyLen u32 | rsv u32 | body
//	       body = nrows x ncols cells; a cell is a u8 type then:
//	         0 NULL, 1 INTEGER (8 bytes, exact int64), 2 REAL (8 bytes
//	         binary64), 3 TEXT (u32 len + UTF-8), 4 BLOB (u32 len + bytes)
//	end    'RB1E' u32 | status i32 | rows u64 | rowsExamined u64 | bytesRead u64
//
// Integers are exact: the legacy engine hands every integer to Go as a
// float64 (flatsqlrt.go, the 2^53 counter sentinel); RB1 does not (T6 #6).
// Parameters use the same cell encoding: u32 count, then the cells.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	rb1MagicHeader = 0x48314252 // "RB1H"
	rb1MagicBlock  = 0x42314252 // "RB1B"
	rb1MagicEnd    = 0x45314252 // "RB1E"
)

// CellType is an RB1 cell's type tag.
type CellType uint8

const (
	CellNull CellType = 0
	CellInt  CellType = 1
	CellReal CellType = 2
	CellText CellType = 3
	CellBlob CellType = 4
)

// Cell is one RB1 value. Text and Blob share B.
type Cell struct {
	Type CellType
	I    int64
	F    float64
	B    []byte
}

// Int returns an integer cell.
func Int(v int64) Cell { return Cell{Type: CellInt, I: v} }

// Real returns a REAL cell.
func Real(v float64) Cell { return Cell{Type: CellReal, F: v} }

// Text returns a TEXT cell.
func Text(s string) Cell { return Cell{Type: CellText, B: []byte(s)} }

// Blob returns a BLOB cell.
func Blob(b []byte) Cell { return Cell{Type: CellBlob, B: b} }

// Null returns a NULL cell.
func Null() Cell { return Cell{} }

// String returns TEXT/BLOB bytes as a string ("" otherwise).
func (c Cell) String() string {
	if c.Type == CellText || c.Type == CellBlob {
		return string(c.B)
	}
	return ""
}

// Int64 returns an INTEGER cell's value (a REAL is truncated; others 0).
func (c Cell) Int64() int64 {
	switch c.Type {
	case CellInt:
		return c.I
	case CellReal:
		return int64(c.F)
	}
	return 0
}

// EncodeParams encodes statement parameters (u32 count, then cells).
func EncodeParams(params []Cell) []byte {
	if len(params) == 0 {
		return nil
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(params)))
	for _, c := range params {
		out = appendCell(out, c)
	}
	return out
}

func appendCell(out []byte, c Cell) []byte {
	out = append(out, byte(c.Type))
	switch c.Type {
	case CellInt:
		out = binary.LittleEndian.AppendUint64(out, uint64(c.I))
	case CellReal:
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(c.F))
	case CellText, CellBlob:
		out = binary.LittleEndian.AppendUint32(out, uint32(len(c.B)))
		out = append(out, c.B...)
	}
	return out
}

// RB1End is the stream's end record.
type RB1End struct {
	Status       int32
	Rows         uint64
	RowsExamined uint64
	BytesRead    uint64
}

// RB1Decoder decodes an RB1 stream fed in any split. OnRow, when set, gets
// each row as it completes (the cells alias the decoder's buffer only until
// the call returns); otherwise rows accumulate in Rows.
type RB1Decoder struct {
	Names []string
	Rows  [][]Cell
	End   *RB1End
	OnRow func([]Cell) error

	buf        []byte
	at         int
	haveHeader bool
}

var errRB1 = errors.New("format2: malformed RB1 stream")

// Feed appends bytes and decodes every complete record in them.
func (d *RB1Decoder) Feed(p []byte) error {
	if d.End != nil {
		if len(p) > 0 {
			return fmt.Errorf("%w: bytes after the end record", errRB1)
		}
		return nil
	}
	d.buf = append(d.buf, p...)
	for {
		ok, err := d.step()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
	}
	if d.at > 0 && d.at >= len(d.buf)/2 {
		d.buf = append(d.buf[:0], d.buf[d.at:]...)
		d.at = 0
	}
	return nil
}

// Done reports whether the end record has been decoded.
func (d *RB1Decoder) Done() bool { return d.End != nil }

func (d *RB1Decoder) step() (bool, error) {
	b := d.buf[d.at:]
	if len(b) < 4 {
		return false, nil
	}
	magic := binary.LittleEndian.Uint32(b)
	switch {
	case !d.haveHeader && magic != rb1MagicEnd:
		// A statement that fails before its first row sends only the end record.
		if magic != rb1MagicHeader {
			return false, fmt.Errorf("%w: header magic %#x", errRB1, magic)
		}
		if len(b) < 8 {
			return false, nil
		}
		n := int(binary.LittleEndian.Uint16(b[4:]))
		at := 8
		names := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if len(b) < at+2 {
				return false, nil
			}
			l := int(binary.LittleEndian.Uint16(b[at:]))
			if len(b) < at+2+l {
				return false, nil
			}
			names = append(names, string(b[at+2:at+2+l]))
			at += 2 + l
		}
		d.Names = names
		d.haveHeader = true
		d.at += at
		return true, nil
	case magic == rb1MagicBlock:
		if len(b) < 16 {
			return false, nil
		}
		nrows := int(binary.LittleEndian.Uint32(b[4:]))
		bodyLen := int(binary.LittleEndian.Uint32(b[8:]))
		if len(b) < 16+bodyLen {
			return false, nil
		}
		body := b[16 : 16+bodyLen]
		ncols := len(d.Names)
		at := 0
		for r := 0; r < nrows; r++ {
			row := make([]Cell, ncols)
			for c := 0; c < ncols; c++ {
				n, err := decodeCell(body[at:], &row[c], d.OnRow == nil)
				if err != nil {
					return false, err
				}
				at += n
			}
			if d.OnRow != nil {
				if err := d.OnRow(row); err != nil {
					return false, err
				}
			} else {
				d.Rows = append(d.Rows, row)
			}
		}
		if at != bodyLen {
			return false, fmt.Errorf("%w: block body %d bytes, cells %d", errRB1, bodyLen, at)
		}
		d.at += 16 + bodyLen
		return true, nil
	case magic == rb1MagicEnd:
		if len(b) < 32 {
			return false, nil
		}
		d.End = &RB1End{
			Status:       int32(binary.LittleEndian.Uint32(b[4:])),
			Rows:         binary.LittleEndian.Uint64(b[8:]),
			RowsExamined: binary.LittleEndian.Uint64(b[16:]),
			BytesRead:    binary.LittleEndian.Uint64(b[24:]),
		}
		d.at += 32
		if d.at != len(d.buf) {
			return false, fmt.Errorf("%w: %d bytes after the end record", errRB1, len(d.buf)-d.at)
		}
		return false, nil
	default:
		return false, fmt.Errorf("%w: record magic %#x", errRB1, magic)
	}
}

// decodeCell decodes one cell from b into c; copy makes TEXT/BLOB own their
// bytes. Returns the cell's length.
func decodeCell(b []byte, c *Cell, copyBytes bool) (int, error) {
	if len(b) < 1 {
		return 0, fmt.Errorf("%w: truncated cell", errRB1)
	}
	c.Type = CellType(b[0])
	switch c.Type {
	case CellNull:
		return 1, nil
	case CellInt, CellReal:
		if len(b) < 9 {
			return 0, fmt.Errorf("%w: truncated number", errRB1)
		}
		v := binary.LittleEndian.Uint64(b[1:])
		if c.Type == CellInt {
			c.I = int64(v)
		} else {
			c.F = math.Float64frombits(v)
		}
		return 9, nil
	case CellText, CellBlob:
		if len(b) < 5 {
			return 0, fmt.Errorf("%w: truncated length", errRB1)
		}
		n := int(binary.LittleEndian.Uint32(b[1:]))
		if len(b) < 5+n {
			return 0, fmt.Errorf("%w: truncated bytes", errRB1)
		}
		if copyBytes {
			c.B = append([]byte(nil), b[5:5+n]...)
		} else {
			c.B = b[5 : 5+n]
		}
		return 5 + n, nil
	default:
		return 0, fmt.Errorf("%w: cell type %d", errRB1, c.Type)
	}
}

// SplitRaw splits a raw-stream result ([u32le size][bytes] per BLOB cell)
// into its frames. Incomplete trailing bytes are returned as rest.
func SplitRaw(b []byte) (frames [][]byte, rest []byte) {
	for len(b) >= 4 {
		n := int(binary.LittleEndian.Uint32(b))
		if len(b) < 4+n {
			break
		}
		frames = append(frames, b[4:4+n])
		b = b[4+n:]
	}
	return frames, b
}
