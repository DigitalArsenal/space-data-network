package fbverify

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Limits mirror the C++ verifier's defaults, tightened to the buffer size.
const (
	// MaxDepth bounds table nesting (C++ default 64).
	MaxDepth = 64
	// MaxTables bounds table visits per buffer (C++ default 1,000,000).
	MaxTables = 1_000_000
	// MaxBufferSize is the largest buffer a uoffset can address (2 GiB - 1).
	MaxBufferSize = 1<<31 - 1

	sizeUOffset = 4
	sizeVOffset = 2
	// slotBudgetPerByte bounds the vtable slots checked per buffer byte. A
	// real buffer spends at least a few bytes per table it holds, so 64 slot
	// checks per byte is far above any real record and still linear.
	slotBudgetPerByte = 64
)

// ErrInvalid is wrapped by every verification failure.
var ErrInvalid = errors.New("flatbuffer failed structural verification")

// VerifyError reports where verification failed.
type VerifyError struct {
	Schema string
	Path   string
	Reason string
}

func (e *VerifyError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: %s: %s", ErrInvalid, e.Schema, e.Reason)
	}
	return fmt.Sprintf("%s: %s at %s: %s", ErrInvalid, e.Schema, e.Path, e.Reason)
}

func (e *VerifyError) Unwrap() error { return ErrInvalid }

type walker struct {
	schema     string
	buf        []byte
	n          int64
	depth      int
	tables     int
	maxTables  int
	slots      int64
	maxSlots   int64
	pathPieces []string
}

func (w *walker) fail(format string, args ...any) error {
	p := ""
	for i, piece := range w.pathPieces {
		if i > 0 {
			p += "."
		}
		p += piece
	}
	return &VerifyError{Schema: w.schema, Path: p, Reason: fmt.Sprintf(format, args...)}
}

func (w *walker) in(pos, size int64) bool {
	return pos >= 0 && size >= 0 && pos <= w.n && size <= w.n-pos
}

func (w *walker) u16(pos int64) int64 { return int64(binary.LittleEndian.Uint16(w.buf[pos:])) }
func (w *walker) u32(pos int64) int64 { return int64(binary.LittleEndian.Uint32(w.buf[pos:])) }
func (w *walker) i32(pos int64) int64 { return int64(int32(binary.LittleEndian.Uint32(w.buf[pos:]))) }

// VerifyBuffer verifies a plain finished buffer (root uoffset at byte 0), the
// form GetRootAs<T>(buf, 0) reads.
func (s *Schema) VerifyBuffer(buf []byte) error {
	return s.verify(buf)
}

// VerifySizePrefixedBuffer verifies a size-prefixed buffer, the form
// GetSizePrefixedRootAs<T>(buf, 0) reads. The u32 prefix must account for
// exactly the rest of the buffer, as in C++ VerifySizePrefixedBuffer.
func (s *Schema) VerifySizePrefixedBuffer(buf []byte) error {
	if len(buf) < sizeUOffset {
		return &VerifyError{Schema: s.name, Reason: "too short for a size prefix"}
	}
	if int64(binary.LittleEndian.Uint32(buf)) != int64(len(buf))-sizeUOffset {
		return &VerifyError{Schema: s.name, Reason: fmt.Sprintf("size prefix %d does not match %d remaining bytes", binary.LittleEndian.Uint32(buf), len(buf)-sizeUOffset)}
	}
	return s.verify(buf[sizeUOffset:])
}

func (s *Schema) verify(buf []byte) error {
	if int64(len(buf)) > MaxBufferSize {
		return &VerifyError{Schema: s.name, Reason: "buffer exceeds 2 GiB"}
	}
	w := &walker{schema: s.name, buf: buf, n: int64(len(buf))}
	w.maxTables = MaxTables
	if lim := len(buf)/sizeUOffset + 1; lim < w.maxTables {
		w.maxTables = lim
	}
	w.maxSlots = int64(len(buf))*slotBudgetPerByte + 1024
	if !w.in(0, sizeUOffset) {
		return w.fail("too short for a root offset")
	}
	root, err := w.offset(0)
	if err != nil {
		return err
	}
	w.pathPieces = append(w.pathPieces, s.root.name)
	return w.table(root, s.root)
}

// offset follows the uoffset stored at pos (C++ VerifyOffset).
func (w *walker) offset(pos int64) (int64, error) {
	if !w.in(pos, sizeUOffset) {
		return 0, w.fail("offset at %d outside buffer", pos)
	}
	o := w.u32(pos)
	if o == 0 {
		return 0, w.fail("zero offset at %d", pos)
	}
	if o >= 1<<31 {
		return 0, w.fail("offset %d at %d has the sign bit set", o, pos)
	}
	target := pos + o
	if !w.in(target, 1) {
		return 0, w.fail("offset at %d points to %d, outside buffer of %d bytes", pos, target, w.n)
	}
	return target, nil
}

func (w *walker) table(pos int64, t *tableDef) error {
	w.depth++
	defer func() { w.depth-- }()
	if w.depth > MaxDepth {
		return w.fail("nesting deeper than %d", MaxDepth)
	}
	w.tables++
	if w.tables > w.maxTables {
		return w.fail("more than %d tables", w.maxTables)
	}
	if !w.in(pos, sizeUOffset) {
		return w.fail("table at %d outside buffer", pos)
	}
	vt := pos - w.i32(pos)
	if !w.in(vt, 2*sizeVOffset) {
		return w.fail("vtable at %d outside buffer", vt)
	}
	vsize := w.u16(vt)
	if vsize < 2*sizeVOffset || vsize%2 != 0 || !w.in(vt, vsize) {
		return w.fail("vtable size %d invalid at %d", vsize, vt)
	}
	tsize := w.u16(vt + sizeVOffset)
	if tsize < sizeUOffset || !w.in(pos, tsize) {
		return w.fail("table size %d at %d outside buffer", tsize, pos)
	}
	fieldPos := func(voffset int64) int64 {
		if voffset+sizeVOffset > vsize {
			return 0
		}
		return w.u16(vt + voffset)
	}
	for i := range t.fields {
		f := &t.fields[i]
		if f.voffset >= vsize {
			break // fields are sorted by slot; the rest are absent
		}
		w.slots++
		if w.slots > w.maxSlots {
			return w.fail("verification work budget exceeded")
		}
		fo := fieldPos(f.voffset)
		var typeCode int64
		if f.typeVOffset != 0 {
			to := fieldPos(f.typeVOffset)
			if to != 0 {
				if f.typ.base == tUnion {
					if !w.in(pos+to, 1) {
						return w.fail("%s_type outside buffer", f.name)
					}
					typeCode = int64(w.buf[pos+to])
				}
			}
			if f.typ.base == tVector {
				if err := w.unionVector(pos, to, fo, f); err != nil {
					return err
				}
				continue
			}
		}
		if fo == 0 {
			continue
		}
		p := pos + fo
		w.pathPieces = append(w.pathPieces, f.name)
		err := w.field(p, f.typ, typeCode)
		w.pathPieces = w.pathPieces[:len(w.pathPieces)-1]
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) field(p int64, ft fieldType, unionType int64) error {
	switch ft.base {
	case tString:
		target, err := w.offset(p)
		if err != nil {
			return err
		}
		return w.str(target)
	case tVector:
		target, err := w.offset(p)
		if err != nil {
			return err
		}
		return w.vector(target, *ft.elem)
	case tTable:
		target, err := w.offset(p)
		if err != nil {
			return err
		}
		return w.table(target, ft.table)
	case tStruct:
		if !w.in(p, ft.strct.size) {
			return w.fail("struct %s at %d outside buffer", ft.strct.name, p)
		}
		return nil
	case tUnion:
		if unionType == 0 {
			// NONE: no accessor follows the value offset.
			return nil
		}
		member, ok := ft.union.members[unionType]
		if !ok {
			return w.fail("union %s has no member %d", ft.union.name, unionType)
		}
		target, err := w.offset(p)
		if err != nil {
			return err
		}
		return w.table(target, member)
	}
	size := scalarSize(ft.base)
	if size == 0 {
		return w.fail("unsupported field type")
	}
	if !w.in(p, size) {
		return w.fail("scalar at %d outside buffer", p)
	}
	return nil
}

func (w *walker) str(target int64) error {
	if !w.in(target, sizeUOffset) {
		return w.fail("string length at %d outside buffer", target)
	}
	l := w.u32(target)
	// length bytes plus the terminating zero
	if !w.in(target+sizeUOffset, l+1) {
		return w.fail("string of %d bytes at %d outside buffer", l, target)
	}
	if w.buf[target+sizeUOffset+l] != 0 {
		return w.fail("string at %d is not zero-terminated", target)
	}
	return nil
}

// vectorBody checks a vector's length word and returns its element count
// and the first element's position.
func (w *walker) vectorBody(target, elemSize int64) (int64, int64, error) {
	if !w.in(target, sizeUOffset) {
		return 0, 0, w.fail("vector length at %d outside buffer", target)
	}
	count := w.u32(target)
	if elemSize > 0 && count > (w.n-target-sizeUOffset)/elemSize {
		return 0, 0, w.fail("vector of %d x %d bytes at %d outside buffer", count, elemSize, target)
	}
	return count, target + sizeUOffset, nil
}

func (w *walker) vector(target int64, elem fieldType) error {
	switch elem.base {
	case tString, tTable:
		count, first, err := w.vectorBody(target, sizeUOffset)
		if err != nil {
			return err
		}
		for i := int64(0); i < count; i++ {
			at, err := w.offset(first + i*sizeUOffset)
			if err != nil {
				return err
			}
			if elem.base == tString {
				err = w.str(at)
			} else {
				err = w.table(at, elem.table)
			}
			if err != nil {
				return err
			}
		}
		return nil
	case tStruct:
		_, _, err := w.vectorBody(target, elem.strct.size)
		return err
	case tUnion:
		return w.fail("a union vector needs its _type vector")
	}
	size := scalarSize(elem.base)
	if size == 0 {
		return w.fail("unsupported vector element type")
	}
	_, _, err := w.vectorBody(target, size)
	return err
}

func (w *walker) unionVector(pos, typeOffset, valueOffset int64, f *tableField) error {
	if valueOffset == 0 && typeOffset == 0 {
		return nil
	}
	w.pathPieces = append(w.pathPieces, f.name)
	defer func() { w.pathPieces = w.pathPieces[:len(w.pathPieces)-1] }()
	if valueOffset == 0 || typeOffset == 0 {
		return w.fail("union vector needs both its _type and value vectors")
	}
	typesAt, err := w.offset(pos + typeOffset)
	if err != nil {
		return err
	}
	typeCount, typeFirst, err := w.vectorBody(typesAt, 1)
	if err != nil {
		return err
	}
	valuesAt, err := w.offset(pos + valueOffset)
	if err != nil {
		return err
	}
	count, first, err := w.vectorBody(valuesAt, sizeUOffset)
	if err != nil {
		return err
	}
	if count != typeCount {
		return w.fail("union vector has %d types and %d values", typeCount, count)
	}
	for i := int64(0); i < count; i++ {
		code := int64(w.buf[typeFirst+i])
		if code == 0 {
			continue
		}
		member, ok := f.typ.elem.union.members[code]
		if !ok {
			return w.fail("union %s has no member %d", f.typ.elem.union.name, code)
		}
		at, err := w.offset(first + i*sizeUOffset)
		if err != nil {
			return err
		}
		if err := w.table(at, member); err != nil {
			return err
		}
	}
	return nil
}
