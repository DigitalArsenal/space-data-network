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
	"fmt"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"

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

// TypeSpecFor builds the registration of an embedded SDS standard.
func TypeSpecFor(schemaName string) (TypeSpec, error) {
	code := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(schemaName), ".fbs"))
	bfbs, ok := sds.SearchSchema(code)
	if !ok {
		return TypeSpec{}, fmt.Errorf("format2: no embedded binary schema for %q", schemaName)
	}
	ident, err := bfbsFileIdent(bfbs)
	if err != nil {
		return TypeSpec{}, fmt.Errorf("format2: %s: %w", code, err)
	}
	spec := TypeSpec{SchemaName: code + ".fbs", BFBS: bfbs, Rules: typeRules[code], Flags: TypeVerifyBFBS | TypeVerifyCID}
	copy(spec.FID[:], ident)
	return spec, nil
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
