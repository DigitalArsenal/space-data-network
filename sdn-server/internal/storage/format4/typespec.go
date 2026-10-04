package format4

import (
	"encoding/binary"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// TypeSpec is one type's engine registration (contract §3.3, register_type).
// Tags 1-7 are format 2's, encoded by format2.TypeSpec.Encode (RingCap is
// ignored by format 4); the rest are format 4's.
type TypeSpec struct {
	format2.TypeSpec        // tags 1-7 exactly as format2.TypeSpec.Encode (RingCap ignored)
	PageSize         uint32 // 8
	Identity         bool   // 9
	A18Bound         uint32 // 10
	EpochProfile     uint8  // 11: 0 none, 1 OMM, 2 MPE
	FullText         bool   // 12
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
	if t.A18Bound != 0 {
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

// A18 bounds (contract §3.3 tag 10; C-24): the newest-N window of a type's
// SQL relations, as format 1's engine hot windows (engineWindowFor): 400000
// for the decorated standards OMM and TBS, 10000 for every other type.
const (
	a18BoundDecorated = 400_000
	a18BoundDefault   = 10_000
)

// CIDOnlyTypeSpec is format 2's CID-only registration (format2.CIDOnlyTypeSpec,
// contract C-25) on format 4: 4 KiB pages and the default A18 bound, no
// epoch profile, no identity, no full text.
func CIDOnlyTypeSpec(typ string, fid [4]byte) TypeSpec {
	return TypeSpec{TypeSpec: format2.CIDOnlyTypeSpec(typ, fid), PageSize: 4096, A18Bound: a18BoundDefault}
}

// TypeSpecFor builds the registration of an embedded SDS standard: format
// 2's (rules, BFBS, file identifier, flags; read-only reuse) plus IQC's
// identity dedupe and 16 KiB pages, the A18 bound (OMM and TBS 400000, else
// 10000), the epoch profile (OMM 1, MPE 2) and full text for every type
// format 1 indexes: every routed standard whose records are not field-sealed.
// A field-sealed standard (a sealed record's stored bytes are neither its
// text nor a FlatBuffer) is CID-only on format 2, and so here: no SQL
// relation, nothing extracted (contract C-46 (7)). The rules are format 2's,
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
	spec := TypeSpec{TypeSpec: base, PageSize: 4096, A18Bound: a18BoundDefault, FullText: true}
	switch typ {
	case "OMM":
		spec.A18Bound = a18BoundDecorated
		spec.EpochProfile = epochProfileOMM
	case "TBS":
		spec.A18Bound = a18BoundDecorated
	case "MPE":
		spec.EpochProfile = epochProfileMPE
	case "IQC":
		spec.Identity = true
		spec.PageSize = 16384
	}
	return spec, nil
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
