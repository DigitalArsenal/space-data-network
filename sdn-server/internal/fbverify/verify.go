package fbverify

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// THE REFERENCE AND WHERE THIS VERIFIER DIFFERS FROM IT
//
// Every check below mirrors flatbuffers' C++ reflection verifier
// (Verifier::Verify, VerifyAlignment, VerifyTableStart, VerifyVectorOrString,
// VerifyString in include/flatbuffers/verifier.h; VerifyObject, VerifyVector,
// VerifyUnion, VerifyStruct in src/reflection.cpp) and the DeepVerifier flatc
// runs after it (src/flatc_wasm_wasi.cpp). A refusal the reference shares is
// ClassConformant. Where this verifier refuses and the reference may accept,
// the refusal names one of these classes, and nothing else may differ:
//
//   - ClassStrictOffset: a uoffset of zero or with the sign bit set. The
//     reference's reflection path follows field offsets without the checks
//     that generated code's VerifyOffset makes, and in flatc-wasi (wasm32) a
//     sign-bit offset wraps the 32-bit pointer back into the buffer. Go's
//     uint32 accessors wrap the same way, so a verified target would be
//     memory-safe, but no builder ever writes such an offset. Refusing it is
//     what generated C++ and every other runtime's verifier do.
//   - ClassStrictWork: more table visits or vtable-slot checks than the
//     buffer's size can account for (len/4+1 tables, 64 slot checks per
//     byte). Only a DAG of shared offsets reaches it; builders never share
//     tables that way. The reference allows up to 1,000,000 table visits
//     whatever the size, which this verifier also enforces.
//   - ClassStrictUndefined: bytes the reference reads without bounds-checking
//     them: a union value's offset slot, and a union vector's type vector
//     when its values vector is present but its type vector is not (the
//     reference dereferences a null vector). Its verdict there depends on
//     memory outside the buffer, so this verifier refuses.
//   - ClassStrictFrame: a bare record whose alignment origin is 4 bytes
//     before its first byte (VerifyBufferAt with origin 4: a size-prefixed
//     build whose prefix was stripped, see internal/storage/recordalign) is
//     verified by the reference behind a synthetic u32 prefix. An offset
//     into that prefix, or a range that fits only with it, passes there; the
//     Go accessor reads the record bare, where it is out of bounds.

// Limits mirror the C++ verifier's defaults.
const (
	// MaxDepth bounds table nesting (C++ default 64).
	MaxDepth = 64
	// MaxTables bounds table visits per buffer (C++ default 1,000,000).
	MaxTables = 1_000_000
	// MaxBufferSize is FLATBUFFERS_MAX_BUFFER_SIZE (2 GiB - 1), the
	// verifier's max_size: no buffer, vector or string may reach it.
	MaxBufferSize = 1<<31 - 1

	sizeUOffset = 4
	sizeVOffset = 2
	// slotBudgetPerByte bounds the vtable slots checked per buffer byte. A
	// real buffer spends at least a few bytes per table it holds, so 64 slot
	// checks per byte is far above any real record and still linear.
	slotBudgetPerByte = 64
)

// Class says how a refusal relates to the reference verifier.
type Class int

const (
	// ClassConformant: the reference refuses this buffer too.
	ClassConformant Class = iota
	// ClassStrictOffset: a zero or sign-bit uoffset (see above).
	ClassStrictOffset
	// ClassStrictWork: the size-proportional work budget (see above).
	ClassStrictWork
	// ClassStrictUndefined: bytes the reference reads unchecked (see above).
	ClassStrictUndefined
	// ClassStrictFrame: in bounds only behind the reference's synthetic
	// prefix (see above).
	ClassStrictFrame
)

func (c Class) String() string {
	switch c {
	case ClassConformant:
		return "conformant"
	case ClassStrictOffset:
		return "strict-offset"
	case ClassStrictWork:
		return "strict-work"
	case ClassStrictUndefined:
		return "strict-undefined"
	case ClassStrictFrame:
		return "strict-frame"
	}
	return fmt.Sprintf("class(%d)", int(c))
}

// ErrInvalid is wrapped by every verification failure.
var ErrInvalid = errors.New("flatbuffer failed structural verification")

// VerifyError reports where verification failed and how the refusal relates
// to the reference verifier.
type VerifyError struct {
	Schema string
	Path   string
	Reason string
	Class  Class
}

func (e *VerifyError) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%s: %s: %s", ErrInvalid, e.Schema, e.Reason)
	}
	return fmt.Sprintf("%s: %s at %s: %s", ErrInvalid, e.Schema, e.Path, e.Reason)
}

func (e *VerifyError) Unwrap() error { return ErrInvalid }

// ClassOf returns the class of a verification error, and false when err is
// not one.
func ClassOf(err error) (Class, bool) {
	var ve *VerifyError
	if errors.As(err, &ve) {
		return ve.Class, true
	}
	return 0, false
}

type walker struct {
	schema string
	buf    []byte
	n      int64
	// origin is how many bytes before buf[0] the alignment origin sits (0,
	// or 4 for a stripped size-prefixed build).
	origin     int64
	depth      int
	tables     int
	maxTables  int
	slots      int64
	maxSlots   int64
	pathPieces []string
}

func (w *walker) failClass(class Class, format string, args ...any) error {
	p := ""
	for i, piece := range w.pathPieces {
		if i > 0 {
			p += "."
		}
		p += piece
	}
	return &VerifyError{Schema: w.schema, Path: p, Reason: fmt.Sprintf(format, args...), Class: class}
}

func (w *walker) fail(format string, args ...any) error {
	return w.failClass(ClassConformant, format, args...)
}

// in is Verifier::Verify(elem, len): len < size && elem <= size - len, on
// the bytes a Go accessor reads.
func (w *walker) in(pos, size int64) bool {
	return pos >= 0 && size >= 0 && size < w.n && pos <= w.n-size
}

// aligned is Verifier::VerifyAlignment, measured from the alignment origin.
func (w *walker) aligned(pos, align int64) bool {
	return (pos+w.origin)&(align-1) == 0
}

// rangeErr refuses a range outside the buffer. When it would fit behind the
// reference's synthetic prefix the refusal is ClassStrictFrame.
func (w *walker) rangeErr(pos, size int64, format string, args ...any) error {
	if w.in(pos, size) {
		return nil
	}
	p, n := pos+w.origin, w.n+w.origin
	if w.origin > 0 && p >= 0 && size >= 0 && size < n && p <= n-size {
		return w.failClass(ClassStrictFrame, format, args...)
	}
	return w.fail(format, args...)
}

// atErr is Verifier::Verify<T>(elem): aligned to the scalar's size and in
// bounds.
func (w *walker) atErr(pos, size int64, format string, args ...any) error {
	if err := w.rangeErr(pos, size, format, args...); err != nil {
		return err
	}
	if !w.aligned(pos, size) {
		return w.fail(format, args...)
	}
	return nil
}

func (w *walker) u16(pos int64) int64 { return int64(binary.LittleEndian.Uint16(w.buf[pos:])) }
func (w *walker) u32(pos int64) int64 { return int64(binary.LittleEndian.Uint32(w.buf[pos:])) }
func (w *walker) i32(pos int64) int64 { return int64(int32(binary.LittleEndian.Uint32(w.buf[pos:]))) }

// VerifyBuffer verifies a plain finished buffer (root uoffset at byte 0), the
// form GetRootAs<T>(buf, 0) reads, with its alignment origin at buf[0]:
// flatbuffers::Verify.
func (s *Schema) VerifyBuffer(buf []byte) error {
	return s.verifyAt(buf, 0, 0)
}

// VerifyBufferAt verifies a plain finished buffer whose alignment origin sits
// origin bytes before buf[0] (0, or 4 for a size-prefixed build whose prefix
// was stripped; internal/storage/recordalign decides which). Bounds are
// always those of buf itself, the bytes GetRootAs<T>(buf, 0) reads.
func (s *Schema) VerifyBufferAt(buf []byte, origin int64) error {
	if origin != 0 && origin != sizeUOffset {
		return &VerifyError{Schema: s.name, Reason: fmt.Sprintf("alignment origin %d is not 0 or 4", origin)}
	}
	return s.verifyAt(buf, 0, origin)
}

// VerifySizePrefixedBuffer verifies a size-prefixed buffer, the form
// GetSizePrefixedRootAs<T>(buf, 0) reads. The u32 prefix must account for
// exactly the rest of the buffer (as flatc's binary_to_json requires), and
// the walk runs on the whole buffer, so alignment is measured from the
// prefix, as flatbuffers::VerifySizePrefixed measures it.
func (s *Schema) VerifySizePrefixedBuffer(buf []byte) error {
	if len(buf) < 2*sizeUOffset {
		return &VerifyError{Schema: s.name, Reason: "too short for a size prefix and a root offset"}
	}
	if int64(binary.LittleEndian.Uint32(buf)) != int64(len(buf))-sizeUOffset {
		return &VerifyError{Schema: s.name, Reason: fmt.Sprintf("size prefix %d does not match %d remaining bytes", binary.LittleEndian.Uint32(buf), len(buf)-sizeUOffset)}
	}
	return s.verifyAt(buf, sizeUOffset, 0)
}

func (s *Schema) verifyAt(buf []byte, rootSlot, origin int64) error {
	if int64(len(buf)) >= MaxBufferSize {
		return &VerifyError{Schema: s.name, Reason: "buffer reaches the 2 GiB limit"}
	}
	w := &walker{schema: s.name, buf: buf, n: int64(len(buf)), origin: origin}
	w.maxTables = MaxTables
	if lim := len(buf)/sizeUOffset + 1; lim < w.maxTables {
		w.maxTables = lim
	}
	w.maxSlots = int64(len(buf))*slotBudgetPerByte + 1024
	// GetAnyRoot / GetAnySizePrefixedRoot read the root offset unchecked;
	// flatc has already required the header to be present.
	if w.n < rootSlot+sizeUOffset {
		return w.fail("too short for a root offset")
	}
	root, err := w.follow(rootSlot)
	if err != nil {
		return err
	}
	w.pathPieces = append(w.pathPieces, s.root.name)
	return w.table(root, s.root)
}

// follow reads the uoffset at slot (already bounds-checked by the caller)
// and returns its target. The target's own checks bound it.
func (w *walker) follow(slot int64) (int64, error) {
	o := w.u32(slot)
	if o == 0 {
		return 0, w.failClass(ClassStrictOffset, "zero offset at %d", slot)
	}
	if o >= 1<<31 {
		return 0, w.failClass(ClassStrictOffset, "offset %d at %d has the sign bit set", o, slot)
	}
	return slot + o, nil
}

// offsetField checks a uoffset field's slot (Table::VerifyField<uoffset_t>)
// and follows it.
func (w *walker) offsetField(slot int64) (int64, error) {
	if err := w.atErr(slot, sizeUOffset, "offset slot at %d misaligned or outside buffer", slot); err != nil {
		return 0, err
	}
	return w.follow(slot)
}

// offsetFieldFollow follows a uoffset field whose slot the reference does NOT
// align-check: reflection VerifyObject reads a table-typed field through
// GetPointer (an unaligned-safe ReadScalar) and checks only the target
// table's start, never the field slot. Go's accessors read the slot with
// binary.LittleEndian too, so an unaligned slot is memory-safe. Strings and
// vectors keep offsetField, which aligns the slot as VerifyField<uoffset_t>
// does.
func (w *walker) offsetFieldFollow(slot int64) (int64, error) {
	if err := w.rangeErr(slot, sizeUOffset, "offset slot at %d outside buffer", slot); err != nil {
		return 0, err
	}
	return w.follow(slot)
}

// table is VerifyObject: VerifyTableStart, then every declared field.
func (w *walker) table(pos int64, t *tableDef) error {
	if err := w.atErr(pos, sizeUOffset, "table at %d misaligned or outside buffer", pos); err != nil {
		return err
	}
	vt := pos - w.i32(pos)
	w.depth++
	defer func() { w.depth-- }()
	w.tables++
	if w.depth > MaxDepth {
		return w.fail("nesting deeper than %d", MaxDepth)
	}
	if w.tables > MaxTables {
		return w.fail("more than %d tables", MaxTables)
	}
	if w.tables > w.maxTables {
		return w.failClass(ClassStrictWork, "more than %d table visits for %d bytes", w.maxTables, w.n)
	}
	if err := w.atErr(vt, sizeVOffset, "vtable at %d misaligned or outside buffer", vt); err != nil {
		return err
	}
	vsize := w.u16(vt)
	if vsize&1 != 0 {
		return w.fail("vtable size %d is odd at %d", vsize, vt)
	}
	if err := w.rangeErr(vt, vsize, "vtable of %d bytes at %d outside buffer", vsize, vt); err != nil {
		return err
	}
	// Table::GetOptionalFieldOffset.
	slot := func(voffset int64) int64 {
		if voffset == 0 || voffset >= vsize {
			return 0
		}
		return w.u16(vt + voffset)
	}
	for i := range t.fields {
		f := &t.fields[i]
		w.slots++
		if w.slots > w.maxSlots {
			return w.failClass(ClassStrictWork, "verification work budget exceeded")
		}
		w.pathPieces = append(w.pathPieces, f.name)
		err := w.field(pos, slot, f)
		w.pathPieces = w.pathPieces[:len(w.pathPieces)-1]
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) field(pos int64, slot func(int64) int64, f *tableField) error {
	fo := slot(f.voffset)
	switch f.typ.base {
	case tString:
		if fo == 0 {
			return nil
		}
		target, err := w.offsetField(pos + fo)
		if err != nil {
			return err
		}
		return w.str(target)
	case tTable:
		if fo == 0 {
			if f.required {
				return w.fail("required table missing")
			}
			return nil
		}
		target, err := w.offsetFieldFollow(pos + fo)
		if err != nil {
			return err
		}
		return w.table(target, f.typ.table)
	case tStruct:
		if fo == 0 {
			if f.required {
				return w.fail("required struct missing")
			}
			return nil
		}
		// Verifier::VerifyFieldStruct: aligned to the struct's minalign.
		p := pos + fo
		if !w.aligned(p, f.typ.strct.align) {
			return w.fail("struct %s at %d misaligned", f.typ.strct.name, p)
		}
		return w.rangeErr(p, f.typ.strct.size, "struct %s at %d outside buffer", f.typ.strct.name, p)
	case tUnion:
		return w.union(pos, slot, fo, f)
	case tVector:
		if f.typ.elem.base == tUnion {
			return w.unionVector(pos, slot, fo, f)
		}
		if fo == 0 {
			if f.required && f.typ.elem.base == tStruct {
				return w.fail("required vector of structs missing")
			}
			return nil
		}
		target, err := w.offsetField(pos + fo)
		if err != nil {
			return err
		}
		return w.vector(target, *f.typ.elem)
	}
	size := scalarSize(f.typ.base)
	if size == 0 {
		return w.fail("unsupported field type")
	}
	if fo == 0 {
		return nil
	}
	return w.atErr(pos+fo, size, "scalar at %d misaligned or outside buffer", pos+fo)
}

// union mirrors the reference's union field: its _type companion is a UType
// scalar field; VerifyUnion refuses a type with no value (VerifyObject on a
// null table, required) and an out-of-range type; the DeepVerifier refuses a
// value whose type is NONE.
func (w *walker) union(pos int64, slot func(int64) int64, fo int64, f *tableField) error {
	utype := int64(0)
	if to := slot(f.typeVOffset); to != 0 {
		if err := w.rangeErr(pos+to, 1, "%s_type outside buffer", f.name); err != nil {
			return err
		}
		utype = int64(w.buf[pos+to])
	}
	if fo == 0 {
		if utype != 0 {
			return w.fail("union type %d with no value", utype)
		}
		return nil
	}
	if utype == 0 {
		return w.fail("union value with type NONE")
	}
	member, ok := f.typ.union.members[utype]
	if !ok {
		return w.fail("union %s has no member %d", f.typ.union.name, utype)
	}
	vslot := pos + fo
	if !w.in(vslot, sizeUOffset) {
		return w.failClass(ClassStrictUndefined, "union value slot at %d outside buffer", vslot)
	}
	target, err := w.follow(vslot)
	if err != nil {
		return err
	}
	return w.table(target, member)
}

// unionVector mirrors the reference: the _type companion is a [ubyte] field
// verified on its own; the values vector, when present, needs a type vector
// of the same length, and each element with a non-NONE type is its member
// table.
func (w *walker) unionVector(pos int64, slot func(int64) int64, fo int64, f *tableField) error {
	typeCount, typeFirst := int64(-1), int64(0)
	if to := slot(f.typeVOffset); to != 0 {
		typesAt, err := w.offsetField(pos + to)
		if err != nil {
			return err
		}
		if typeCount, typeFirst, err = w.vectorBody(typesAt, 1); err != nil {
			return err
		}
	}
	if fo == 0 {
		return nil
	}
	valuesAt, err := w.offsetField(pos + fo)
	if err != nil {
		return err
	}
	count, first, err := w.vectorBody(valuesAt, sizeUOffset)
	if err != nil {
		return err
	}
	if typeCount < 0 {
		return w.failClass(ClassStrictUndefined, "union vector values without a type vector")
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
		at, err := w.follow(first + i*sizeUOffset)
		if err != nil {
			return err
		}
		if err := w.table(at, member); err != nil {
			return err
		}
	}
	return nil
}

// vectorBody is VerifyVectorOrString: an aligned, in-bounds length word, an
// element count below max_size/elem_size, and the whole body in bounds. It
// returns the element count and the first element's position.
func (w *walker) vectorBody(target, elemSize int64) (int64, int64, error) {
	if err := w.atErr(target, sizeUOffset, "vector length at %d misaligned or outside buffer", target); err != nil {
		return 0, 0, err
	}
	count := w.u32(target)
	if count >= MaxBufferSize/elemSize {
		return 0, 0, w.fail("vector of %d x %d bytes exceeds the 2 GiB limit", count, elemSize)
	}
	if err := w.rangeErr(target, sizeUOffset+count*elemSize, "vector of %d x %d bytes at %d outside buffer", count, elemSize, target); err != nil {
		return 0, 0, err
	}
	return count, target + sizeUOffset, nil
}

// str is Verifier::VerifyString: a byte vector followed by a zero.
func (w *walker) str(target int64) error {
	count, first, err := w.vectorBody(target, 1)
	if err != nil {
		return err
	}
	end := first + count
	if err := w.rangeErr(end, 1, "string at %d has no terminator inside the buffer", target); err != nil {
		return err
	}
	if w.buf[end] != 0 {
		return w.fail("string at %d is not zero-terminated", target)
	}
	return nil
}

func (w *walker) vector(target int64, elem fieldType) error {
	switch elem.base {
	case tString, tTable:
		count, first, err := w.vectorBody(target, sizeUOffset)
		if err != nil {
			return err
		}
		for i := int64(0); i < count; i++ {
			at, err := w.follow(first + i*sizeUOffset)
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
		// VerifyVectorOfStructs: the body only; no element alignment.
		_, _, err := w.vectorBody(target, elem.strct.size)
		return err
	}
	size := scalarSize(elem.base)
	if size == 0 {
		return w.fail("unsupported vector element type")
	}
	_, _, err := w.vectorBody(target, size)
	return err
}
