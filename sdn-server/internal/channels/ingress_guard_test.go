package channels

import (
	"encoding/binary"
	"testing"
	"time"
)

// oobBuffer returns a FlatBuffer carrying ident whose every vtable slot points
// at one uoffset 2 GiB past its end; sizePrefixed selects the envelope form.
func oobBuffer(ident string, slots int, sizePrefixed bool) []byte {
	vtableSize := 4 + 2*slots
	vtable := 8
	table := vtable + vtableSize
	if table%4 != 0 {
		table += 4 - table%4
	}
	inner := make([]byte, table+12)
	binary.LittleEndian.PutUint32(inner[0:], uint32(table))
	copy(inner[4:8], ident)
	binary.LittleEndian.PutUint16(inner[vtable:], uint16(vtableSize))
	binary.LittleEndian.PutUint16(inner[vtable+2:], 12)
	for i := 0; i < slots; i++ {
		binary.LittleEndian.PutUint16(inner[vtable+4+2*i:], 4)
	}
	binary.LittleEndian.PutUint32(inner[table:], uint32(table-vtable))
	binary.LittleEndian.PutUint32(inner[table+4:], 0x7fffff00)
	if !sizePrefixed {
		return inner
	}
	out := make([]byte, 4+len(inner))
	binary.LittleEndian.PutUint32(out, uint32(len(inner)))
	copy(out[4:], inner)
	return out
}

// Security re-audit NET-01: an untrusted announcement's PNM, and the DPM
// manifest fetched on its say-so, are refused when malformed instead of
// panicking the accessors that read them.
func TestSignedEnvelopeVerifiersRefuseMalformedBuffers(t *testing.T) {
	if _, err := VerifySignedPNMEnvelope(oobBuffer("$PNM", 9, true)); err == nil {
		t.Fatal("malformed PNM verified")
	}
	if _, err := VerifySignedDPMManifest(oobBuffer("$DPM", 40, false), ""); err == nil {
		t.Fatal("malformed DPM verified")
	}
	if _, err := DecodeDatasetCatalog(oobBuffer("$DPM", 40, false), nil, "", time.Now()); err == nil {
		t.Fatal("malformed dataset catalog decoded")
	}
}
