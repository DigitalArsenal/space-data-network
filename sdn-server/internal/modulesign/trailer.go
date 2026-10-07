package modulesign

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
)

// The publication trailer the module SDK writes and every SDN loader reads
// (space-data-module-sdk src/transport/records.js, internal/modulert): the
// module, then an SDS $REC record collection holding one $MBL bundle listing,
// then an 8-byte footer, the collection's length (u32 little-endian) and
// "$REC". Loaders strip the trailer before compiling.
const (
	trailerMagic            = "$REC"
	recordCollectionVersion = "1.0.0"
	// recordTypeMBL is MBL's RecordType ordinal, frozen append-only since SDS
	// v1.183.0 (schema/REC/RECORDTYPE_ORDINALS.json). Readers select the
	// record by its standard string; the ordinal is written so they see no
	// drift between the two.
	recordTypeMBL = 80
	// sdsSectionPrefix names the custom sections SDS owns: the canonical
	// module hash leaves them out (the SDK's canonicalization rule).
	sdsSectionPrefix  = "sds."
	bundleSectionName = "rec.mbl"
)

// Artifact appends to module the publication trailer carrying entry, a
// signature this package issued over exactly these module bytes. The result
// is a module artifact in the SDK's single-file form, which verifies with the
// SDK's verifyModuleArtifact and loads anywhere a published module loads.
func Artifact(module []byte, entry SignatureEntry) ([]byte, error) {
	if len(module) < wasmPreambleLen || string(module[:4]) != string(wasmMagic) {
		return nil, fmt.Errorf("module artifact: not a wasm module")
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("module artifact: signature entry: %w", err)
	}
	sum := sha256.Sum256(payload)
	canonical, err := canonicalModuleHash(module)
	if err != nil {
		return nil, err
	}

	b := flatbuffers.NewBuilder(len(payload) + 512)
	// The signature entry, as the SDK writes it.
	entryID := b.CreateString("signature")
	section := b.CreateString("sds.signature")
	media := b.CreateString("application/json")
	description := b.CreateString("Detached signature payload.")
	digest := b.CreateByteVector(sum[:])
	body := b.CreateByteVector(payload)
	MBL.ModuleBundleEntryStart(b)
	MBL.ModuleBundleEntryAddEntryId(b, entryID)
	MBL.ModuleBundleEntryAddRole(b, MBL.ModuleBundleEntryRoleSIGNATURE)
	MBL.ModuleBundleEntryAddSectionName(b, section)
	MBL.ModuleBundleEntryAddPayloadEncoding(b, MBL.ModulePayloadEncodingJSON_UTF8)
	MBL.ModuleBundleEntryAddMediaType(b, media)
	MBL.ModuleBundleEntryAddSha256(b, digest)
	MBL.ModuleBundleEntryAddPayload(b, body)
	MBL.ModuleBundleEntryAddDescription(b, description)
	signature := MBL.ModuleBundleEntryEnd(b)
	MBL.MBLStartEntriesVector(b, 1)
	b.PrependUOffsetT(signature)
	entries := b.EndVector(1)
	prefix := b.CreateString(sdsSectionPrefix)
	container := b.CreateString(bundleSectionName)
	algorithm := b.CreateString("sha256")
	MBL.CanonicalizationRuleStart(b)
	MBL.CanonicalizationRuleAddVersion(b, 1)
	MBL.CanonicalizationRuleAddStrippedCustomSectionPrefix(b, prefix)
	MBL.CanonicalizationRuleAddBundleSectionName(b, container)
	MBL.CanonicalizationRuleAddHashAlgorithm(b, algorithm)
	rule := MBL.CanonicalizationRuleEnd(b)
	moduleHash := b.CreateByteVector(canonical[:])
	format := b.CreateString("space-data-module")
	MBL.MBLStart(b)
	MBL.MBLAddBundleVersion(b, 1)
	MBL.MBLAddModuleFormat(b, format)
	MBL.MBLAddCanonicalization(b, rule)
	MBL.MBLAddCanonicalModuleHash(b, moduleHash)
	MBL.MBLAddEntries(b, entries)
	listing := MBL.MBLEnd(b)

	// REC's Record: value_type (slot 0), value (slot 1), standard (slot 2).
	standard := b.CreateString("MBL")
	b.StartObject(3)
	b.PrependByteSlot(0, recordTypeMBL, 0)
	b.PrependUOffsetTSlot(1, listing, 0)
	b.PrependUOffsetTSlot(2, standard, 0)
	record := b.EndObject()

	// REC's root: version (slot 0), records (slot 1).
	version := b.CreateString(recordCollectionVersion)
	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(record)
	records := b.EndVector(1)
	b.StartObject(2)
	b.PrependUOffsetTSlot(0, version, 0)
	b.PrependUOffsetTSlot(1, records, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte(trailerMagic))
	collection := b.FinishedBytes()

	out := make([]byte, 0, len(module)+len(collection)+8)
	out = append(out, module...)
	out = append(out, collection...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(collection)))
	return append(out, trailerMagic...), nil
}

// canonicalModuleHash is SHA-256 of module without its sds.* custom sections,
// the SDK's computeCanonicalModuleHash.
func canonicalModuleHash(module []byte) ([32]byte, error) {
	out := append([]byte(nil), module[:wasmPreambleLen]...)
	for at := wasmPreambleLen; at < len(module); {
		start := at
		id := module[at]
		size, n := leb128(module[at+1:])
		if n == 0 || at+1+n+int(size) > len(module) {
			return [32]byte{}, fmt.Errorf("module artifact: malformed wasm section at byte %d", at)
		}
		body := module[at+1+n : at+1+n+int(size)]
		at += 1 + n + int(size)
		if id == 0 {
			length, m := leb128(body)
			if m == 0 || m+int(length) > len(body) {
				return [32]byte{}, fmt.Errorf("module artifact: malformed custom section at byte %d", start)
			}
			if strings.HasPrefix(string(body[m:m+int(length)]), sdsSectionPrefix) {
				continue
			}
		}
		out = append(out, module[start:at]...)
	}
	return sha256.Sum256(out), nil
}

// leb128 decodes an unsigned LEB128 u32, returning it and the bytes it used
// (0 when b does not hold one).
func leb128(b []byte) (uint32, int) {
	var value uint32
	for i := 0; i < len(b) && i < 5; i++ {
		value |= uint32(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return value, i + 1
		}
	}
	return 0, 0
}
