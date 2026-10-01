package format4test

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
)

// DefaultExtract is the Fake's field extraction for OMM, MPE and CAT, as
// format 1's Go extraction (storage extractIndexedFields, recordSupersedeKey)
// and format 2's rule texts define them. Other types extract nothing.
func DefaultExtract(typ string, plain []byte) (f Fields, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed %s buffer: %v", typ, r)
		}
	}()
	switch typ {
	case "OMM":
		var o *OMM.OMM
		switch {
		case OMM.OMMBufferHasIdentifier(plain):
			o = OMM.GetRootAsOMM(plain, 0)
		case OMM.SizePrefixedOMMBufferHasIdentifier(plain):
			o = OMM.GetSizePrefixedRootAsOMM(plain, 0)
		default:
			return f, errors.New("not an OMM buffer")
		}
		if id := o.NORAD_CAT_ID(); id > 0 {
			f.Col0 = ptr(int64(id))
		}
		f.Col1 = nonEmpty(string(o.OBJECT_ID()))
		s := strings.TrimSpace(string(o.EPOCH()))
		if s == "" {
			s = strings.TrimSpace(string(o.CREATION_DATE()))
		}
		if e, ok := parseEpoch(s); ok {
			f.Epoch = &e
		}
		f.Key = objectKey(f, 0, 1)
		f.Text = strings.TrimSpace(string(o.OBJECT_NAME()) + " " + string(o.OBJECT_ID()))
	case "MPE":
		var m *MPE.MPE
		switch {
		case MPE.MPEBufferHasIdentifier(plain):
			m = MPE.GetRootAsMPE(plain, 0)
		case MPE.SizePrefixedMPEBufferHasIdentifier(plain):
			m = MPE.GetSizePrefixedRootAsMPE(plain, 0)
		default:
			return f, errors.New("not an MPE buffer")
		}
		f.Col1 = nonEmpty(string(m.ENTITY_ID()))
		if v := m.EPOCH(); v != 0 {
			e := int64(math.Floor(v))
			f.Epoch = &e
		}
		f.Key = objectKey(f, 1)
	case "CAT":
		var c *CAT.CAT
		switch {
		case CAT.CATBufferHasIdentifier(plain):
			c = CAT.GetRootAsCAT(plain, 0)
		case CAT.SizePrefixedCATBufferHasIdentifier(plain):
			c = CAT.GetSizePrefixedRootAsCAT(plain, 0)
		default:
			return f, errors.New("not a CAT buffer")
		}
		if id := c.NORAD_CAT_ID(); id > 0 {
			f.Col0 = ptr(int64(id))
		}
		f.Col1 = nonEmpty(string(c.OBJECT_ID()))
		if v := strings.TrimSpace(c.OBJECT_TYPE().String()); v != "" && v != "UNKNOWN" {
			f.Col2 = &v
		}
		if v := strings.TrimSpace(c.OPS_STATUS_CODE().String()); v != "" && v != "UNKNOWN" {
			f.Col3 = &v
		}
		f.Key = objectKey(f, 0, 1)
		tab := c.Table()
		uri := strings.TrimSpace(string(stringSlot(tab, 52)))
		obj := strings.TrimSpace(string(stringSlot(tab, 54)))
		switch {
		case uri != "" && obj != "":
			f.Supersede = "uri:" + uri + "\x00" + obj
		case f.Col0 != nil:
			f.Supersede = "norad:" + strconv.FormatInt(*f.Col0, 10)
		case f.Col1 != nil:
			f.Supersede = "object:" + *f.Col1
		}
	}
	return f, nil
}

func stringSlot(tab flatbuffers.Table, slot flatbuffers.VOffsetT) []byte {
	o := flatbuffers.UOffsetT(tab.Offset(slot))
	if o == 0 {
		return nil
	}
	return tab.ByteVector(o + tab.Pos)
}

func nonEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// objectKey is the object rule ("object 0,1"): the first present column.
func objectKey(f Fields, cols ...int) string {
	for _, c := range cols {
		switch {
		case c == 0 && f.Col0 != nil:
			return strconv.FormatInt(*f.Col0, 10)
		case c == 1 && f.Col1 != nil:
			return *f.Col1
		}
	}
	return ""
}

// parseEpoch is format 1's parseEpochString.
func parseEpoch(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000000", "2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Unix(), true
		}
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
		return int64(v), true
	}
	return 0, false
}
