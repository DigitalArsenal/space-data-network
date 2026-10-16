package flowrt

// The typed flows (typed contracts 1.0) answer HTTP with SDS 1.249.0 records:
// $NST from peers discovery, $PNV from standards and pnm history, $DAV from
// latest-dataset and $QRP from the public query surface. The pinned Go
// standards module (lib/go v1.242.0) and the vendored third_party copy predate
// DAV and PNV and lack NST's ETAG / STANDARDS and QRP's surface fields, so the
// integration tests read these records by FIELD SLOT through the FlatBuffers
// table API. A slot is the field's position in its table's schema declaration
// (SDS schema/<CODE>/main.fbs); vtable offset = 4 + 2*slot.

import (
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
)

// $DAV (DAV.fbs, Dataset Availability).
const (
	davSTANDARD               = 0
	davKNOWN                  = 4
	davAVAILABLE              = 5
	davUNAVAILABLE_REASON     = 6
	davPINNED                 = 8
	davPNM_CID                = 15
	davPNM_FILE_ID            = 16
	davPNM_SIGNATURE_VERIFIED = 18

	davReasonNotHeld = 2 // davUnavailableReason.NotHeld
)

// $NST (NST.fbs, Node Status) and its NSTNode.
const (
	nstNODES          = 0
	nstSOURCE_PEER_ID = 2
	nstETAG           = 3

	nodePEER_ID   = 0
	nodeDN        = 1
	nodeIS_ONLINE = 8
	nodeIS_SELF   = 15
	nodeSTANDARDS = 28
)

// $PNV (PNV.fbs, Publication Notice Verification) and its PNVEntry.
const (
	pnvVERIFIER_PEER_ID = 0
	pnvENTRIES          = 2
	pnvETAG             = 3

	entryPUBLISHER_PEER_ID  = 1
	entrySTANDARD           = 3
	entryFILE_ID            = 5
	entryCID                = 7
	entrySIGNATURE_TYPE     = 10
	entrySIGNATURE          = 11
	entrySIGNATURE_VERIFIED = 12
	entryATTRIBUTION        = 13
	entryPUBLISHER_KEY      = 14
	entryKEY_SOURCE         = 15
	entryPNM_BYTES          = 17

	pnvAttributionDirect       = 1 // pnvAttribution.Direct
	pnvKeySourceIdentityRecord = 1 // pnvKeySource.IdentityRecord
)

// $QRP (QRP.fbs, Query Request/Page) and its QRPTable / QRPColumn.
const (
	qrpKIND           = 0
	qrpERROR_CODE     = 2
	qrpMESSAGE        = 3
	qrpETAG           = 33
	qrpTABLES         = 42
	qrpMAX_ELAPSED_MS = 43
	qrpMAX_ROWS       = 44

	qrpKindError = 2 // qrpKind.Error

	qrpTableNAME    = 0
	qrpTableKIND    = 1
	qrpTableSOURCE  = 2
	qrpTableRECORDS = 3
	qrpTableCOLUMNS = 4

	qrpColumnNAME = 0
)

// sdsTable is one SDS table read by slot.
type sdsTable struct{ tab flatbuffers.Table }

// readSDSRecord checks body is one plain (root-offset-first) FlatBuffer with
// the given file identifier and anchors its root table.
func readSDSRecord(t *testing.T, body []byte, identifier string) sdsTable {
	t.Helper()
	if len(body) < 8 || !flatbuffers.BufferHasIdentifier(body, identifier) {
		t.Fatalf("body is not a %s FlatBuffer: %q", identifier, body[:min(len(body), 16)])
	}
	return sdsTable{flatbuffers.Table{Bytes: body, Pos: flatbuffers.GetUOffsetT(body)}}
}

// readSizePrefixedSDSRecord is readSDSRecord for a size-prefixed buffer (the
// stream form: [u32 size][root offset][identifier]...).
func readSizePrefixedSDSRecord(t *testing.T, body []byte, identifier string) sdsTable {
	t.Helper()
	if len(body) < 12 || !flatbuffers.BufferHasIdentifier(body[flatbuffers.SizeUint32:], identifier) {
		t.Fatalf("body is not a size-prefixed %s FlatBuffer: %q", identifier, body[:min(len(body), 16)])
	}
	return sdsTable{flatbuffers.Table{Bytes: body, Pos: flatbuffers.GetUOffsetT(body[flatbuffers.SizeUint32:]) + flatbuffers.SizeUint32}}
}

// field is the slot's absolute position, 0 when the field is absent.
func (d sdsTable) field(slot int) flatbuffers.UOffsetT {
	if o := flatbuffers.UOffsetT(d.tab.Offset(flatbuffers.VOffsetT(4 + 2*slot))); o != 0 {
		return o + d.tab.Pos
	}
	return 0
}

func (d sdsTable) str(slot int) string {
	if at := d.field(slot); at != 0 {
		return string(d.tab.ByteVector(at))
	}
	return ""
}

func (d sdsTable) bytes(slot int) []byte {
	if at := d.field(slot); at != 0 {
		return d.tab.ByteVector(at)
	}
	return nil
}

func (d sdsTable) byte(slot int) byte {
	if at := d.field(slot); at != 0 {
		return d.tab.GetByte(at)
	}
	return 0
}

func (d sdsTable) flag(slot int) bool { return d.byte(slot) != 0 }

func (d sdsTable) u32(slot int) uint32 {
	if at := d.field(slot); at != 0 {
		return d.tab.GetUint32(at)
	}
	return 0
}

func (d sdsTable) i64(slot int) int64 {
	if at := d.field(slot); at != 0 {
		return d.tab.GetInt64(at)
	}
	return 0
}

func (d sdsTable) u64(slot int) uint64 {
	if at := d.field(slot); at != 0 {
		return d.tab.GetUint64(at)
	}
	return 0
}

// vector is a vector field's element start and length (Vector / VectorLen
// take the table-relative offset).
func (d sdsTable) vector(slot int) (flatbuffers.UOffsetT, int) {
	at := d.field(slot)
	if at == 0 {
		return 0, 0
	}
	rel := at - d.tab.Pos
	return d.tab.Vector(rel), d.tab.VectorLen(rel)
}

// tables is a vector-of-tables field.
func (d sdsTable) tables(slot int) []sdsTable {
	start, n := d.vector(slot)
	out := make([]sdsTable, n)
	for i := range out {
		elem := start + flatbuffers.UOffsetT(i)*flatbuffers.SizeUOffsetT
		out[i] = sdsTable{flatbuffers.Table{Bytes: d.tab.Bytes, Pos: d.tab.Indirect(elem)}}
	}
	return out
}

// strs is a vector-of-strings field.
func (d sdsTable) strs(slot int) []string {
	start, n := d.vector(slot)
	out := make([]string, n)
	for i := range out {
		out[i] = string(d.tab.ByteVector(start + flatbuffers.UOffsetT(i)*flatbuffers.SizeUOffsetT))
	}
	return out
}
