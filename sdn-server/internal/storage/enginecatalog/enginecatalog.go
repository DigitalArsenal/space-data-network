// Package enginecatalog derives the FlatSQL engine table graph for EVERY
// embedded SDS standard from the embedded .fbs IDLs.
//
// OWNER DIRECTIVE (2026-08-25): "The routing of $IRM just needs to be another
// one of the standards ingested like all the others in the standards engine."
// There is therefore no per-standard branch anywhere in the store: a standard
// is engine-routed BECAUSE it is one of the standards the node embeds, and the
// only thing this package decides is WHICH COLUMNS of that standard's root
// table can be projected into SQL safely.
//
// THE PROJECTION RULE, and why it is a LEADING RUN rather than a filter.
// FlatBuffers vtable slots are POSITIONAL: slot i belongs to the i-th field in
// declaration order. The engine's schema parser (flatsql
// cpp/src/schema_parser.cpp) assigns `fieldId = position in the emitted table`,
// so an emitted column reads whatever slot its own position names. Skipping a
// field in the middle would therefore shift every following column onto its
// NEIGHBOUR's slot and decode a plausible wrong value — the worst possible
// failure, because it is silent. Truncating a TRAILING run cannot shift
// anything, so the projection emits fields in declaration order and STOPS at
// the first one whose type the engine cannot represent (a nested table, a
// struct, a union, or a vector of anything but bytes).
//
// Nothing is lost by stopping: `_data` on every row is the WHOLE record
// FlatBuffer, so a consumer that decodes the frame with the real binding still
// sees every field. The columns exist for predicates and ordering, not for
// carriage — which is exactly how the pinned $OMM and $TBS tables already
// treat them.
//
// THE >=1 COLUMN INVARIANT IS FATAL, NOT COSMETIC. A table with no columns
// makes the engine emit `CREATE TABLE x(, "_source" TEXT, ...)`
// (cpp/src/sqlite_vtab.cpp) and `SELECT , "_source", ... FROM ...`
// (cpp/src/sqlite_engine.cpp) — both malformed — from the THROWING
// updateSQLiteTable / createUnifiedView. The embedded engine is the
// -fignore-exceptions build where a throw lowers to `unreachable`, so a
// zero-column table poisons the runtime at boot. When a root table's very
// first field is already non-representable, the projection therefore emits
// that first field as a `string` column: slot 0 is still slot 0, the value is
// junk (documented), memory safety is the engine's bounds-checked reader, and
// the table is legal.
package enginecatalog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Column is one projected SQL column: the SDS field name verbatim (SDS JSON /
// column capitalization is never re-spelled) and the engine IDL type emitted
// for it.
type Column struct {
	Name string
	Type string
	// Junk marks the >=1-column-invariant fallback: the field's real type is
	// not representable and the column reads its slot as a string.
	Junk bool
	// Terminal marks a column that is the LAST one the projection may emit
	// (a union discriminator, whose value offset occupies the next slot).
	Terminal bool
}

// Binding is one routed standard.
type Binding struct {
	Schema  string // "IRM.fbs"
	Table   string // "IRM" — ALWAYS the standard code, never the root_type name
	Root    string // root_type name as declared ("PeerGraphSnapshot" for PGR)
	FileID  string // "$IRM"
	Columns []Column
}

// Skip is an embedded standard that is NOT routed, with the reason.
type Skip struct {
	Schema string
	Reason string
}

// Catalog is the generation result.
type Catalog struct {
	Bindings []Binding
	Skipped  []Skip
}

// SkipNoFileIdentifier is the reason string for a standard whose IDL declares
// no file_identifier. Such a standard CANNOT be routed: ingest routes on the
// 4 header bytes and FlatSQLDatabase::registerFileId is the only thing that
// materializes a table, so there is nothing to register. This is an IDL gap,
// not a policy carve-out — the moment the standard declares an identifier it
// routes automatically with no code change here.
const SkipNoFileIdentifier = "declares no file_identifier"

// SkipPinned marks the standards whose table graph is pinned elsewhere as a
// cross-host contract (engineRecordSchema / engineTBSTableGraph) and must not
// be re-emitted.
const SkipPinned = "table graph is pinned as a cross-host contract"

var (
	reInclude   = regexp.MustCompile(`(?m)^\s*include\s+"([^"]+)"\s*;`)
	reEnum      = regexp.MustCompile(`(?m)^\s*enum\s+(\w+)\s*:\s*(\w+)`)
	reStruct    = regexp.MustCompile(`(?m)^\s*struct\s+(\w+)`)
	reUnion     = regexp.MustCompile(`(?m)^\s*union\s+(\w+)`)
	reRootType  = regexp.MustCompile(`(?m)^\s*root_type\s+(\w+)\s*;`)
	reFileIdent = regexp.MustCompile(`(?m)^\s*file_identifier\s*"([^"]{4})"\s*;`)
	reField     = regexp.MustCompile(`^([A-Za-z_]\w*)\s*:\s*(.+)$`)
	reAttrs     = regexp.MustCompile(`\(([^)]*)\)`)
)

// scalarTypes are the FlatBuffers scalars the engine's schema parser maps to a
// real SQL column type (schema_parser.cpp idlTypeToValueType). An unknown type
// silently DEFAULTS TO STRING there, which is precisely why this package
// classifies types itself instead of handing the parser whatever the IDL says.
var scalarTypes = map[string]bool{
	"bool": true, "byte": true, "ubyte": true, "short": true, "ushort": true,
	"int": true, "uint": true, "long": true, "ulong": true,
	"float": true, "double": true,
	"int8": true, "uint8": true, "int16": true, "uint16": true,
	"int32": true, "uint32": true, "int64": true, "uint64": true,
	"float32": true, "float64": true,
}

// byteVectorTypes are the vector element types the engine reads as a BLOB.
var byteVectorTypes = map[string]bool{"ubyte": true, "uint8": true, "byte": true, "int8": true}

// engineMetaColumns are the columns the vtab adds itself; a projected field
// may never collide with one.
var engineMetaColumns = map[string]bool{"_source": true, "_rowid": true, "_offset": true, "_data": true}

type schemaDoc struct {
	enums   map[string]string // name -> underlying scalar
	structs map[string]bool
	unions  map[string]bool
	tables  map[string]string // name -> body
	root    string
	fileID  string
}

// stripComments removes `//`-to-end-of-line comments (including `///` doc
// comments) without touching text inside a string literal.
func stripComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for _, line := range strings.Split(src, "\n") {
		inString := false
		cut := -1
		for i := 0; i < len(line); i++ {
			switch {
			case line[i] == '"' && (i == 0 || line[i-1] != '\\'):
				inString = !inString
			case !inString && line[i] == '/' && i+1 < len(line) && line[i+1] == '/':
				cut = i
			}
			if cut >= 0 {
				break
			}
		}
		if cut >= 0 {
			line = line[:cut]
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// parseTables extracts every `table Name { body }` block. FlatBuffers table
// bodies never nest braces, so a scan to the matching '}' is exact.
func parseTables(src string) map[string]string {
	tables := map[string]string{}
	re := regexp.MustCompile(`(?m)^\s*table\s+(\w+)\s*\{`)
	for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		open := strings.IndexByte(src[loc[1]-1:], '{')
		if open < 0 {
			continue
		}
		start := loc[1] - 1 + open + 1
		end := strings.IndexByte(src[start:], '}')
		if end < 0 {
			continue
		}
		tables[name] = src[start : start+end]
	}
	return tables
}

// parseSchema reads one IDL and FOLLOWS ITS INCLUDES for type declarations.
//
// Includes are resolved because the alternative is silently WORSE, not safer:
// an unresolved type name is non-representable, so the projection would stop
// at the first field typed with an enum that lives in a shared IDL and drop
// every representable field after it. 49 of the embedded standards include
// another file. Only ENUM and UNION declarations are merged — a table or
// struct from an include stops the run either way, so nothing else can change
// the slot arithmetic.
func parseSchema(dir, name string) (*schemaDoc, error) {
	doc := &schemaDoc{
		enums:   map[string]string{},
		structs: map[string]bool{},
		unions:  map[string]bool{},
		tables:  map[string]string{},
	}
	seen := map[string]bool{}
	var load func(file string, root bool) error
	load = func(file string, root bool) error {
		if seen[file] {
			return nil
		}
		seen[file] = true
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			if root {
				return err
			}
			// A missing include is a schema-set gap, not a routing decision:
			// the types it would have declared stay unknown and therefore
			// stop the run, which is the safe direction.
			return nil
		}
		src := stripComments(string(raw))
		for _, m := range reEnum.FindAllStringSubmatch(src, -1) {
			if _, ok := doc.enums[m[1]]; !ok || root {
				doc.enums[m[1]] = strings.TrimSpace(m[2])
			}
		}
		for _, m := range reStruct.FindAllStringSubmatch(src, -1) {
			doc.structs[m[1]] = true
		}
		for _, m := range reUnion.FindAllStringSubmatch(src, -1) {
			doc.unions[m[1]] = true
		}
		if root {
			doc.tables = parseTables(src)
			if m := reRootType.FindStringSubmatch(src); m != nil {
				doc.root = m[1]
			}
			if m := reFileIdent.FindStringSubmatch(src); m != nil {
				doc.fileID = m[1]
			}
		}
		for _, m := range reInclude.FindAllStringSubmatch(src, -1) {
			if err := load(filepath.Base(m[1]), false); err != nil {
				return err
			}
		}
		return nil
	}
	if err := load(name, true); err != nil {
		return nil, err
	}
	return doc, nil
}

type field struct {
	name  string
	typ   string
	hasID bool
}

// parseFields returns the root table's fields IN DECLARATION ORDER.
func (d *schemaDoc) parseFields(body string) []field {
	var fields []field
	for _, raw := range strings.Split(body, ";") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		hasID := false
		if m := reAttrs.FindStringSubmatch(entry); m != nil {
			attrs := strings.ToLower(m[1])
			for _, a := range strings.Split(attrs, ",") {
				if strings.HasPrefix(strings.TrimSpace(a), "id") {
					hasID = true
				}
			}
			entry = strings.TrimSpace(reAttrs.ReplaceAllString(entry, ""))
		}
		m := reField.FindStringSubmatch(entry)
		if m == nil {
			continue
		}
		typ := m[2]
		if eq := strings.IndexByte(typ, '='); eq >= 0 {
			typ = typ[:eq]
		}
		typ = strings.Join(strings.Fields(typ), "")
		fields = append(fields, field{name: m[1], typ: typ, hasID: hasID})
	}
	return fields
}

// project applies the leading-run rule to one field. ok=false means STOP.
//
// A UNION is the one field kind that ends the run WITH a column: flatc gives a
// union TWO slots — the ubyte discriminator at slot i and the value offset at
// slot i+1 — so the discriminator is exactly representable and the value is
// not. Emitting `<NAME>_type:ubyte` and stopping is therefore honest, and it
// is why nothing is ever emitted PAST a union: continuing would land the next
// column on the value-offset slot.
func (d *schemaDoc) project(f field) (Column, bool) {
	typ := f.typ
	if d.unions[typ] {
		return Column{Name: f.name + "_type", Type: "ubyte", Terminal: true}, true
	}
	if strings.HasPrefix(typ, "[") && strings.HasSuffix(typ, "]") {
		inner := typ[1 : len(typ)-1]
		if byteVectorTypes[inner] {
			return Column{Name: f.name, Type: "[ubyte]"}, true
		}
		return Column{}, false
	}
	if scalarTypes[typ] {
		return Column{Name: f.name, Type: typ}, true
	}
	if typ == "string" {
		return Column{Name: f.name, Type: "string"}, true
	}
	if base, ok := d.enums[typ]; ok {
		// Enums are resolved to their UNDERLYING scalar on purpose. The engine
		// parser defaults an unknown type name to STRING, so leaving the enum
		// name in would make it read a one-byte ordinal as a string offset.
		if scalarTypes[base] {
			return Column{Name: f.name, Type: base}, true
		}
		return Column{}, false
	}
	return Column{}, false
}

// Build derives the catalog from the .fbs files in schemaDir. `pinned` names
// the standards whose table graph lives elsewhere as a cross-host contract
// (OMM, TBS) and is reported as Skipped rather than emitted.
func Build(schemaDir string, pinned map[string]bool) (*Catalog, error) {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".fbs") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	cat := &Catalog{}
	byFileID := map[string]string{}
	for _, name := range names {
		code := strings.TrimSuffix(name, ".fbs")
		if pinned[name] {
			cat.Skipped = append(cat.Skipped, Skip{Schema: name, Reason: SkipPinned})
			continue
		}
		doc, err := parseSchema(schemaDir, name)
		if err != nil {
			return nil, err
		}
		if doc.fileID == "" {
			cat.Skipped = append(cat.Skipped, Skip{Schema: name, Reason: SkipNoFileIdentifier})
			continue
		}
		if prev, dup := byFileID[doc.fileID]; dup {
			return nil, fmt.Errorf("file identifier %q is declared by both %s and %s: routing it would send one standard's records into the other's table", doc.fileID, prev, name)
		}
		byFileID[doc.fileID] = name
		if doc.root == "" {
			return nil, fmt.Errorf("%s declares a file_identifier but no root_type", name)
		}
		body, ok := doc.tables[doc.root]
		if !ok {
			return nil, fmt.Errorf("%s: root_type %s is not a table in the same IDL", name, doc.root)
		}
		fields := doc.parseFields(body)
		if len(fields) == 0 {
			return nil, fmt.Errorf("%s: root table %s declares no fields, so no legal engine table can be emitted", name, doc.root)
		}
		for _, f := range fields {
			if f.hasID {
				return nil, fmt.Errorf("%s: field %s carries an explicit (id:) attribute, so vtable slots are NOT positional and this projection rule does not hold", name, f.name)
			}
		}

		var cols []Column
		for _, f := range fields {
			col, ok := doc.project(f)
			if !ok {
				break
			}
			if engineMetaColumns[col.Name] {
				return nil, fmt.Errorf("%s: field %s collides with an engine meta column", name, col.Name)
			}
			cols = append(cols, col)
			if col.Terminal {
				break
			}
		}
		if len(cols) == 0 {
			// >=1 COLUMN INVARIANT. Slot 0 already holds something the engine
			// cannot represent, and a zero-column table emits malformed DDL
			// that TRAPS the no-exceptions engine at boot. Emit slot 0 as a
			// string: the read is fully bounds-checked
			// (flatsql cpp/src/database.cpp readStringField), so the worst
			// case is a junk or NULL cell — never an out-of-bounds read.
			cols = []Column{{Name: fields[0].name, Type: "string", Junk: true}}
		}
		cat.Bindings = append(cat.Bindings, Binding{
			Schema:  name,
			Table:   code,
			Root:    doc.root,
			FileID:  doc.fileID,
			Columns: cols,
		})
	}
	return cat, nil
}

// SchemaText renders the engine IDL fragment: one `table <CODE> { ... }` per
// routed standard, in schema order. No comments are emitted INSIDE a table
// body — the engine's schema parser is a regex over the raw text and would
// read a commented line as a field.
func (c *Catalog) SchemaText() string {
	var b strings.Builder
	for _, bind := range c.Bindings {
		b.WriteString("  table ")
		b.WriteString(bind.Table)
		b.WriteString(" {\n")
		for _, col := range bind.Columns {
			b.WriteString("    ")
			b.WriteString(col.Name)
			b.WriteString(":")
			b.WriteString(col.Type)
			b.WriteString(";\n")
		}
		b.WriteString("  }\n")
	}
	return b.String()
}

// Render produces the exact bytes of the generated Go file in the `storage`
// package. Deterministic: schema order is sorted and every map literal is
// emitted in sorted key order, so regenerating an unchanged tree is a no-op
// and the golden test is a byte comparison.
func (c *Catalog) Render() []byte {
	var b strings.Builder
	b.WriteString(generatedHeader)
	b.WriteString("\n")
	b.WriteString(catalogGraphDoc)
	b.WriteString("const engineStandardCatalogGraph = `\n")
	b.WriteString(c.SchemaText())
	b.WriteString("`\n\n")

	b.WriteString(bindingsDoc)
	b.WriteString("var engineGeneratedStandardBindings = map[string]engineRoutedSchema{\n")
	width := 0
	for _, bind := range c.Bindings {
		if n := len(bind.Schema) + 3; n > width {
			width = n
		}
	}
	for _, bind := range c.Bindings {
		key := fmt.Sprintf("%q:", bind.Schema)
		b.WriteString(fmt.Sprintf("\t%-*s {Table: %q, FileID: %q},\n", width, key, bind.Table, bind.FileID))
	}
	b.WriteString("}\n\n")

	b.WriteString(unroutableDoc)
	b.WriteString("var engineUnroutableSchemas = map[string]string{\n")
	unroutable := make([]Skip, 0, len(c.Skipped))
	pinned := make([]Skip, 0, len(c.Skipped))
	for _, s := range c.Skipped {
		if s.Reason == SkipPinned {
			pinned = append(pinned, s)
			continue
		}
		unroutable = append(unroutable, s)
	}
	sort.Slice(unroutable, func(i, j int) bool { return unroutable[i].Schema < unroutable[j].Schema })
	for _, s := range unroutable {
		b.WriteString(fmt.Sprintf("\t%q: %q,\n", s.Schema, s.Reason))
	}
	b.WriteString("}\n\n")

	b.WriteString(fallbackDoc)
	b.WriteString("var engineUnprojectableFirstFields = map[string]string{\n")
	for _, bind := range c.Bindings {
		if len(bind.Columns) == 1 && bind.Columns[0].Junk {
			b.WriteString(fmt.Sprintf("\t%q: %q,\n", bind.Schema, bind.Columns[0].Name))
		}
	}
	b.WriteString("}\n\n")

	b.WriteString(pinnedDoc)
	b.WriteString("var enginePinnedSchemas = map[string]bool{\n")
	sort.Slice(pinned, func(i, j int) bool { return pinned[i].Schema < pinned[j].Schema })
	for _, s := range pinned {
		b.WriteString(fmt.Sprintf("\t%q: true,\n", s.Schema))
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

const generatedHeader = `// Code generated by sdn-server/internal/storage/gen; DO NOT EDIT.
// Regenerate with: go generate ./sdn-server/internal/storage/...
//
// OWNER DIRECTIVE (2026-08-25): "The routing of $IRM just needs to be another
// one of the standards ingested like all the others in the standards engine."
// This file is that directive made structural — every embedded SDS standard
// that declares a file_identifier gets an engine table here, so no standard is
// ever routed (or forgotten) by name.

package storage
`

const catalogGraphDoc = `// engineStandardCatalogGraph is the engine table graph for every routed
// standard except the two whose text is pinned as a cross-host contract
// (engineRecordSchema for $OMM, engineTBSTableGraph for $TBS). Columns are the
// LEADING RUN of exactly-representable root-table fields in declaration order
// — see internal/storage/enginecatalog for why a leading run and not a filter.
// Everything past the stop point still travels in ` + "`_data`" + `, which is the
// whole record.
`

const bindingsDoc = `// engineGeneratedStandardBindings binds each generated table to the 4-byte
// FlatBuffer file identifier its standard declares. RegisterFileID is what
// materializes a table in the engine, and it THROWS on a name that is not in
// the schema — so this map and engineStandardCatalogGraph are generated
// together and can never disagree.
`

const unroutableDoc = `// engineUnroutableSchemas records the embedded standards that are NOT routed,
// with the reason. This is a DECLARATION, not a carve-out: each entry names a
// property of the IDL, and the standard routes automatically once the IDL
// stops having it. TestEveryEmbeddedStandardIsRoutedOrDeclaredUnroutable fails
// if an entry goes stale.
`

const fallbackDoc = `// engineUnprojectableFirstFields names the standards whose root table already
// starts with a field the engine cannot represent. The >=1 COLUMN INVARIANT
// forces a column anyway (a zero-column table emits malformed DDL and traps
// the no-exceptions engine), so their single column reads slot 0 as a string:
// bounds-checked and memory-safe, but NOT that field's value. Consumers of
// these standards read ` + "`_data`" + `, which is the whole record. The entry
// disappears by itself if the standard ever declares a representable first
// field.
`

const pinnedDoc = `// enginePinnedSchemas are routed, but their table text lives elsewhere as a
// cross-host contract and must not be regenerated here.
`

// PinnedSchemas are the standards whose engine table text is a CROSS-HOST
// CONTRACT held elsewhere: shared-test-vectors/flatsql-parity.json pins
// engineRecordSchema ($OMM) byte-for-byte and both hosts build their parity
// database from it, and engineTBSTableGraph carries the cellular tile
// contract's point fields. They are routed like every other standard; only
// their TEXT is not generated.
var PinnedSchemas = map[string]bool{
	"OMM.fbs": true,
	"TBS.fbs": true,
}
