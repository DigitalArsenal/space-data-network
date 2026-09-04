package api

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWireBuildQRPRoundTripsEveryField(t *testing.T) {
	in := QRPFields{
		Kind:           QRPKindPage,
		Status:         QRPStatusPartial,
		ErrorCode:      "debounce",
		Message:        "Next eligible at 2026-09-03T12:00:00Z",
		RetryAfterMs:   4500,
		SchemaName:     "OMM.fbs",
		FileIdentifier: "$OMM",
		ProviderID:     "space-data-network-02",
		SourceName:     "celestrak-gp",
		BatchID:        "batch-7",
		ProducerPeerID: "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U",
		OriginID:       "celestrak.org",
		FromEpochMs:    1700000000000,
		ToEpochMs:      1700003600000,
		Page:           3,
		Limit:          2000,
		Cursor:         "c-3",
		NextCursor:     "c-4",
		RecordCount:    1999,
		TotalCount:     123456789,
		Scanned:        5000,
		Stored:         123456789,
		Partial:        true,
		Truncated:      true,
		AsOfMs:         1756900000000,
		Stale:          true,
		ElapsedMs:      42,
		GeneratedAtMs:  1756900001000,
		Etag:           `"abc"`,
		CID:            "bafy-manifest",
		ArchiveID:      "archive-omm-20260903T120000Z",
	}
	frame := BuildQRP(in)
	if got := FrameIdentifier(frame); got != "$QRP" {
		t.Fatalf("identifier = %q, want $QRP", got)
	}
	if int(binary.LittleEndian.Uint32(frame[:4]))+4 != len(frame) {
		t.Fatalf("size prefix %d does not cover the %d-byte frame", binary.LittleEndian.Uint32(frame[:4]), len(frame))
	}
	q, err := ParseQRP(frame)
	if err != nil {
		t.Fatalf("ParseQRP: %v", err)
	}
	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"KIND", int8(q.KIND()), in.Kind},
		{"STATUS", int8(q.STATUS()), in.Status},
		{"ERROR_CODE", string(q.ErrorCode()), in.ErrorCode},
		{"MESSAGE", string(q.MESSAGE()), in.Message},
		{"RETRY_AFTER_MS", q.RetryAfterMs(), in.RetryAfterMs},
		{"SCHEMA_NAME", string(q.SchemaName()), in.SchemaName},
		{"FILE_IDENTIFIER", string(q.FileIdentifier()), in.FileIdentifier},
		{"PROVIDER_ID", string(q.ProviderId()), in.ProviderID},
		{"SOURCE_NAME", string(q.SourceName()), in.SourceName},
		{"BATCH_ID", string(q.BatchId()), in.BatchID},
		{"PRODUCER_PEER_ID", string(q.ProducerPeerId()), in.ProducerPeerID},
		{"ORIGIN_ID", string(q.OriginId()), in.OriginID},
		{"FROM_EPOCH_MS", q.FromEpochMs(), in.FromEpochMs},
		{"TO_EPOCH_MS", q.ToEpochMs(), in.ToEpochMs},
		{"PAGE", q.PAGE(), in.Page},
		{"LIMIT", q.LIMIT(), in.Limit},
		{"CURSOR", string(q.CURSOR()), in.Cursor},
		{"NEXT_CURSOR", string(q.NextCursor()), in.NextCursor},
		{"RECORD_COUNT", q.RecordCount(), in.RecordCount},
		{"TOTAL_COUNT", q.TotalCount(), in.TotalCount},
		{"SCANNED", q.SCANNED(), in.Scanned},
		{"STORED", q.STORED(), in.Stored},
		{"PARTIAL", q.PARTIAL(), in.Partial},
		{"TRUNCATED", q.TRUNCATED(), in.Truncated},
		{"AS_OF_MS", q.AsOfMs(), in.AsOfMs},
		{"STALE", q.STALE(), in.Stale},
		{"ELAPSED_MS", q.ElapsedMs(), in.ElapsedMs},
		{"GENERATED_AT_MS", q.GeneratedAtMs(), in.GeneratedAtMs},
		{"ETAG", string(q.ETAG()), in.Etag},
		{"CID", string(q.CID()), in.CID},
		{"ARCHIVE_ID", string(q.ArchiveId()), in.ArchiveID},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// A bare (unprefixed) root is tolerated on decode.
	bare, err := ParseQRP(frame[4:])
	if err != nil {
		t.Fatalf("ParseQRP(bare): %v", err)
	}
	if string(bare.ArchiveId()) != in.ArchiveID {
		t.Fatalf("bare decode ARCHIVE_ID = %q", string(bare.ArchiveId()))
	}
	if _, err := ParseQRP([]byte("not a frame at all")); err == nil {
		t.Fatal("ParseQRP accepted a non-$QRP buffer")
	}
}

func TestWireReadFramesSplitsAndRejectsTruncation(t *testing.T) {
	a := BuildQRP(QRPFields{Kind: QRPKindRequest, SchemaName: "A.fbs"})
	b := BuildQRP(QRPFields{Kind: QRPKindPage, SchemaName: "B.fbs", RecordCount: 2})
	c := BuildQRP(QRPFields{Kind: QRPKindError, ErrorCode: "c"})
	stream := append(append(append([]byte{}, a...), b...), c...)

	frames, err := ReadFrames(bytes.NewReader(stream), MaxRequestFrameBytes)
	if err != nil {
		t.Fatalf("ReadFrames: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	for i, want := range [][]byte{a, b, c} {
		if !bytes.Equal(frames[i], want) {
			t.Fatalf("frame %d differs from its input (prefix must be kept)", i)
		}
		if FrameIdentifier(frames[i]) != "$QRP" {
			t.Fatalf("frame %d identifier = %q", i, FrameIdentifier(frames[i]))
		}
	}
	q, err := ParseQRP(frames[1])
	if err != nil || string(q.SchemaName()) != "B.fbs" || q.RecordCount() != 2 {
		t.Fatalf("frame 1 decoded wrong: err=%v", err)
	}

	// A prefix that promises more bytes than the body carries is an error.
	if _, err := ReadFrames(bytes.NewReader(stream[:len(stream)-5]), MaxRequestFrameBytes); err == nil {
		t.Fatal("ReadFrames accepted a truncated last frame")
	}
	// A dangling prefix fragment is an error too.
	if _, err := ReadFrames(bytes.NewReader(append(append([]byte{}, a...), 0x10, 0x00)), MaxRequestFrameBytes); err == nil {
		t.Fatal("ReadFrames accepted a dangling prefix fragment")
	}
	// The byte budget is enforced.
	if _, err := ReadFrames(bytes.NewReader(stream), int64(len(a))); err == nil {
		t.Fatal("ReadFrames accepted a stream over the limit")
	}
	// An empty body is an empty stream, not an error.
	if frames, err := ReadFrames(bytes.NewReader(nil), 0); err != nil || len(frames) != 0 {
		t.Fatalf("empty stream: frames=%d err=%v", len(frames), err)
	}
}

func TestWireWriteErrorFrameIsOneQRPErrorFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteErrorFrame(rec, http.StatusTooManyRequests, "debounce", "Next eligible at 2026-09-03T15:00:00Z", 2500*time.Millisecond)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != StreamContentType {
		t.Fatalf("Content-Type = %q", ct)
	}
	if f := rec.Header().Get(StreamFormatHeader); f != StreamFormat {
		t.Fatalf("%s = %q", StreamFormatHeader, f)
	}
	if n := rec.Header().Get(StreamRecordCountHeader); n != "1" {
		t.Fatalf("%s = %q, want 1", StreamRecordCountHeader, n)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "3" {
		t.Fatalf("Retry-After = %q, want 3 (2.5 s rounded up)", ra)
	}
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil || len(frames) != 1 {
		t.Fatalf("body is not exactly one frame: n=%d err=%v", len(frames), err)
	}
	q, err := ParseQRP(frames[0])
	if err != nil {
		t.Fatalf("ParseQRP: %v", err)
	}
	if int8(q.KIND()) != QRPKindError || int8(q.STATUS()) != QRPStatusError {
		t.Fatalf("KIND/STATUS = %d/%d, want Error/Error", q.KIND(), q.STATUS())
	}
	if string(q.ErrorCode()) != "debounce" {
		t.Fatalf("ERROR_CODE = %q", string(q.ErrorCode()))
	}
	if !strings.HasPrefix(string(q.MESSAGE()), "Next eligible at ") {
		t.Fatalf("MESSAGE = %q", string(q.MESSAGE()))
	}
	if q.RetryAfterMs() != 2500 {
		t.Fatalf("RETRY_AFTER_MS = %d", q.RetryAfterMs())
	}

	// No retry hint: no Retry-After header, RETRY_AFTER_MS 0.
	rec = httptest.NewRecorder()
	WriteErrorFrame(rec, http.StatusNotFound, "not_found", "This node does not run a fetch for this source.", 0)
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("Retry-After set without a retry hint")
	}
	frames, _ = SplitFrames(rec.Body.Bytes())
	q, _ = ParseQRP(frames[0])
	if q.RetryAfterMs() != 0 || string(q.ErrorCode()) != "not_found" {
		t.Fatalf("404 frame: code=%q retry=%d", string(q.ErrorCode()), q.RetryAfterMs())
	}
}

func TestWireWriteFrameStreamMirrorsHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	a := BuildQRP(QRPFields{Kind: QRPKindPage})
	b := BuildQRP(QRPFields{Kind: QRPKindPage, Page: 1})
	WriteFrameStream(rec, http.StatusOK, [][]byte{a, b}, map[string]string{
		StreamSchemaHeader:     "OMM.fbs",
		StreamNextCursorHeader: "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get(StreamRecordCountHeader) != "2" {
		t.Fatalf("record count = %q", rec.Header().Get(StreamRecordCountHeader))
	}
	if rec.Header().Get(StreamSchemaHeader) != "OMM.fbs" {
		t.Fatalf("schema header = %q", rec.Header().Get(StreamSchemaHeader))
	}
	if rec.Header().Get(StreamNextCursorHeader) != "" {
		t.Fatal("empty mirror header must not be set")
	}
	if rec.Body.Len() != len(a)+len(b) {
		t.Fatalf("body = %d bytes, want %d", rec.Body.Len(), len(a)+len(b))
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(a)+len(b)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(a)+len(b))
	}
}
