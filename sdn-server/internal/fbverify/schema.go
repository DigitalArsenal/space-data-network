// Package fbverify is a structural FlatBuffers verifier for Go.
//
// The Go FlatBuffers runtime (github.com/google/flatbuffers/go) ships no
// verifier: its generated accessors trust every offset in the buffer and
// slice-index on it, so one out-of-range uoffset in an untrusted message is a
// runtime panic, and on a goroutine without recover() a process crash
// (security re-audit NET-01, 2026-10-15). This package is the missing piece,
// the Go counterpart of the C++ flatbuffers::Verifier: it reads the schema's
// IDL, models every table's vtable slots, struct layouts, vectors, strings and
// unions, and walks a buffer checking that every offset an accessor could
// follow lands inside it.
//
// It is held to flatbuffers' own C++ verifier: the reflection verifier
// (flatbuffers::Verify / VerifySizePrefixed, src/reflection.cpp) plus the
// DeepVerifier that flatc's binary_to_json runs after it
// (src/flatc_wasm_wasi.cpp). The node embeds that exact code as
// flatc-wasi.wasm, and the differential fuzz harness
// (internal/sds/structure_fuzz_test.go) compares the two verdicts. Like the
// reference it checks bounds, alignment (from the start of the buffer it is
// given, size prefix included), vtables, strings, vectors, unions, `required`
// tables, structs and struct vectors, deprecated fields, nesting depth and
// table count. verify.go lists where it is deliberately stricter.
//
// Every refusal carries a Class saying how it relates to the reference. All
// arithmetic is done in int64 on the real positions, so an offset that a
// uint32 accessor would wrap back into range is refused, never "fixed up".
package fbverify

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

type baseType int

const (
	tNone baseType = iota
	tBool
	tInt8
	tUint8
	tInt16
	tUint16
	tInt32
	tUint32
	tInt64
	tUint64
	tFloat32
	tFloat64
	tString
	tVector
	tStruct
	tTable
	tUnion
	tArray // fixed-length array, structs only
)

var scalarNames = map[string]baseType{
	"bool":    tBool,
	"byte":    tInt8,
	"int8":    tInt8,
	"ubyte":   tUint8,
	"uint8":   tUint8,
	"short":   tInt16,
	"int16":   tInt16,
	"ushort":  tUint16,
	"uint16":  tUint16,
	"int":     tInt32,
	"int32":   tInt32,
	"uint":    tUint32,
	"uint32":  tUint32,
	"long":    tInt64,
	"int64":   tInt64,
	"ulong":   tUint64,
	"uint64":  tUint64,
	"float":   tFloat32,
	"float32": tFloat32,
	"double":  tFloat64,
	"float64": tFloat64,
}

func scalarSize(b baseType) int64 {
	switch b {
	case tBool, tInt8, tUint8:
		return 1
	case tInt16, tUint16:
		return 2
	case tInt32, tUint32, tFloat32:
		return 4
	case tInt64, tUint64, tFloat64:
		return 8
	}
	return 0
}

// fieldType is a resolved wire type.
type fieldType struct {
	base     baseType
	elem     *fieldType // vector / array element
	arrayLen int64
	strct    *structDef
	table    *tableDef
	union    *unionDef
}

type tableField struct {
	name string
	// voffset is the field's byte offset inside the vtable (4 + 2*slot).
	voffset int64
	typ     fieldType
	// typeVOffset is the companion _type field of a union (or union vector).
	typeVOffset int64
	// required is the IDL's (required): the reference refuses a buffer that
	// leaves out a required table, struct or vector of structs.
	required bool
}

type tableDef struct {
	name   string
	fields []tableField // sorted by voffset
}

type structDef struct {
	name  string
	size  int64
	align int64
	// fields with their resolved byte offsets; only nested layout matters for
	// verification, which bounds-checks the struct as a whole.
	state int // 0 unresolved, 1 resolving, 2 done
	raw   *rawDecl
}

type unionDef struct {
	name    string
	members map[int64]*tableDef
}

// Schema is one compiled root schema.
type Schema struct {
	name       string
	root       *tableDef
	identifier string
}

// Name returns the schema's source name.
func (s *Schema) Name() string { return s.name }

// FileIdentifier returns the schema's declared file_identifier, or "".
func (s *Schema) FileIdentifier() string { return s.identifier }

// RootType returns the root table's qualified name.
func (s *Schema) RootType() string { return s.root.name }

// compiler resolves one root file and its transitive includes.
type compiler struct {
	loader  func(name string) ([]byte, bool)
	resolve func(includePath, fromName string) string
	decls   map[string]*rawDecl
	files   map[string]*rawFile
	tables  map[string]*tableDef
	structs map[string]*structDef
	unions  map[string]*unionDef
	pending []*rawDecl
	missing []string
}

// DefaultIncludeResolver maps an include path to a source name: the SDS
// layout `../RFM/main.fbs` names RFM.fbs, and any other path names its base.
func DefaultIncludeResolver(includePath, _ string) string {
	clean := path.Clean(strings.ReplaceAll(includePath, "\\", "/"))
	base := path.Base(clean)
	if base == "main.fbs" {
		dir := path.Base(path.Dir(clean))
		if dir != "." && dir != "/" && dir != ".." {
			return dir + ".fbs"
		}
	}
	return base
}

func compileSchema(name string, loader func(string) ([]byte, bool), resolve func(string, string) string) (*Schema, error) {
	if resolve == nil {
		resolve = DefaultIncludeResolver
	}
	c := &compiler{
		loader:  loader,
		resolve: resolve,
		decls:   map[string]*rawDecl{},
		files:   map[string]*rawFile{},
		tables:  map[string]*tableDef{},
		structs: map[string]*structDef{},
		unions:  map[string]*unionDef{},
	}
	if err := c.load(name, nil); err != nil {
		return nil, err
	}
	rootFile := c.files[name]
	if rootFile.rootType == "" {
		return nil, fmt.Errorf("%s: no root_type", name)
	}
	rootDecl, ok := c.lookup(rootFile.rootType, rootFile.rootNS)
	if !ok || rootDecl.kind != "table" {
		return nil, fmt.Errorf("%s: root_type %s is not a table", name, rootFile.rootType)
	}
	root, err := c.table(rootDecl)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	// Resolve every table the root reaches. A type that cannot be resolved
	// fails the whole compile, so the schema is refused up front rather than
	// failing open on the first buffer that happens to use it.
	for len(c.pending) > 0 {
		d := c.pending[0]
		c.pending = c.pending[1:]
		if err := c.fillTable(d); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return &Schema{name: name, root: root, identifier: rootFile.ident}, nil
}

func (c *compiler) load(name string, stack []string) error {
	if _, done := c.files[name]; done {
		return nil
	}
	for _, s := range stack {
		if s == name {
			return nil // include cycle: already being loaded
		}
	}
	src, ok := c.loader(name)
	if !ok {
		if len(stack) > 0 {
			// An include the node does not hold (REC.fbs names every
			// standard, not all of which are embedded). Its types stay
			// unresolved: a field typed by one fails the compile, and a
			// union member naming one is dropped, so a buffer carrying that
			// member is refused rather than walked blind.
			c.missing = append(c.missing, name)
			return nil
		}
		return fmt.Errorf("schema %s is not available", name)
	}
	f, err := parseFile(string(src))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	c.files[name] = f
	for _, d := range f.decls {
		if prev, dup := c.decls[d.name]; dup {
			if !sameDecl(prev, d) {
				return fmt.Errorf("%s: %s %s is declared twice with different definitions", name, d.kind, d.name)
			}
			continue
		}
		c.decls[d.name] = d
	}
	for _, inc := range f.includes {
		if err := c.load(c.resolve(inc, name), append(stack, name)); err != nil {
			return err
		}
	}
	return nil
}

func sameDecl(a, b *rawDecl) bool {
	if a.kind != b.kind || a.underlying != b.underlying || len(a.fields) != len(b.fields) || len(a.members) != len(b.members) {
		return false
	}
	for i := range a.fields {
		fa, fb := a.fields[i], b.fields[i]
		if fa.name != fb.name || fa.typ != fb.typ || fa.attrs["id"] != fb.attrs["id"] || fa.attrs["deprecated"] != fb.attrs["deprecated"] {
			return false
		}
	}
	for i := range a.members {
		if a.members[i] != b.members[i] {
			return false
		}
	}
	return true
}

// lookup resolves a type name the way flatc does: innermost namespace first.
func (c *compiler) lookup(name, ns string) (*rawDecl, bool) {
	for {
		qualified := name
		if ns != "" {
			qualified = ns + "." + name
		}
		if d, ok := c.decls[qualified]; ok {
			return d, true
		}
		if ns == "" {
			return nil, false
		}
		if i := strings.LastIndexByte(ns, '.'); i >= 0 {
			ns = ns[:i]
		} else {
			ns = ""
		}
	}
}

func (c *compiler) table(d *rawDecl) (*tableDef, error) {
	if t, ok := c.tables[d.name]; ok {
		return t, nil
	}
	t := &tableDef{name: d.name}
	c.tables[d.name] = t
	c.pending = append(c.pending, d)
	return t, nil
}

func (c *compiler) fillTable(d *rawDecl) error {
	t := c.tables[d.name]
	// Slot assignment: sequential, a union (or union vector) takes two slots
	// (its _type companion first); explicit id: attributes override, and
	// then every field must carry one.
	withID := 0
	for _, f := range d.fields {
		if _, ok := f.attrs["id"]; ok {
			withID++
		}
	}
	if withID != 0 && withID != len(d.fields) {
		return fmt.Errorf("table %s: id: must be given on every field or none", d.name)
	}
	slot := int64(0)
	used := map[int64]string{}
	for _, f := range d.fields {
		ft, err := c.resolveType(f.typ, d.namespace, false)
		if err != nil {
			return fmt.Errorf("table %s field %s: %w", d.name, f.name, err)
		}
		isUnion := ft.base == tUnion || (ft.base == tVector && ft.elem.base == tUnion)
		var valueSlot int64
		if withID > 0 {
			var id int64
			if _, err := fmt.Sscan(f.attrs["id"], &id); err != nil || id < 0 || id > 0x7fff {
				return fmt.Errorf("table %s field %s: bad id %q", d.name, f.name, f.attrs["id"])
			}
			valueSlot = id
			if isUnion && id == 0 {
				return fmt.Errorf("table %s field %s: a union id must leave room for its _type", d.name, f.name)
			}
		} else {
			if isUnion {
				slot++ // the _type field
			}
			valueSlot = slot
			slot++
		}
		for _, s := range []int64{valueSlot, valueSlot - 1} {
			if s == valueSlot-1 && !isUnion {
				continue
			}
			if prev, dup := used[s]; dup {
				return fmt.Errorf("table %s: fields %s and %s share slot %d", d.name, prev, f.name, s)
			}
			used[s] = f.name
		}
		tf := tableField{name: f.name, voffset: 4 + 2*valueSlot, typ: ft}
		if isUnion {
			tf.typeVOffset = 4 + 2*(valueSlot-1)
		}
		if tf.voffset > 0xffff {
			return fmt.Errorf("table %s field %s: vtable offset out of range", d.name, f.name)
		}
		// Deprecated fields stay: no Go accessor reads them, but the
		// reference verifies every field the schema declares, and other
		// readers of the same bytes (flatc's JSON printer) follow them.
		_, tf.required = f.attrs["required"]
		t.fields = append(t.fields, tf)
	}
	sort.Slice(t.fields, func(i, j int) bool { return t.fields[i].voffset < t.fields[j].voffset })
	return nil
}

func (c *compiler) resolveType(rt rawType, ns string, inStruct bool) (fieldType, error) {
	elem, err := c.resolveNamed(rt.name, ns)
	if err != nil {
		return fieldType{}, err
	}
	switch {
	case rt.arrayLen > 0:
		if !inStruct {
			return fieldType{}, fmt.Errorf("fixed-length arrays are only allowed in structs")
		}
		if elem.base != tStruct && scalarSize(elem.base) == 0 {
			return fieldType{}, fmt.Errorf("array of %s is not allowed", rt.name)
		}
		e := elem
		return fieldType{base: tArray, elem: &e, arrayLen: int64(rt.arrayLen)}, nil
	case rt.vector:
		if inStruct {
			return fieldType{}, fmt.Errorf("vectors are not allowed in structs")
		}
		if elem.base == tVector {
			return fieldType{}, fmt.Errorf("nested vectors are not allowed")
		}
		e := elem
		return fieldType{base: tVector, elem: &e}, nil
	}
	if inStruct && elem.base != tStruct && scalarSize(elem.base) == 0 {
		return fieldType{}, fmt.Errorf("struct fields must be scalars or structs, not %s", rt.name)
	}
	return elem, nil
}

func (c *compiler) resolveNamed(name, ns string) (fieldType, error) {
	if b, ok := scalarNames[name]; ok {
		return fieldType{base: b}, nil
	}
	if name == "string" {
		return fieldType{base: tString}, nil
	}
	d, ok := c.lookup(name, ns)
	if !ok {
		if len(c.missing) > 0 {
			return fieldType{}, fmt.Errorf("unknown type %s (unavailable includes: %s)", name, strings.Join(c.missing, ", "))
		}
		return fieldType{}, fmt.Errorf("unknown type %s", name)
	}
	switch d.kind {
	case "table":
		t, err := c.table(d)
		if err != nil {
			return fieldType{}, err
		}
		return fieldType{base: tTable, table: t}, nil
	case "struct":
		s, err := c.structLayout(d)
		if err != nil {
			return fieldType{}, err
		}
		return fieldType{base: tStruct, strct: s}, nil
	case "enum":
		b, ok := scalarNames[d.underlying]
		if !ok || b == tFloat32 || b == tFloat64 {
			return fieldType{}, fmt.Errorf("enum %s: bad underlying type %s", d.name, d.underlying)
		}
		return fieldType{base: b}, nil
	case "union":
		u, err := c.unionDef(d)
		if err != nil {
			return fieldType{}, err
		}
		return fieldType{base: tUnion, union: u}, nil
	}
	return fieldType{}, fmt.Errorf("type %s has unsupported kind %s", name, d.kind)
}

func (c *compiler) unionDef(d *rawDecl) (*unionDef, error) {
	if u, ok := c.unions[d.name]; ok {
		return u, nil
	}
	u := &unionDef{name: d.name, members: map[int64]*tableDef{}}
	c.unions[d.name] = u
	for _, m := range d.members {
		if m.value <= 0 || m.value > 255 {
			return nil, fmt.Errorf("union %s: member %s value %d out of range", d.name, m.typeName, m.value)
		}
		md, ok := c.lookup(m.typeName, d.namespace)
		if !ok {
			if len(c.missing) > 0 {
				continue // declared in an include the node does not hold
			}
			return nil, fmt.Errorf("union %s: unknown member %s", d.name, m.typeName)
		}
		if md.kind != "table" {
			return nil, fmt.Errorf("union %s: member %s is a %s; only table members are supported", d.name, m.typeName, md.kind)
		}
		if _, dup := u.members[m.value]; dup {
			return nil, fmt.Errorf("union %s: duplicate member value %d", d.name, m.value)
		}
		t, err := c.table(md)
		if err != nil {
			return nil, err
		}
		u.members[m.value] = t
	}
	return u, nil
}

func (c *compiler) structLayout(d *rawDecl) (*structDef, error) {
	s, ok := c.structs[d.name]
	if !ok {
		s = &structDef{name: d.name, raw: d}
		c.structs[d.name] = s
	}
	switch s.state {
	case 2:
		return s, nil
	case 1:
		return nil, fmt.Errorf("struct %s contains itself", d.name)
	}
	s.state = 1
	var offset, align int64 = 0, 1
	for _, f := range d.fields {
		ft, err := c.resolveType(f.typ, d.namespace, true)
		if err != nil {
			return nil, fmt.Errorf("struct %s field %s: %w", d.name, f.name, err)
		}
		size, fa := typeSizeAlign(ft)
		if fa > align {
			align = fa
		}
		offset = (offset + fa - 1) / fa * fa
		offset += size
	}
	if fa, ok := d.attrs["force_align"]; ok {
		var forced int64
		if _, err := fmt.Sscan(fa, &forced); err != nil || forced < align || forced > 256 || forced&(forced-1) != 0 {
			return nil, fmt.Errorf("struct %s: bad force_align %q", d.name, fa)
		}
		align = forced
	}
	s.size = (offset + align - 1) / align * align
	s.align = align
	if s.size == 0 {
		return nil, fmt.Errorf("struct %s is empty", d.name)
	}
	s.state = 2
	return s, nil
}

func typeSizeAlign(ft fieldType) (int64, int64) {
	switch ft.base {
	case tStruct:
		return ft.strct.size, ft.strct.align
	case tArray:
		size, align := typeSizeAlign(*ft.elem)
		return size * ft.arrayLen, align
	}
	n := scalarSize(ft.base)
	return n, n
}
