package fbverify

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
)

// testIDL exercises every wire shape the verifier models: strings, scalar and
// struct vectors, string and table vectors, nested and recursive tables,
// structs with padding, enums, a union, a deprecated slot and a field after it.
const testIDL = `
// line comment
/* block
   comment */
namespace t;
attribute "aligned_max_count";

struct Vec3 { x: double; y: double; z: double; }
struct Padded { a: ubyte; b: double; }
enum Color : ubyte { Red, Green = 2, Blue }

table Leaf {
  name: string;
  vals: [double] (aligned_max_count: 8);
}

union Any { Leaf, Node }

table Node {
  leaf: Leaf;
  leaves: [Leaf];
  names: [string];
  pos: Vec3;
  color: Color = Green;
  any: Any;
  points: [Vec3];
  child: Node;
  dep: int (deprecated);
  after: int = -1;
  pad: Padded;
}

root_type Node;
file_identifier "TNOD";
`

// slots of Node, as the IDL assigns them (any takes two: _type then value).
const (
	slotLeaf = iota
	slotLeaves
	slotNames
	slotPos
	slotColor
	slotAnyType
	slotAny
	slotPoints
	slotChild
	slotDep
	slotAfter
	slotPad
	nodeSlots
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	r.AddSource("T.fbs", []byte(testIDL))
	if _, err := r.Schema("T.fbs"); err != nil {
		t.Fatalf("compile test schema: %v", err)
	}
	return r
}

func buildLeaf(b *flatbuffers.Builder, name string, vals []float64) flatbuffers.UOffsetT {
	n := b.CreateString(name)
	b.StartVector(8, len(vals), 8)
	for i := len(vals) - 1; i >= 0; i-- {
		b.PrependFloat64(vals[i])
	}
	v := b.EndVector(len(vals))
	b.StartObject(2)
	b.PrependUOffsetTSlot(0, n, 0)
	b.PrependUOffsetTSlot(1, v, 0)
	return b.EndObject()
}

// buildNode returns a finished Node buffer with every field set and a child
// chain of the given depth.
func buildNode(depth int, sizePrefixed bool) []byte {
	b := flatbuffers.NewBuilder(256)
	var child flatbuffers.UOffsetT
	for d := 0; d < depth; d++ {
		b.StartObject(nodeSlots)
		if child != 0 {
			b.PrependUOffsetTSlot(slotChild, child, 0)
		}
		child = b.EndObject()
	}
	leaf := buildLeaf(b, "leaf", []float64{1, 2, 3})
	l1 := buildLeaf(b, "a", nil)
	l2 := buildLeaf(b, "b", []float64{4})
	anyLeaf := buildLeaf(b, "union member", []float64{5})
	b.StartVector(4, 2, 4)
	b.PrependUOffsetT(l2)
	b.PrependUOffsetT(l1)
	leaves := b.EndVector(2)
	s1, s2 := b.CreateString("x"), b.CreateString("yz")
	b.StartVector(4, 2, 4)
	b.PrependUOffsetT(s2)
	b.PrependUOffsetT(s1)
	names := b.EndVector(2)
	b.StartVector(24, 2, 8)
	for i := 0; i < 2; i++ {
		b.Prep(8, 24)
		b.PrependFloat64(3)
		b.PrependFloat64(2)
		b.PrependFloat64(1)
	}
	points := b.EndVector(2)

	b.StartObject(nodeSlots)
	b.PrependUOffsetTSlot(slotLeaf, leaf, 0)
	b.PrependUOffsetTSlot(slotLeaves, leaves, 0)
	b.PrependUOffsetTSlot(slotNames, names, 0)
	b.Prep(8, 24)
	b.PrependFloat64(9)
	b.PrependFloat64(8)
	b.PrependFloat64(7)
	b.Slot(slotPos)
	b.PrependByteSlot(slotColor, 3, 2)
	b.PrependByteSlot(slotAnyType, 1, 0)
	b.PrependUOffsetTSlot(slotAny, anyLeaf, 0)
	b.PrependUOffsetTSlot(slotPoints, points, 0)
	if child != 0 {
		b.PrependUOffsetTSlot(slotChild, child, 0)
	}
	b.PrependInt32Slot(slotAfter, 5, -1)
	b.Prep(8, 16)
	b.Pad(7)
	b.PrependFloat64(1.5)
	b.PrependByte(1)
	b.Slot(slotPad)
	root := b.EndObject()
	if sizePrefixed {
		b.FinishSizePrefixedWithFileIdentifier(root, []byte("TNOD"))
	} else {
		b.FinishWithFileIdentifier(root, []byte("TNOD"))
	}
	return append([]byte(nil), b.FinishedBytes()...)
}

// fieldPos finds where the root table's field at slot is stored.
func fieldPos(t *testing.T, buf []byte, slot int) int {
	t.Helper()
	root := int(binary.LittleEndian.Uint32(buf))
	vt := root - int(int32(binary.LittleEndian.Uint32(buf[root:])))
	vsize := int(binary.LittleEndian.Uint16(buf[vt:]))
	vo := 4 + 2*slot
	if vo >= vsize {
		t.Fatalf("slot %d absent", slot)
	}
	fo := int(binary.LittleEndian.Uint16(buf[vt+vo:]))
	if fo == 0 {
		t.Fatalf("slot %d absent", slot)
	}
	return root + fo
}

func target(buf []byte, pos int) int { return pos + int(binary.LittleEndian.Uint32(buf[pos:])) }

func mustFail(t *testing.T, r *Registry, buf []byte, want string) {
	t.Helper()
	err := r.VerifyBuffer("T.fbs", buf)
	if err == nil {
		t.Fatalf("corrupt buffer verified; want failure containing %q", want)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error %v does not wrap ErrInvalid", err)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
}

func TestVerifyAcceptsWellFormedBuffers(t *testing.T) {
	r := testRegistry(t)
	if err := r.VerifyBuffer("T.fbs", buildNode(3, false)); err != nil {
		t.Fatalf("bare buffer: %v", err)
	}
	if err := r.VerifySizePrefixedBuffer("T.fbs", buildNode(3, true)); err != nil {
		t.Fatalf("size-prefixed buffer: %v", err)
	}
	// An empty table is fine: every field is optional to the verifier.
	b := flatbuffers.NewBuilder(0)
	b.StartObject(0)
	b.Finish(b.EndObject())
	if err := r.VerifyBuffer("T.fbs", b.FinishedBytes()); err != nil {
		t.Fatalf("empty table: %v", err)
	}
}

func TestVerifyRefusesCorruptOffsets(t *testing.T) {
	r := testRegistry(t)
	cases := []struct {
		name string
		edit func(t *testing.T, buf []byte)
		want string
	}{
		{"root offset outside buffer", func(t *testing.T, b []byte) { binary.LittleEndian.PutUint32(b, uint32(len(b)+4)) }, "outside buffer"},
		{"zero root offset", func(t *testing.T, b []byte) { binary.LittleEndian.PutUint32(b, 0) }, "zero offset"},
		{"vtable outside buffer", func(t *testing.T, b []byte) {
			root := binary.LittleEndian.Uint32(b)
			binary.LittleEndian.PutUint32(b[root:], uint32(0x7fff0000))
		}, "vtable"},
		{"string offset outside buffer (the NET-01 shape)", func(t *testing.T, b []byte) {
			leaf := target(b, fieldPos(t, b, slotLeaf))
			_ = leaf
			names := target(b, fieldPos(t, b, slotNames))
			binary.LittleEndian.PutUint32(b[names+4:], 0x7fffff00)
		}, "outside buffer"},
		{"string offset with the sign bit", func(t *testing.T, b []byte) {
			names := target(b, fieldPos(t, b, slotNames))
			binary.LittleEndian.PutUint32(b[names+4:], 0xffffff00)
		}, "sign bit"},
		{"string length past the end", func(t *testing.T, b []byte) {
			names := target(b, fieldPos(t, b, slotNames))
			s := target(b, names+4)
			binary.LittleEndian.PutUint32(b[s:], uint32(len(b)))
		}, "outside buffer"},
		{"string not zero-terminated", func(t *testing.T, b []byte) {
			names := target(b, fieldPos(t, b, slotNames))
			s := target(b, names+4)
			b[s+4+1] = 'Q' // "x" then its terminator
		}, "zero-terminated"},
		{"scalar vector count overflows", func(t *testing.T, b []byte) {
			leaf := target(b, fieldPos(t, b, slotLeaf))
			// Leaf.vals is slot 1
			vt := leaf - int(int32(binary.LittleEndian.Uint32(b[leaf:])))
			fo := int(binary.LittleEndian.Uint16(b[vt+6:]))
			vec := target(b, leaf+fo)
			binary.LittleEndian.PutUint32(b[vec:], 0x20000000)
		}, "vector"},
		{"struct vector count overflows", func(t *testing.T, b []byte) {
			vec := target(b, fieldPos(t, b, slotPoints))
			binary.LittleEndian.PutUint32(b[vec:], 0x0fffffff)
		}, "vector"},
		{"union type that is not a member", func(t *testing.T, b []byte) {
			b[fieldPos(t, b, slotAnyType)] = 9
		}, "no member 9"},
		{"nested table offset outside buffer", func(t *testing.T, b []byte) {
			binary.LittleEndian.PutUint32(b[fieldPos(t, b, slotLeaf):], uint32(len(b)))
		}, "outside buffer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := buildNode(2, false)
			tc.edit(t, buf)
			mustFail(t, r, buf, tc.want)
		})
	}
}

// A uoffset whose uint32 sum wraps back into the buffer is what a Go accessor
// would follow to the wrong place; the verifier works in 64 bits and refuses it.
func TestVerifyRefusesWrappingOffsets(t *testing.T) {
	r := testRegistry(t)
	buf := buildNode(1, false)
	p := fieldPos(t, buf, slotLeaf)
	// p + o == p + 2^32 - 8 (mod 2^32) == p - 8: inside the buffer once wrapped.
	binary.LittleEndian.PutUint32(buf[p:], uint32(1<<32-8))
	mustFail(t, r, buf, "")
}

func TestVerifySizePrefixMustMatch(t *testing.T) {
	r := testRegistry(t)
	buf := buildNode(1, true)
	binary.LittleEndian.PutUint32(buf, uint32(len(buf)))
	if err := r.VerifySizePrefixedBuffer("T.fbs", buf); err == nil || !strings.Contains(err.Error(), "size prefix") {
		t.Fatalf("mismatched size prefix: %v", err)
	}
}

func TestVerifyBoundsNestingDepth(t *testing.T) {
	r := testRegistry(t)
	if err := r.VerifyBuffer("T.fbs", buildNode(MaxDepth-2, false)); err != nil {
		t.Fatalf("depth %d: %v", MaxDepth-2, err)
	}
	mustFail(t, r, buildNode(MaxDepth+1, false), "nesting deeper")
}

// A vector of offsets that all point at one table makes the verifier visit it
// once per element; the table budget is sized from the buffer, so a crafted
// DAG cannot make verification super-linear.
func TestVerifyBoundsWorkOnSharedOffsets(t *testing.T) {
	r := testRegistry(t)
	b := flatbuffers.NewBuilder(0)
	// Inner: a Node whose leaves vector repeats one leaf many times.
	leaf := buildLeaf(b, "shared", nil)
	const repeat = 5000
	b.StartVector(4, repeat, 4)
	for i := 0; i < repeat; i++ {
		b.PrependUOffsetT(leaf)
	}
	vec := b.EndVector(repeat)
	b.StartObject(nodeSlots)
	b.PrependUOffsetTSlot(slotLeaves, vec, 0)
	inner := b.EndObject()
	// Outer: many Nodes each pointing at that inner node through leaves of
	// tables is not expressible, so chain `child` through a vector of Nodes
	// via the union: each union member re-visits inner.
	b.StartObject(nodeSlots)
	b.PrependUOffsetTSlot(slotChild, inner, 0)
	root := b.EndObject()
	b.Finish(root)
	if err := r.VerifyBuffer("T.fbs", b.FinishedBytes()); err != nil {
		t.Fatalf("shared offsets within budget: %v", err)
	}

	// Now a DAG: 64 levels, each level a Node whose leaves... a Node's child
	// is a single offset, so build fan-out with `any` + `child` both pointing
	// at the level below: 2^depth visits for linear bytes.
	b = flatbuffers.NewBuilder(0)
	var below flatbuffers.UOffsetT
	for d := 0; d < 40; d++ {
		b.StartObject(nodeSlots)
		if below != 0 {
			b.PrependUOffsetTSlot(slotChild, below, 0)
			b.PrependByteSlot(slotAnyType, 2, 0) // Node
			b.PrependUOffsetTSlot(slotAny, below, 0)
		}
		below = b.EndObject()
	}
	b.Finish(below)
	mustFail(t, r, b.FinishedBytes(), "tables")
}

func TestCompileModelsSlotsAndLayouts(t *testing.T) {
	r := testRegistry(t)
	s, err := r.Schema("T.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if s.RootType() != "t.Node" || s.FileIdentifier() != "TNOD" {
		t.Fatalf("root %q ident %q", s.RootType(), s.FileIdentifier())
	}
	want := map[string]int64{"leaf": slotLeaf, "leaves": slotLeaves, "names": slotNames, "pos": slotPos,
		"color": slotColor, "any": slotAny, "points": slotPoints, "child": slotChild, "after": slotAfter, "pad": slotPad}
	for _, f := range s.root.fields {
		w, ok := want[f.name]
		if !ok {
			t.Fatalf("unexpected field %s (deprecated fields are not walked)", f.name)
		}
		if f.voffset != 4+2*w {
			t.Fatalf("field %s voffset %d, want %d", f.name, f.voffset, 4+2*w)
		}
		if f.name == "any" && f.typeVOffset != 4+2*slotAnyType {
			t.Fatalf("any_type voffset %d", f.typeVOffset)
		}
		if f.name == "pad" && (f.typ.strct.size != 16 || f.typ.strct.align != 8) {
			t.Fatalf("Padded layout %d/%d, want 16/8", f.typ.strct.size, f.typ.strct.align)
		}
		if f.name == "pos" && f.typ.strct.size != 24 {
			t.Fatalf("Vec3 size %d", f.typ.strct.size)
		}
	}
}

func TestCompileExplicitIDs(t *testing.T) {
	r := NewRegistry()
	r.AddSource("I.fbs", []byte(`
union U { A }
table A { x: int; }
table R { u: U (id: 3); s: string (id: 0); n: int (id: 1); }
root_type R;`))
	s, err := r.Schema("I.fbs")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]int64{}
	for _, f := range s.root.fields {
		got[f.name] = [2]int64{f.voffset, f.typeVOffset}
	}
	if got["s"][0] != 4 || got["n"][0] != 6 || got["u"][0] != 10 || got["u"][1] != 8 {
		t.Fatalf("explicit ids resolved to %v", got)
	}
}

func TestCompileRefusesWhatItCannotModel(t *testing.T) {
	for name, idl := range map[string]string{
		"unknown field type": `table R { x: Missing; } root_type R;`,
		"no root_type":       `table R { x: int; }`,
		"partial ids":        `table R { a: int (id: 0); b: int; } root_type R;`,
		"shared slot":        `table R { a: int (id: 0); b: int (id: 0); } root_type R;`,
		"vector in struct":   `struct S { v: [int]; } table R { s: S; } root_type R;`,
		"recursive struct":   `struct S { s: S; } table R { s: S; } root_type R;`,
		"rpc service":        `rpc_service X { M(R):R; } table R { a: int; } root_type R;`,
		"conflicting dup":    `table R { a: int; } table R { a: long; } root_type R;`,
	} {
		r := NewRegistry()
		r.AddSource("X.fbs", []byte(idl))
		if _, err := r.Schema("X.fbs"); err == nil {
			t.Errorf("%s: compiled", name)
		}
	}
}

func TestCompileResolvesSDSIncludes(t *testing.T) {
	if got := DefaultIncludeResolver("../RFM/main.fbs", "X.fbs"); got != "RFM.fbs" {
		t.Fatalf("include resolved to %q", got)
	}
	if got := DefaultIncludeResolver("lib/common.fbs", "X.fbs"); got != "common.fbs" {
		t.Fatalf("include resolved to %q", got)
	}
	r := NewRegistry()
	r.AddSource("B.fbs", []byte(`table B { s: string; } root_type B;`))
	r.AddSource("A.fbs", []byte(`include "../B/main.fbs"; table A { b: B; } root_type A;`))
	if _, err := r.Schema("A.fbs"); err != nil {
		t.Fatal(err)
	}
	// A union member from an include the node does not hold is dropped, so a
	// buffer carrying it is refused; a field typed by one fails the compile.
	r.AddSource("U.fbs", []byte(`include "../GONE/main.fbs"; include "../B/main.fbs"; union V { B, Gone } table U { v: V; } root_type U;`))
	s, err := r.Schema("U.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.root.fields) != 1 || len(s.root.fields[0].typ.union.members) != 1 {
		t.Fatalf("union members %v", s.root.fields[0].typ.union.members)
	}
	r.AddSource("F.fbs", []byte(`include "../GONE/main.fbs"; table F { g: Gone; } root_type F;`))
	if _, err := r.Schema("F.fbs"); err == nil {
		t.Fatal("a field typed by an unavailable include compiled")
	}
}

// Every Space Data Standard the node embeds must compile: a schema the
// verifier cannot model would otherwise be refused on every ingress path.
func TestEveryEmbeddedSDSSchemaCompiles(t *testing.T) {
	files, err := filepath.Glob("../sds/schemas/*.fbs")
	if err != nil || len(files) < 200 {
		t.Fatalf("embedded schemas: %d (%v)", len(files), err)
	}
	r := NewRegistry()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r.AddSource(filepath.Base(f), b)
	}
	for _, f := range files {
		if _, err := r.Schema(filepath.Base(f)); err != nil {
			t.Errorf("%v", err)
		}
	}
}
