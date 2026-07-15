package modulert

import (
	"encoding/binary"
	"fmt"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	invokeArenaAlignment = 8

	payloadWireFormatFlatbuffer    = 0
	payloadWireFormatAlignedBinary = 1
)

type pluginInvokeFrame struct {
	PortID string
	Offset uint32
	Size   uint32
}

type InvokeInputFrame struct {
	PortID            string
	Payload           []byte
	Offset            uint32
	Size              uint32
	SchemaName        string
	FileIdentifier    string
	RootTypeName      string
	WireFormat        byte
	FixedStringLength uint16
	ByteLength        uint32
	RequiredAlignment uint16
	Alignment         uint16
}

type pluginInvokeResponse struct {
	StatusCode   int32
	ErrorCode    string
	ErrorMessage string
	OutputFrames []pluginInvokeFrame
	PayloadArena []byte
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

		payload := append([]byte(nil), frame.Payload...)
		alignment := normalizeInvokeAlignment(frame.Alignment, frame.RequiredAlignment)
		if uint32(alignment) > arenaAlignment {
			arenaAlignment = uint32(alignment)
		}
		alignedOffset := alignInvokeOffset(offset, alignment)
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
		packed = append(packed, frame)

		offset = alignedOffset + uint32(len(payload))
	}

	return packed, payloadArena, arenaAlignment, nil
}

func normalizeInvokeAlignment(alignment uint16, requiredAlignment uint16) uint16 {
	normalized := alignment
	if requiredAlignment > normalized {
		normalized = requiredAlignment
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
	piv.TABAddMutability(builder, piv.EnumValuesbufferMutability["IMMUTABLE"])
	piv.TABAddOwnership(builder, piv.EnumValuesbufferOwnership["HOST_OWNED"])
	if portIDOffset != 0 {
		piv.TABAddPortId(builder, portIDOffset)
	}
	return piv.TABEnd(builder)
}

func buildFlatBufferTypeRef(builder *flatbuffers.Builder, frame InvokeInputFrame) flatbuffers.UOffsetT {
	if frame.SchemaName == "" && frame.FileIdentifier == "" && frame.RootTypeName == "" {
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
	if rootTypeNameOffset != 0 {
		piv.FlatBufferTypeRefAddRootType(builder, rootTypeNameOffset)
	}
	return piv.FlatBufferTypeRefEnd(builder)
}

func alignInvokeOffset(offset uint32, alignment uint16) uint32 {
	if alignment <= 1 {
		return offset
	}
	remainder := offset % uint32(alignment)
	if remainder == 0 {
		return offset
	}
	return offset + uint32(alignment) - remainder
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
		StatusCode:   envelope.StatusCode(),
		ErrorCode:    string(envelope.ErrorCode()),
		ErrorMessage: string(envelope.ErrorMessage()),
		PayloadArena: append([]byte(nil), envelope.PayloadArenaBytes()...),
	}

	outputCount := envelope.OutputsLength()
	response.OutputFrames = make([]pluginInvokeFrame, 0, outputCount)
	for index := 0; index < outputCount; index += 1 {
		var frameTable piv.TAB
		if !envelope.Outputs(&frameTable, index) {
			continue
		}
		response.OutputFrames = append(response.OutputFrames, pluginInvokeFrame{
			PortID: string(frameTable.PortId()),
			Offset: frameTable.Offset(),
			Size:   frameTable.Size(),
		})
	}

	return response, nil
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
