package modulert

import (
	"context"
	"fmt"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// InvokeOutputFrame is one named output emitted by a module-sdk method.
type InvokeOutputFrame struct {
	PortID  string
	Payload []byte
}

// InvokeMethodOutputs calls plugin_invoke_stream and returns every output frame
// in the order supplied by the guest. InvokeMethodFrames intentionally keeps
// its historical single-output selection behavior.
func (m *Module) InvokeMethodOutputs(ctx context.Context, methodID string, inputFrames []InvokeInputFrame) (outputs []InvokeOutputFrame, err error) {
	started := time.Now()
	defer func() {
		m.recordInvokeResult(started, err)
	}()

	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.refreshMemoryStatsLocked()

	if m.mod == nil {
		return nil, fmt.Errorf("module not loaded")
	}
	if m.paused {
		return nil, fmt.Errorf("module paused")
	}

	req, err := encodePluginInvokeRequestFrames(methodID, inputFrames)
	if err != nil {
		return nil, fmt.Errorf("encode invoke request: %w", err)
	}
	reqPtr, err := m.mod.Allocate(req)
	if err != nil {
		return nil, fmt.Errorf("allocate request: %w", err)
	}
	defer m.mod.SecureDeallocate(reqPtr, uint32(len(req)))

	responseLenPtr, err := m.mod.AllocateSize(4)
	if err != nil {
		return nil, fmt.Errorf("allocate response length: %w", err)
	}
	defer m.mod.SecureDeallocate(responseLenPtr, 4)
	if err := m.mod.WriteMemory(responseLenPtr, []byte{0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("zero response length: %w", err)
	}

	results, err := m.mod.ExecuteContext(ctx, "plugin_invoke_stream",
		int32(reqPtr), int32(len(req)), int32(responseLenPtr),
	)
	if err != nil {
		return nil, fmt.Errorf("plugin_invoke_stream(%s): %w", methodID, err)
	}
	responsePtr := uint32(wasmrt.ToInt32(results[0]))
	responseLenBytes, err := m.mod.ReadMemory(responseLenPtr, 4)
	if err != nil {
		return nil, fmt.Errorf("read response length: %w", err)
	}
	responseLen, err := decodeUint32LE(responseLenBytes)
	if err != nil {
		return nil, fmt.Errorf("decode response length: %w", err)
	}
	if responseLen == 0 {
		return nil, fmt.Errorf("plugin_invoke_stream(%s) returned an empty response", methodID)
	}
	if responsePtr == 0 {
		return nil, fmt.Errorf("plugin_invoke_stream(%s) returned a null response pointer", methodID)
	}
	defer m.mod.SecureDeallocate(responsePtr, responseLen)

	responseBytes, err := m.mod.ReadMemory(responsePtr, responseLen)
	if err != nil {
		return nil, fmt.Errorf("read invoke response: %w", err)
	}
	response, err := decodePluginInvokeResponseBytes(responseBytes)
	if err != nil {
		return nil, fmt.Errorf("decode invoke response: %w (raw %d bytes: %.400q)", err, len(responseBytes), responseBytes)
	}
	return extractPluginInvokeOutputs(response)
}

func extractPluginInvokeOutputs(response *pluginInvokeResponse) ([]InvokeOutputFrame, error) {
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

	outputs := make([]InvokeOutputFrame, 0, len(response.OutputFrames))
	for _, frame := range response.OutputFrames {
		end := uint64(frame.Offset) + uint64(frame.Size)
		if end > uint64(len(response.PayloadArena)) {
			return nil, fmt.Errorf(
				"plugin invoke output frame %q exceeds payload arena: offset=%d size=%d arena=%d",
				frame.PortID, frame.Offset, frame.Size, len(response.PayloadArena),
			)
		}
		outputs = append(outputs, InvokeOutputFrame{
			PortID:  frame.PortID,
			Payload: append([]byte(nil), response.PayloadArena[frame.Offset:end]...),
		})
	}
	return outputs, nil
}
