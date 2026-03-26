package wasiplugin

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
	invoke "github.com/spacedatanetwork/sdn-server/internal/wasiplugin/fbs/orbpro/invoke"
	pluginfbs "github.com/spacedatanetwork/sdn-server/internal/wasiplugin/fbs/orbpro/plugin"
	stream "github.com/spacedatanetwork/sdn-server/internal/wasiplugin/fbs/orbpro/stream"
)

const (
	rawDataSchemaName = "orbpro.plugin.RawDataPayload"
	requestPortID     = "request"

	requestPayloadTypeID        = "application/json;orbpro.protection.request"
	publicKeyPayloadTypeID      = "application/json;orbpro.protection.public-key"
	challengePayloadTypeID      = "application/json;orbpro.protection.challenge"
	statusPayloadTypeID         = "application/json;orbpro.protection.status"
	configurationPayloadTypeID  = "application/json;orbpro.protection.runtime-configuration"
	responsePacketPayloadTypeID = "application/octet-stream;orbpro.protection.response-packet"
	requestPacketPayloadTypeID  = "application/octet-stream;orbpro.protection.request-packet"
)

type RuntimeConfiguration struct {
	ServerPrivateKeyHex string `json:"serverPrivateKeyHex,omitempty"`
	GenerateRandomKey   bool   `json:"generateRandomKey,omitempty"`
	MaxSkewMs           int64  `json:"maxSkewMs,omitempty"`
	ActiveKeyVersion    uint32 `json:"activeKeyVersion,omitempty"`
}

type RuntimeState struct {
	Version          int    `json:"version"`
	ProtocolVersion  int    `json:"protocolVersion"`
	Curve            string `json:"curve"`
	PublicKeyHex     string `json:"publicKeyHex"`
	ExpiresAtMs      int64  `json:"expiresAtMs"`
	ActiveKeyVersion uint32 `json:"activeKeyVersion"`
	MaxSkewMs        int64  `json:"maxSkewMs"`
}

type StatusState struct {
	Status           int32  `json:"status"`
	ActiveKeyVersion uint32 `json:"activeKeyVersion"`
	ExpiresAtMs      int64  `json:"expiresAtMs"`
	PublicKeyHex     string `json:"publicKeyHex,omitempty"`
}

type InvokeInputFrame struct {
	PortID      string
	TypeID      string
	Payload     []byte
	Alignment   uint16
	StreamID    uint32
	Sequence    uint64
	TraceID     uint64
	EndOfStream bool
}

type InvokeOutputFrame struct {
	PortID      string
	TypeID      string
	Payload     []byte
	Alignment   uint16
	StreamID    uint32
	Sequence    uint64
	TraceID     uint64
	EndOfStream bool
}

type InvokeResult struct {
	StatusCode       int32
	Yielded          bool
	BacklogRemaining uint32
	ErrorCode        string
	ErrorMessage     string
	Outputs          []InvokeOutputFrame
}

type InvokeError struct {
	MethodID     string
	StatusCode   int32
	ErrorCode    string
	ErrorMessage string
}

func (err *InvokeError) Error() string {
	if err == nil {
		return "plugin invoke failed"
	}
	if err.ErrorMessage != "" {
		return fmt.Sprintf(
			`plugin method "%s" failed with status %d (%s): %s`,
			err.MethodID,
			err.StatusCode,
			err.ErrorCode,
			err.ErrorMessage,
		)
	}
	return fmt.Sprintf(
		`plugin method "%s" failed with status %d (%s)`,
		err.MethodID,
		err.StatusCode,
		err.ErrorCode,
	)
}

func (state RuntimeState) PublicKeyBytes() ([]byte, error) {
	if state.PublicKeyHex == "" {
		return nil, fmt.Errorf("runtime state does not include publicKeyHex")
	}
	decoded, err := hex.DecodeString(state.PublicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode publicKeyHex: %w", err)
	}
	return decoded, nil
}

func encodeRawDataPayload(typeID string, data []byte) []byte {
	builder := flatbuffers.NewBuilder(len(typeID) + len(data) + 128)
	typeIDOffset := builder.CreateString(typeID)
	dataOffset := builder.CreateByteVector(data)
	pluginfbs.RawDataPayloadStart(builder)
	pluginfbs.RawDataPayloadAddTypeId(builder, typeIDOffset)
	pluginfbs.RawDataPayloadAddData(builder, dataOffset)
	root := pluginfbs.RawDataPayloadEnd(builder)
	pluginfbs.FinishRawDataPayloadBuffer(builder, root)
	return bytes.Clone(builder.FinishedBytes())
}

func decodeRawDataPayload(data []byte) (string, []byte, error) {
	if len(data) == 0 {
		return "application/octet-stream", nil, nil
	}
	payload := pluginfbs.GetRootAsRawDataPayload(data, 0)
	typeID := string(payload.TypeId())
	if typeID == "" {
		typeID = "application/octet-stream"
	}
	return typeID, bytes.Clone(payload.DataBytes()), nil
}

func encodePluginInvokeRequest(methodID string, inputs []InvokeInputFrame) ([]byte, error) {
	if methodID == "" {
		return nil, fmt.Errorf("methodID is required")
	}

	normalizedInputs := make([]InvokeInputFrame, 0, len(inputs))
	for _, input := range inputs {
		frame := input
		if frame.PortID == "" {
			frame.PortID = requestPortID
		}
		if frame.TypeID == "" {
			frame.TypeID = requestPayloadTypeID
		}
		if frame.Alignment == 0 {
			frame.Alignment = 8
		}
		normalizedInputs = append(normalizedInputs, frame)
	}

	type frameRecord struct {
		input         InvokeInputFrame
		encodedRaw    []byte
		alignedOffset uint32
	}

	arena := make([]byte, 0, 1024)
	records := make([]frameRecord, 0, len(normalizedInputs))
	for _, input := range normalizedInputs {
		encodedRaw := encodeRawDataPayload(input.TypeID, input.Payload)
		offset := alignOffset(uint32(len(arena)), input.Alignment)
		for uint32(len(arena)) < offset {
			arena = append(arena, 0)
		}
		arena = append(arena, encodedRaw...)
		records = append(records, frameRecord{
			input:         input,
			encodedRaw:    encodedRaw,
			alignedOffset: offset,
		})
	}

	builder := flatbuffers.NewBuilder(2048)
	frameOffsets := make([]flatbuffers.UOffsetT, 0, len(records))
	for _, record := range records {
		schemaNameOffset := builder.CreateString(rawDataSchemaName)
		stream.FlatBufferTypeRefStart(builder)
		stream.FlatBufferTypeRefAddSchemaName(builder, schemaNameOffset)
		stream.FlatBufferTypeRefAddAcceptsAnyFlatbuffer(builder, false)
		typeRefOffset := stream.FlatBufferTypeRefEnd(builder)

		portIDOffset := builder.CreateString(record.input.PortID)
		stream.TypedArenaBufferStart(builder)
		stream.TypedArenaBufferAddTypeRef(builder, typeRefOffset)
		stream.TypedArenaBufferAddPortId(builder, portIDOffset)
		stream.TypedArenaBufferAddAlignment(builder, record.input.Alignment)
		stream.TypedArenaBufferAddOffset(builder, record.alignedOffset)
		stream.TypedArenaBufferAddSize(builder, uint32(len(record.encodedRaw)))
		stream.TypedArenaBufferAddOwnership(builder, stream.BufferOwnershipBORROWED)
		stream.TypedArenaBufferAddGeneration(builder, 0)
		stream.TypedArenaBufferAddMutability(builder, stream.BufferMutabilityIMMUTABLE)
		stream.TypedArenaBufferAddTraceId(builder, record.input.TraceID)
		stream.TypedArenaBufferAddStreamId(builder, record.input.StreamID)
		stream.TypedArenaBufferAddSequence(builder, record.input.Sequence)
		stream.TypedArenaBufferAddEndOfStream(builder, record.input.EndOfStream)
		frameOffsets = append(frameOffsets, stream.TypedArenaBufferEnd(builder))
	}

	methodIDOffset := builder.CreateString(methodID)
	payloadArenaOffset := builder.CreateByteVector(arena)

	var inputFramesOffset flatbuffers.UOffsetT
	if len(frameOffsets) > 0 {
		invoke.PluginInvokeRequestStartInputFramesVector(builder, len(frameOffsets))
		for index := len(frameOffsets) - 1; index >= 0; index-- {
			builder.PrependUOffsetT(frameOffsets[index])
		}
		inputFramesOffset = builder.EndVector(len(frameOffsets))
	}

	invoke.PluginInvokeRequestStart(builder)
	invoke.PluginInvokeRequestAddMethodId(builder, methodIDOffset)
	if inputFramesOffset != 0 {
		invoke.PluginInvokeRequestAddInputFrames(builder, inputFramesOffset)
	}
	if len(arena) > 0 {
		invoke.PluginInvokeRequestAddPayloadArena(builder, payloadArenaOffset)
	}
	root := invoke.PluginInvokeRequestEnd(builder)
	invoke.FinishPluginInvokeRequestBuffer(builder, root)
	return bytes.Clone(builder.FinishedBytes()), nil
}

func decodePluginInvokeResponse(data []byte) (*InvokeResult, error) {
	if len(data) == 0 {
		return &InvokeResult{}, nil
	}
	if !invoke.PluginInvokeResponseBufferHasIdentifier(data) {
		return nil, fmt.Errorf("PluginInvokeResponse identifier mismatch")
	}

	response := invoke.GetRootAsPluginInvokeResponse(data, 0)
	arena := response.PayloadArenaBytes()
	result := &InvokeResult{
		StatusCode:       response.StatusCode(),
		Yielded:          response.Yielded(),
		BacklogRemaining: response.BacklogRemaining(),
		ErrorCode:        string(response.ErrorCode()),
		ErrorMessage:     string(response.ErrorMessage()),
		Outputs:          make([]InvokeOutputFrame, 0, response.OutputFramesLength()),
	}

	for index := 0; index < response.OutputFramesLength(); index++ {
		var frame stream.TypedArenaBuffer
		if !response.OutputFrames(&frame, index) {
			continue
		}
		offset := frame.Offset()
		size := frame.Size()
		if int(offset+size) > len(arena) {
			return nil, fmt.Errorf(
				"output frame %d exceeds payload arena bounds (%d + %d > %d)",
				index,
				offset,
				size,
				len(arena),
			)
		}
		typeID, payload, err := decodeRawDataPayload(arena[offset : offset+size])
		if err != nil {
			return nil, fmt.Errorf("decode output frame %d: %w", index, err)
		}
		result.Outputs = append(
			result.Outputs,
			InvokeOutputFrame{
				PortID:      string(frame.PortId()),
				TypeID:      typeID,
				Payload:     payload,
				Alignment:   frame.Alignment(),
				StreamID:    frame.StreamId(),
				Sequence:    frame.Sequence(),
				TraceID:     frame.TraceId(),
				EndOfStream: frame.EndOfStream(),
			},
		)
	}

	return result, nil
}

func decodeJSONPayload(data []byte, target interface{}) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	return json.Unmarshal(trimmed, target)
}

func singleOutputFrame(result *InvokeResult, expectedPort string) (*InvokeOutputFrame, error) {
	if result == nil {
		return nil, fmt.Errorf("invoke result is nil")
	}
	for _, output := range result.Outputs {
		if expectedPort == "" || output.PortID == expectedPort {
			frame := output
			return &frame, nil
		}
	}
	return nil, fmt.Errorf("invoke result does not contain output port %q", expectedPort)
}

func invokeStatusError(methodID string, result *InvokeResult) error {
	if result == nil || result.StatusCode == 0 {
		return nil
	}
	return &InvokeError{
		MethodID:     methodID,
		StatusCode:   result.StatusCode,
		ErrorCode:    result.ErrorCode,
		ErrorMessage: result.ErrorMessage,
	}
}

func alignOffset(offset uint32, alignment uint16) uint32 {
	if alignment <= 1 {
		return offset
	}
	mask := uint32(alignment) - 1
	return (offset + mask) &^ mask
}
