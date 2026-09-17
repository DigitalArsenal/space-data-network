package main

// Record synthesis for EVERY embedded SDS standard.
//
// internal/sds ships four concrete builders (OMM, EPM, PNM, CAT), so a
// benchmark that must cover all 236 embedded standards has to synthesise the
// other 232 itself. That synthesis is not "random bytes with a header glued
// on", and the reason is the write path:
//
//   - sds.Validator.VerifyEnvelope admits a buffer only when its root uoffset,
//     the vtable it points at and the root table's inline size all land inside
//     the buffer AND bytes 4..8 carry the schema's declared file_identifier.
//     Junk fails every one of those, which is the point of that function.
//   - The engine places a record by those same four identifier bytes and then
//     reads the LEADING RUN of representable root-table fields as real columns
//     (internal/storage/enginecatalog). A buffer whose slot types disagree with
//     that run is read as garbage: at best the ingest is skipped and the
//     benchmark measures a write path the records never reached, at worst the
//     engine reads a scalar slot as an offset.
//
// So the per-schema layout is derived from the SAME IDL projection the engine's
// own catalog is generated from, and the bulk filler goes in ONE EXTRA vtable
// slot past the projected run — a slot every FlatBuffers reader ignores by
// design, so it can never be mistaken for a projected field.
//
// TWO LIMITS ON THAT INVARIANT, both measured rather than assumed:
//
//   - IT DOES NOT HOLD FOR THE PINNED STANDARDS. This derives layout with
//     enginecatalog.Build(schemaDir, nil), but the store builds its catalog
//     with enginecatalog.PinnedSchemas, and OMM.fbs and TBS.fbs are pinned:
//     their engine table text is a cross-host contract that lives elsewhere,
//     so the projection used here is not necessarily the one the store binds.
//     For those two the filler slot may land inside the store's projected run.
//   - 22 OF THE 236 GET A JUNK COLUMN, not a typed one. enginecatalog has a
//     >=1-column-invariant fallback (Column{Junk: true}) for root tables with
//     nothing projectable, and two more (KMF, VCM) project zero columns. For
//     those the "correctly-typed leading run" is one untyped byte slot.
//
// Neither changes what this benchmark is for — per-record store cost, which the
// record-size sweep shows dominates regardless — but a throughput number is
// only worth what its inputs are, so the inputs are stated exactly.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// minFillerBytes keeps at least a sequence counter in the filler slot.
// EVERY RECORD MUST HAVE UNIQUE BYTES: FlatSQLStore.storeOne dedupes on the
// sha256 CID of the buffer, and a repeat CID takes a far cheaper path
// (mirrorRoutedRecordFromExisting — no insert, no index row, no engine
// ingest). A synthesiser emitting one canned record per schema would measure
// the DEDUPE path and report a throughput the node cannot actually sustain.
const minFillerBytes = 8

// synthesisableTypes is every engine column type schemaTemplate.build can
// write, mirroring enginecatalog's own projection (scalars, string, [ubyte]).
var synthesisableTypes = map[string]bool{
	"string": true, "[ubyte]": true, "bool": true,
	"byte": true, "int8": true, "ubyte": true, "uint8": true,
	"short": true, "int16": true, "ushort": true, "uint16": true,
	"int": true, "int32": true, "uint": true, "uint32": true,
	"long": true, "int64": true, "ulong": true, "uint64": true,
	"float": true, "float32": true, "double": true, "float64": true,
}

// schemaTemplate is one standard's synthesis plan: the identifier the store
// routes on, the typed leading-run columns the engine will read back, and the
// filler slot that makes each record unique and brings it to the target size.
type schemaTemplate struct {
	schema   string // "OMM.fbs"
	code     string // "OMM"
	fileID   string // "$OMM"; empty for the one IDL that declares none (VCM)
	cols     []enginecatalog.Column
	fillSlot int
	fill     int // filler bytes, solved once so records land near the target size

	// scratch buffers, reused per record so the synthesis ceiling this
	// benchmark reports is the FlatBuffers cost and not an allocator's.
	offsets []flatbuffers.UOffsetT
	filler  []byte
	strbuf  []byte
	blob    []byte
}

// buildTemplates plans one record layout per schema.
//
// The identifier comes from the VALIDATOR (the same map VerifyEnvelope checks
// against), never from the catalog: the catalog deliberately skips standards
// it cannot project — the field-sealed one ($KMF) and the one IDL with no
// file_identifier ($VCM) — and those still have to be streamed. The catalog
// supplies layout only, and a schema it skipped falls back to a single filler
// slot, which is structurally valid for any standard because the engine never
// reads a slot its table does not declare.
func buildTemplates(schemaDir string, schemas []string, v *sds.Validator, recordBytes int) ([]*schemaTemplate, error) {
	catalog, err := enginecatalog.Build(schemaDir, nil)
	if err != nil {
		return nil, fmt.Errorf("derive engine column layout from %s: %w", schemaDir, err)
	}
	cols := make(map[string][]enginecatalog.Column, len(catalog.Bindings))
	for _, b := range catalog.Bindings {
		cols[b.Schema] = b.Columns
	}

	builder := flatbuffers.NewBuilder(4096)
	out := make([]*schemaTemplate, 0, len(schemas))
	for _, name := range schemas {
		ident, _ := v.FileIdentifier(name)
		t := &schemaTemplate{
			schema: name,
			code:   strings.TrimSuffix(name, ".fbs"),
			fileID: ident,
			cols:   cols[name],
		}
		// A union discriminator is Terminal because the NEXT vtable slot holds
		// that union's value offset. Writing the filler there would hand the
		// engine a byte vector where a union value belongs, so the filler skips
		// past it and the discriminator stays at its NONE default.
		t.fillSlot = len(t.cols)
		if n := len(t.cols); n > 0 && t.cols[n-1].Terminal {
			t.fillSlot++
		}
		t.offsets = make([]flatbuffers.UOffsetT, len(t.cols))
		t.strbuf = make([]byte, 0, 64)
		t.blob = []byte(t.code + "-blob-column")

		// Reject an unknown column type HERE, not in build(): a type
		// enginecatalog grows later would otherwise surface as a panic a
		// million records into a run, and the answer to it is a code change
		// in this file, not a retry.
		for _, c := range t.cols {
			if !synthesisableTypes[c.Type] {
				return nil, fmt.Errorf("%s column %s has engine type %q, which this synthesiser cannot write", name, c.Name, c.Type)
			}
		}

		// Solve the filler length by measuring, not by predicting: vtable
		// width, string lengths and FlatBuffers' alignment padding all vary
		// per standard, so a formula would be wrong for most of the 236.
		t.fill = minFillerBytes
		t.filler = make([]byte, t.fill)
		probe := t.build(builder, 1)
		if want := recordBytes - len(probe) + t.fill; want > t.fill {
			t.fill = want
		}
		t.filler = make([]byte, t.fill)

		// Admission is proven HERE, once per standard, rather than per record:
		// every record of a standard shares this layout, and a schema whose
		// synthesised buffer the store would refuse must fail the run before
		// any throughput is reported rather than quietly skew it.
		rec := t.build(builder, 1)
		if err := v.VerifyEnvelope(name, rec); err != nil {
			return nil, fmt.Errorf("synthesised %s record is not admissible: %w", name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// build writes one record for seq into b and returns b's finished bytes. The
// slice aliases the builder, so a caller that keeps it past the next build
// must copy it.
func (t *schemaTemplate) build(b *flatbuffers.Builder, seq uint64) []byte {
	// Reset keeps the capacity and drops the contents, so a 1 GiB run does not
	// grow one builder buffer to 1 GiB (the "do not build it in RAM first"
	// constraint) and does not reallocate per record either.
	b.Reset()

	// Strings and vectors are nested objects: FlatBuffers requires every one
	// of them to be finished BEFORE the table that references them starts.
	for i, c := range t.cols {
		switch c.Type {
		case "string":
			t.strbuf = append(t.strbuf[:0], t.code...)
			t.strbuf = append(t.strbuf, '-')
			t.strbuf = strconv.AppendUint(t.strbuf, seq, 10)
			t.offsets[i] = b.CreateByteString(t.strbuf)
		case "[ubyte]":
			t.offsets[i] = b.CreateByteVector(t.blob)
		}
	}
	for i := range t.filler {
		t.filler[i] = byte(seq >> (8 * (uint(i) % 8)))
	}
	fillOffset := b.CreateByteVector(t.filler)

	b.StartObject(t.fillSlot + 1)
	for i, c := range t.cols {
		if c.Terminal {
			// Union discriminator: leave it at NONE (see buildTemplates).
			continue
		}
		switch c.Type {
		case "string", "[ubyte]":
			b.PrependUOffsetT(t.offsets[i])
		case "bool":
			b.PrependBool(seq&1 == 1)
		case "byte", "int8":
			b.PrependInt8(int8(seq))
		case "ubyte", "uint8":
			b.PrependUint8(uint8(seq))
		case "short", "int16":
			b.PrependInt16(int16(seq))
		case "ushort", "uint16":
			b.PrependUint16(uint16(seq))
		case "int", "int32":
			b.PrependInt32(int32(seq))
		case "uint", "uint32":
			b.PrependUint32(uint32(seq))
		case "long", "int64":
			b.PrependInt64(int64(seq))
		case "ulong", "uint64":
			b.PrependUint64(seq)
		case "float", "float32":
			b.PrependFloat32(float32(seq))
		case "double", "float64":
			b.PrependFloat64(float64(seq))
		default:
			// enginecatalog only ever emits the types above; a new one must
			// stop the run rather than silently leave a slot empty and let the
			// engine read the next field at the wrong offset.
			panic("sds-stream-bench: unhandled engine column type " + c.Type)
		}
		// Slot() is called unconditionally, never PrependXSlot(o, x, d):
		// PrependXSlot elides the write when the value equals the default, so
		// a scalar column that happened to synthesise to 0 would vanish from
		// the vtable and the record would not be the layout this template
		// claims to produce.
		b.Slot(i)
	}
	b.PrependUOffsetT(fillOffset)
	b.Slot(t.fillSlot)
	root := b.EndObject()

	// Bare (not size-prefixed) is the CANONICAL STORED FORM: its CID is the
	// sha256 of exactly these bytes, and the publish boundary refuses a
	// size-prefixed record outright (sds.DetectEnvelopeForm).
	if t.fileID != "" {
		b.FinishWithFileIdentifier(root, []byte(t.fileID))
	} else {
		b.Finish(root)
	}
	return b.FinishedBytes()
}

// locateSchemaDir finds the embedded IDLs. They are reachable through
// internal/sds's embed.FS only from inside that package, and enginecatalog
// parses a DIRECTORY, so the benchmark resolves the checkout path by walking
// up from the working directory.
func locateSchemaDir(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		for _, candidate := range []string{
			filepath.Join(dir, "internal", "sds", "schemas"),
			filepath.Join(dir, "sdn-server", "internal", "sds", "schemas"),
		} {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no internal/sds/schemas directory at or above %s: pass -schema-dir", start)
		}
		dir = parent
	}
}
