package sds

import (
	"context"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestWXFAdmissionUsesReleasedIdentifierAndMetadata(t *testing.T) {
	v, err := NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !v.HasSchema("WXF.fbs") {
		t.Fatal("WXF schema is not embedded")
	}
	if !IsPublicReadSchema("WXF.fbs") {
		t.Fatal("published weather cannot be read anonymously")
	}
	if ident, ok := v.FileIdentifier("WXF.fbs"); !ok || ident != WXF.WXFIdentifier {
		t.Fatalf("WXF identifier = %q, found=%v", ident, ok)
	}
	b := flatbuffers.NewBuilder(256)
	id := b.CreateString("weather-field")
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 1)
	WXF.WXFGridAddNLON(b, 1)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, id)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALID_TIME_MS(b, 1788912000000)
	WXF.WXFAddTIME_BASIS(b, WXF.EnumValueswxfTimeBasis["ValidTimeOnly"])
	WXF.WXFAddLICENSE_CLASS(b, WXF.EnumValueswxfLicenseClass["OpenAttribution"])
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	record := b.FinishedBytes()
	if err := v.Validate(context.Background(), "WXF.fbs", record); err != nil {
		t.Fatal(err)
	}
	decoded := WXF.GetSizePrefixedRootAsWXF(record, 0)
	if decoded.TIME_BASIS() != WXF.EnumValueswxfTimeBasis["ValidTimeOnly"] || decoded.LICENSE_CLASS() != WXF.EnumValueswxfLicenseClass["OpenAttribution"] {
		t.Fatal("released weather time-basis or licence enum was lost")
	}
	bad := append([]byte(nil), record...)
	copy(bad[8:12], "$CAT")
	for _, payload := range [][]byte{nil, []byte(`{"FIELD_ID":"json"}`), bad} {
		if err := v.Validate(context.Background(), "WXF.fbs", payload); err == nil {
			t.Fatal("non-WXF envelope admitted")
		}
	}
}
