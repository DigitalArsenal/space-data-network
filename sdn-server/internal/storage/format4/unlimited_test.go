package format4

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func visibilityTag(t *testing.T, b []byte) (uint32, bool) {
	t.Helper()
	for len(b) >= 6 {
		tag, n := binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint32(b[2:])
		b = b[6:]
		if uint64(n) > uint64(len(b)) {
			t.Fatal("malformed TLV")
		}
		if tag == 10 {
			if n != 4 {
				t.Fatal("wrong visibility width")
			}
			return binary.LittleEndian.Uint32(b), true
		}
		b = b[n:]
	}
	return 0, false
}

func TestUnlimitedVisibilityEncoding(t *testing.T) {
	if _, present := visibilityTag(t, (TypeSpec{}).Encode()); present {
		t.Fatal("missing policy must stay absent")
	}
	for _, typ := range []string{"OMM", "TBS", "OCM", "CAT", "MPE", "WXF", "IQC"} {
		spec := typeStorageOptions(typ)
		value, present := visibilityTag(t, spec.Encode())
		if !spec.UnlimitedVisibility || !present || value != 0 {
			t.Fatalf("%s lacks explicit unlimited policy", typ)
		}
	}
	spec := CIDOnlyTypeSpec("OPAQUE", [4]byte{'$', 'O', 'P', 'Q'})
	if value, present := visibilityTag(t, spec.Encode()); !present || value != 0 {
		t.Fatal("opaque policy")
	}
}

type visibilityControl struct {
	capabilities  int32
	err           error
	registrations int
}

func (c *visibilityControl) ControlCall(string, ...interface{}) (int32, error) {
	return c.capabilities, c.err
}
func (c *visibilityControl) ControlBytes(string, []byte) (int32, error) {
	c.registrations++
	return 0, nil
}
func (c *visibilityControl) ControlOut(string, int) ([]byte, int32, error) { return nil, 0, nil }
func (c *visibilityControl) StopWithin(time.Duration) (int32, error)       { return 0, nil }

func TestUnlimitedVisibilityRequiresCapability(t *testing.T) {
	for _, caps := range []int32{0, -1, 1} {
		c := &visibilityControl{capabilities: caps}
		e := &Engine{ctl: c}
		err := e.RegisterType(CIDOnlyTypeSpec("OPAQUE", [4]byte{'$', 'O', 'P', 'Q'}))
		if caps == 1 {
			if err != nil || c.registrations != 1 {
				t.Fatalf("supported engine: %v", err)
			}
		} else if err == nil || c.registrations != 0 {
			t.Fatal("unsupported engine received registration")
		}
	}
	c := &visibilityControl{err: errors.New("missing export")}
	if err := (&Engine{ctl: c}).RegisterType(TypeSpec{UnlimitedVisibility: true}); err == nil || c.registrations != 0 {
		t.Fatal("missing capability export accepted")
	}
}
