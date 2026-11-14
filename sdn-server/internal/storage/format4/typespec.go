package format4

import (
	"encoding/binary"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// TypeSpec is one type's engine registration (contract §3.3, register_type).
// Tags 1-7 are format 2's, encoded by format2.TypeSpec.Encode (RingCap is
// ignored by format 4); the rest are format 4's.
type TypeSpec struct {
	format2.TypeSpec           // tags 1-7 exactly as format2.TypeSpec.Encode (RingCap ignored)
	PageSize            uint32 // 8
	Identity            bool   // 9
	A18Bound            uint32 // 10
	UnlimitedVisibility bool   // tag 10 is present with zero, after a capability check
	EpochProfile        uint8  // 11: 0 none, 1 OMM, 2 MPE
	FullText            bool   // 12
}

// Epoch profiles (type spec tag 11): how an EPOCH op keys an entity.
const (
	epochProfileNone uint8 = 0
	epochProfileOMM  uint8 = 1 // entity = COL0 -> COL1 -> cid
	epochProfileMPE  uint8 = 2 // entity = COL1 -> COL0 -> cid
)

// Encode serializes the spec: format 2's tags 1-7, then tags 8-12 in
// ascending order, each only when set (absent = the engine default).
func (t TypeSpec) Encode() []byte {
	out := t.TypeSpec.Encode()
	if t.PageSize != 0 {
		out = tlvU32(out, 8, t.PageSize)
	}
	if t.Identity {
		out = tlvU8(out, 9, 1)
	}
	if t.UnlimitedVisibility {
		out = tlvU32(out, 10, 0)
	} else if t.A18Bound != 0 {
		out = tlvU32(out, 10, t.A18Bound)
	}
	if t.EpochProfile != 0 {
		out = tlvU8(out, 11, t.EpochProfile)
	}
	if t.FullText {
		out = tlvU8(out, 12, 1)
	}
	return out
}

// CIDOnlyTypeSpec is format 2's CID-only registration (format2.CIDOnlyTypeSpec,
// contract C-25) on format 4: 4 KiB pages and unlimited visibility, no
// epoch profile, no identity, no full text.
func CIDOnlyTypeSpec(typ string, fid [4]byte) TypeSpec {
	return TypeSpec{TypeSpec: format2.CIDOnlyTypeSpec(typ, fid), PageSize: 4096, UnlimitedVisibility: true}
}

// TypeSpecFor builds the registration of an embedded SDS standard: format
// 2's (rules, BFBS, file identifier, flags; read-only reuse) plus IQC's
// identity dedupe and 16 KiB pages, unlimited visibility, the epoch profile
// (OMM 1, MPE 2) and full text for every type
// format 1 indexes, except numeric weather grids. Field-sealed types have no
// full-text index either.
// A field-sealed standard (a sealed record's stored bytes are neither its
// text nor a FlatBuffer) has a metadata-only SQL relation with no extracted
// plaintext fields. The rules are format 2's,
// unchanged (contract v15: one file per source feed x standard, no bucket
// rule).
func TypeSpecFor(schemaName string) (TypeSpec, error) {
	base, err := format2.TypeSpecFor(schemaName)
	if err != nil {
		return TypeSpec{}, err
	}
	typ := base.TypeName()
	if len(base.BFBS) == 0 {
		return CIDOnlyTypeSpec(typ, base.FID), nil
	}
	spec := typeStorageOptions(typ)
	spec.TypeSpec = base
	return spec, nil
}

// typeStorageOptions keeps storage policy separate from schema validation and
// extraction rules. Changing an optional derived index must not remove the
// schema, record verification, SQL relation, or provenance indexes.
func typeStorageOptions(typ string) TypeSpec {
	spec := TypeSpec{PageSize: 4096, UnlimitedVisibility: true, FullText: true}
	switch typ {
	case "WXF":
		// Grid samples are numeric payloads. Reflecting every float into text
		// adds a second, large derived index to each ingest. The BFBS and
		// ordinary record/source indexes still serve metadata and SQL reads.
		spec.FullText = false
	case "OMM":
		spec.EpochProfile = epochProfileOMM
	case "MPE":
		spec.EpochProfile = epochProfileMPE
	case "IQC":
		spec.Identity = true
		spec.PageSize = 16384
	}
	return spec
}

func tlvBytes(out []byte, tag uint16, v []byte) []byte {
	out = binary.LittleEndian.AppendUint16(out, tag)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(v)))
	return append(out, v...)
}

func tlvU8(out []byte, tag uint16, v uint8) []byte { return tlvBytes(out, tag, []byte{v}) }

func tlvU32(out []byte, tag uint16, v uint32) []byte {
	return tlvBytes(out, tag, binary.LittleEndian.AppendUint32(nil, v))
}

func tlvU64(out []byte, tag uint16, v uint64) []byte {
	return tlvBytes(out, tag, binary.LittleEndian.AppendUint64(nil, v))
}

func tlvText(out []byte, tag uint16, s string) []byte { return tlvBytes(out, tag, []byte(s)) }
