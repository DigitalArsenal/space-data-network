package logservice

import (
	"encoding/binary"
	"testing"
)

// B2 (cutover rehearsal): a publication log entry and head carry their
// schemas' file identifiers after the size prefix; the format-2 engine
// refuses a buffer without its type's identifier (-102), and on 98fdd37e9
// every PLOG entry was built without one.
func TestPublicationLogBuffersCarryTheirFileIdentifiers(t *testing.T) {
	for _, c := range []struct {
		name, id string
		buf      []byte
	}{
		{"PLOG", "PLOG", buildPLGFlatBuffer(7, "OMM.fbs", "12D3KooWTestPeer", "bafyTestCID", "", "abcdef", 1700000000, []byte{1}, nil, "")},
		{"PLHD", "PLHD", buildPLHFlatBuffer("OMM.fbs", "12D3KooWTestPeer", 7, "abcdef", 7, "/dns4/example.com/tcp/443", 1700000000, nil, "", "")},
	} {
		if len(c.buf) < 12 || int(binary.LittleEndian.Uint32(c.buf)) != len(c.buf)-4 {
			t.Fatalf("%s: not size-prefixed (%d bytes)", c.name, len(c.buf))
		}
		if got := string(c.buf[8:12]); got != c.id {
			t.Fatalf("%s: file identifier %q, want %q", c.name, got, c.id)
		}
	}
}
