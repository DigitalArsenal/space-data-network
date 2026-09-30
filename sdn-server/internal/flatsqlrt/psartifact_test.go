package flatsqlrt

import (
	"encoding/binary"
	"testing"
)

// The embedded partition-store engine is the published release's file: its
// sha256 equals the one the release ships in wasm/integrity.json.
func TestEmbeddedPSThreadsArtifact(t *testing.T) {
	if got := PSThreadsDigest(); got != PSThreadsSHA256 {
		t.Fatalf("embedded flatsql-ps-threads.wasm sha256 %s, want %s (%s)", got, PSThreadsSHA256, PSThreadsPackage)
	}
	if n := len(PSThreadsWasm()); n != 2629364 {
		t.Fatalf("embedded flatsql-ps-threads.wasm is %d bytes, want 2629364", n)
	}
	b := PSThreadsWasm()
	if string(b[:4]) != "\x00asm" || binary.LittleEndian.Uint32(b[4:8]) != 1 {
		t.Fatalf("embedded artifact is not a wasm module: % x", b[:8])
	}
}
