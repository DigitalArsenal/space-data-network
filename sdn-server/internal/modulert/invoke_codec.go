package modulert

import (
	"encoding/binary"
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	pluginInvokeRequestIdentifier  = "PINQ"
	pluginInvokeResponseIdentifier = "PINS"

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

	packedFrames, payloadArena, err := packInvokeInputFrames(frames)
	if err != nil {
		return nil, err
	}

	builder := flatbuffers.NewBuilder(256 + len(payloadArena))

	methodIDOffset := builder.CreateString(methodID)
	payloadArenaOffset := builder.CreateByteVector(payloadArena)

	inputFrameOffsets := make([]flatbuffers.UOffsetT, 0, len(packedFrames))
	for _, frame := range packedFrames {
		typeRefOffset := buildFlatBufferTypeRef(builder, frame)
		portIDOffset := flatbuffers.UOffsetT(0)
		if frame.PortID != "" {
			portIDOffset = builder.CreateString(frame.PortID)
		}

		builder.StartObject(12)
		builder.PrependUOffsetTSlot(0, typeRefOffset, 0)
		builder.PrependUOffsetTSlot(1, portIDOffset, 0)
		builder.PrependUint16Slot(2, frame.Alignment, 8)
		builder.PrependUint32Slot(3, frame.Offset, 0)
		builder.PrependUint32Slot(4, frame.Size, 0)
		builder.PrependByteSlot(5, 0, 0)
		builder.PrependUint32Slot(6, 0, 0)
		builder.PrependByteSlot(7, 0, 0)
		builder.PrependUint64Slot(8, 0, 0)
		builder.PrependUint32Slot(9, 0, 0)
		builder.PrependUint64Slot(10, 0, 0)
		builder.PrependBoolSlot(11, false, false)
		inputFrameOffsets = append(inputFrameOffsets, builder.EndObject())
	}

	builder.StartVector(4, len(inputFrameOffsets), 4)
	for index := len(inputFrameOffsets) - 1; index >= 0; index -= 1 {
		builder.PrependUOffsetT(inputFrameOffsets[index])
	}
	inputFramesOffset := builder.EndVector(len(inputFrameOffsets))

	builder.StartObject(3)
	builder.PrependUOffsetTSlot(0, methodIDOffset, 0)
	builder.PrependUOffsetTSlot(1, inputFramesOffset, 0)
	builder.PrependUOffsetTSlot(2, payloadArenaOffset, 0)
	root := builder.EndObject()
	builder.FinishWithFileIdentifier(root, []byte(pluginInvokeRequestIdentifier))

	return builder.FinishedBytes(), nil
}

func packInvokeInputFrames(frames []InvokeInputFrame) ([]InvokeInputFrame, []byte, error) {
	if len(frames) == 0 {
		return nil, nil, fmt.Errorf("at least one input frame is required")
	}

	packed := make([]InvokeInputFrame, 0, len(frames))
	payloadArena := make([]byte, 0)
	var offset uint32

	for _, frame := range frames {
		if frame.PortID == "" {
			return nil, nil, fmt.Errorf("input frame port id is required")
		}

		payload := append([]byte(nil), frame.Payload...)
		alignment := frame.Alignment
		if alignment == 0 {
			alignment = frame.RequiredAlignment
		}
		if alignment == 0 {
			alignment = 8
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

	return packed, payloadArena, nil
}

func buildFlatBufferTypeRef(builder *flatbuffers.Builder, frame InvokeInputFrame) flatbuffers.UOffsetT {
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

	builder.StartObject(9)
	builder.PrependUOffsetTSlot(0, schemaNameOffset, 0)
	builder.PrependUOffsetTSlot(1, fileIdentifierOffset, 0)
	builder.PrependUOffsetTSlot(2, 0, 0)
	builder.PrependBoolSlot(3, false, false)
	builder.PrependByteSlot(4, frame.WireFormat, payloadWireFormatFlatbuffer)
	builder.PrependUOffsetTSlot(5, rootTypeNameOffset, 0)
	builder.PrependUint16Slot(6, frame.FixedStringLength, 0)
	builder.PrependUint32Slot(7, frame.ByteLength, 0)
	builder.PrependUint16Slot(8, frame.RequiredAlignment, 0)
	return builder.EndObject()
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
	if !flatbuffers.BufferHasIdentifier(data, pluginInvokeResponseIdentifier) {
		return nil, fmt.Errorf("plugin invoke response buffer identifier mismatch")
	}

	root := &flatbuffers.Table{}
	root.Bytes = data
	root.Pos = flatbuffers.GetUOffsetT(data)

	response := &pluginInvokeResponse{}
	if o := root.Offset(4); o != 0 {
		offset := flatbuffers.UOffsetT(o)
		response.StatusCode = root.GetInt32(offset + root.Pos)
	}
	if o := root.Offset(12); o != 0 {
		offset := flatbuffers.UOffsetT(o)
		response.PayloadArena = append([]byte(nil), root.ByteVector(offset+root.Pos)...)
	}
	if o := root.Offset(14); o != 0 {
		offset := flatbuffers.UOffsetT(o)
		response.ErrorCode = string(root.ByteVector(offset + root.Pos))
	}
	if o := root.Offset(16); o != 0 {
		offset := flatbuffers.UOffsetT(o)
		response.ErrorMessage = string(root.ByteVector(offset + root.Pos))
	}
	if o := root.Offset(10); o != 0 {
		offset := flatbuffers.UOffsetT(o)
		vectorStart := root.Vector(offset)
		vectorLen := root.VectorLen(offset)
		response.OutputFrames = make([]pluginInvokeFrame, 0, vectorLen)
		for index := 0; index < vectorLen; index += 1 {
			framePos := root.Indirect(vectorStart + flatbuffers.UOffsetT(index*4))
			frameTable := &flatbuffers.Table{Bytes: data, Pos: framePos}
			frame := pluginInvokeFrame{}
			if fo := frameTable.Offset(6); fo != 0 {
				frameOffset := flatbuffers.UOffsetT(fo)
				frame.PortID = string(frameTable.ByteVector(frameOffset + frameTable.Pos))
			}
			if fo := frameTable.Offset(10); fo != 0 {
				frameOffset := flatbuffers.UOffsetT(fo)
				frame.Offset = frameTable.GetUint32(frameOffset + frameTable.Pos)
			}
			if fo := frameTable.Offset(12); fo != 0 {
				frameOffset := flatbuffers.UOffsetT(fo)
				frame.Size = frameTable.GetUint32(frameOffset + frameTable.Pos)
			}
			response.OutputFrames = append(response.OutputFrames, frame)
		}
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
