package modulert

import (
	"bytes"
	"testing"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
)

func TestInvokeAlignedInputCarriesCanonicalLayoutWithoutSchemaIdentity(t *testing.T) {
	frames := []InvokeInputFrame{
		{PortID: "configure", Payload: []byte(`{"enabled":true}`), WireFormat: payloadWireFormatAlignedBinary},
		{PortID: "fixed", Payload: []byte("hello\x00\x00\x00"), WireFormat: payloadWireFormatAlignedBinary, FixedStringLength: 8, ByteLength: 8, RequiredAlignment: 16},
		{PortID: "aligned", Payload: []byte{1, 2, 3}, WireFormat: payloadWireFormatAlignedBinary, Alignment: 32},
	}
	encoded, err := encodePluginInvokeRequestFrames("configure", frames)
	if err != nil {
		t.Fatal(err)
	}
	request := piv.GetRootAsPIV(encoded, 0).Request(nil)
	if request == nil || request.InputsLength() != len(frames) {
		t.Fatal("missing canonical PIV input frames")
	}
	for i, input := range frames {
		var frame piv.TAB
		if !request.Inputs(&frame, i) {
			t.Fatalf("missing frame %d", i)
		}
		typeRef := frame.TypeRef(nil)
		if typeRef == nil {
			t.Fatalf("frame %d omitted aligned TypeRef", i)
		}
		if len(typeRef.SchemaName()) != 0 || len(typeRef.FileIdentifier()) != 0 || len(typeRef.RootType()) != 0 {
			t.Fatalf("frame %d invented a schema identity", i)
		}
		if byte(typeRef.WireFormat()) != payloadWireFormatAlignedBinary || byte(frame.WireFormat()) != payloadWireFormatAlignedBinary {
			t.Fatalf("frame %d did not preserve aligned wire format", i)
		}
		alignment := normalizeInvokeAlignment(input.Alignment, input.RequiredAlignment)
		if typeRef.RequiredAlignment() != alignment || frame.Alignment() != uint32(alignment) || frame.Offset()%uint32(alignment) != 0 {
			t.Fatalf("frame %d has inconsistent layout: TypeRef alignment %d, TAB alignment %d, offset %d", i, typeRef.RequiredAlignment(), frame.Alignment(), frame.Offset())
		}
		if typeRef.ByteLength() != uint32(len(input.Payload)) || typeRef.FixedStringLength() != input.FixedStringLength {
			t.Fatalf("frame %d lost payload length or fixed string layout", i)
		}
		if got := request.PayloadArenaBytes()[frame.Offset() : frame.Offset()+frame.Size()]; !bytes.Equal(got, input.Payload) {
			t.Fatalf("frame %d changed payload: %q", i, got)
		}
	}
}

func TestInvokeFlatBufferTypeIdentityRemainsUnchanged(t *testing.T) {
	encoded, err := encodePluginInvokeRequestFrames("read", []InvokeInputFrame{
		{PortID: "opaque", Payload: []byte{1}},
		{PortID: "typed", Payload: []byte{2}, SchemaName: "State.fbs", FileIdentifier: "STAT", RootTypeName: "State"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := piv.GetRootAsPIV(encoded, 0).Request(nil)
	var opaque, typed piv.TAB
	if !request.Inputs(&opaque, 0) || !request.Inputs(&typed, 1) {
		t.Fatal("missing FlatBuffer inputs")
	}
	if opaque.TypeRef(nil) != nil || byte(opaque.WireFormat()) != payloadWireFormatFlatbuffer {
		t.Fatal("untyped FlatBuffer acquired an aligned layout")
	}
	typeRef := typed.TypeRef(nil)
	if typeRef == nil || string(typeRef.SchemaName()) != "State.fbs" || string(typeRef.FileIdentifier()) != "STAT" || string(typeRef.RootType()) != "State" {
		t.Fatal("FlatBuffer schema identity changed")
	}
	if byte(typeRef.WireFormat()) != payloadWireFormatFlatbuffer || typeRef.RequiredAlignment() != 0 || typeRef.ByteLength() != 0 || typeRef.FixedStringLength() != 0 {
		t.Fatal("FlatBuffer acquired aligned binary constraints")
	}
}
