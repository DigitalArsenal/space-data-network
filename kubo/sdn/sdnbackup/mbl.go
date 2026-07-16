package sdnbackup

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
)

// $MBL is the reuse-first envelope for everything that crosses an adapter
// boundary (spec A.3, 1.4): its ModuleBundleEntry is already a generic
// {entry_id, role, section_name, payload_encoding, media_type, sha256, payload,
// description} wrapper with a per-entry sha256 that appmanifest.ToMBL/FromMBL
// verify on read. This file adds three uses on top of that same envelope:
//
//   - BlobToMBL / BlobFromMBL — one TRANSPORT entry carrying a backup unit's
//     bytes, self-verifying by sha256 AND by entry_id == content hash.
//   - FlowBundleToMBL / FlowBundleFromMBL — a flow's on-disk triple
//     {runtime.wasm, flow.json, artifact.json} as three entries, so the one
//     non-content-addressed substrate becomes one content-hashable blob.
//   - BuildReceiptMBL / ParseReceiptMBL — one attestation entry per backup
//     unit plus a JSON summary entry (the $PNM/$REC stand-in, spec A.3/D.2).
const (
	moduleFormatBlob     = "sdn-backup-blob/1"
	moduleFormatFlow     = "sdn-flow-bundle/1"
	moduleFormatReceipt  = "sdn-backup-receipt/1"
	mediaOctetStream     = "application/octet-stream"
	mediaJSON            = "application/json"
	sectionFlowWASM      = "flow.runtime.wasm"
	sectionFlowJSON      = "flow.json"
	sectionFlowArtifact  = "flow.artifact.json"
	sectionBlobDesc      = "sdn.backup.blob"
	sectionReceiptHeader = "sdn.backup.receipt"
)

// mblEntry is a fully-resolved entry to be built into an $MBL.
type mblEntry struct {
	EntryID   string
	Role      MBL.ModuleBundleEntryRole
	Section   string
	Encoding  MBL.ModulePayloadEncoding
	MediaType string
	Payload   []byte
	Desc      string
	// sha256 is always the digest of Payload; set implicitly by buildMBL.
}

// buildMBL assembles a finished $MBL FlatBuffer from entries. Each entry's
// sha256 slot is the SHA-256 of its payload (so FromMBL can verify per entry).
// FlatBuffer ordering rule respected: every nested string/vector for an entry
// is created before its table is opened, all entry tables are finished before
// the entries vector, and module_format is created after the vector.
func buildMBL(moduleFormat string, entries []mblEntry) []byte {
	b := flatbuffers.NewBuilder(1024)
	offs := make([]flatbuffers.UOffsetT, len(entries))
	for i, e := range entries {
		idOff := b.CreateString(e.EntryID)
		var secOff, mediaOff, descOff flatbuffers.UOffsetT
		if e.Section != "" {
			secOff = b.CreateString(e.Section)
		}
		if e.MediaType != "" {
			mediaOff = b.CreateString(e.MediaType)
		}
		if e.Desc != "" {
			descOff = b.CreateString(e.Desc)
		}
		sum := sha256.Sum256(e.Payload)
		shaOff := b.CreateByteVector(sum[:])
		var payOff flatbuffers.UOffsetT
		if len(e.Payload) > 0 {
			payOff = b.CreateByteVector(e.Payload)
		}

		MBL.ModuleBundleEntryStart(b)
		MBL.ModuleBundleEntryAddEntryId(b, idOff)
		MBL.ModuleBundleEntryAddRole(b, e.Role)
		if secOff != 0 {
			MBL.ModuleBundleEntryAddSectionName(b, secOff)
		}
		MBL.ModuleBundleEntryAddPayloadEncoding(b, e.Encoding)
		if mediaOff != 0 {
			MBL.ModuleBundleEntryAddMediaType(b, mediaOff)
		}
		MBL.ModuleBundleEntryAddSha256(b, shaOff)
		if payOff != 0 {
			MBL.ModuleBundleEntryAddPayload(b, payOff)
		}
		if descOff != 0 {
			MBL.ModuleBundleEntryAddDescription(b, descOff)
		}
		offs[i] = MBL.ModuleBundleEntryEnd(b)
	}

	MBL.MBLStartEntriesVector(b, len(entries))
	for i := len(offs) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offs[i])
	}
	entriesOff := b.EndVector(len(entries))

	fmtOff := b.CreateString(moduleFormat)
	MBL.MBLStart(b)
	MBL.MBLAddBundleVersion(b, 1)
	MBL.MBLAddModuleFormat(b, fmtOff)
	MBL.MBLAddEntries(b, entriesOff)
	root := MBL.MBLEnd(b)
	MBL.FinishMBLBuffer(b, root)
	return b.FinishedBytes()
}

// parsedEntry is one entry read back from an $MBL, with its payload already
// sha256-verified against the entry's declared digest.
type parsedEntry struct {
	EntryID   string
	Role      MBL.ModuleBundleEntryRole
	Section   string
	Encoding  MBL.ModulePayloadEncoding
	MediaType string
	Payload   []byte
	Desc      string
}

// parseMBL decodes an $MBL FlatBuffer, verifying each entry's sha256 against
// its payload. buf may be untrusted (delivered by a provider), so every
// accessor is guarded by recover: a crafted buffer yields an error, not a
// panic — the same posture appmanifest.FromMBL takes.
func parseMBL(buf []byte) (format string, entries []parsedEntry, err error) {
	defer func() {
		if r := recover(); r != nil {
			format = ""
			entries = nil
			err = fmt.Errorf("sdnbackup: malformed $MBL flatbuffer: %v", r)
		}
	}()

	if len(buf) < 8 || !MBL.MBLBufferHasIdentifier(buf) {
		return "", nil, errors.New("sdnbackup: buffer does not carry the $MBL file identifier")
	}
	root := MBL.GetRootAsMBL(buf, 0)
	format = string(root.ModuleFormat())

	var entry MBL.ModuleBundleEntry
	for i := 0; i < root.EntriesLength(); i++ {
		if !root.Entries(&entry, i) {
			continue
		}
		payload := entry.PayloadBytes()
		if want := entry.Sha256Bytes(); len(want) == sha256.Size {
			got := sha256.Sum256(payload)
			if !bytes.Equal(want, got[:]) {
				return "", nil, fmt.Errorf("sdnbackup: entry %q sha256 mismatch: declares %x, payload hashes to %x", string(entry.EntryId()), want, got)
			}
		}
		entries = append(entries, parsedEntry{
			EntryID:   string(entry.EntryId()),
			Role:      entry.Role(),
			Section:   string(entry.SectionName()),
			Encoding:  entry.PayloadEncoding(),
			MediaType: string(entry.MediaType()),
			Payload:   append([]byte(nil), payload...),
			Desc:      string(entry.Description()),
		})
	}
	return format, entries, nil
}

// blobDesc is the JSON carried in a blob entry's description to recover the
// unit's kind + re-stage hints on Get.
type blobDesc struct {
	Kind Kind `json:"kind"`
	Meta Meta `json:"meta"`
}

// BlobToMBL wraps a backup unit as a single TRANSPORT-role $MBL entry:
// entry_id = content hash, sha256 = digest of bytes, description = JSON{kind,
// meta}. This is the on-the-wire form an adapter stores and returns; a WASM
// adapter would receive the identical bytes via $PIV.
func BlobToMBL(blob BackupBlob) ([]byte, error) {
	if blob.ContentHash == "" {
		return nil, errors.New("sdnbackup: blob has no content hash")
	}
	if got := HashBytes(blob.Bytes); got != blob.ContentHash {
		return nil, fmt.Errorf("sdnbackup: blob content hash %s does not match its bytes (%s)", blob.ContentHash, got)
	}
	desc, err := json.Marshal(blobDesc{Kind: blob.Kind, Meta: blob.Meta})
	if err != nil {
		return nil, err
	}
	return buildMBL(moduleFormatBlob, []mblEntry{{
		EntryID:   blob.ContentHash,
		Role:      MBL.ModuleBundleEntryRoleTRANSPORT,
		Section:   sectionBlobDesc,
		Encoding:  MBL.ModulePayloadEncodingRAW_BYTES,
		MediaType: mediaOctetStream,
		Payload:   blob.Bytes,
		Desc:      string(desc),
	}}), nil
}

// BlobFromMBL is the inverse of BlobToMBL. It verifies the entry sha256 (in
// parseMBL) AND that entry_id == sha256(payload), so a corrupt or substituted
// blob is rejected — the verify-by-hash guard the spec requires (C.4).
func BlobFromMBL(buf []byte) (BackupBlob, error) {
	format, entries, err := parseMBL(buf)
	if err != nil {
		return BackupBlob{}, err
	}
	if format != moduleFormatBlob {
		return BackupBlob{}, fmt.Errorf("sdnbackup: not a backup blob $MBL (module_format=%q)", format)
	}
	for _, e := range entries {
		if e.Role != MBL.ModuleBundleEntryRoleTRANSPORT {
			continue
		}
		got := HashBytes(e.Payload)
		if got != e.EntryID {
			return BackupBlob{}, fmt.Errorf("sdnbackup: blob entry_id %s does not match payload hash %s", e.EntryID, got)
		}
		var d blobDesc
		if len(e.Desc) > 0 {
			if err := json.Unmarshal([]byte(e.Desc), &d); err != nil {
				return BackupBlob{}, fmt.Errorf("sdnbackup: decode blob description: %w", err)
			}
		}
		return BackupBlob{ContentHash: got, Kind: d.Kind, Meta: d.Meta, Bytes: e.Payload}, nil
	}
	return BackupBlob{}, errors.New("sdnbackup: $MBL carries no TRANSPORT-role blob entry")
}

// FlowBundleToMBL wraps a flow's on-disk triple as a three-entry $MBL. The
// programId rides in the wasm entry's description so restore can Install it
// even when flow.json is absent. flowJSON and artifact are optional.
func FlowBundleToMBL(programID string, wasm, flowJSON, artifact []byte) ([]byte, error) {
	if len(wasm) == 0 {
		return nil, errors.New("sdnbackup: flow bundle requires runtime.wasm bytes")
	}
	entries := []mblEntry{{
		EntryID:   "runtime.wasm",
		Role:      MBL.ModuleBundleEntryRoleTRANSPORT,
		Section:   sectionFlowWASM,
		Encoding:  MBL.ModulePayloadEncodingRAW_BYTES,
		MediaType: mediaOctetStream,
		Payload:   wasm,
		Desc:      programID,
	}}
	if len(flowJSON) > 0 {
		entries = append(entries, mblEntry{
			EntryID:   "flow.json",
			Role:      MBL.ModuleBundleEntryRoleMANIFEST,
			Section:   sectionFlowJSON,
			Encoding:  MBL.ModulePayloadEncodingJSON_UTF8,
			MediaType: mediaJSON,
			Payload:   flowJSON,
		})
	}
	if len(artifact) > 0 {
		entries = append(entries, mblEntry{
			EntryID:   "artifact.json",
			Role:      MBL.ModuleBundleEntryRoleAUXILIARY,
			Section:   sectionFlowArtifact,
			Encoding:  MBL.ModulePayloadEncodingJSON_UTF8,
			MediaType: mediaJSON,
			Payload:   artifact,
		})
	}
	return buildMBL(moduleFormatFlow, entries), nil
}

// FlowBundleFromMBL is the inverse of FlowBundleToMBL.
func FlowBundleFromMBL(buf []byte) (programID string, wasm, flowJSON, artifact []byte, err error) {
	format, entries, err := parseMBL(buf)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if format != moduleFormatFlow {
		return "", nil, nil, nil, fmt.Errorf("sdnbackup: not a flow bundle $MBL (module_format=%q)", format)
	}
	for _, e := range entries {
		switch e.Section {
		case sectionFlowWASM:
			wasm = e.Payload
			programID = e.Desc
		case sectionFlowJSON:
			flowJSON = e.Payload
		case sectionFlowArtifact:
			artifact = e.Payload
		}
	}
	if len(wasm) == 0 {
		return "", nil, nil, nil, errors.New("sdnbackup: flow bundle $MBL carries no runtime.wasm entry")
	}
	return programID, wasm, flowJSON, artifact, nil
}
