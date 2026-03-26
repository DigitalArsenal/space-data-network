package wasiplugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

const (
	hostcallImportModuleName = "sdn_host"

	hostcallStatusOK    int32 = 0
	hostcallStatusError int32 = 1

	maxHostcallRequestBytes  = 64 * 1024
	maxHostcallResponseBytes = 1024 * 1024
)

var (
	hostcallCapabilities = []string{"clock", "random"}
	hostcallOperations   = []string{
		"host.runtimeTarget",
		"host.listCapabilities",
		"host.listSupportedCapabilities",
		"host.listOperations",
		"host.hasCapability",
		"clock.now",
		"clock.monotonicNow",
		"clock.nowIso",
		"random.bytes",
	}
)

type hostcallEnvelope struct {
	Ok     bool                 `json:"ok"`
	Result interface{}          `json:"result"`
	Error  *hostcallErrorRecord `json:"error,omitempty"`
}

type hostcallErrorRecord struct {
	Name       string `json:"name"`
	Message    string `json:"message"`
	Code       string `json:"code,omitempty"`
	Operation  string `json:"operation,omitempty"`
	Capability string `json:"capability,omitempty"`
}

type hostCapabilityError struct {
	message    string
	code       string
	operation  string
	capability string
}

func (err *hostCapabilityError) Error() string {
	return err.message
}

type jsonHostcallBridge struct {
	monotonicBase    time.Time
	lastStatusCode   int32
	lastResponseJSON []byte
}

func newJSONHostcallBridge() *jsonHostcallBridge {
	bridge := &jsonHostcallBridge{
		monotonicBase: time.Now(),
	}
	bridge.resetEnvelope()
	return bridge
}

func (bridge *jsonHostcallBridge) newImportModule() (*wasmedge.Module, error) {
	module := wasmedge.NewModule(hostcallImportModuleName)
	if module == nil {
		return nil, fmt.Errorf("create %s import module", hostcallImportModuleName)
	}

	callJSONType := wasmedge.NewFunctionType(
		[]*wasmedge.ValType{
			wasmedge.NewValTypeI32(),
			wasmedge.NewValTypeI32(),
			wasmedge.NewValTypeI32(),
			wasmedge.NewValTypeI32(),
		},
		[]*wasmedge.ValType{wasmedge.NewValTypeI32()},
	)
	responseLenType := wasmedge.NewFunctionType(
		nil,
		[]*wasmedge.ValType{wasmedge.NewValTypeI32()},
	)
	readResponseType := wasmedge.NewFunctionType(
		[]*wasmedge.ValType{
			wasmedge.NewValTypeI32(),
			wasmedge.NewValTypeI32(),
		},
		[]*wasmedge.ValType{wasmedge.NewValTypeI32()},
	)
	statusType := wasmedge.NewFunctionType(
		nil,
		[]*wasmedge.ValType{wasmedge.NewValTypeI32()},
	)

	module.AddFunction(
		"call_json",
		wasmedge.NewFunction(callJSONType, bridge.callJSON, nil, 0),
	)
	module.AddFunction(
		"response_len",
		wasmedge.NewFunction(responseLenType, bridge.responseLen, nil, 0),
	)
	module.AddFunction(
		"read_response",
		wasmedge.NewFunction(readResponseType, bridge.readResponse, nil, 0),
	)
	module.AddFunction(
		"clear_response",
		wasmedge.NewFunction(statusType, bridge.clearResponse, nil, 0),
	)
	module.AddFunction(
		"last_status_code",
		wasmedge.NewFunction(statusType, bridge.lastStatusCodeFn, nil, 0),
	)

	return module, nil
}

func (bridge *jsonHostcallBridge) callJSON(
	_ interface{},
	callframe *wasmedge.CallingFrame,
	params []interface{},
) ([]interface{}, wasmedge.Result) {
	status := bridge.callJSONInternal(callframe, params)
	return []interface{}{status}, wasmedge.Result_Success
}

func (bridge *jsonHostcallBridge) responseLen(
	_ interface{},
	_ *wasmedge.CallingFrame,
	_ []interface{},
) ([]interface{}, wasmedge.Result) {
	return []interface{}{int32(len(bridge.lastResponseJSON))}, wasmedge.Result_Success
}

func (bridge *jsonHostcallBridge) readResponse(
	_ interface{},
	callframe *wasmedge.CallingFrame,
	params []interface{},
) ([]interface{}, wasmedge.Result) {
	memory, err := guestMemory(callframe)
	if err != nil {
		return []interface{}{int32(-1)}, wasmedge.Result_Success
	}

	dstPtr, err := coerceUint32Param(params, 0)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return []interface{}{int32(-1)}, wasmedge.Result_Success
	}
	dstLen, err := coerceUint32Param(params, 1)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return []interface{}{int32(-1)}, wasmedge.Result_Success
	}

	copied := minUint32(uint32(len(bridge.lastResponseJSON)), dstLen)
	if copied == 0 {
		return []interface{}{int32(0)}, wasmedge.Result_Success
	}

	if err := memory.SetData(bridge.lastResponseJSON[:copied], uint(dstPtr), uint(copied)); err != nil {
		bridge.setErrorEnvelope(err, "")
		return []interface{}{int32(-1)}, wasmedge.Result_Success
	}
	return []interface{}{int32(copied)}, wasmedge.Result_Success
}

func (bridge *jsonHostcallBridge) clearResponse(
	_ interface{},
	_ *wasmedge.CallingFrame,
	_ []interface{},
) ([]interface{}, wasmedge.Result) {
	bridge.resetEnvelope()
	return []interface{}{hostcallStatusOK}, wasmedge.Result_Success
}

func (bridge *jsonHostcallBridge) lastStatusCodeFn(
	_ interface{},
	_ *wasmedge.CallingFrame,
	_ []interface{},
) ([]interface{}, wasmedge.Result) {
	return []interface{}{bridge.lastStatusCode}, wasmedge.Result_Success
}

func (bridge *jsonHostcallBridge) callJSONInternal(
	callframe *wasmedge.CallingFrame,
	params []interface{},
) int32 {
	memory, err := guestMemory(callframe)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return hostcallStatusError
	}

	operationPtr, err := coerceUint32Param(params, 0)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return hostcallStatusError
	}
	operationLen, err := coerceUint32Param(params, 1)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return hostcallStatusError
	}
	payloadPtr, err := coerceUint32Param(params, 2)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return hostcallStatusError
	}
	payloadLen, err := coerceUint32Param(params, 3)
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return hostcallStatusError
	}

	if payloadLen > maxHostcallRequestBytes {
		bridge.setErrorEnvelope(
			fmt.Errorf("Hostcall request exceeds %d byte limit.", maxHostcallRequestBytes),
			"",
		)
		return hostcallStatusError
	}

	operationBytes, err := memory.GetData(uint(operationPtr), uint(operationLen))
	if err != nil {
		bridge.setErrorEnvelope(err, "")
		return hostcallStatusError
	}
	operation := string(operationBytes)

	var payload interface{}
	if payloadLen > 0 {
		payloadBytes, err := memory.GetData(uint(payloadPtr), uint(payloadLen))
		if err != nil {
			bridge.setErrorEnvelope(err, operation)
			return hostcallStatusError
		}
		if len(strings.TrimSpace(string(payloadBytes))) > 0 {
			if err := json.Unmarshal(payloadBytes, &payload); err != nil {
				bridge.setErrorEnvelope(err, operation)
				return hostcallStatusError
			}
		}
	}

	result, err := bridge.dispatch(operation, payload)
	if err != nil {
		bridge.setErrorEnvelope(err, operation)
		return hostcallStatusError
	}
	if err := bridge.setEnvelope(
		hostcallStatusOK,
		hostcallEnvelope{
			Ok:     true,
			Result: encodeHostcallValue(result),
		},
	); err != nil {
		bridge.setErrorEnvelope(err, operation)
		return hostcallStatusError
	}
	return hostcallStatusOK
}

func (bridge *jsonHostcallBridge) dispatch(operation string, payload interface{}) (interface{}, error) {
	switch strings.TrimSpace(operation) {
	case "host.runtimeTarget":
		return "wasmedge", nil
	case "host.listCapabilities":
		return cloneSortedStrings(hostcallCapabilities), nil
	case "host.listSupportedCapabilities":
		return cloneSortedStrings(hostcallCapabilities), nil
	case "host.listOperations":
		return cloneSortedStrings(hostcallOperations), nil
	case "host.hasCapability":
		return bridge.hasCapability(getStringField(payload, "capability")), nil
	case "clock.now":
		return time.Now().UnixMilli(), nil
	case "clock.monotonicNow":
		return float64(time.Since(bridge.monotonicBase).Nanoseconds()) / float64(time.Millisecond), nil
	case "clock.nowIso":
		return time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), nil
	case "random.bytes":
		length, err := getNonNegativeIntField(payload, "length")
		if err != nil {
			return nil, err
		}
		return fillRandomBytes(length)
	case "schedule.parse", "schedule.matches", "schedule.next":
		return nil, &hostCapabilityError{
			message:    `Capability "schedule_cron" is not supported by the Go WasmEdge host.`,
			code:       "host-capability-unsupported",
			operation:  operation,
			capability: "schedule_cron",
		}
	case "filesystem.resolvePath":
		return nil, &hostCapabilityError{
			message:    `Capability "filesystem" is not supported by the Go WasmEdge host.`,
			code:       "host-capability-unsupported",
			operation:  operation,
			capability: "filesystem",
		}
	default:
		return nil, fmt.Errorf(
			`Operation "%s" is not available in the synchronous hostcall ABI.`,
			operation,
		)
	}
}

func (bridge *jsonHostcallBridge) hasCapability(capability string) bool {
	for _, supported := range hostcallCapabilities {
		if supported == capability {
			return true
		}
	}
	return false
}

func (bridge *jsonHostcallBridge) resetEnvelope() {
	_ = bridge.setEnvelope(
		hostcallStatusOK,
		hostcallEnvelope{
			Ok:     true,
			Result: nil,
		},
	)
}

func (bridge *jsonHostcallBridge) setErrorEnvelope(err error, operation string) {
	envelope := hostcallEnvelope{
		Ok:    false,
		Error: serializeHostcallError(err, operation),
	}
	if setErr := bridge.setEnvelope(hostcallStatusError, envelope); setErr != nil {
		_ = bridge.setEnvelope(
			hostcallStatusError,
			hostcallEnvelope{
				Ok:    false,
				Error: serializeHostcallError(setErr, operation),
			},
		)
	}
}

func (bridge *jsonHostcallBridge) setEnvelope(statusCode int32, envelope hostcallEnvelope) error {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(encoded) > maxHostcallResponseBytes {
		return fmt.Errorf(
			"Hostcall response exceeds %d byte limit.",
			maxHostcallResponseBytes,
		)
	}
	bridge.lastStatusCode = statusCode
	bridge.lastResponseJSON = encoded
	return nil
}

func encodeHostcallValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return map[string]interface{}{
			"__type": "bytes",
			"base64": base64.StdEncoding.EncodeToString(typed),
		}
	case []string:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, encodeHostcallValue(item))
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = encodeHostcallValue(item)
		}
		return out
	default:
		return value
	}
}

func serializeHostcallError(err error, operation string) *hostcallErrorRecord {
	record := &hostcallErrorRecord{
		Name:      "Error",
		Message:   err.Error(),
		Operation: operation,
	}
	if capabilityErr, ok := err.(*hostCapabilityError); ok {
		record.Code = capabilityErr.code
		record.Capability = capabilityErr.capability
		if capabilityErr.operation != "" {
			record.Operation = capabilityErr.operation
		}
	}
	return record
}

func guestMemory(callframe *wasmedge.CallingFrame) (*wasmedge.Memory, error) {
	if callframe == nil {
		return nil, fmt.Errorf("hostcall is missing a calling frame")
	}
	memory := callframe.GetMemoryByIndex(0)
	if memory == nil {
		return nil, fmt.Errorf("hostcall could not resolve guest memory")
	}
	return memory, nil
}

func coerceUint32Param(params []interface{}, index int) (uint32, error) {
	if index < 0 || index >= len(params) {
		return 0, fmt.Errorf("missing hostcall parameter %d", index)
	}
	switch value := params[index].(type) {
	case int32:
		if value < 0 {
			return 0, fmt.Errorf("hostcall parameter %d must be non-negative", index)
		}
		return uint32(value), nil
	case uint32:
		return value, nil
	case int64:
		if value < 0 || value > math.MaxUint32 {
			return 0, fmt.Errorf("hostcall parameter %d is out of range", index)
		}
		return uint32(value), nil
	case uint64:
		if value > math.MaxUint32 {
			return 0, fmt.Errorf("hostcall parameter %d is out of range", index)
		}
		return uint32(value), nil
	case int:
		if value < 0 || uint64(value) > math.MaxUint32 {
			return 0, fmt.Errorf("hostcall parameter %d is out of range", index)
		}
		return uint32(value), nil
	case uint:
		if uint64(value) > math.MaxUint32 {
			return 0, fmt.Errorf("hostcall parameter %d is out of range", index)
		}
		return uint32(value), nil
	default:
		return 0, fmt.Errorf("unsupported hostcall parameter type %T", params[index])
	}
}

func getStringField(payload interface{}, field string) string {
	object, ok := payload.(map[string]interface{})
	if !ok {
		return ""
	}
	value, ok := object[field]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func getNonNegativeIntField(payload interface{}, field string) (int, error) {
	if payload == nil {
		return 0, nil
	}
	object, ok := payload.(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("%s must be an object.", field)
	}
	value, ok := object[field]
	if !ok {
		return 0, nil
	}
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed != math.Trunc(typed) || typed > math.MaxInt {
			return 0, fmt.Errorf("%s must be a non-negative integer.", field)
		}
		return int(typed), nil
	case int:
		if typed < 0 {
			return 0, fmt.Errorf("%s must be a non-negative integer.", field)
		}
		return typed, nil
	default:
		return 0, fmt.Errorf("%s must be a non-negative integer.", field)
	}
}

func cloneSortedStrings(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
