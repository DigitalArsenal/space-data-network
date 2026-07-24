package modulert

import (
	"bytes"
	"strings"
	"testing"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestEncodePluginInvokeRequestFramesEmitsPIV(t *testing.T) {
	schemaHash := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	requestBytes, err := encodePluginInvokeRequestFrames("propagate", []InvokeInputFrame{
		{
			PortID:         "state",
			Payload:        []byte{1, 2, 3},
			SchemaName:     "State.fbs",
			FileIdentifier: "STAT",
			SchemaVersion:  "2026.07.21",
			SchemaHash:     schemaHash,
			RootTypeName:   "State",
			Ownership:      byte(piv.EnumValuesbufferOwnership["HOST_OWNED"]),
			Mutability:     byte(piv.EnumValuesbufferMutability["IMMUTABLE"]),
			FrameID:        0x0102030405060708,
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
	typeRef := stateFrame.TypeRef(nil)
	if got := string(typeRef.SchemaVersion()); got != "2026.07.21" {
		t.Fatalf("state SCHEMA_VERSION = %q, want 2026.07.21", got)
	}
	if got := typeRef.SchemaHashBytes(); !bytes.Equal(got, schemaHash) {
		t.Fatalf("state SCHEMA_HASH = %x, want %x", got, schemaHash)
	}
	if got := stateFrame.FrameId(); got != 0x0102030405060708 {
		t.Fatalf("state FRAME_ID = %#x, want %#x", got, uint64(0x0102030405060708))
	}
	if got := byte(stateFrame.Ownership()); got != byte(piv.EnumValuesbufferOwnership["HOST_OWNED"]) {
		t.Fatalf("state OWNERSHIP = %d, want HOST_OWNED", got)
	}
	if got := byte(stateFrame.Mutability()); got != byte(piv.EnumValuesbufferMutability["IMMUTABLE"]) {
		t.Fatalf("state MUTABILITY = %d, want IMMUTABLE", got)
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

func TestPackInvokeInputFramesRejectsAlignmentAboveModuleABILimit(t *testing.T) {
	_, _, _, err := packInvokeInputFrames([]InvokeInputFrame{{
		PortID:    "oversized-alignment",
		Payload:   []byte{1},
		Alignment: 1 << 16,
	}})
	if err == nil || !strings.Contains(err.Error(), "module ABI limit") {
		t.Fatalf("packInvokeInputFrames() error = %v, want module ABI alignment rejection", err)
	}
}

func TestAlignInvokeOffsetCheckedRejectsUint32Overflow(t *testing.T) {
	if _, ok := alignInvokeOffsetChecked(^uint32(0)-3, 8); ok {
		t.Fatal("alignInvokeOffsetChecked() accepted uint32 overflow")
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

func TestDecodePluginInvokeResponsePreservesCompleteTABContract(t *testing.T) {
	wantHash := []byte{0xde, 0xad, 0xbe, 0xef}
	responseBytes := buildPIVResponseWithMetadataForTest(pluginInvokeFrame{
		PortID:            "ephemeris",
		Offset:            0,
		Size:              32,
		Alignment:         32,
		WireFormat:        payloadWireFormatAlignedBinary,
		SchemaName:        "Ephemeris.fbs",
		FileIdentifier:    "EPHM",
		SchemaVersion:     "3.2.1",
		SchemaHash:        wantHash,
		RootTypeName:      "Ephemeris",
		FixedStringLength: 64,
		ByteLength:        32,
		RequiredAlignment: 32,
		Ownership:         byte(piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]),
		Mutability:        byte(piv.EnumValuesbufferMutability["IMMUTABLE"]),
		FrameID:           0x8877665544332211,
	}, make([]byte, 32))

	response, err := decodePluginInvokeResponseBytes(responseBytes)
	if err != nil {
		t.Fatalf("decodePluginInvokeResponseBytes() error = %v", err)
	}
	if len(response.OutputFrames) != 1 {
		t.Fatalf("output frame count = %d, want 1", len(response.OutputFrames))
	}
	got := response.OutputFrames[0]
	if got.PortID != "ephemeris" || got.Offset != 0 || got.Size != 32 || got.Alignment != 32 ||
		got.WireFormat != payloadWireFormatAlignedBinary || got.SchemaName != "Ephemeris.fbs" ||
		got.FileIdentifier != "EPHM" || got.SchemaVersion != "3.2.1" ||
		!bytes.Equal(got.SchemaHash, wantHash) || got.RootTypeName != "Ephemeris" ||
		got.FixedStringLength != 64 || got.ByteLength != 32 || got.RequiredAlignment != 32 ||
		got.Ownership != byte(piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]) ||
		got.Mutability != byte(piv.EnumValuesbufferMutability["IMMUTABLE"]) ||
		got.FrameID != 0x8877665544332211 {
		t.Fatalf("decoded output metadata = %+v", got)
	}
}

func TestDecodePluginInvokeResponseRejectsAlignmentAboveModuleABILimit(t *testing.T) {
	responseBytes := buildPIVResponseWithMetadataForTest(pluginInvokeFrame{
		PortID:     "oversized-alignment",
		Offset:     0,
		Size:       1,
		Alignment:  1 << 16,
		WireFormat: payloadWireFormatFlatbuffer,
		Ownership:  byte(piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]),
		Mutability: byte(piv.EnumValuesbufferMutability["IMMUTABLE"]),
	}, []byte{1})

	_, err := decodePluginInvokeResponseBytes(responseBytes)
	if err == nil || !strings.Contains(err.Error(), "module ABI limit") {
		t.Fatalf("decodePluginInvokeResponseBytes() error = %v, want module ABI alignment rejection", err)
	}
}

func TestDecodePluginInvokeResponsePreservesYieldContinuation(t *testing.T) {
	builder := flatbuffers.NewBuilder(128)
	piv.PIVResponseStart(builder)
	piv.PIVResponseAddStatusCode(builder, 0)
	piv.PIVResponseAddStatus(builder, piv.EnumValuespivStatus["YIELDED"])
	piv.PIVResponseAddYielded(builder, true)
	piv.PIVResponseAddBacklogRemaining(builder, 3817)
	responseOffset := piv.PIVResponseEnd(builder)
	piv.PIVStart(builder)
	piv.PIVAddResponse(builder, responseOffset)
	root := piv.PIVEnd(builder)
	piv.FinishPIVBuffer(builder, root)

	response, err := decodePluginInvokeResponseBytes(builder.FinishedBytes())
	if err != nil {
		t.Fatalf("decodePluginInvokeResponseBytes() error = %v", err)
	}
	if !response.Yielded || response.BacklogRemaining != 3817 {
		t.Fatalf("continuation = yielded:%v backlog:%d, want true/3817", response.Yielded, response.BacklogRemaining)
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

func buildPIVResponseWithMetadataForTest(frame pluginInvokeFrame, payload []byte) []byte {
	builder := flatbuffers.NewBuilder(256)
	arenaOffset := createAlignedPIVByteVector(builder, payload, uint32(frame.Alignment))
	portIDOffset := builder.CreateString(frame.PortID)
	schemaNameOffset := builder.CreateString(frame.SchemaName)
	fileIdentifierOffset := builder.CreateString(frame.FileIdentifier)
	schemaVersionOffset := builder.CreateString(frame.SchemaVersion)
	rootTypeOffset := builder.CreateString(frame.RootTypeName)
	schemaHashOffset := builder.CreateByteVector(frame.SchemaHash)
	wireFormat := piv.EnumValuespayloadWireFormat["FLATBUFFER"]
	if frame.WireFormat == payloadWireFormatAlignedBinary {
		wireFormat = piv.EnumValuespayloadWireFormat["ALIGNED_BINARY"]
	}
	ownership := piv.EnumValuesbufferOwnership["HOST_OWNED"]
	if frame.Ownership == byte(piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]) {
		ownership = piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]
	} else if frame.Ownership == byte(piv.EnumValuesbufferOwnership["TRANSFERRED"]) {
		ownership = piv.EnumValuesbufferOwnership["TRANSFERRED"]
	}
	mutability := piv.EnumValuesbufferMutability["IMMUTABLE"]
	if frame.Mutability == byte(piv.EnumValuesbufferMutability["SINGLE_WRITER_MUTABLE"]) {
		mutability = piv.EnumValuesbufferMutability["SINGLE_WRITER_MUTABLE"]
	} else if frame.Mutability == byte(piv.EnumValuesbufferMutability["APPEND_ONLY"]) {
		mutability = piv.EnumValuesbufferMutability["APPEND_ONLY"]
	}

	piv.FlatBufferTypeRefStart(builder)
	piv.FlatBufferTypeRefAddSchemaName(builder, schemaNameOffset)
	piv.FlatBufferTypeRefAddFileIdentifier(builder, fileIdentifierOffset)
	piv.FlatBufferTypeRefAddSchemaVersion(builder, schemaVersionOffset)
	piv.FlatBufferTypeRefAddRootType(builder, rootTypeOffset)
	piv.FlatBufferTypeRefAddSchemaHash(builder, schemaHashOffset)
	piv.FlatBufferTypeRefAddWireFormat(builder, wireFormat)
	piv.FlatBufferTypeRefAddFixedStringLength(builder, frame.FixedStringLength)
	piv.FlatBufferTypeRefAddByteLength(builder, frame.ByteLength)
	piv.FlatBufferTypeRefAddRequiredAlignment(builder, frame.RequiredAlignment)
	typeRefOffset := piv.FlatBufferTypeRefEnd(builder)

	piv.TABStart(builder)
	piv.TABAddOffset(builder, frame.Offset)
	piv.TABAddSize(builder, frame.Size)
	piv.TABAddAlignment(builder, frame.Alignment)
	piv.TABAddWireFormat(builder, wireFormat)
	piv.TABAddTypeRef(builder, typeRefOffset)
	piv.TABAddMutability(builder, mutability)
	piv.TABAddOwnership(builder, ownership)
	piv.TABAddFrameId(builder, frame.FrameID)
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
