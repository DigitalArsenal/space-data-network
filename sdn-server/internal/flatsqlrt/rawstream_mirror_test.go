package flatsqlrt

import (
	"bytes"
	"testing"
)

// TestRawStreamMirror covers the HOST-side mirror (loop C.5c): a repeated
// (sql, params) query with an unchanged engine generation is served from the
// host buffer with zero engine execution; any mutation (generation bump)
// invalidates; the precomputed FNV-1a 64 hash and frame count ride along.
func TestRawStreamMirror(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-raw-mirror")

	original := fixtureBuffer(t)
	if _, err := db.IngestOne(original); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	const sql = "SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?"
	first, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (cold): %v", err)
	}
	if first.MirrorHit {
		t.Fatal("cold query reported MirrorHit=true")
	}
	if first.FNV1a64 != FNV1a64WordFolded(first.Bytes) {
		t.Fatalf("FNV1a64 = %016x, want %016x", first.FNV1a64, FNV1a64WordFolded(first.Bytes))
	}
	if first.FrameCount != 1 {
		t.Fatalf("FrameCount = %d, want 1", first.FrameCount)
	}

	second, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (warm): %v", err)
	}
	if !second.MirrorHit || !second.CacheHit {
		t.Fatalf("warm query MirrorHit=%v CacheHit=%v, want true/true", second.MirrorHit, second.CacheHit)
	}
	if !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatal("mirror hit returned different bytes")
	}
	if second.FNV1a64 != first.FNV1a64 || second.FrameCount != first.FrameCount ||
		second.Rows != first.Rows || second.Columns != first.Columns {
		t.Fatalf("mirror hit metadata differs: %+v vs %+v", second, first)
	}

	// Different params → different mirror key → engine executes.
	other, err := db.QueryRawFlatBufferStream(sql, 12345)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (other params): %v", err)
	}
	if other.MirrorHit {
		t.Fatal("different params must not hit the mirror")
	}
	if other.Rows != 0 {
		t.Fatalf("other params rows = %d, want 0", other.Rows)
	}

	// Mutation bumps the engine generation → the mirror entry is stale by
	// key and can never be served again.
	if _, err := db.IngestOne(original); err != nil {
		t.Fatalf("IngestOne (second): %v", err)
	}
	third, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (post-ingest): %v", err)
	}
	if third.MirrorHit || third.CacheHit {
		t.Fatalf("post-ingest MirrorHit=%v CacheHit=%v, want false/false", third.MirrorHit, third.CacheHit)
	}
	if third.Rows != 2 || third.FrameCount != 2 {
		t.Fatalf("post-ingest rows=%d frames=%d, want 2/2", third.Rows, third.FrameCount)
	}
	if third.FNV1a64 == first.FNV1a64 {
		t.Fatal("two-frame stream hashed identically to one-frame stream")
	}

	// And the new result is mirrored again.
	fourth, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (re-warm): %v", err)
	}
	if !fourth.MirrorHit {
		t.Fatal("re-warm query was not served from the mirror")
	}
	if !bytes.Equal(fourth.Bytes, third.Bytes) {
		t.Fatal("re-warm mirror bytes differ from post-ingest materialization")
	}
}

// TestFNV1a64WordFoldedVectors pins the hash constants (offset basis, prime,
// word folding) against fixed vectors — the SDK's fnv1a64Hex and
// foundation/decision-gate's fnv1a64_etag must produce these same values.
func TestFNV1a64WordFoldedVectors(t *testing.T) {
	if got := FNV1a64WordFolded(nil); got != 0x14650FB0739D0383 {
		t.Fatalf("empty hash = %016x, want the offset basis 14650fb0739d0383", got)
	}
	// One full word + tail.
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	want := func() uint64 {
		const prime = 1099511628211
		h := uint64(1469598103934665603)
		h ^= uint64(0x0807060504030201)
		h *= prime
		h ^= 9
		h *= prime
		return h
	}()
	if got := FNV1a64WordFolded(data); got != want {
		t.Fatalf("hash = %016x, want %016x", got, want)
	}
}

func TestCountStreamFrames(t *testing.T) {
	// Two frames with a zero-length padding prefix between them.
	stream := []byte{
		2, 0, 0, 0, 0xAA, 0xBB,
		0, 0, 0, 0, // zero-length prefix: skipped as padding
		1, 0, 0, 0, 0xCC,
	}
	if got := countStreamFrames(stream); got != 2 {
		t.Fatalf("frames = %d, want 2", got)
	}
	if got := countStreamFrames(nil); got != 0 {
		t.Fatalf("empty frames = %d, want 0", got)
	}
	if got := countStreamFrames([]byte{9, 0, 0, 0, 1}); got != -1 {
		t.Fatalf("truncated stream frames = %d, want -1", got)
	}
}
