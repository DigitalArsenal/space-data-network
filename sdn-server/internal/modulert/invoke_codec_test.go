package modulert

import (
	"bytes"
	"strings"
	"testing"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestEncodePluginInvokeRequestFramesEmitsPIV(t *testing.T) {
	requestBytes, err := encodePluginInvokeRequestFrames("propagate", []InvokeInputFrame{
		{
			PortID:         "state",
			Payload:        []byte{1, 2, 3},
			SchemaName:     "State.fbs",
			FileIdentifier: "STAT",
			RootTypeName:   "State",
		},
		{
			PortID:            "vectors",
			Payload:           []byte{4, 5, 6, 7},
			WireFormat:        payloadWireFormatAlignedBinary,
			RequiredAlignment: 16,
		},
	})
	if err != nil {
		t.Fatalf("encodePluginInvokeRequestFrames() error = %v", err)
	}
	if !piv.PIVBufferHasIdentifier(requestBytes) {
		t.Fatalf("encoded request is not a SDS PIV envelope")
	}
	if flatbuffers.BufferHasIdentifier(requestBytes, string([]byte{'P', 'I', 'N', 'Q'})) {
		t.Fatalf("encoded request still uses legacy request identifier")
	}

	root := piv.GetRootAsPIV(requestBytes, 0)
	envelope := root.Request(nil)
	if envelope == nil {
		t.Fatalf("encoded PIV envelope has no request")
	}
	if got := string(envelope.MethodId()); got != "propagate" {
		t.Fatalf("METHOD_ID = %q, want propagate", got)
	}
	if got := envelope.InputsLength(); got != 2 {
		t.Fatalf("INPUTS length = %d, want 2", got)
	}

	arena := envelope.PayloadArenaBytes()
	var stateFrame piv.TAB
	if !envelope.Inputs(&stateFrame, 0) {
		t.Fatalf("missing state TAB")
	}
	if got := string(stateFrame.PortId()); got != "state" {
		t.Fatalf("first PORT_ID = %q, want state", got)
	}
	if got := stateFrame.Alignment(); got != invokeArenaAlignment {
		t.Fatalf("state ALIGNMENT = %d, want %d", got, invokeArenaAlignment)
	}
	if got := string(stateFrame.TypeRef(nil).FileIdentifier()); got != "STAT" {
		t.Fatalf("state FILE_IDENTIFIER = %q, want STAT", got)
	}
	if got := arena[stateFrame.Offset() : stateFrame.Offset()+stateFrame.Size()]; !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("state payload = %v", got)
	}

	var vectorFrame piv.TAB
	if !envelope.Inputs(&vectorFrame, 1) {
		t.Fatalf("missing vectors TAB")
	}
	if got := string(vectorFrame.PortId()); got != "vectors" {
		t.Fatalf("second PORT_ID = %q, want vectors", got)
	}
	if got := vectorFrame.Alignment(); got != 16 {
		t.Fatalf("vectors ALIGNMENT = %d, want 16", got)
	}
	if got := vectorFrame.Offset() % 16; got != 0 {
		t.Fatalf("vectors OFFSET %% 16 = %d, want 0", got)
	}
	if got := arena[vectorFrame.Offset() : vectorFrame.Offset()+vectorFrame.Size()]; !bytes.Equal(got, []byte{4, 5, 6, 7}) {
		t.Fatalf("vectors payload = %v", got)
	}
}

func TestDecodePluginInvokeResponseBytesReadsPIV(t *testing.T) {
	responseBytes := buildPIVResponseForTest("response", []byte("ok"))

	response, err := decodePluginInvokeResponseBytes(responseBytes)
	if err != nil {
		t.Fatalf("decodePluginInvokeResponseBytes() error = %v", err)
	}
	payload, err := extractPluginInvokePayload(response, "response")
	if err != nil {
		t.Fatalf("extractPluginInvokePayload() error = %v", err)
	}
	if string(payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(payload))
	}
}

func TestDecodePluginInvokeResponseBytesRejectsLegacyIdentifier(t *testing.T) {
	builder := flatbuffers.NewBuilder(16)
	builder.StartObject(0)
	root := builder.EndObject()
	builder.FinishWithFileIdentifier(root, []byte{'P', 'I', 'N', 'S'})

	_, err := decodePluginInvokeResponseBytes(builder.FinishedBytes())
	if err == nil || !strings.Contains(err.Error(), "SDS PIV") {
		t.Fatalf("decodePluginInvokeResponseBytes() error = %v, want SDS PIV mismatch", err)
	}
}

func buildPIVResponseForTest(portID string, payload []byte) []byte {
	builder := flatbuffers.NewBuilder(128)
	arenaOffset := createAlignedPIVByteVector(builder, payload, invokeArenaAlignment)
	portIDOffset := builder.CreateString(portID)

	piv.TABStart(builder)
	piv.TABAddOffset(builder, 0)
	piv.TABAddSize(builder, uint32(len(payload)))
	piv.TABAddAlignment(builder, invokeArenaAlignment)
	piv.TABAddWireFormat(builder, piv.EnumValuespayloadWireFormat["FLATBUFFER"])
	piv.TABAddMutability(builder, piv.EnumValuesbufferMutability["IMMUTABLE"])
	piv.TABAddOwnership(builder, piv.EnumValuesbufferOwnership["HOST_OWNED"])
	piv.TABAddPortId(builder, portIDOffset)
	frameOffset := piv.TABEnd(builder)

	piv.PIVResponseStartOutputsVector(builder, 1)
	builder.PrependUOffsetT(frameOffset)
	outputsOffset := builder.EndVector(1)

	piv.PIVResponseStart(builder)
	piv.PIVResponseAddStatusCode(builder, 0)
	piv.PIVResponseAddStatus(builder, piv.EnumValuespivStatus["OK"])
	piv.PIVResponseAddOutputs(builder, outputsOffset)
	piv.PIVResponseAddPayloadArena(builder, arenaOffset)
	responseOffset := piv.PIVResponseEnd(builder)

	piv.PIVStart(builder)
	piv.PIVAddResponse(builder, responseOffset)
	root := piv.PIVEnd(builder)
	piv.FinishPIVBuffer(builder, root)
	return builder.FinishedBytes()
}
