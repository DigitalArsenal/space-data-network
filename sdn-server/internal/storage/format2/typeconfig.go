package format2

// Per-type engine configuration (design A19): an SDS type's behaviour is
// engine DATA, not Go code. The router registers each type with its binary
// schema (the node's embedded BFBS, internal/sds) and a rule text that
// reproduces storage.extractIndexedFields and record_supersede.go exactly:
// the engine's golden vectors check these same texts against those Go
// functions over 420 real and edge-case frames (flatsql PARTITION-STORE.md §5).
//
// COL numbering is the legacy sdn_record_index layout: 0 norad_cat_id,
// 1 entity_id, 2 object_type, 3 ops_status_code, 4 epoch_day.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// Legacy index columns as engine COL numbers.
const (
	ColNoradCatID    = 0
	ColEntityID      = 1
	ColObjectType    = 2
	ColOpsStatusCode = 3
	ColEpochDay      = 4
)

// typeRules are the extraction rule texts (flatsql ps/extract.h grammar).
var typeRules = map[string]string{
	// extractIndexedFields, case "OMM.fbs".
	"OMM": "epoch str:EPOCH|str:CREATION_DATE\ncol 0 u64pos:NORAD_CAT_ID\ncol 1 str:OBJECT_ID\nepoch_day 4\nobject 0,1\n",
	// case "MPE.fbs": floor(EPOCH) seconds, 0 = absent.
	"MPE": "epoch f64floor:EPOCH\ncol 1 str:ENTITY_ID\nepoch_day 4\nobject 1\n",
	// case "OEM.fbs": the first block's OBJECT is required; epoch = START_TIME,
	// else the first data line's EPOCH.
	"OEM": "require EPHEMERIS_DATA_BLOCK[0].OBJECT\n" +
		"epoch str:EPHEMERIS_DATA_BLOCK[0].START_TIME|str:EPHEMERIS_DATA_BLOCK[0].EPHEMERIS_DATA_LINES[0].EPOCH\n" +
		"col 0 u64pos:EPHEMERIS_DATA_BLOCK[0].OBJECT.NORAD_CAT_ID\ncol 1 str:EPHEMERIS_DATA_BLOCK[0].OBJECT.OBJECT_ID\n" +
		"epoch_day 4\nobject 0,1\n",
	// case "CAT.fbs"; supersede = record_supersede.go recordSupersedeKey.
	"CAT": "col 0 u64pos:NORAD_CAT_ID\ncol 1 str:OBJECT_ID\ncol 2 enum:OBJECT_TYPE\ncol 3 enum:OPS_STATUS_CODE\nobject 0,1\n" +
		"supersede pair:uri:CATALOG_URI,CATALOG_OBJECT_ID|u64:norad:NORAD_CAT_ID|str:object:OBJECT_ID\n",
	// case "PNM.fbs".
	"PNM": "col 1 str:FILE_ID\n",
	// case "RFB.fbs": transmitter, else ID; no epoch.
	"RFB": "col 0 u64pos:NORAD_CAT_ID\ncol 1 str:ID_TRANSMITTER|str:ID\n",
}

// Type config flags (flatsql TypeConfig::Flag).
const (
	TypeVerifyBFBS uint32 = 1
	TypeVerifyCID  uint32 = 2
	TypeControl    uint32 = 4
)

// TypeSpec is one type's engine registration.
type TypeSpec struct {
	SchemaName string // "OMM.fbs": the engine's type name is the part before the dot
	FID        [4]byte
	BFBS       []byte
	Rules      string
	MaxFrame   uint64 // 0: engine default (16 MiB; the ring entry bounds it further)
	RingCap    uint64 // 0: engine default
	Flags      uint32
}

// Encode serializes the spec (flatsql TypeConfig::build: TLV tags 1-7).
func (t TypeSpec) Encode() []byte {
	c := tlv(nil).bytes(1, []byte(t.SchemaName)).bytes(2, t.FID[:]).bytes(3, t.BFBS).bytes(4, []byte(t.Rules))
	maxFrame := t.MaxFrame
	if maxFrame == 0 {
		maxFrame = 16 << 20
	}
	ringCap := t.RingCap
	if ringCap == 0 {
		ringCap = 4 << 20
	}
	return c.u64(5, maxFrame).u64(6, ringCap).u32(7, t.Flags)
}

// TypeName is the SQL name of the type ("OMM").
func (t TypeSpec) TypeName() string {
	name, _, _ := strings.Cut(t.SchemaName, ".")
	return name
}

// CIDOnlyTypeSpec is the registration of a type the engine extracts nothing
// from (contract C-25): its file identifier and verify-CID, no binary schema
// and no rules. The engine stores, dedupes and serves its frames by CID,
// arrival and tags, and gives it no SQL relation.
func CIDOnlyTypeSpec(code string, fid [4]byte) TypeSpec {
	return TypeSpec{SchemaName: code + ".fbs", FID: fid, Flags: TypeVerifyCID}
}

// TypeSpecFor builds the registration of an embedded SDS standard.
//
// A standard whose records SDN seals at rest (encfield's registry: the stored
// bytes are an SDF1 envelope, not the record) registers CID-only, as one
// without a binary schema does: the engine extracts nothing from it and gives
// it no SQL relation, as format 1 keeps sealed standards off its engine
// (contract C-46 (7); format 1's empty table and unsealed rule fields for
// one with a binary schema are C-48 (c)). One whose sealed field an
// extraction rule reads is refused (ErrSealedRuleField).
func TypeSpecFor(schemaName string) (TypeSpec, error) {
	code := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(schemaName), ".fbs"))
	if err := SealedRuleField(code); err != nil {
		return TypeSpec{}, err
	}
	bfbs, ok := sds.SearchSchema(code)
	if !ok {
		return TypeSpec{}, fmt.Errorf("format2: no embedded binary schema for %q", schemaName)
	}
	ident, err := bfbsFileIdent(bfbs)
	if err != nil {
		return TypeSpec{}, fmt.Errorf("format2: %s: %w", code, err)
	}
	var fid [4]byte
	copy(fid[:], ident)
	if encfield.HasEncryptedFields(code) {
		return CIDOnlyTypeSpec(code, fid), nil
	}
	return TypeSpec{SchemaName: code + ".fbs", FID: fid, BFBS: bfbs, Rules: typeRules[code], Flags: TypeVerifyBFBS | TypeVerifyCID}, nil
}

// ErrSealedRuleField refuses a type one of whose fields SDN seals at rest
// while an extraction rule reads it (contract C-46 (7)): the rule would put
// the field's plaintext in an index at rest.
var ErrSealedRuleField = errors.New("a field sealed at rest is an extraction-rule field")

// SealedRuleField returns ErrSealedRuleField, naming the field and the rule,
// when an extraction rule of the type (typeRules: format 1's
// extractIndexedFields and recordSupersedeKey) reads one of its sealed fields
// (encfield's registry), and nil otherwise. A sealed field is a root-table
// field (encfield seals by field id); a rule reads the root field its path
// starts with. The id resolves through the type's binary schema; the
// registration's field name is checked as well, so neither a schema-less
// type nor a mislabelled registration slips past.
func SealedRuleField(code string) error {
	sealed := encfield.EncryptedFields(code)
	if len(sealed) == 0 {
		return nil
	}
	for _, r := range ruleReadsOf(code) {
		for _, f := range sealed {
			if r.field == f.Name || r.id == int(f.FieldID) {
				return fmt.Errorf("%w: %s.%s (field %d) is sealed at rest and the %q rule reads %s; refusing the type so its plaintext never lands in an index",
					ErrSealedRuleField, code, f.Name, f.FieldID, r.directive, r.field)
			}
		}
	}
	return nil
}

// ruleRead is one root field a rule directive reads, with its field id (-1:
// the type has no binary schema naming it).
type ruleRead struct {
	directive, field string
	id               int
}

// ruleRoots caches ruleReadsOf: the embedded rules and binary schemas never
// change in a process.
var ruleRoots sync.Map // code -> []ruleRead

// ruleReadsOf is the root fields the type's rules read, ids resolved.
func ruleReadsOf(code string) []ruleRead {
	if v, ok := ruleRoots.Load(code); ok {
		return v.([]ruleRead)
	}
	reads := ruleReads(typeRules[code])
	ids := map[string]uint16{}
	if len(reads) > 0 {
		if fields, ok := RootFields(code); ok {
			for _, f := range fields {
				ids[f.Name] = f.ID
			}
		}
	}
	for i := range reads {
		reads[i].id = -1
		if id, ok := ids[reads[i].field]; ok {
			reads[i].id = int(id)
		}
	}
	ruleRoots.Store(code, reads)
	return reads
}

// ruleReads lists the root fields a rule text reads (flatsql's rule grammar,
// typecfg extract.cpp compile): every path of epoch, col, require and
// supersede, by the root field the path starts with; object and epoch_day
// name columns, which col rules fill.
func ruleReads(rules string) []ruleRead {
	var out []ruleRead
	add := func(directive, path string) {
		path = strings.TrimSpace(path)
		if i := strings.IndexAny(path, ".["); i >= 0 {
			path = path[:i]
		}
		if path != "" {
			out = append(out, ruleRead{directive: directive, field: path})
		}
	}
	for _, line := range strings.Split(rules, "\n") {
		line, _, _ = strings.Cut(line, "#")
		directive, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		rest = strings.TrimSpace(rest)
		switch directive {
		case "col":
			_, rest, _ = strings.Cut(rest, " ")
			fallthrough
		case "epoch":
			for _, alt := range strings.Split(rest, "|") {
				_, path, _ := strings.Cut(alt, ":") // kind:path
				add(directive, path)
			}
		case "require":
			add(directive, rest)
		case "supersede":
			for _, alt := range strings.Split(rest, "|") {
				parts := strings.SplitN(alt, ":", 3) // kind:prefix:path; a pair's path is "a,b"
				if len(parts) < 3 {
					continue
				}
				for _, path := range strings.Split(parts[2], ",") {
					add(directive, path)
				}
			}
		}
	}
	return out
}

// RootField is one field of a binary schema's root table (reflection.Field).
type RootField struct {
	Name string
	// ID is the field's id: its declaration order, the vtable slot encfield
	// seals and the rules resolve.
	ID uint16
	// BaseType is its reflection.BaseType (BaseBool, BaseUInt, ...).
	BaseType int8
	// Encrypted: the field carries the (encrypted) attribute.
	Encrypted bool
}

// reflection.BaseType values.
const (
	BaseBool int8 = 2
	BaseUInt int8 = 8
)

// RootFields lists the root table fields of a standard's embedded binary
// schema (sds.SearchSchema); false when it has none.
func RootFields(schemaName string) ([]RootField, bool) {
	code := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(schemaName), ".fbs"))
	bfbs, ok := sds.SearchSchema(code)
	if !ok {
		return nil, false
	}
	fields, err := bfbsRootFields(bfbs)
	return fields, err == nil
}

// bfbsRootFields reads reflection.Schema.root_table (field 4) and each of its
// fields (Object.fields 1): Field.name 0, type 1 (Type.base_type 0), id 2,
// attributes 9 (KeyValue.key 0).
func bfbsRootFields(bfbs []byte) (fields []RootField, err error) {
	defer func() {
		if r := recover(); r != nil {
			fields, err = nil, fmt.Errorf("binary schema: %v", r)
		}
	}()
	if len(bfbs) < 8 {
		return nil, fmt.Errorf("binary schema is %d bytes", len(bfbs))
	}
	table := func(pos flatbuffers.UOffsetT) flatbuffers.Table { return flatbuffers.Table{Bytes: bfbs, Pos: pos} }
	sub := func(t flatbuffers.Table, slot flatbuffers.VOffsetT) (flatbuffers.Table, bool) {
		o := flatbuffers.UOffsetT(t.Offset(slot))
		if o == 0 {
			return flatbuffers.Table{}, false
		}
		return table(t.Indirect(o + t.Pos)), true
	}
	vec := func(t flatbuffers.Table, slot flatbuffers.VOffsetT) []flatbuffers.Table {
		o := flatbuffers.UOffsetT(t.Offset(slot))
		if o == 0 {
			return nil
		}
		start, n := t.Vector(o), t.VectorLen(o)
		out := make([]flatbuffers.Table, n)
		for i := range out {
			out[i] = table(t.Indirect(start + flatbuffers.UOffsetT(4*i)))
		}
		return out
	}
	str := func(t flatbuffers.Table, slot flatbuffers.VOffsetT) string {
		if o := flatbuffers.UOffsetT(t.Offset(slot)); o != 0 {
			return string(t.ByteVector(o + t.Pos))
		}
		return ""
	}
	root, ok := sub(table(flatbuffers.GetUOffsetT(bfbs)), 12)
	if !ok {
		return nil, fmt.Errorf("binary schema has no root table")
	}
	for _, f := range vec(root, 6) {
		rf := RootField{Name: str(f, 4)}
		if o := flatbuffers.UOffsetT(f.Offset(8)); o != 0 {
			rf.ID = f.GetUint16(o + f.Pos)
		}
		if t, ok := sub(f, 6); ok {
			if o := flatbuffers.UOffsetT(t.Offset(4)); o != 0 {
				rf.BaseType = t.GetInt8(o + t.Pos)
			}
		}
		for _, kv := range vec(f, 22) {
			if str(kv, 4) == "encrypted" {
				rf.Encrypted = true
			}
		}
		fields = append(fields, rf)
	}
	return fields, nil
}

// bfbsFileIdent reads reflection.Schema.file_ident (field 2) from a BFBS.
func bfbsFileIdent(bfbs []byte) (string, error) {
	if len(bfbs) < 8 {
		return "", fmt.Errorf("binary schema is %d bytes", len(bfbs))
	}
	var t flatbuffers.Table
	t.Bytes = bfbs
	t.Pos = flatbuffers.UOffsetT(binary.LittleEndian.Uint32(bfbs))
	if int(t.Pos) >= len(bfbs) {
		return "", fmt.Errorf("binary schema root out of range")
	}
	o := flatbuffers.UOffsetT(t.Offset(8)) // field 2: file_ident
	if o == 0 {
		return "", fmt.Errorf("binary schema has no file identifier")
	}
	ident := string(t.ByteVector(o + t.Pos))
	if len(ident) != 4 {
		return "", fmt.Errorf("file identifier %q is not 4 bytes", ident)
	}
	return ident, nil
}
