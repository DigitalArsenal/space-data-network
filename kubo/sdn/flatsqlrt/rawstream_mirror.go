package flatsqlrt

// Host-side raw-stream MIRROR cache (loop C.5c near-zero-copy egress).
//
// The engine already caches materialized raw streams in ITS memory keyed by
// (schema, generation, sql, params) — flatsql cc4885c. But every host read
// of a cached artifact still costs one full copy out of linear memory plus
// the engine round-trip on the dedicated thread. This mirror holds the LAST
// FEW materialized streams host-side under the engine's OWN identity — the
// exact (sql, params-blob, queryCacheGeneration) triple its cache keys embed
// — so a repeated read-only query whose generation is unchanged returns the
// previously copied Go buffer with ZERO engine execution and ZERO memory
// copies. This is pure copy elimination, not decision logic: the generation
// counter (bumped by EVERY engine mutator: ingest, markDeleted,
// clearTombstones, DML/DDL through query(), loadAndRebuild, ...) is the
// engine's own staleness authority, and hitting the mirror is byte-for-byte
// equivalent to re-reading the engine's cached artifact.
//
// Caveat shared with the engine's own cache: read-only SQL containing
// non-deterministic functions (random(), datetime('now')) would be served
// stale between mutations — the engine's raw-stream cache has the identical
// exposure by design; the raw-stream contract is deterministic
// SELECT-_data-shaped queries.
//
// Each mirror entry also carries the stream's word-folded FNV-1a 64 content
// hash and its size-prefixed frame count (both computed ONCE per
// materialization, on the cache-miss path) — the metadata "deliver":"ref"
// hostcall responses expose so guests can build entity tags and record-count
// headers without the bytes ever entering their memory.

import (
	"encoding/binary"
	"strconv"
)

const (
	// rawMirrorMaxEntries bounds the number of mirrored streams per database.
	rawMirrorMaxEntries = 16
	// rawMirrorMaxTotalBytes bounds the mirror's total payload budget per
	// database (a single stream larger than this is returned uncached).
	rawMirrorMaxTotalBytes = 256 * 1024 * 1024
)

type rawMirrorEntry struct {
	key    string
	stream *RawStream
}

// rawStreamMirror is a tiny LRU. It is NOT internally locked: all access
// happens under the Runtime's module lock, exactly like the engine state it
// mirrors.
type rawStreamMirror struct {
	entries    map[string]*rawMirrorEntry
	order      []string // most-recent first
	totalBytes int
}

func newRawStreamMirror() *rawStreamMirror {
	return &rawStreamMirror{entries: make(map[string]*rawMirrorEntry)}
}

func rawMirrorKey(sql string, paramsBlob []byte, generation uint64) string {
	return strconv.FormatUint(generation, 10) + "\x1f" + sql + "\x1f" + string(paramsBlob)
}

func (m *rawStreamMirror) touch(key string) {
	for i, k := range m.order {
		if k == key {
			copy(m.order[1:i+1], m.order[:i])
			m.order[0] = key
			return
		}
	}
}

func (m *rawStreamMirror) get(key string) *RawStream {
	entry, ok := m.entries[key]
	if !ok {
		return nil
	}
	m.touch(key)
	return entry.stream
}

func (m *rawStreamMirror) put(key string, stream *RawStream) {
	if len(stream.Bytes) > rawMirrorMaxTotalBytes {
		return
	}
	if existing, ok := m.entries[key]; ok {
		m.totalBytes += len(stream.Bytes) - len(existing.stream.Bytes)
		existing.stream = stream
		m.touch(key)
	} else {
		m.entries[key] = &rawMirrorEntry{key: key, stream: stream}
		m.order = append([]string{key}, m.order...)
		m.totalBytes += len(stream.Bytes)
	}
	for len(m.order) > 0 &&
		(len(m.entries) > rawMirrorMaxEntries || m.totalBytes > rawMirrorMaxTotalBytes) {
		evict := m.order[len(m.order)-1]
		m.order = m.order[:len(m.order)-1]
		if entry, ok := m.entries[evict]; ok {
			m.totalBytes -= len(entry.stream.Bytes)
			delete(m.entries, evict)
		}
	}
}

// FNV1a64WordFolded is the canonical content hash of aligned raw streams:
// FNV-1a 64 folded over 8-byte little-endian words, tail bytes folded
// individually. It MUST stay bit-identical to foundation/decision-gate's
// fnv1a64_etag hasher and the SDK's fnv1a64Hex (src/http) — reference-mode
// entity tags equal hashed-stream entity tags only because all three agree.
// NOTE the offset basis 1469598103934665603 is the deployed decision-gate
// constant (the textbook basis without its trailing "7"); the tag is opaque,
// matching bit-for-bit is what matters.
func FNV1a64WordFolded(b []byte) uint64 {
	const prime = 1099511628211
	hash := uint64(1469598103934665603)
	i := 0
	for ; i+8 <= len(b); i += 8 {
		hash ^= binary.LittleEndian.Uint64(b[i:])
		hash *= prime
	}
	for ; i < len(b); i++ {
		hash ^= uint64(b[i])
		hash *= prime
	}
	return hash
}

// countStreamFrames counts size-prefixed frames, skipping zero-length
// prefixes as padding — the same rule the modules' count_stream_frames
// applies for x-sdn-record-count. Returns -1 when the framing is malformed
// (callers then omit the count, never report a wrong one).
func countStreamFrames(b []byte) int {
	count := 0
	offset := 0
	for offset < len(b) {
		if len(b)-offset < 4 {
			return -1
		}
		frameSize := int(binary.LittleEndian.Uint32(b[offset:]))
		offset += 4
		if frameSize == 0 {
			continue
		}
		if frameSize > len(b)-offset {
			return -1
		}
		offset += frameSize
		count++
	}
	return count
}
