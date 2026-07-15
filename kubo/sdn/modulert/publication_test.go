package modulert

import (
	"bytes"
	"encoding/binary"
	"testing"
)

var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

func appendPublicationTrailer(payload, rec []byte) []byte {
	out := make([]byte, 0, len(payload)+len(rec)+publicationTrailerFooterLength)
	out = append(out, payload...)
	out = append(out, rec...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(rec)))
	return append(out, publicationTrailerMagic...)
}

func TestStripPublicationTrailerRemovesTrailer(t *testing.T) {
	rec := bytes.Repeat([]byte{0xab}, 64)
	protected := appendPublicationTrailer(wasmHeader, rec)
	if !HasPublicationTrailer(protected) {
		t.Fatalf("expected trailer detection")
	}
	got := StripPublicationTrailer(protected)
	if !bytes.Equal(got, wasmHeader) {
		t.Fatalf("stripped payload mismatch: %x", got)
	}
}

func TestStripPublicationTrailerLeavesPlainWasm(t *testing.T) {
	plain := append(append([]byte{}, wasmHeader...), 1, 2, 3, 4)
	if got := StripPublicationTrailer(plain); !bytes.Equal(got, plain) {
		t.Fatalf("plain wasm modified")
	}
	if HasPublicationTrailer(plain) {
		t.Fatalf("false trailer detection")
	}
}

func TestStripPublicationTrailerRejectsInconsistentFooter(t *testing.T) {
	bogus := make([]byte, 16)
	copy(bogus, wasmHeader)
	binary.LittleEndian.PutUint32(bogus[8:], 4096) // REC longer than the file
	copy(bogus[12:], publicationTrailerMagic)
	if got := StripPublicationTrailer(bogus); !bytes.Equal(got, bogus) {
		t.Fatalf("inconsistent footer must be ignored")
	}
}

func TestStripPublicationTrailerShortInput(t *testing.T) {
	tiny := []byte{0x24, 0x52, 0x45}
	if got := StripPublicationTrailer(tiny); !bytes.Equal(got, tiny) {
		t.Fatalf("short input modified")
	}
}
