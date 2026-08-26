package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IRM"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"

	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// ==========================================================================
// AN INDEPENDENT ORACLE FOR THE GENERATED CATALOG
// ==========================================================================
//
// enginecatalog derives the projection with its OWN regex reading of the
// .fbs IDLs. Every test that re-derives the expected columns from that same
// package — or from the generated bindings the package emitted — is
// SELF-CONSISTENT and therefore blind: it passes for any binding the
// generator agrees with itself about, including a wrong one.
//
// This file supplies the second, independent derivation: the VENDORED FLATC
// OUTPUT (sdn-server/third_party/spacedatastandards-go, the module the
// daemon actually links through the go.mod replace of
// github.com/DigitalArsenal/spacedatastandards.org/lib/go). Those files were
// produced by the FlatBuffers compiler from the same IDLs, and they state the
// two facts the projection is made of:
//
//   - the SLOT of every root-table field, verbatim from flatc's own
//     `builder.Prepend<Kind>Slot(<slot>, ...)`;
//   - the TYPE of every field, from the prepend kind (which resolves enums
//     to their underlying scalar exactly as the engine must) plus the
//     accessor signature (which is what separates a string from a byte
//     vector from a sub-table from a union value — all four are a bare
//     uoffset on the write side).
//
// Nothing here reads enginecatalog's parser, its output text, or its type
// tables. When the two derivations agree, the catalog is right; when they
// disagree, exactly one of them is, and the test names which field.

// flatcBindingDir is the vendored flatc output for the SDS pin whose IDLs
// live in embeddedSchemaDir. go.mod replaces the published lib/go module with
// this directory, so it is the code the daemon links, not a stale copy.
const flatcBindingDir = "../../third_party/spacedatastandards-go"

// flatcFieldKind classifies a root-table field the way the ENGINE has to see
// it: representable as a SQL column, or an offset to something the vtab
// cannot decode.
type flatcFieldKind string

const (
	flatcScalar     flatcFieldKind = "scalar"      // bool / int / float, enums resolved
	flatcString     flatcFieldKind = "string"      // string
	flatcBytes      flatcFieldKind = "bytes"       // [ubyte] / [byte]
	flatcVector     flatcFieldKind = "vector"      // vector of anything else
	flatcObject     flatcFieldKind = "table"       // sub-table
	flatcStruct     flatcFieldKind = "struct"      // inline struct
	flatcUnionValue flatcFieldKind = "union-value" // the second slot of a union
)

func (k flatcFieldKind) representable() bool {
	return k == flatcScalar || k == flatcString || k == flatcBytes
}

// flatcField is one root-table field as flatc emitted it.
type flatcField struct {
	Name string
	Slot int
	Kind flatcFieldKind
	// Type is the canonical scalar token ("f64", "u8", ...) for a scalar
	// field, empty otherwise. It comes from the prepend kind, so an enum
	// field carries its UNDERLYING scalar — the same resolution the engine
	// needs and the generator claims to perform.
	Type string
}

// flatcRoot is one standard's root table as flatc emitted it.
type flatcRoot struct {
	Code   string
	Root   string
	FileID string
	Slots  int // builder.StartObject(N)
	Fields []flatcField
}

// engineFlatcUnvendoredStandards are the routed standards the vendored lib/go
// module ships NO package for, so no flatc oracle exists to check them
// against. This is a VENDOR GAP, not a carve-out: the test asserts the
// package is genuinely absent, so each entry disappears by itself the moment
// the bindings are published, and a standard can never be quietly dropped
// from the oracle by adding its name here.
var engineFlatcUnvendoredStandards = map[string]string{
	"PGR.fbs":  "no PGR package in the vendored lib/go",
	"PLHD.fbs": "no PLHD package in the vendored lib/go",
	"PLOG.fbs": "no PLOG package in the vendored lib/go",
	"RHD.fbs":  "no RHD package in the vendored lib/go",
}

var (
	reFlatcIdentConst = regexp.MustCompile(`(?m)^const (\w+)Identifier = "([^"]*)"`)
	reFlatcMutate     = regexp.MustCompile(`^Mutate[A-Z_]`)
)

// prependKindToScalar maps flatc's Go builder call to the canonical scalar
// token. flatc emits the UNDERLYING scalar's prepend for an enum field and
// PrependByteSlot for a union discriminator, which is precisely why the
// prepend call — not the IDL type name — is the honest type oracle.
var prependKindToScalar = map[string]string{
	"Bool":    "bool",
	"Byte":    "u8",
	"Uint8":   "u8",
	"Int8":    "i8",
	"Int16":   "i16",
	"Uint16":  "u16",
	"Int32":   "i32",
	"Uint32":  "u32",
	"Int64":   "i64",
	"Uint64":  "u64",
	"Float32": "f32",
	"Float64": "f64",
}

// canonicalEngineType maps an IDL type as the CATALOG spells it to the same
// canonical token, so a comparison against the flatc oracle is about width
// and signedness rather than spelling.
func canonicalEngineType(idl string) string {
	switch idl {
	case "bool":
		return "bool"
	case "byte", "int8":
		return "i8"
	case "ubyte", "uint8":
		return "u8"
	case "short", "int16":
		return "i16"
	case "ushort", "uint16":
		return "u16"
	case "int", "int32":
		return "i32"
	case "uint", "uint32":
		return "u32"
	case "long", "int64":
		return "i64"
	case "ulong", "uint64":
		return "u64"
	case "float", "float32":
		return "f32"
	case "double", "float64":
		return "f64"
	case "string":
		return "str"
	case "[ubyte]", "[byte]", "[uint8]", "[int8]":
		return "bytes"
	}
	return "?" + idl
}

// engineIDLTypeFor spells a canonical scalar token the way an engine schema
// declares it, so a flatc-derived projection can be handed to the engine
// verbatim.
func engineIDLTypeFor(token string) string {
	switch token {
	case "bool":
		return "bool"
	case "i8":
		return "byte"
	case "u8":
		return "ubyte"
	case "i16":
		return "short"
	case "u16":
		return "ushort"
	case "i32":
		return "int"
	case "u32":
		return "uint"
	case "i64":
		return "long"
	case "u64":
		return "ulong"
	case "f32":
		return "float"
	case "f64":
		return "double"
	}
	return token
}

// loadFlatcRoot parses the vendored flatc package for one standard code.
func loadFlatcRoot(code string) (*flatcRoot, error) {
	dir := filepath.Join(flatcBindingDir, code)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sources := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sources = append(sources, string(raw))
	}
	sort.Strings(sources)

	// THE ROOT IS FLATC'S TO NAME. flatc emits `const <Root>Identifier` only
	// for the type the IDL declares as root_type, so the root name AND the
	// four header bytes both come from the compiler here — neither is taken
	// from the catalog under test.
	root, fileID := "", ""
	for _, src := range sources {
		for _, m := range reFlatcIdentConst.FindAllStringSubmatch(src, -1) {
			if m[2] == "$"+code || m[2] == code {
				root, fileID = m[1], m[2]
			}
		}
	}
	if root == "" {
		return nil, fmt.Errorf("%s: no flatc root identifier constant in %s", code, dir)
	}

	reStart := regexp.MustCompile(`(?m)^func ` + root + `Start\(builder \*flatbuffers\.Builder\) \{\n\tbuilder\.StartObject\((\d+)\)`)
	reAdd := regexp.MustCompile(`(?m)^func ` + root + `Add(\w+)\(builder \*flatbuffers\.Builder, (\w+) ([^)]*)\) \{\n\tbuilder\.Prepend(\w+)Slot\((\d+),`)
	reAcc := regexp.MustCompile(`(?m)^func \(rcv \*` + root + `\) (\w+)\(([^)]*)\) ([^{]*)\{`)

	out := &flatcRoot{Code: code, Root: root, FileID: fileID, Slots: -1}
	accessors := map[string][2]string{} // name -> (params, results)
	type addSpec struct {
		slot int
		kind string
	}
	adds := map[string]addSpec{}
	for _, src := range sources {
		if m := reStart.FindStringSubmatch(src); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("%s: StartObject count %q: %w", code, m[1], err)
			}
			out.Slots = n
		}
		for _, m := range reAcc.FindAllStringSubmatch(src, -1) {
			name := m[1]
			if reFlatcMutate.MatchString(name) || name == "Init" || name == "Table" {
				continue
			}
			accessors[name] = [2]string{strings.TrimSpace(m[2]), strings.TrimSpace(m[3])}
		}
		for _, m := range reAdd.FindAllStringSubmatch(src, -1) {
			// m[1] is the exported suffix, m[2] the parameter name — flatc
			// names the parameter with the IDL field name VERBATIM, and only
			// the verbatim variant carries the Prepend call (the camelCase
			// twin delegates to it), so this match is the field itself.
			slot, err := strconv.Atoi(m[5])
			if err != nil {
				return nil, fmt.Errorf("%s: slot %q: %w", code, m[5], err)
			}
			adds[m[2]] = addSpec{slot: slot, kind: m[4]}
		}
	}
	if out.Slots < 0 {
		return nil, fmt.Errorf("%s: no %sStart in the vendored bindings", code, root)
	}

	for name, spec := range adds {
		field := flatcField{Name: name, Slot: spec.slot}
		switch spec.kind {
		case "UOffsetT":
			params, results := "", ""
			if sig, ok := accessors[name]; ok {
				params, results = sig[0], sig[1]
			}
			_, hasLength := accessors[name+"Length"]
			switch {
			case hasLength:
				// A vector. Its ELEMENT accessor is what says whether the
				// engine can read it as a BLOB: flatc returns byte/int8 per
				// element for a byte vector and an object/handle for
				// anything else.
				if _, hasBytes := accessors[name+"Bytes"]; hasBytes &&
					(results == "byte" || results == "int8" || results == "uint8") {
					field.Kind = flatcBytes
				} else {
					field.Kind = flatcVector
				}
			case params == "obj *flatbuffers.Table":
				field.Kind = flatcUnionValue
			case strings.HasPrefix(params, "obj *"):
				field.Kind = flatcObject
			case params == "" && results == "[]byte":
				field.Kind = flatcString
			default:
				return nil, fmt.Errorf("%s.%s: uoffset field with accessor (%s) %s that this oracle cannot classify",
					code, name, params, results)
			}
		case "Struct":
			field.Kind = flatcStruct
		default:
			scalar, ok := prependKindToScalar[spec.kind]
			if !ok {
				return nil, fmt.Errorf("%s.%s: unknown flatc prepend kind %q", code, name, spec.kind)
			}
			field.Kind = flatcScalar
			field.Type = scalar
		}
		out.Fields = append(out.Fields, field)
	}
	sort.Slice(out.Fields, func(i, j int) bool { return out.Fields[i].Slot < out.Fields[j].Slot })
	for i, f := range out.Fields {
		if f.Slot != i {
			return nil, fmt.Errorf("%s: field %s sits at slot %d, expected a dense slot %d — the oracle missed a field",
				code, f.Name, f.Slot, i)
		}
	}
	if len(out.Fields) != out.Slots {
		return nil, fmt.Errorf("%s: %sStart declares %d slots but the oracle recovered %d fields",
			code, root, out.Slots, len(out.Fields))
	}
	return out, nil
}

// loadFlatcOracle parses every vendored binding the routed catalog needs.
func loadFlatcOracle(t *testing.T, bindings []enginecatalog.Binding) map[string]*flatcRoot {
	t.Helper()
	roots := make(map[string]*flatcRoot, len(bindings))
	for _, binding := range bindings {
		if _, unvendored := engineFlatcUnvendoredStandards[binding.Schema]; unvendored {
			// THE GAP MUST STILL BE REAL. If the package appeared, the entry
			// is stale and the standard must be checked like every other.
			if _, err := os.Stat(filepath.Join(flatcBindingDir, binding.Table)); err == nil {
				t.Errorf("%s is listed as unvendored but %s/%s exists: drop the entry so the oracle checks it",
					binding.Schema, flatcBindingDir, binding.Table)
			}
			continue
		}
		root, err := loadFlatcRoot(binding.Table)
		if err != nil {
			t.Fatalf("flatc oracle: %v", err)
		}
		roots[binding.Schema] = root
	}
	return roots
}

// ==========================================================================
// THE PROJECTION RULE, RE-DERIVED FROM FLATC
// ==========================================================================

// flatcMutation names an edit to the generator's projection rule. The
// unmutated form must reproduce the committed catalog EXACTLY (that is what
// TestGeneratedCatalogMatchesTheVendoredFlatcBindings asserts), which is what
// makes mutating this function equivalent to mutating the generator.
type flatcMutation string

const (
	// flatcMutationNone is enginecatalog.Build's rule: emit the leading run
	// of representable fields in declaration order and STOP at the first
	// field the engine cannot read.
	flatcMutationNone flatcMutation = ""
	// flatcMutationContinue is `if !ok { continue }` in place of
	// `if !ok { break }` (enginecatalog.Build's projection loop): skip a
	// non-representable field and keep emitting. Every column after the skip
	// then reads its NEIGHBOUR's slot.
	flatcMutationContinue flatcMutation = "continue-past-unprojectable"
	// flatcMutationNoTerminal drops Column.Terminal on a union
	// discriminator, so the projection continues past a union — onto the
	// value-offset slot flatc gives the union's second half.
	flatcMutationNoTerminal flatcMutation = "drop-union-terminal"
)

// projectFromFlatc applies the projection rule to a flatc-derived root.
func projectFromFlatc(root *flatcRoot, mutation flatcMutation) []enginecatalog.Column {
	var cols []enginecatalog.Column
	for _, f := range root.Fields {
		if !f.Kind.representable() {
			switch mutation {
			case flatcMutationContinue:
				continue
			case flatcMutationNoTerminal:
				// Only the union's value slot is skipped: the generator
				// without Terminal still breaks on a table/vector/struct.
				if f.Kind == flatcUnionValue {
					continue
				}
				return cols
			default:
				return cols
			}
		}
		col := enginecatalog.Column{Name: f.Name}
		switch f.Kind {
		case flatcString:
			col.Type = "string"
		case flatcBytes:
			col.Type = "[ubyte]"
		default:
			col.Type = engineIDLTypeFor(f.Type)
		}
		cols = append(cols, col)
		// A union discriminator ends the run: the next slot is the union's
		// value offset. Terminal is the generator's name for that stop.
		if mutation != flatcMutationNoTerminal && f.Kind == flatcScalar &&
			f.Slot+1 < len(root.Fields) && root.Fields[f.Slot+1].Kind == flatcUnionValue {
			break
		}
	}
	return cols
}

// flatcColumnMismatch compares one generated binding with the flatc oracle
// and returns every disagreement, in engine column order. It is the
// STRUCTURAL half of the guard; the value half runs the engine.
func flatcColumnMismatch(binding enginecatalog.Binding, root *flatcRoot) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if binding.Root != root.Root {
		add("%s: catalog root_type %q, flatc root %q", binding.Schema, binding.Root, root.Root)
	}
	if binding.FileID != root.FileID {
		add("%s: catalog file id %q, flatc identifier %q", binding.Schema, binding.FileID, root.FileID)
	}
	junk := len(binding.Columns) == 1 && binding.Columns[0].Junk
	if junk {
		if len(root.Fields) == 0 {
			add("%s: flatc reports no fields at all", binding.Schema)
			return problems
		}
		if root.Fields[0].Kind.representable() {
			add("%s: falls back to a junk column, but flatc says slot 0 (%s) is a readable %s",
				binding.Schema, root.Fields[0].Name, root.Fields[0].Kind)
		}
		if binding.Columns[0].Name != root.Fields[0].Name {
			add("%s: junk column is named %q, flatc names slot 0 %q",
				binding.Schema, binding.Columns[0].Name, root.Fields[0].Name)
		}
		// THE FALLBACK MUST BE A FIXED-WIDTH READ. Slot 0 holds an offset to
		// something the vtab cannot decode; a string/blob column would hand
		// the bytes it lands on to the PUBLIC JSON writer verbatim.
		if got := canonicalEngineType(binding.Columns[0].Type); got != "u8" {
			add("%s: junk column type %q (%s) is not the fixed-width fallback",
				binding.Schema, binding.Columns[0].Type, got)
		}
		return problems
	}

	for i, col := range binding.Columns {
		if i >= len(root.Fields) {
			add("%s: column %d %q has no field at slot %d in flatc's table (%d fields)",
				binding.Schema, i, col.Name, i, len(root.Fields))
			continue
		}
		f := root.Fields[i]
		if col.Name != f.Name {
			add("%s: column %d is %q but flatc puts %q at slot %d — the column reads its NEIGHBOUR's value",
				binding.Schema, i, col.Name, f.Name, i)
		}
		want := ""
		switch f.Kind {
		case flatcScalar:
			want = f.Type
		case flatcString:
			want = "str"
		case flatcBytes:
			want = "bytes"
		default:
			add("%s: column %d %q projects slot %d, which flatc says is a %s and the engine cannot read",
				binding.Schema, i, col.Name, i, f.Kind)
			continue
		}
		if got := canonicalEngineType(col.Type); got != want {
			add("%s.%s: column type %q (%s), flatc says %s", binding.Schema, col.Name, col.Type, got, want)
		}
	}

	// THE STOP POINT. Truncating a trailing run is safe; stopping EARLY
	// silently drops columns, and stopping LATE is the neighbour-slot bug.
	if n := len(binding.Columns); n < len(root.Fields) {
		if next := root.Fields[n]; next.Kind.representable() {
			add("%s: projection stops at %d columns, but flatc says slot %d (%s) is a readable %s — the run stopped early",
				binding.Schema, n, n, next.Name, next.Kind)
		}
	}
	return problems
}

// TestGeneratedCatalogMatchesTheVendoredFlatcBindings is the independent
// structural oracle: every projected column's NAME, TYPE and SLOT, plus the
// exact stop point, checked against flatc's own output rather than against
// the parser that produced them.
func TestGeneratedCatalogMatchesTheVendoredFlatcBindings(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	roots := loadFlatcOracle(t, catalog.Bindings)
	if len(roots) < len(catalog.Bindings)-len(engineFlatcUnvendoredStandards) {
		t.Fatalf("oracle covers %d of %d generated standards", len(roots), len(catalog.Bindings))
	}

	checked := 0
	for _, binding := range catalog.Bindings {
		root, ok := roots[binding.Schema]
		if !ok {
			continue
		}
		for _, problem := range flatcColumnMismatch(binding, root) {
			t.Error(problem)
		}
		// THE RULE ITSELF, not just its output: re-deriving the projection
		// from flatc must reproduce the committed columns exactly. This is
		// what makes projectFromFlatc a faithful stand-in for the generator
		// in the mutation test below.
		if !binding.Columns[0].Junk {
			want := projectFromFlatc(root, flatcMutationNone)
			if len(want) != len(binding.Columns) {
				t.Errorf("%s: flatc-derived projection has %d columns, catalog has %d",
					binding.Schema, len(want), len(binding.Columns))
			} else {
				for i := range want {
					if want[i].Name != binding.Columns[i].Name ||
						canonicalEngineType(want[i].Type) != canonicalEngineType(binding.Columns[i].Type) {
						t.Errorf("%s: flatc-derived column %d is %s:%s, catalog has %s:%s",
							binding.Schema, i, want[i].Name, want[i].Type,
							binding.Columns[i].Name, binding.Columns[i].Type)
					}
				}
			}
		}
		checked++
	}
	if checked != len(catalog.Bindings)-len(engineFlatcUnvendoredStandards) {
		t.Fatalf("checked %d standards against flatc, want %d",
			checked, len(catalog.Bindings)-len(engineFlatcUnvendoredStandards))
	}
	t.Logf("flatc oracle agreed on %d standards (%d unvendored)", checked, len(engineFlatcUnvendoredStandards))
}

// TestUnprojectableFallbackSetMatchesFlatc pins the OTHER direction of the
// fallback: the standards that fall back are exactly the standards whose slot
// 0 flatc says is unreadable. Nothing may fall back "for safety", and no
// standard with an unreadable slot 0 may escape the >=1-column invariant.
func TestUnprojectableFallbackSetMatchesFlatc(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	roots := loadFlatcOracle(t, catalog.Bindings)
	for _, binding := range catalog.Bindings {
		root, ok := roots[binding.Schema]
		if !ok {
			continue
		}
		fallback := len(binding.Columns) == 1 && binding.Columns[0].Junk
		unreadable := len(root.Fields) > 0 && !root.Fields[0].Kind.representable()
		if fallback != unreadable {
			t.Errorf("%s: fallback=%v but flatc says slot 0 (%s) unreadable=%v",
				binding.Schema, fallback, root.Fields[0].Name, unreadable)
		}
		if fallback {
			if _, declared := engineUnprojectableFirstFields[binding.Schema]; !declared {
				t.Errorf("%s falls back but is not declared in engineUnprojectableFirstFields", binding.Schema)
			}
		}
	}
}

// ==========================================================================
// THE VALUE GUARD, WRITTEN BY THE ORACLE AND READ BY THE ENGINE
// ==========================================================================

// flatcSyntheticRecord builds ONE root FlatBuffer for a standard using
// FLATC's slot numbers and field types — the same arithmetic
// `<Root>Add<FIELD>` performs, dispatched by table instead of by name so all
// 221 vendored standards are covered rather than the handful a test could
// hand-write.
//
// Two properties make the read-back an actual assertion:
//
//   - every projected slot gets a DISTINCT value, so a column that reads its
//     neighbour's slot returns the wrong one rather than a coincidence;
//   - the slots AFTER the projected run are POPULATED too (a real sub-table,
//     a real vector), so a projection that ran one column too far reads live
//     bytes instead of a conveniently absent field.
func flatcSyntheticRecord(root *flatcRoot, projected int) (want []any, buf []byte) {
	b := flatbuffers.NewBuilder(4096)

	// Phase 1: every nested object must exist before the root table starts.
	// EVERY slot the builder can write is written, projected or not — a
	// column that overran its run must land on live bytes, not on a
	// conveniently absent field.
	want = make([]any, projected)
	offsets := make(map[int]flatbuffers.UOffsetT, len(root.Fields))
	for _, f := range root.Fields {
		switch f.Kind {
		case flatcString:
			v := fmt.Sprintf("%s-slot-%d", root.Code, f.Slot)
			if f.Slot < projected {
				want[f.Slot] = v
			}
			offsets[f.Slot] = b.CreateString(v)
		case flatcBytes:
			v := []byte{byte(f.Slot%251 + 1), 0xA5, byte((f.Slot+7)%251 + 1)}
			if f.Slot < projected {
				want[f.Slot] = v
			}
			offsets[f.Slot] = b.CreateByteVector(v)
		case flatcObject, flatcUnionValue:
			offsets[f.Slot] = flatcNestedTable(b, fmt.Sprintf("%s-%d", root.Code, f.Slot))
		case flatcVector:
			offsets[f.Slot] = flatcNestedVector(b, fmt.Sprintf("%s-%d", root.Code, f.Slot))
		}
	}

	b.StartObject(root.Slots)
	for _, f := range root.Fields {
		if off, ok := offsets[f.Slot]; ok {
			b.PrependUOffsetTSlot(f.Slot, off, 0)
			continue
		}
		if f.Kind != flatcScalar {
			// Only an inline STRUCT reaches here: writing one needs the
			// struct's own size and alignment, which this oracle does not
			// parse. No embedded standard declares a struct in its root
			// table today (TestNoRootTableDeclaresAnInlineStruct pins that),
			// and leaving a slot unset cannot shift any other slot.
			continue
		}
		v := flatcScalarValue(b, f)
		if f.Slot < projected {
			want[f.Slot] = v
		}
	}
	root2 := b.EndObject()
	b.FinishWithFileIdentifier(root2, []byte(root.FileID))
	return want, b.FinishedBytes()
}

// flatcScalarValue writes a distinct value of the field's own type into its
// slot and returns what the engine must read back.
func flatcScalarValue(b *flatbuffers.Builder, f flatcField) any {
	i := f.Slot
	switch f.Type {
	case "bool":
		b.PrependBoolSlot(i, true, false)
		return true
	case "i8":
		v := int8(i%100 + 1)
		b.PrependInt8Slot(i, v, 0)
		return int64(v)
	case "u8":
		v := uint8(i%200 + 1)
		b.PrependByteSlot(i, v, 0)
		return int64(v)
	case "i16":
		v := int16((i + 1) * 7)
		b.PrependInt16Slot(i, v, 0)
		return int64(v)
	case "u16":
		v := uint16((i + 1) * 9)
		b.PrependUint16Slot(i, v, 0)
		return int64(v)
	case "i32":
		v := int32((i+1)*1000 + 7)
		b.PrependInt32Slot(i, v, 0)
		return int64(v)
	case "u32":
		v := uint32((i+1)*1000 + 11)
		b.PrependUint32Slot(i, v, 0)
		return int64(v)
	case "i64":
		v := int64((i+1)*1_000_003 + 13)
		b.PrependInt64Slot(i, v, 0)
		return v
	case "u64":
		v := uint64((i+1)*1_000_003 + 17)
		b.PrependUint64Slot(i, v, 0)
		return int64(v)
	case "f32":
		// Exactly representable in float32, so widening to float64 on the
		// read side is lossless and the comparison is exact.
		v := float32(i+1) + 0.5
		b.PrependFloat32Slot(i, v, 0)
		return float64(v)
	case "f64":
		v := float64(i+1) + 0.25
		b.PrependFloat64Slot(i, v, 0)
		return v
	}
	return nil
}

// flatcNestedTable writes a sub-table carrying a string and two doubles —
// the shape of a real SDS sub-record, and one whose bytes are NOT valid
// UTF-8, which is what makes the junk-column guard meaningful.
func flatcNestedTable(b *flatbuffers.Builder, tag string) flatbuffers.UOffsetT {
	s := b.CreateString(tag)
	b.StartObject(3)
	b.PrependUOffsetTSlot(0, s, 0)
	b.PrependFloat64Slot(1, 12.3456789, 0)
	b.PrependFloat64Slot(2, -98.7654321, 0)
	return b.EndObject()
}

// flatcNestedVector writes a one-element vector of offsets to such a table.
func flatcNestedVector(b *flatbuffers.Builder, tag string) flatbuffers.UOffsetT {
	sub := flatcNestedTable(b, tag)
	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(sub)
	return b.EndVector(1)
}

// engineCellEquals compares an expected Go value with what the engine
// returned for that column, tolerating the widening the vtab does on
// integers.
func engineCellEquals(want, got any) bool {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		return ok && g == w
	case []byte:
		switch g := got.(type) {
		case []byte:
			return string(g) == string(w)
		case string:
			return g == string(w)
		}
		return false
	case bool:
		switch g := got.(type) {
		case bool:
			return g == w
		case int64:
			return (g != 0) == w
		case float64:
			return (g != 0) == w
		}
		return false
	case int64:
		switch g := got.(type) {
		case int64:
			return g == w
		case float64:
			return g == float64(w)
		}
		return false
	case float64:
		switch g := got.(type) {
		case float64:
			return g == w
		case int64:
			return float64(g) == w
		}
		return false
	}
	return false
}

// flatcValueMismatch ingests one oracle-built record into db and reports
// every column that did not read its own field's value back.
func flatcValueMismatch(db *flatsqlrt.Database, table string, cols []enginecatalog.Column, root *flatcRoot) []string {
	want, buf := flatcSyntheticRecord(root, len(cols))
	if _, err := db.IngestOneWithSource(buf, engineDefaultSource); err != nil {
		return []string{fmt.Sprintf("%s: ingest: %v", table, err)}
	}
	quoted := make([]string, 0, len(cols))
	for _, col := range cols {
		quoted = append(quoted, quoteEngineRelation(col.Name))
	}
	res, err := db.Query("SELECT " + strings.Join(quoted, ", ") + " FROM " + quoteEngineRelation(table))
	if err != nil {
		return []string{fmt.Sprintf("%s: query: %v", table, err)}
	}
	if len(res.Rows) != 1 {
		return []string{fmt.Sprintf("%s: %d rows after one ingest, want 1", table, len(res.Rows))}
	}
	row := res.Rows[0]
	if len(row) != len(cols) {
		return []string{fmt.Sprintf("%s: %d cells for %d columns", table, len(row), len(cols))}
	}
	var problems []string
	for i, col := range cols {
		if !engineCellEquals(want[i], row[i]) {
			problems = append(problems, fmt.Sprintf(
				"%s.%s (slot %d, %s) = %#v, want %#v — the column read the wrong vtable slot",
				table, col.Name, i, col.Type, row[i], want[i]))
		}
	}
	return problems
}

// TestNoRootTableDeclaresAnInlineStruct pins the one field kind the oracle
// cannot WRITE. An inline struct occupies its slot directly, so building one
// needs the struct's size and alignment; no embedded standard declares one in
// a routed root table today, and if that changes the synthetic records would
// silently leave that slot empty and weaken every value assertion after it.
func TestNoRootTableDeclaresAnInlineStruct(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	for _, root := range loadFlatcOracle(t, catalog.Bindings) {
		for _, f := range root.Fields {
			if f.Kind == flatcStruct {
				t.Errorf("%s.%s (slot %d) is an inline struct: teach flatcSyntheticRecord to write it before this lands",
					root.Code, f.Name, f.Slot)
			}
		}
	}
}

// TestGeneratedColumnsMatchARecordBuiltWithTheFlatcBuilder anchors the
// oracle-laid-out records to a record written the way production writes one:
// through the vendored flatc BUILDER, by field NAME, with values a human
// chose. If the generic construction above ever drifted from what
// `<Root>Add<FIELD>` does, this fails and the whole value guard is suspect.
func TestGeneratedColumnsMatchARecordBuiltWithTheFlatcBuilder(t *testing.T) {
	const (
		jobID      = "cellular-worldwide"
		providerID = "mls"
		ingestorID = "space-data-network-02"
		sequence   = uint64(7)
	)
	b := flatbuffers.NewBuilder(512)
	sourceURL := b.CreateString("https://example.invalid/bulk/catalog.csv")
	IRM.IRMSourceStart(b)
	IRM.IRMSourceAddSOURCE_URL(b, sourceURL)
	source := IRM.IRMSourceEnd(b)

	job := b.CreateString(jobID)
	provider := b.CreateString(providerID)
	ingestor := b.CreateString(ingestorID)
	IRM.IRMStart(b)
	IRM.IRMAddJOB_ID(b, job)
	IRM.IRMAddSEQUENCE(b, sequence)
	IRM.IRMAddPROVIDER_ID(b, provider)
	IRM.IRMAddINGESTOR_ID(b, ingestor)
	IRM.IRMAddSOURCE(b, source) // the first field the projection cannot read
	IRM.FinishIRMBuffer(b, IRM.IRMEnd(b))
	record := b.FinishedBytes()

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()
	tags := SourceTags{ProviderID: providerID, SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	if _, err := store.StoreWithSourceTags("IRM.fbs", record, "peer-flatc", nil, tags); err != nil {
		t.Fatalf("store $IRM: %v", err)
	}

	binding, ok := engineRoutedSchemaFor("IRM.fbs")
	if !ok {
		t.Fatal("IRM.fbs is not routed")
	}
	res, err := store.engineDB.Query(
		`SELECT JOB_ID, SEQUENCE, PROVIDER_ID, INGESTOR_ID FROM ` + quoteEngineRelation(binding.Table))
	if err != nil {
		t.Fatalf("read projected columns: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(res.Rows))
	}
	for i, want := range []any{jobID, int64(sequence), providerID, ingestorID} {
		if !engineCellEquals(want, res.Rows[0][i]) {
			t.Errorf("column %d = %#v, want %#v — the column does not read the field flatc wrote", i, res.Rows[0][i], want)
		}
	}
}
