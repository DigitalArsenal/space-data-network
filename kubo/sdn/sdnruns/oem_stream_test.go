package sdnruns

import (
	"encoding/binary"
	"testing"
)

// fakeOEM builds a minimal non-size-prefixed $OEM-shaped record: [u32 root][$OEM]
// then payload. Only the file-id at bytes[4:8] matters to splitOEMStream.
func fakeOEM(payload string) []byte {
	b := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(b[0:4], 8) // dummy root offset
	copy(b[4:8], "$OEM")
	copy(b[8:], payload)
	return b
}

func frameStream(recs ...[]byte) []byte {
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(recs)))
	for _, r := range recs {
		var l [4]byte
		binary.LittleEndian.PutUint32(l[:], uint32(len(r)))
		out = append(out, l[:]...)
		out = append(out, r...)
	}
	return out
}

func TestSplitOEMStream(t *testing.T) {
	a := fakeOEM("object-A-states")
	b := fakeOEM("object-B")
	stream := frameStream(a, b)

	recs, err := splitOEMStream(stream)
	if err != nil {
		t.Fatalf("splitOEMStream: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if string(recs[0][4:8]) != "$OEM" || string(recs[1][4:8]) != "$OEM" {
		t.Fatalf("records missing $OEM file id")
	}
	if string(recs[0][8:]) != "object-A-states" || string(recs[1][8:]) != "object-B" {
		t.Fatalf("record payloads not preserved: %q %q", recs[0][8:], recs[1][8:])
	}
	// The split must copy: mutating the source stream must not affect the records.
	for i := range stream {
		stream[i] = 0
	}
	if string(recs[0][4:8]) != "$OEM" {
		t.Fatalf("records alias the source buffer (not copied)")
	}
}

func TestSplitOEMStreamEmpty(t *testing.T) {
	recs, err := splitOEMStream(frameStream())
	if err != nil {
		t.Fatalf("empty stream: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d records, want 0", len(recs))
	}
}

func TestSplitOEMStreamRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"too short":        {1, 2},
		"truncated length": {2, 0, 0, 0, 8, 0, 0, 0}, // claims 2 records, no data
		"bad file id":      frameStream(append([]byte{0, 0, 0, 0, '$', 'O', 'M', 'M'}, []byte("x")...)),
		"trailing bytes":   append(frameStream(fakeOEM("a")), 0xFF),
		"record too small":  {1, 0, 0, 0, 2, 0, 0, 0, 0, 0}, // len=2 (<8)
	}
	for name, in := range cases {
		if _, err := splitOEMStream(in); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
