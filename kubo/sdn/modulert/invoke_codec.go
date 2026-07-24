package modulert

import (
	"encoding/binary"
	"fmt"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	invokeArenaAlignment    = 8
	maxInvokeFrameAlignment = uint32(1<<16 - 1)

	payloadWireFormatFlatbuffer    = 0
	payloadWireFormatAlignedBinary = 1
)

type pluginInvokeFrame struct {
	PortID            string
	Offset            uint32
	Size              uint32
	Alignment         uint32
	WireFormat        byte
	SchemaName        string
	FileIdentifier    string
	SchemaVersion     string
	SchemaHash        []byte
	RootTypeName      string
	FixedStringLength uint16
	ByteLength        uint32
	RequiredAlignment uint16
	Ownership         byte
	Mutability        byte
	FrameID           uint64
}

type InvokeInputFrame struct {
	PortID            string
	Payload           []byte
	Offset            uint32
	Size              uint32
	SchemaName        string
	FileIdentifier    string
	SchemaVersion     string
	SchemaHash        []byte
	RootTypeName      string
	WireFormat        byte
	FixedStringLength uint16
	ByteLength        uint32
	RequiredAlignment uint16
	Alignment         uint32
	Ownership         byte
	Mutability        byte
	FrameID           uint64
}

type pluginInvokeResponse struct {
	StatusCode       int32
	Yielded          bool
	BacklogRemaining uint32
	ErrorCode        string
	ErrorMessage     string
	OutputFrames     []pluginInvokeFrame
	PayloadArena     []byte
}

// EncodeInvokeRequestFrames encodes an SDS $PIV plugin-invoke request for the
// given method id and input frames. These are the exact bytes plugin_invoke_stream
// consumes — and the exact bytes a COMMAND-surface module expects on stdin (an
// emscripten-built command module whose main() reads a $PIV request
// from stdin, dispatches it, and writes the $PIV response to stdout). It lets a
// host drive such a command module with the same request encoding the reactor ABI
// uses.
func EncodeInvokeRequestFrames(methodID string, frames []InvokeInputFrame) ([]byte, error) {
	return encodePluginInvokeRequestFrames(methodID, frames)
}

// DecodeInvokeResponsePayload decodes an SDS $PIV plugin-invoke response (as
// written to stdout by a command-surface module, or returned by
// plugin_invoke_stream) and returns the payload of the preferred output port,
// falling back to the first output frame. A non-zero status / error envelope is
// surfaced as an error (same rule as the reactor path).
func DecodeInvokeResponsePayload(responseBytes []byte, preferredPortID string) ([]byte, error) {
	resp, err := decodePluginInvokeResponseBytes(responseBytes)
	if err != nil {
		return nil, err
	}
	return extractPluginInvokePayload(resp, preferredPortID)
}

func encodePluginInvokeRequest(methodID string, payload []byte) ([]byte, error) {
	return encodePluginInvokeRequestFrames(methodID, []InvokeInputFrame{
		{
			PortID:  "request",
			Payload: payload,
		},
	})
}

func encodePluginInvokeRequestFrames(methodID string, frames []InvokeInputFrame) ([]byte, error) {
	if methodID == "" {
		return nil, fmt.Errorf("method id is required")
	}

	packedFrames, payloadArena, arenaAlignment, err := packInvokeInputFrames(frames)
	if err != nil {
		return nil, err
	}

	builder := flatbuffers.NewBuilder(256 + len(payloadArena))

	methodIDOffset := builder.CreateString(methodID)
	payloadArenaOffset := createAlignedPIVByteVector(builder, payloadArena, arenaAlignment)

	inputFrameOffsets := make([]flatbuffers.UOffsetT, 0, len(packedFrames))
	for _, frame := range packedFrames {
		inputFrameOffsets = append(inputFrameOffsets, buildPIVTAB(builder, frame))
	}

	piv.PIVRequestStartInputsVector(builder, len(inputFrameOffsets))
	for index := len(inputFrameOffsets) - 1; index >= 0; index -= 1 {
		builder.PrependUOffsetT(inputFrameOffsets[index])
	}
	inputFramesOffset := builder.EndVector(len(inputFrameOffsets))

	piv.PIVRequestStart(builder)
	piv.PIVRequestAddMethodId(builder, methodIDOffset)
	piv.PIVRequestAddInputs(builder, inputFramesOffset)
	piv.PIVRequestAddPayloadArena(builder, payloadArenaOffset)
	requestOffset := piv.PIVRequestEnd(builder)

	piv.PIVStart(builder)
	piv.PIVAddRequest(builder, requestOffset)
	root := piv.PIVEnd(builder)
	piv.FinishPIVBuffer(builder, root)

	return builder.FinishedBytes(), nil
}

func packInvokeInputFrames(frames []InvokeInputFrame) ([]InvokeInputFrame, []byte, uint32, error) {
	if len(frames) == 0 {
		return nil, nil, 0, fmt.Errorf("at least one input frame is required")
	}

	packed := make([]InvokeInputFrame, 0, len(frames))
	payloadArena := make([]byte, 0)
	var offset uint32
	arenaAlignment := uint32(invokeArenaAlignment)

	for _, frame := range frames {
		if frame.PortID == "" {
			return nil, nil, 0, fmt.Errorf("input frame port id is required")
		}
		if frame.WireFormat > payloadWireFormatAlignedBinary {
			return nil, nil, 0, fmt.Errorf("input frame %q has invalid wire format %d", frame.PortID, frame.WireFormat)
		}
		if frame.Ownership > byte(piv.EnumValuesbufferOwnership["TRANSFERRED"]) {
			return nil, nil, 0, fmt.Errorf("input frame %q has invalid ownership %d", frame.PortID, frame.Ownership)
		}
		if frame.Mutability > byte(piv.EnumValuesbufferMutability["APPEND_ONLY"]) {
			return nil, nil, 0, fmt.Errorf("input frame %q has invalid mutability %d", frame.PortID, frame.Mutability)
		}

		alignment := normalizeInvokeAlignment(frame.Alignment, frame.RequiredAlignment)
		if !isPowerOfTwo32(alignment) {
			return nil, nil, 0, fmt.Errorf("input frame %q alignment %d is not a power of two", frame.PortID, alignment)
		}
		if alignment > maxInvokeFrameAlignment {
			return nil, nil, 0, fmt.Errorf("input frame %q alignment %d exceeds the module ABI limit", frame.PortID, alignment)
		}
		if alignment > arenaAlignment {
			arenaAlignment = alignment
		}
		alignedOffset, ok := alignInvokeOffsetChecked(offset, alignment)
		if !ok {
			return nil, nil, 0, fmt.Errorf("input frame %q aligned offset overflows uint32", frame.PortID)
		}
		payloadEnd := uint64(alignedOffset) + uint64(len(frame.Payload))
		if payloadEnd > uint64(^uint32(0)) {
			return nil, nil, 0, fmt.Errorf("input frame %q exceeds the uint32 payload arena", frame.PortID)
		}
		payload := append([]byte(nil), frame.Payload...)
		padding := int(alignedOffset - offset)
		if padding > 0 {
			payloadArena = append(payloadArena, make([]byte, padding)...)
		}
		payloadArena = append(payloadArena, payload...)

		if frame.WireFormat == 0 {
			frame.WireFormat = payloadWireFormatFlatbuffer
		}
		if frame.ByteLength == 0 && frame.WireFormat == payloadWireFormatAlignedBinary {
			frame.ByteLength = uint32(len(payload))
		}
		frame.Offset = alignedOffset
		frame.Size = uint32(len(payload))
		frame.Alignment = alignment
		// Payload bytes were copied into the request arena owned by this host.
		// Ownership and mutability from a source arena cannot cross the instance
		// boundary; the effective TAB contract is host-owned and immutable.
		frame.Ownership = byte(piv.EnumValuesbufferOwnership["HOST_OWNED"])
		frame.Mutability = byte(piv.EnumValuesbufferMutability["IMMUTABLE"])
		packed = append(packed, frame)

		offset = uint32(payloadEnd)
	}

	return packed, payloadArena, arenaAlignment, nil
}

func normalizeInvokeAlignment(alignment uint32, requiredAlignment uint16) uint32 {
	normalized := alignment
	if uint32(requiredAlignment) > normalized {
		normalized = uint32(requiredAlignment)
	}
	if normalized < invokeArenaAlignment {
		return invokeArenaAlignment
	}
	return normalized
}

func createAlignedPIVByteVector(
	builder *flatbuffers.Builder,
	payload []byte,
	alignment uint32,
) flatbuffers.UOffsetT {
	if alignment == 0 {
		alignment = 1
	}
	builder.StartVector(1, len(payload), int(alignment))
	for index := len(payload) - 1; index >= 0; index -= 1 {
		builder.PrependByte(payload[index])
	}
	return builder.EndVector(len(payload))
}

func buildPIVTAB(builder *flatbuffers.Builder, frame InvokeInputFrame) flatbuffers.UOffsetT {
	typeRefOffset := buildFlatBufferTypeRef(builder, frame)
	portIDOffset := flatbuffers.UOffsetT(0)
	if frame.PortID != "" {
		portIDOffset = builder.CreateString(frame.PortID)
	}

	wireFormat := piv.EnumValuespayloadWireFormat["FLATBUFFER"]
	if frame.WireFormat == payloadWireFormatAlignedBinary {
		wireFormat = piv.EnumValuespayloadWireFormat["ALIGNED_BINARY"]
	}

	piv.TABStart(builder)
	piv.TABAddOffset(builder, frame.Offset)
	piv.TABAddSize(builder, frame.Size)
	piv.TABAddAlignment(builder, uint32(frame.Alignment))
	piv.TABAddWireFormat(builder, wireFormat)
	if typeRefOffset != 0 {
		piv.TABAddTypeRef(builder, typeRefOffset)
	}
	mutability := piv.EnumValuesbufferMutability["IMMUTABLE"]
	if frame.Mutability == byte(piv.EnumValuesbufferMutability["SINGLE_WRITER_MUTABLE"]) {
		mutability = piv.EnumValuesbufferMutability["SINGLE_WRITER_MUTABLE"]
	} else if frame.Mutability == byte(piv.EnumValuesbufferMutability["APPEND_ONLY"]) {
		mutability = piv.EnumValuesbufferMutability["APPEND_ONLY"]
	}
	ownership := piv.EnumValuesbufferOwnership["HOST_OWNED"]
	if frame.Ownership == byte(piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]) {
		ownership = piv.EnumValuesbufferOwnership["PLUGIN_OWNED"]
	} else if frame.Ownership == byte(piv.EnumValuesbufferOwnership["TRANSFERRED"]) {
		ownership = piv.EnumValuesbufferOwnership["TRANSFERRED"]
	}
	piv.TABAddMutability(builder, mutability)
	piv.TABAddOwnership(builder, ownership)
	if frame.FrameID != 0 {
		piv.TABAddFrameId(builder, frame.FrameID)
	}
	if portIDOffset != 0 {
		piv.TABAddPortId(builder, portIDOffset)
	}
	return piv.TABEnd(builder)
}

func buildFlatBufferTypeRef(builder *flatbuffers.Builder, frame InvokeInputFrame) flatbuffers.UOffsetT {
	if frame.SchemaName == "" && frame.FileIdentifier == "" && frame.SchemaVersion == "" &&
		len(frame.SchemaHash) == 0 && frame.RootTypeName == "" {
		return 0
	}

	schemaNameOffset := flatbuffers.UOffsetT(0)
	if frame.SchemaName != "" {
		schemaNameOffset = builder.CreateString(frame.SchemaName)
	}
	fileIdentifierOffset := flatbuffers.UOffsetT(0)
	if frame.FileIdentifier != "" {
		fileIdentifierOffset = builder.CreateString(frame.FileIdentifier)
	}
	schemaVersionOffset := flatbuffers.UOffsetT(0)
	if frame.SchemaVersion != "" {
		schemaVersionOffset = builder.CreateString(frame.SchemaVersion)
	}
	schemaHashOffset := flatbuffers.UOffsetT(0)
	if len(frame.SchemaHash) > 0 {
		schemaHashOffset = builder.CreateByteVector(frame.SchemaHash)
	}
	rootTypeNameOffset := flatbuffers.UOffsetT(0)
	if frame.RootTypeName != "" {
		rootTypeNameOffset = builder.CreateString(frame.RootTypeName)
	}

	piv.FlatBufferTypeRefStart(builder)
	if schemaNameOffset != 0 {
		piv.FlatBufferTypeRefAddSchemaName(builder, schemaNameOffset)
	}
	if fileIdentifierOffset != 0 {
		piv.FlatBufferTypeRefAddFileIdentifier(builder, fileIdentifierOffset)
	}
	if schemaVersionOffset != 0 {
		piv.FlatBufferTypeRefAddSchemaVersion(builder, schemaVersionOffset)
	}
	if schemaHashOffset != 0 {
		piv.FlatBufferTypeRefAddSchemaHash(builder, schemaHashOffset)
	}
	if rootTypeNameOffset != 0 {
		piv.FlatBufferTypeRefAddRootType(builder, rootTypeNameOffset)
	}
	wireFormat := piv.EnumValuespayloadWireFormat["FLATBUFFER"]
	if frame.WireFormat == payloadWireFormatAlignedBinary {
		wireFormat = piv.EnumValuespayloadWireFormat["ALIGNED_BINARY"]
	}
	piv.FlatBufferTypeRefAddWireFormat(builder, wireFormat)
	if frame.FixedStringLength != 0 {
		piv.FlatBufferTypeRefAddFixedStringLength(builder, frame.FixedStringLength)
	}
	if frame.ByteLength != 0 {
		piv.FlatBufferTypeRefAddByteLength(builder, frame.ByteLength)
	}
	if frame.RequiredAlignment != 0 {
		piv.FlatBufferTypeRefAddRequiredAlignment(builder, frame.RequiredAlignment)
	}
	return piv.FlatBufferTypeRefEnd(builder)
}

func alignInvokeOffsetChecked(offset uint32, alignment uint32) (uint32, bool) {
	if alignment <= 1 {
		return offset, true
	}
	remainder := offset % alignment
	if remainder == 0 {
		return offset, true
	}
	padding := alignment - remainder
	if padding > ^uint32(0)-offset {
		return 0, false
	}
	return offset + padding, true
}

func decodePluginInvokeResponseBytes(data []byte) (*pluginInvokeResponse, error) {
	if !piv.PIVBufferHasIdentifier(data) {
		return nil, fmt.Errorf("SDS PIV invoke response buffer identifier mismatch")
	}

	root := piv.GetRootAsPIV(data, 0)
	envelope := root.Response(nil)
	if envelope == nil {
		return nil, fmt.Errorf("SDS PIV invoke envelope does not contain a response")
	}

	response := &pluginInvokeResponse{
		StatusCode:       envelope.StatusCode(),
		Yielded:          envelope.Yielded(),
		BacklogRemaining: envelope.BacklogRemaining(),
		ErrorCode:        string(envelope.ErrorCode()),
		ErrorMessage:     string(envelope.ErrorMessage()),
		PayloadArena:     append([]byte(nil), envelope.PayloadArenaBytes()...),
	}

	outputCount := envelope.OutputsLength()
	response.OutputFrames = make([]pluginInvokeFrame, 0, outputCount)
	for index := 0; index < outputCount; index += 1 {
		var frameTable piv.TAB
		if !envelope.Outputs(&frameTable, index) {
			continue
		}
		frame := pluginInvokeFrame{
			PortID:     string(frameTable.PortId()),
			Offset:     frameTable.Offset(),
			Size:       frameTable.Size(),
			Alignment:  frameTable.Alignment(),
			WireFormat: byte(frameTable.WireFormat()),
			Ownership:  byte(frameTable.Ownership()),
			Mutability: byte(frameTable.Mutability()),
			FrameID:    frameTable.FrameId(),
		}
		if frame.WireFormat > payloadWireFormatAlignedBinary {
			return nil, fmt.Errorf("plugin invoke output frame %q has invalid wire format %d", frame.PortID, frame.WireFormat)
		}
		if frame.Ownership > byte(piv.EnumValuesbufferOwnership["TRANSFERRED"]) {
			return nil, fmt.Errorf("plugin invoke output frame %q has invalid ownership %d", frame.PortID, frame.Ownership)
		}
		if frame.Mutability > byte(piv.EnumValuesbufferMutability["APPEND_ONLY"]) {
			return nil, fmt.Errorf("plugin invoke output frame %q has invalid mutability %d", frame.PortID, frame.Mutability)
		}
		if typeRef := frameTable.TypeRef(nil); typeRef != nil {
			frame.SchemaName = string(typeRef.SchemaName())
			frame.FileIdentifier = string(typeRef.FileIdentifier())
			frame.SchemaVersion = string(typeRef.SchemaVersion())
			frame.SchemaHash = append([]byte(nil), typeRef.SchemaHashBytes()...)
			frame.RootTypeName = string(typeRef.RootType())
			frame.FixedStringLength = typeRef.FixedStringLength()
			frame.ByteLength = typeRef.ByteLength()
			frame.RequiredAlignment = typeRef.RequiredAlignment()
			if byte(typeRef.WireFormat()) != frame.WireFormat {
				return nil, fmt.Errorf("plugin invoke output frame %q TAB/type wire formats differ", frame.PortID)
			}
		}
		if err := validateInvokeFrameLayout(frame, len(response.PayloadArena)); err != nil {
			return nil, err
		}
		response.OutputFrames = append(response.OutputFrames, frame)
	}

	return response, nil
}

func validateInvokeFrameLayout(frame pluginInvokeFrame, arenaLength int) error {
	if frame.Alignment == 0 || !isPowerOfTwo32(frame.Alignment) {
		return fmt.Errorf("plugin invoke output frame %q has invalid alignment %d", frame.PortID, frame.Alignment)
	}
	if frame.Alignment > maxInvokeFrameAlignment {
		return fmt.Errorf("plugin invoke output frame %q alignment %d exceeds the module ABI limit", frame.PortID, frame.Alignment)
	}
	end := uint64(frame.Offset) + uint64(frame.Size)
	if end <= uint64(arenaLength) && frame.Offset%frame.Alignment != 0 {
		return fmt.Errorf("plugin invoke output frame %q offset %d is not aligned to %d", frame.PortID, frame.Offset, frame.Alignment)
	}
	if frame.WireFormat != payloadWireFormatAlignedBinary {
		return nil
	}
	if frame.RequiredAlignment == 0 || !isPowerOfTwo32(uint32(frame.RequiredAlignment)) ||
		frame.Alignment < uint32(frame.RequiredAlignment) {
		return fmt.Errorf("plugin invoke aligned output frame %q has invalid required alignment %d/%d", frame.PortID, frame.RequiredAlignment, frame.Alignment)
	}
	if end <= uint64(arenaLength) && frame.Offset%uint32(frame.RequiredAlignment) != 0 {
		return fmt.Errorf("plugin invoke aligned output frame %q offset %d violates required alignment %d", frame.PortID, frame.Offset, frame.RequiredAlignment)
	}
	if frame.ByteLength == 0 || frame.ByteLength != frame.Size {
		return fmt.Errorf("plugin invoke aligned output frame %q byte length %d does not match size %d", frame.PortID, frame.ByteLength, frame.Size)
	}
	return nil
}

func isPowerOfTwo32(value uint32) bool {
	return value != 0 && value&(value-1) == 0
}

func extractPluginInvokePayload(response *pluginInvokeResponse, preferredPortID string) ([]byte, error) {
	if response == nil {
		return nil, fmt.Errorf("plugin invoke response is required")
	}
	if response.StatusCode != 0 {
		if response.ErrorMessage != "" {
			return nil, fmt.Errorf("plugin invoke failed (%d): %s", response.StatusCode, response.ErrorMessage)
		}
		if response.ErrorCode != "" {
			return nil, fmt.Errorf("plugin invoke failed (%d): %s", response.StatusCode, response.ErrorCode)
		}
		return nil, fmt.Errorf("plugin invoke failed with status %d", response.StatusCode)
	}
	if response.ErrorCode != "" || response.ErrorMessage != "" {
		if response.ErrorMessage != "" {
			return nil, fmt.Errorf("plugin invoke failed: %s", response.ErrorMessage)
		}
		return nil, fmt.Errorf("plugin invoke failed: %s", response.ErrorCode)
	}
	if len(response.OutputFrames) == 0 {
		return nil, nil
	}

	selected := response.OutputFrames[0]
	if preferredPortID != "" {
		for _, frame := range response.OutputFrames {
			if frame.PortID == preferredPortID {
				selected = frame
				break
			}
		}
	}

	end := uint64(selected.Offset) + uint64(selected.Size)
	if end > uint64(len(response.PayloadArena)) {
		return nil, fmt.Errorf(
			"plugin invoke output frame %q exceeds payload arena: offset=%d size=%d arena=%d",
			selected.PortID,
			selected.Offset,
			selected.Size,
			len(response.PayloadArena),
		)
	}

	return append([]byte(nil), response.PayloadArena[selected.Offset:end]...), nil
}

func decodeUint32LE(data []byte) (uint32, error) {
	if len(data) != 4 {
		return 0, fmt.Errorf("expected 4-byte little-endian integer, got %d bytes", len(data))
	}
	return binary.LittleEndian.Uint32(data), nil
}
