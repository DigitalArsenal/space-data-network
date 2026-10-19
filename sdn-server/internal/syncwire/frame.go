package syncwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// MaxFrameBytes bounds one frame read off a stream: a request, or a page
// header (a page of SEGMENT rows included). Record and shard payload bytes
// that follow a header are counted by the header, not by this bound.
const MaxFrameBytes = 32 << 20

// ErrNotAuthorized marks a refusal of the read surface; Classify answers it
// 403 not_authorized.
var ErrNotAuthorized = errors.New("not authorized")

// SplitFrames splits an aligned size-prefixed stream into frames that KEEP
// their length prefix, so each one is a complete size-prefixed buffer.
func SplitFrames(stream []byte) ([][]byte, error) {
	var frames [][]byte
	off := 0
	for off < len(stream) {
		if len(stream)-off < FramePrefixLength {
			return nil, fmt.Errorf("truncated frame prefix at byte %d", off)
		}
		length := int(binary.LittleEndian.Uint32(stream[off : off+FramePrefixLength]))
		end := off + FramePrefixLength + length
		if length < 0 || end > len(stream) {
			return nil, fmt.Errorf("frame at byte %d declares %d bytes but only %d remain", off, length, len(stream)-off-FramePrefixLength)
		}
		frames = append(frames, stream[off:end])
		off = end
	}
	return frames, nil
}

// ReadFrame reads one size-prefixed frame (prefix kept) of at most max
// payload bytes. An empty frame and an oversized one are errors.
func ReadFrame(r io.Reader, max int) ([]byte, error) {
	if max <= 0 {
		max = MaxFrameBytes
	}
	var prefix [FramePrefixLength]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("read frame prefix: %w", err)
	}
	length := binary.LittleEndian.Uint32(prefix[:])
	if length == 0 {
		return nil, errors.New("empty frame")
	}
	if int64(length) > int64(max) {
		return nil, fmt.Errorf("frame of %d bytes exceeds the %d-byte limit", length, max)
	}
	frame := make([]byte, FramePrefixLength+int(length))
	copy(frame, prefix[:])
	if _, err := io.ReadFull(r, frame[FramePrefixLength:]); err != nil {
		return nil, fmt.Errorf("read frame payload: %w", err)
	}
	return frame, nil
}

// ReadRequest reads exactly one $QRP{KIND=Request} frame.
func ReadRequest(r io.Reader) (*QRP.QRP, error) {
	frame, err := ReadFrame(r, MaxFrameBytes)
	if err != nil {
		return nil, err
	}
	q, err := ParseQRP(frame)
	if err != nil {
		return nil, err
	}
	if int8(q.KIND()) != QRPKindRequest {
		return nil, errors.New("a request is one $QRP frame of KIND Request")
	}
	return q, nil
}

// RemoteError is a $QRP{KIND=Error} frame a peer answered with.
type RemoteError struct {
	Status       int8
	Code         string
	Message      string
	RetryAfterMs uint32
	HTTPStatus   uint16
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("peer refused (%s): %.1024q", e.Code, e.Message)
}

// ReadPage reads one reply frame: a $QRP Page is returned with its bytes; a
// $QRP Error frame is returned as a *RemoteError; anything else is an error.
func ReadPage(r io.Reader) (*QRP.QRP, []byte, error) {
	frame, err := ReadFrame(r, MaxFrameBytes)
	if err != nil {
		return nil, nil, err
	}
	q, err := ParseQRP(frame)
	if err != nil {
		return nil, frame, err
	}
	switch int8(q.KIND()) {
	case QRPKindPage:
		return q, frame, nil
	case QRPKindError:
		return nil, frame, &RemoteError{Status: int8(q.STATUS()), Code: string(q.ERROR_CODE()), Message: string(q.MESSAGE()),
			RetryAfterMs: q.RETRY_AFTER_MS(), HTTPStatus: q.HTTP_STATUS()}
	}
	return nil, frame, fmt.Errorf("unexpected $QRP KIND %d in a reply", q.KIND())
}

// Request writes one $QRP{KIND=Request} frame.
func Request(w io.Writer, f QRPFields) error {
	f.Kind = QRPKindRequest
	_, err := w.Write(BuildQRP(f))
	return err
}

// Failure is how one error answers on the wire.
type Failure struct {
	Status     int8
	Code       string
	HTTPStatus int
	RetryAfter time.Duration
}

// Classify maps a read error to its $QRP status, stable code, HTTP status
// and retry hint. HTTP and libp2p answer the same error the same way.
func Classify(err error) Failure {
	switch {
	case errors.Is(err, storage.ErrEngineRebuilding):
		return Failure{QRPStatusBusy, "engine_rebuilding", http.StatusServiceUnavailable, 30 * time.Second}
	case errors.Is(err, storage.ErrSearchIndexBuilding):
		return Failure{QRPStatusCold, "search_index_building", http.StatusServiceUnavailable, 2 * time.Second}
	case errors.Is(err, datasync.ErrResumeCursorExpired):
		return Failure{QRPStatusError, "resume_cursor_expired", http.StatusGone, 0}
	case errors.Is(err, datasync.ErrResumeCursorQuery):
		return Failure{QRPStatusError, "resume_cursor_query_mismatch", http.StatusBadRequest, 0}
	case errors.Is(err, datasync.ErrResumeCursorInvalid):
		return Failure{QRPStatusError, "invalid_resume_cursor", http.StatusBadRequest, 0}
	case errors.Is(err, ErrNotAuthorized):
		return Failure{QRPStatusError, "not_authorized", http.StatusForbidden, 0}
	}
	return Failure{QRPStatusError, "bad_request", http.StatusBadRequest, 0}
}

// ErrorFrame builds the one $QRP{KIND=Error} frame that answers a failure.
func ErrorFrame(status int8, code, msg string, httpStatus int, retryAfter time.Duration) []byte {
	var retryMs uint32
	if retryAfter > 0 {
		ms := retryAfter.Milliseconds()
		if ms > math.MaxUint32 {
			ms = math.MaxUint32
		}
		if ms <= 0 {
			ms = 1
		}
		retryMs = uint32(ms)
	}
	return BuildQRP(QRPFields{
		Kind:          QRPKindError,
		Status:        status,
		ErrorCode:     code,
		Message:       msg,
		RetryAfterMs:  retryMs,
		GeneratedAtMs: time.Now().UnixMilli(),
		HTTPStatus:    uint16(httpStatus),
	})
}

// ErrorFrameFor is ErrorFrame for a classified error.
func ErrorFrameFor(err error) []byte {
	f := Classify(err)
	return ErrorFrame(f.Status, f.Code, err.Error(), f.HTTPStatus, f.RetryAfter)
}
