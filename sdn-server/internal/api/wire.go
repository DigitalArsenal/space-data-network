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
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
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

	framePrefixLength     = syncwire.FramePrefixLength
	frameIdentifierOffset = syncwire.FrameIdentifierOffset
	frameIdentifierLength = syncwire.FrameIdentifierLength
)

// The $QRP kit lives in internal/syncwire (shared with the libp2p lanes);
// these names keep the api callers unchanged.
const (
	QRPKindRequest = syncwire.QRPKindRequest
	QRPKindPage    = syncwire.QRPKindPage
	QRPKindError   = syncwire.QRPKindError

	QRPStatusOk      = syncwire.QRPStatusOk
	QRPStatusPartial = syncwire.QRPStatusPartial
	QRPStatusBusy    = syncwire.QRPStatusBusy
	QRPStatusCold    = syncwire.QRPStatusCold
	QRPStatusError   = syncwire.QRPStatusError
)

type (
	QRPFields      = syncwire.QRPFields
	QRPColumnField = syncwire.QRPColumnField
	QRPCell        = syncwire.QRPCell
	QRPTableField  = syncwire.QRPTableField
)

var (
	BuildQRP        = syncwire.BuildQRP
	ParseQRP        = syncwire.ParseQRP
	FrameIdentifier = syncwire.FrameIdentifier
	SplitFrames     = syncwire.SplitFrames
)

// enumOf converts a schema ordinal into the generated (unexported) enum type
// named by one of the published EnumValues* maps.
func enumOf[T ~int8](_ map[string]T, n int8) T { return T(n) }

// $QRP QRPValue KIND values (qrpValueKind) used by the api row builders.
const (
	qrpValueKindNull int8 = 0
	qrpValueKindInt  int8 = 2
	qrpValueKindText int8 = 4
)

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
	writeStatusFrame(w, status, QRPStatusError, code, msg, retryAfter)
}

// writeStatusFrame is WriteErrorFrame with an explicit $QRP STATUS (Busy,
// Cold or Error).
func writeStatusFrame(w http.ResponseWriter, status int, qrpStatus int8, code, msg string, retryAfter time.Duration) {
	hdr := map[string]string{"Cache-Control": "no-store"}
	if retryAfter > 0 {
		hdr["Retry-After"] = strconv.FormatInt(int64((retryAfter+time.Second-1)/time.Second), 10)
	}
	WriteFrameStream(w, status, [][]byte{syncwire.ErrorFrame(qrpStatus, code, msg, status, retryAfter)}, hdr)
}

// writeFailure answers err classified the way every transport answers it
// (syncwire.Classify).
func writeFailure(w http.ResponseWriter, err error) {
	f := syncwire.Classify(err)
	writeStatusFrame(w, f.HTTPStatus, f.Status, f.Code, err.Error(), f.RetryAfter)
}
