package api

// Wire kit for the FlatBuffer-only dashboard lanes (fbcs program, SDS
// 1.208.0). Every new dashboard body is an aligned size-prefixed stream
// [u32le len][root buffer with its file identifier at bytes 4..8]* served as
// application/vnd.sdn.flatbuffers.stream; every 4xx/5xx body is exactly one
// $QRP{KIND=Error} frame. Nothing here projects a record to JSON.
//
// Only the PUBLISHED lib/go module is used (published-deps law); the generated
// enum types are unexported, so values travel here as the untyped constants
// the schema declares and cross into the generated types through enumOf.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	// StreamContentType marks a body as a raw size-prefixed FlatBuffer frame
	// stream, never JSON.
	StreamContentType = "application/vnd.sdn.flatbuffers.stream"
	// StreamFormat names the framing: a 4-byte little-endian length prefix
	// ahead of every root buffer (the /ws/status and /api/v1/dashboard/stats
	// framing).
	StreamFormat = "flatsql-size-prefixed-le-u32"
	// StreamFormatHeader mirrors StreamFormat on every frame response.
	StreamFormatHeader = "X-SDN-Stream-Format"
	// StreamRecordCountHeader mirrors the number of frames in the body.
	StreamRecordCountHeader = "X-SDN-Record-Count"
	// StreamSchemaHeader mirrors the store-form schema name of a single-type
	// stream ("OMM.fbs").
	StreamSchemaHeader = "X-SDN-Schema"
	// StreamNextCursorHeader mirrors the $QRP NEXT_CURSOR of a paged stream.
	StreamNextCursorHeader = "X-SDN-Next-Cursor"

	// MaxRequestFrameBytes bounds a frame-stream request body.
	MaxRequestFrameBytes int64 = 16 << 20

	// framePrefixLength is the u32le length prefix.
	framePrefixLength = 4
	// frameIdentifierOffset is where the file identifier sits inside a
	// prefixed frame: prefix (4) + root uoffset (4).
	frameIdentifierOffset = 8
	// frameIdentifierLength is the FlatBuffers file identifier width.
	frameIdentifierLength = 4
)

// $QRP KIND values (qrpKind).
const (
	QRPKindRequest int8 = 0
	QRPKindPage    int8 = 1
	QRPKindError   int8 = 2
)

// $QRP STATUS values (qrpStatus).
const (
	QRPStatusOk      int8 = 0
	QRPStatusPartial int8 = 1
	QRPStatusBusy    int8 = 2
	QRPStatusCold    int8 = 3
	QRPStatusError   int8 = 4
)

// enumOf converts a schema ordinal into the generated (unexported) enum type
// named by one of the published EnumValues* maps.
func enumOf[T ~int8](_ map[string]T, n int8) T { return T(n) }

// QRPFields is every $QRP field the Go lanes write. Zero values are omitted
// from the buffer (FlatBuffers defaults), so a Page header and an Error frame
// share one builder.
type QRPFields struct {
	Kind           int8
	Status         int8
	ErrorCode      string
	Message        string
	RetryAfterMs   uint32
	SchemaName     string
	FileIdentifier string
	ProviderID     string
	SourceName     string
	BatchID        string
	ProducerPeerID string
	OriginID       string
	FromEpochMs    int64
	ToEpochMs      int64
	Page           uint32
	Limit          uint32
	Cursor         string
	NextCursor     string
	RecordCount    uint32
	TotalCount     uint64
	Scanned        uint64
	Stored         uint64
	Partial        bool
	Truncated      bool
	AsOfMs         int64
	Stale          bool
	ElapsedMs      uint32
	GeneratedAtMs  int64
	Etag           string
	CID            string
	ArchiveID      string
}

// BuildQRP serializes one size-prefixed $QRP frame.
func BuildQRP(f QRPFields) []byte {
	b := flatbuffers.NewBuilder(512)
	str := func(s string) flatbuffers.UOffsetT {
		if s == "" {
			return 0
		}
		return b.CreateString(s)
	}
	errorCode := str(f.ErrorCode)
	message := str(f.Message)
	schemaName := str(f.SchemaName)
	fileIdentifier := str(f.FileIdentifier)
	providerID := str(f.ProviderID)
	sourceName := str(f.SourceName)
	batchID := str(f.BatchID)
	producerPeerID := str(f.ProducerPeerID)
	originID := str(f.OriginID)
	cursor := str(f.Cursor)
	nextCursor := str(f.NextCursor)
	etag := str(f.Etag)
	cid := str(f.CID)
	archiveID := str(f.ArchiveID)

	QRP.QRPStart(b)
	QRP.QRPAddKIND(b, enumOf(QRP.EnumValuesqrpKind, f.Kind))
	QRP.QRPAddSTATUS(b, enumOf(QRP.EnumValuesqrpStatus, f.Status))
	if errorCode != 0 {
		QRP.QRPAddERROR_CODE(b, errorCode)
	}
	if message != 0 {
		QRP.QRPAddMESSAGE(b, message)
	}
	QRP.QRPAddRETRY_AFTER_MS(b, f.RetryAfterMs)
	if schemaName != 0 {
		QRP.QRPAddSCHEMA_NAME(b, schemaName)
	}
	if fileIdentifier != 0 {
		QRP.QRPAddFILE_IDENTIFIER(b, fileIdentifier)
	}
	if providerID != 0 {
		QRP.QRPAddPROVIDER_ID(b, providerID)
	}
	if sourceName != 0 {
		QRP.QRPAddSOURCE_NAME(b, sourceName)
	}
	if batchID != 0 {
		QRP.QRPAddBATCH_ID(b, batchID)
	}
	if producerPeerID != 0 {
		QRP.QRPAddPRODUCER_PEER_ID(b, producerPeerID)
	}
	if originID != 0 {
		QRP.QRPAddORIGIN_ID(b, originID)
	}
	QRP.QRPAddFROM_EPOCH_MS(b, f.FromEpochMs)
	QRP.QRPAddTO_EPOCH_MS(b, f.ToEpochMs)
	QRP.QRPAddPAGE(b, f.Page)
	QRP.QRPAddLIMIT(b, f.Limit)
	if cursor != 0 {
		QRP.QRPAddCURSOR(b, cursor)
	}
	if nextCursor != 0 {
		QRP.QRPAddNEXT_CURSOR(b, nextCursor)
	}
	QRP.QRPAddRECORD_COUNT(b, f.RecordCount)
	QRP.QRPAddTOTAL_COUNT(b, f.TotalCount)
	QRP.QRPAddSCANNED(b, f.Scanned)
	QRP.QRPAddSTORED(b, f.Stored)
	QRP.QRPAddPARTIAL(b, f.Partial)
	QRP.QRPAddTRUNCATED(b, f.Truncated)
	QRP.QRPAddAS_OF_MS(b, f.AsOfMs)
	QRP.QRPAddSTALE(b, f.Stale)
	QRP.QRPAddELAPSED_MS(b, f.ElapsedMs)
	QRP.QRPAddGENERATED_AT_MS(b, f.GeneratedAtMs)
	if etag != 0 {
		QRP.QRPAddETAG(b, etag)
	}
	if cid != 0 {
		QRP.QRPAddCID(b, cid)
	}
	if archiveID != 0 {
		QRP.QRPAddARCHIVE_ID(b, archiveID)
	}
	root := QRP.QRPEnd(b)
	QRP.FinishSizePrefixedQRPBuffer(b, root)
	return b.FinishedBytes()
}

// ParseQRP decodes a $QRP frame. A size-prefixed frame is the wire form; a
// bare finished buffer is tolerated for producers that call Finish directly.
func ParseQRP(frame []byte) (*QRP.QRP, error) {
	switch {
	case len(frame) >= frameIdentifierOffset+frameIdentifierLength && QRP.SizePrefixedQRPBufferHasIdentifier(frame):
		if int64(binary.LittleEndian.Uint32(frame[:framePrefixLength]))+framePrefixLength > int64(len(frame)) {
			return nil, errors.New("$QRP frame is shorter than its length prefix")
		}
		return QRP.GetSizePrefixedRootAsQRP(frame, 0), nil
	case len(frame) >= framePrefixLength+frameIdentifierLength && QRP.QRPBufferHasIdentifier(frame):
		return QRP.GetRootAsQRP(frame, 0), nil
	}
	return nil, fmt.Errorf("frame is not a $QRP buffer (identifier %q)", FrameIdentifier(frame))
}

// FrameIdentifier returns the file identifier of a size-prefixed frame
// (bytes 8..12), or "" when the frame is too short to carry one.
func FrameIdentifier(frame []byte) string {
	if len(frame) < frameIdentifierOffset+frameIdentifierLength {
		return ""
	}
	return string(frame[frameIdentifierOffset : frameIdentifierOffset+frameIdentifierLength])
}

// SplitFrames splits an aligned size-prefixed stream into frames that KEEP
// their length prefix, so each one is a complete size-prefixed buffer.
func SplitFrames(stream []byte) ([][]byte, error) {
	var frames [][]byte
	off := 0
	for off < len(stream) {
		if len(stream)-off < framePrefixLength {
			return nil, fmt.Errorf("truncated frame prefix at byte %d", off)
		}
		length := int(binary.LittleEndian.Uint32(stream[off : off+framePrefixLength]))
		end := off + framePrefixLength + length
		if length < 0 || end > len(stream) {
			return nil, fmt.Errorf("frame at byte %d declares %d bytes but only %d remain", off, length, len(stream)-off-framePrefixLength)
		}
		frames = append(frames, stream[off:end])
		off = end
	}
	return frames, nil
}

// ReadFrames reads a whole frame stream (at most max bytes) and splits it.
// Frames are returned WITH their length prefix. A body longer than max is an
// error, never a silent truncation.
func ReadFrames(r io.Reader, max int64) ([][]byte, error) {
	if max <= 0 {
		max = MaxRequestFrameBytes
	}
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read frame stream: %w", err)
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("frame stream exceeds the %d-byte limit", max)
	}
	return SplitFrames(body)
}

// WriteFrameStream writes frames (already size-prefixed) as one
// application/vnd.sdn.flatbuffers.stream body. hdr adds or overrides mirror
// headers (X-SDN-Schema, X-SDN-Next-Cursor, Retry-After, Content-Disposition
// ...); X-SDN-Record-Count is always len(frames).
func WriteFrameStream(w http.ResponseWriter, status int, frames [][]byte, hdr map[string]string) {
	total := 0
	for _, frame := range frames {
		total += len(frame)
	}
	h := w.Header()
	h.Set("Content-Type", StreamContentType)
	h.Set(StreamFormatHeader, StreamFormat)
	h.Set(StreamRecordCountHeader, strconv.Itoa(len(frames)))
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	for key, value := range hdr {
		if value == "" {
			continue
		}
		h.Set(key, value)
	}
	h.Set("Content-Length", strconv.Itoa(total))
	w.WriteHeader(status)
	for _, frame := range frames {
		if _, err := w.Write(frame); err != nil {
			return
		}
	}
}

// WriteErrorFrame answers a 4xx/5xx with exactly one $QRP{KIND=Error,
// STATUS=Error} frame carrying the code, a plain-language message and the
// retry hint. retryAfter > 0 also sets the Retry-After header (whole seconds,
// rounded up).
func WriteErrorFrame(w http.ResponseWriter, status int, code, msg string, retryAfter time.Duration) {
	var retryMs uint32
	hdr := map[string]string{}
	if retryAfter > 0 {
		ms := retryAfter.Milliseconds()
		if ms > math.MaxUint32 {
			ms = math.MaxUint32
		}
		if ms <= 0 {
			ms = 1
		}
		retryMs = uint32(ms)
		hdr["Retry-After"] = strconv.FormatInt(int64(math.Ceil(retryAfter.Seconds())), 10)
	}
	hdr["Cache-Control"] = "no-store"
	frame := BuildQRP(QRPFields{
		Kind:          QRPKindError,
		Status:        QRPStatusError,
		ErrorCode:     code,
		Message:       msg,
		RetryAfterMs:  retryMs,
		GeneratedAtMs: time.Now().UnixMilli(),
	})
	WriteFrameStream(w, status, [][]byte{frame}, hdr)
}
