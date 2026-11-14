package format4

import (
	"encoding/binary"
	"testing"
)

func TestCatalogRelationBound(t *testing.T) {
	for typ, want := range map[string]uint32{"OCM": 100_000, "CAT": 100_000, "OMM": 400_000, "TBS": 400_000, "MPE": 10_000, "WXF": 10_000} {
		spec := typeStorageOptions(typ)
		if spec.A18Bound != want {
			t.Errorf("%s bound = %d, want %d", typ, spec.A18Bound, want)
		}
	}
}

func TestCatalogBoundEncodedRegistration(t *testing.T) {
	spec, err := TypeSpecFor("OCM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	if spec.A18Bound != 100000 || len(spec.BFBS) == 0 {
		t.Fatal("real OCM registration lost policy or schema")
	}
	found := false
	for b := spec.Encode(); len(b) >= 6; {
		tag, n := binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])
		b = b[6:]
		if uint64(n) > uint64(len(b)) {
			t.Fatal("invalid TLV")
		}
		if tag == 10 {
			if n != 4 || binary.LittleEndian.Uint32(b) != 100000 {
				t.Fatal("wrong encoded bound")
			}
			found = true
		}
		b = b[n:]
	}
	if !found {
		t.Fatal("bound tag missing")
	}
}
