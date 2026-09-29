// Package recordalign places a stored SDS record where a stock FlatBuffers
// verifier accepts it. It works at the read boundary and never changes the
// record's bytes, so CIDs, signatures and dedupe identity are untouched
// (owner decision 2026-09-29: align at read time).
//
// # The rule
//
// A FlatBuffers builder writes back to front and aligns every scalar
// relative to the END of its buffer. Finish pads the whole finished buffer,
// size prefix included, to the builder's largest alignment. A stock verifier
// (C++ flatbuffers::Verifier, which flatc and the FlatSQL engine embed, and
// Rust's) checks each scalar's alignment relative to the START of the buffer
// it is handed, not its address. A record verifies only when the verifier's
// start sits where the builder's start was, modulo 8.
//
// The node stores the canonical record: the bare finished buffer, file
// identifier at byte 4. A publisher that finished with a size prefix has it
// removed at publish (api.PublishHandler.canonicalRecordBytes). For a build
// with 8-byte fields that leaves them at 4 mod 8 from the record's first
// byte, and the record's length at 4 mod 8. A bare build keeps both at 0
// mod 8. So the length alone locates the record's alignment origin:
//
//	len%8 == 0: the origin is the record's first byte. Verify it bare.
//	len%8 == 4: the origin is 4 bytes earlier. Verify [u32 len][record]
//	            size-prefixed. That frame is, byte for byte, the builder's
//	            original output, and it is the frame every node stream,
//	            dataset shard and datasync page already carries.
//
// A record with no 8-byte fields verifies at either origin. Copying a
// len%8 == 4 record to an 8-aligned address does not help: the check is
// relative to the buffer the verifier is given.
//
// The rule is for bare stored records (file identifier at byte 4). A record
// an internal writer stored with its builder's own size prefix (identifier
// at byte 8: PNM, the local EPM) is at its origin already and verifies
// size-prefixed as stored; sds.Validator tells the two apart before calling
// Record.
//
// Measured on the dev node's 893,042 stored records (2026-09-29,
// sds.TestStoredRecordAlignmentOnStore): every record verifies at the origin
// this rule gives it; 816,559 (all OMM and MPE, 254,586 CAT) fail at their
// first byte.
//
// In-process Go reads of record fields (the generated Go bindings and
// encoding/binary) have no alignment requirement and need none of this.
package recordalign

import (
	"encoding/binary"
	"unsafe"
)

// PrefixLen is the length of the u32 size prefix a stripped record is
// verified behind.
const PrefixLen = 4

// OriginOffset is how many bytes before a stored record of length n its
// alignment origin sits: 4 when n is 4 mod 8 (a size-prefixed build with its
// prefix stripped), else 0.
func OriginOffset(n int) int {
	if n%8 == PrefixLen {
		return PrefixLen
	}
	return 0
}

// View is a record placed for a stock verifier.
type View struct {
	// Buf is what the verifier is handed: the record itself, or the record
	// behind its u32 length.
	Buf []byte
	// SizePrefixed reports that Buf is [u32 len][record]: use the verifier's
	// size-prefixed entry point (flatc: FlatcSizePrefixed).
	SizePrefixed bool
	// Copied reports that Buf is a new 8-aligned buffer (the record could not
	// be verified where it lay).
	Copied bool
}

// Record returns the verifier view of a bare stored record. It is the record
// itself when its origin is its first byte, and otherwise one copy of it
// behind its u32 length in a new 8-aligned buffer (len+4 bytes).
func Record(record []byte) View {
	if OriginOffset(len(record)) == 0 {
		return View{Buf: record}
	}
	buf := Aligned(PrefixLen + len(record))
	binary.LittleEndian.PutUint32(buf, uint32(len(record)))
	copy(buf[PrefixLen:], record)
	return View{Buf: buf, SizePrefixed: true, Copied: true}
}

// Frame returns the verifier view of a record carried in exactly one frame
// [u32 len][record], without copying: the frame when the record's origin is
// the frame's start, else the record. ok is false when frame is not exactly
// one frame.
func Frame(frame []byte) (View, bool) {
	if len(frame) < PrefixLen || int64(binary.LittleEndian.Uint32(frame)) != int64(len(frame)-PrefixLen) {
		return View{}, false
	}
	if OriginOffset(len(frame)-PrefixLen) == PrefixLen {
		return View{Buf: frame, SizePrefixed: true}, true
	}
	return View{Buf: frame[PrefixLen:]}, true
}

// Aligned returns n zero bytes whose first byte is 8-aligned in memory (a
// plain make([]byte) may land small buffers at 4 mod 8).
func Aligned(n int) []byte {
	if n <= 0 {
		return []byte{}
	}
	words := make([]uint64, (n+7)/8)
	return unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), n)
}
