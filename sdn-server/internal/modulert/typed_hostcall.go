package modulert

// Typed hostcalls (typed contracts 1.0): a module's call to the node is an SDS $PIV invocation in the module ->
// host direction. PIVRequest.METHOD_ID is the operation; PAYLOAD_ARENA holds its frames, named by TAB.PORT_ID - the
// "params" aligned record (hostops, the SDK's schemas/runtime/HostOps.fbs), list ports (runs of aligned records) and
// raw ports (unbounded bytes). The node answers with a $PIV response carrying the "result" record and the
// operation's list / raw ports, or STATUS FAILED with ERROR_CODE (hostcallErrorCodes) and ERROR_MESSAGE.
//
// The capability handlers keep their existing request maps: this file turns a typed call into the map a handler
// already reads (numbers as numbers, bytes as the base64 the handlers decode internally - never text the module
// sees) and the handler's answer into the operation's records and frames. The JSON envelope path still serves
// modules built before 1.0 (graph: sdk-compliance-typed-only-20261013).

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/hostops"
)

// hostcallErrorCodes is the SDK's HOSTCALL_ERROR_CODES.
var hostcallErrorCodes = []string{"unsupported", "not-authorized", "invalid-params", "failed", "refused", "sandbox", "timeout", "too-large"}

// hostcallError is a typed hostcall failure with its ERROR_CODE.
type hostcallError struct{ code, message string }

func (e *hostcallError) Error() string { return e.message }

func typedErr(code, format string, args ...any) error {
	return &hostcallError{code: code, message: fmt.Sprintf(format, args...)}
}

// maxRandomBytes is the one bound for random.bytes and crypto.random: above it the call is refused (too-large).
const maxRandomBytes = 8192

func isTypedHostcall(payload []byte) bool {
	return len(payload) >= 8 && string(payload[4:8]) == "$PIV"
}

type typedCall struct {
	op     string
	spec   hostops.Operation
	params map[string]any
	lists  map[string][]map[string]any
	raw    map[string][]byte
}

type typedAnswer struct {
	result map[string]any
	lists  map[string][]map[string]any
	raw    map[string][]byte
}

// DispatchTyped answers one $PIV hostcall with a $PIV response. It never fails: errors become STATUS FAILED.
func (hb *HostBridge) DispatchTyped(payload []byte) (response []byte) {
	// A host function that panics takes the node down with it: a fault here is the call's failure, not the node's.
	defer func() {
		if r := recover(); r != nil {
			response = encodeTypedFailure(typedErr("failed", "hostcall failed in the host: %v", r))
		}
	}()
	call, err := decodeTypedCall(payload)
	if err != nil {
		return encodeTypedFailure(err)
	}
	answer, err := hb.answerTyped(call)
	if err != nil {
		return encodeTypedFailure(err)
	}
	out, err := encodeTypedAnswer(call, answer)
	if err != nil {
		return encodeTypedFailure(err)
	}
	return out
}

// ---- the $PIV request and response -------------------------------------------------------------------------------

func decodeTypedCall(payload []byte) (*typedCall, error) {
	if !piv.PIVBufferHasIdentifier(payload) {
		return nil, typedErr("invalid-params", "hostcall is not an SDS $PIV buffer")
	}
	request := piv.GetRootAsPIV(payload, 0).Request(nil)
	if request == nil {
		return nil, typedErr("invalid-params", "$PIV hostcall carries no request")
	}
	op := string(request.MethodId())
	spec, ok := hostops.Operations[op]
	if !ok {
		return nil, typedErr("unsupported", "operation %q is not a host operation", op)
	}
	call := &typedCall{op: op, spec: spec, lists: map[string][]map[string]any{}, raw: map[string][]byte{}}
	arena := request.PayloadArenaBytes()
	aligned := piv.EnumValuespayloadWireFormat["ALIGNED_BINARY"]
	for i := 0; i < request.InputsLength(); i++ {
		var tab piv.TAB
		if !request.Inputs(&tab, i) {
			continue
		}
		port := string(tab.PortId())
		end := uint64(tab.Offset()) + uint64(tab.Size())
		if end > uint64(len(arena)) {
			return nil, typedErr("invalid-params", "%s: frame %q exceeds the payload arena", op, port)
		}
		frame := append([]byte(nil), arena[tab.Offset():end]...)
		ref := tab.TypeRef(nil)
		if tab.WireFormat() == aligned && ref != nil && len(ref.RootType()) > 0 {
			root := string(ref.RootType())
			if port == "params" {
				if root != spec.Params {
					return nil, typedErr("invalid-params", "%s takes %s params, the frame carries %s", op, orNone(spec.Params), root)
				}
				record, err := hostops.Read(root, frame)
				if err != nil {
					return nil, typedErr("invalid-params", "%s: %v", op, err)
				}
				call.params = record
				continue
			}
			if want := spec.ListsIn[port]; want != "" {
				if root != want {
					return nil, typedErr("invalid-params", "%s: list %q takes %s, the frame carries %s", op, port, want, root)
				}
				list, err := hostops.ReadList(root, frame)
				if err != nil {
					return nil, typedErr("invalid-params", "%s: %v", op, err)
				}
				call.lists[port] = list
				continue
			}
			return nil, typedErr("invalid-params", "%s has no record port %q", op, port)
		}
		if !contains(spec.RawIn, port) {
			return nil, typedErr("invalid-params", "%s has no raw port %q", op, port)
		}
		call.raw[port] = frame
	}
	if call.params == nil {
		call.params = map[string]any{}
	}
	return call, nil
}

func orNone(s string) string {
	if s == "" {
		return "no"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// typedOutFrame is one response frame: an aligned record / run of records (root set) or raw bytes.
type typedOutFrame struct {
	port, root string
	single     bool
	align      int
	bytes      []byte
}

func encodeTypedAnswer(call *typedCall, answer *typedAnswer) ([]byte, error) {
	var frames []typedOutFrame
	if answer.result != nil {
		if call.spec.Result == "" {
			return nil, typedErr("failed", "%s answers no result record", call.op)
		}
		record, err := hostops.Write(call.spec.Result, answer.result)
		if err != nil {
			return nil, recordErr(err)
		}
		t := hostops.Records[call.spec.Result]
		frames = append(frames, typedOutFrame{port: "result", root: call.spec.Result, single: true, align: t.Align, bytes: record})
	}
	for _, port := range sortedKeys(answer.lists) {
		root := call.spec.ListsOut[port]
		if root == "" {
			return nil, typedErr("failed", "%s has no list port %q", call.op, port)
		}
		run, err := hostops.WriteList(root, answer.lists[port])
		if err != nil {
			return nil, recordErr(err)
		}
		frames = append(frames, typedOutFrame{port: port, root: root, align: hostops.Records[root].Align, bytes: run})
	}
	for _, port := range sortedKeys(answer.raw) {
		if !contains(call.spec.RawOut, port) {
			return nil, typedErr("failed", "%s has no raw port %q", call.op, port)
		}
		frames = append(frames, typedOutFrame{port: port, align: 1, bytes: answer.raw[port]})
	}
	return encodeTypedResponse(frames, 0, "", ""), nil
}

// recordErr is a record that could not be written: a string or list that does not fit is too-large.
func recordErr(err error) error {
	if strings.Contains(err.Error(), "exceed") {
		return typedErr("too-large", "%v", err)
	}
	return typedErr("failed", "%v", err)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func encodeTypedFailure(err error) []byte {
	code, message := "failed", err.Error()
	var he *hostcallError
	if errors.As(err, &he) {
		code = he.code
	}
	if !contains(hostcallErrorCodes, code) {
		code = "failed"
	}
	return encodeTypedResponse(nil, 1, code, message)
}

func encodeTypedResponse(frames []typedOutFrame, statusCode int32, errorCode, errorMessage string) []byte {
	var arena []byte
	type placed struct {
		frame         typedOutFrame
		offset, align uint32
	}
	var laid []placed
	for _, f := range frames {
		align := uint32(invokeArenaAlignment)
		if uint32(f.align) > align {
			align = uint32(f.align)
		}
		offset := alignInvokeOffset(uint32(len(arena)), uint16(align))
		arena = append(arena, make([]byte, int(offset)-len(arena))...)
		arena = append(arena, f.bytes...)
		laid = append(laid, placed{frame: f, offset: offset, align: align})
	}
	b := flatbuffers.NewBuilder(256 + len(arena))
	var arenaOffset flatbuffers.UOffsetT
	if len(arena) > 0 {
		arenaOffset = createAlignedPIVByteVector(b, arena, invokeArenaAlignment)
	}
	tabs := make([]flatbuffers.UOffsetT, 0, len(laid))
	for _, p := range laid {
		frame := InvokeInputFrame{PortID: p.frame.port, Offset: p.offset, Size: uint32(len(p.frame.bytes)), Alignment: uint16(p.align)}
		if p.frame.root != "" {
			frame.WireFormat = payloadWireFormatAlignedBinary
			frame.SchemaName = "HostOps.fbs"
			frame.RootTypeName = p.frame.root
			frame.RequiredAlignment = uint16(p.frame.align)
			if p.frame.single {
				frame.ByteLength = uint32(len(p.frame.bytes)) // one record; 0 = a run of records
			}
		}
		tabs = append(tabs, buildPIVTAB(b, frame))
	}
	var outputs flatbuffers.UOffsetT
	if len(tabs) > 0 {
		piv.PIVResponseStartOutputsVector(b, len(tabs))
		for i := len(tabs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(tabs[i])
		}
		outputs = b.EndVector(len(tabs))
	}
	var codeOffset, messageOffset flatbuffers.UOffsetT
	if errorCode != "" {
		codeOffset = b.CreateString(errorCode)
		messageOffset = b.CreateString(errorMessage)
	}
	piv.PIVResponseStart(b)
	piv.PIVResponseAddStatusCode(b, statusCode)
	if statusCode != 0 {
		piv.PIVResponseAddStatus(b, piv.EnumValuespivStatus["FAILED"])
	}
	if outputs != 0 {
		piv.PIVResponseAddOutputs(b, outputs)
	}
	if arenaOffset != 0 {
		piv.PIVResponseAddPayloadArena(b, arenaOffset)
	}
	if codeOffset != 0 {
		piv.PIVResponseAddErrorCode(b, codeOffset)
		piv.PIVResponseAddErrorMessage(b, messageOffset)
	}
	response := piv.PIVResponseEnd(b)
	piv.PIVStart(b)
	piv.PIVAddResponse(b, response)
	piv.FinishPIVBuffer(b, piv.PIVEnd(b))
	return b.FinishedBytes()
}

// ---- answering: built-in operations natively, capability operations through their handlers -----------------------

func (hb *HostBridge) answerTyped(call *typedCall) (*typedAnswer, error) {
	switch call.op {
	case "host.runtimeTarget":
		return rawText("value", "server"), nil
	case "host.listCapabilities":
		caps := make([]string, 0, len(hb.granted))
		for c := range hb.granted {
			caps = append(caps, c)
		}
		sort.Strings(caps)
		return textItems(caps), nil
	case "host.listSupportedCapabilities":
		return textItems(supportedCapabilities), nil
	case "host.listOperations":
		return textItems(hb.servedOperations()), nil
	case "host.hasCapability":
		return &typedAnswer{result: map[string]any{"VALUE": hb.granted[str(call.params["CAPABILITY"])]}}, nil
	case "clock.now":
		return &typedAnswer{result: map[string]any{"UNIX_MS": uint64(time.Now().UnixMilli())}}, nil
	case "clock.nowIso":
		return rawText("value", time.Now().UTC().Format(time.RFC3339Nano)), nil
	case "clock.monotonicNow":
		return &typedAnswer{result: map[string]any{"VALUE": float64(time.Now().UnixNano()) / 1e6}}, nil
	case "random.bytes":
		n := int(num(call.params["LENGTH"]))
		if n == 0 {
			n = 32
		}
		if n > maxRandomBytes {
			return nil, typedErr("too-large", "random.bytes: %d bytes requested, at most %d", n, maxRandomBytes)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return nil, typedErr("failed", "random.bytes: %v", err)
		}
		return &typedAnswer{raw: map[string][]byte{"data": buf}}, nil
	case "node.publicKey":
		if hb.nodeCtx == nil || hb.nodeCtx.PublicKeyHex == "" {
			return nil, typedErr("failed", "node public key not available")
		}
		return rawText("value", hb.nodeCtx.PublicKeyHex), nil
	case "node.peerId":
		if hb.nodeCtx == nil || hb.nodeCtx.PeerID == "" {
			return nil, typedErr("failed", "node peer ID not available")
		}
		return rawText("value", hb.nodeCtx.PeerID), nil
	case "plugin.getConfig":
		var config map[string]interface{}
		if hb.nodeCtx != nil {
			config = hb.nodeCtx.Config
		}
		entries, err := configEntries(config)
		if err != nil {
			return nil, err
		}
		return &typedAnswer{lists: map[string][]map[string]any{"entries": entries}}, nil
	}

	adapter := typedAdapters[call.op]
	request := map[string]any{}
	var err error
	if adapter.in != nil {
		request, err = adapter.in(call)
	} else {
		request, err = defaultTypedRequest(call)
	}
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, typedErr("invalid-params", "%s: %v", call.op, err)
	}
	result, segments, err := decodeHandlerAnswer(hb.Dispatch(call.op, payload))
	if err != nil {
		return nil, err
	}
	if adapter.out != nil {
		return adapter.out(call, result, segments)
	}
	return defaultTypedAnswer(call, result, segments)
}

func rawText(port, value string) *typedAnswer {
	return &typedAnswer{raw: map[string][]byte{port: []byte(value)}}
}

func textItems(values []string) *typedAnswer {
	items := make([]map[string]any, 0, len(values))
	for _, v := range values {
		items = append(items, map[string]any{"VALUE": v})
	}
	return &typedAnswer{lists: map[string][]map[string]any{"items": items}}
}

// configEntries flattens the module config into dotted-path entries ("targets.0.provider_id"), sorted by key.
func configEntries(config map[string]interface{}) ([]map[string]any, error) {
	var entries []map[string]any
	var walk func(prefix string, value interface{}) error
	walk = func(prefix string, value interface{}) error {
		switch v := value.(type) {
		case map[string]interface{}:
			for _, k := range sortedKeys(v) {
				key := k
				if prefix != "" {
					key = prefix + "." + k
				}
				if err := walk(key, v[k]); err != nil {
					return err
				}
			}
		case []interface{}:
			for i, item := range v {
				if err := walk(prefix+"."+strconv.Itoa(i), item); err != nil {
					return err
				}
			}
		case nil:
		case bool:
			entries = append(entries, map[string]any{"KEY": prefix, "KIND": uint8(2), "BOOL_VALUE": v})
		case string:
			entries = append(entries, map[string]any{"KEY": prefix, "KIND": uint8(0), "STRING_VALUE": v})
		default:
			n, ok := toFloat(v)
			if !ok {
				return typedErr("failed", "plugin.getConfig: %s is a %T", prefix, v)
			}
			entries = append(entries, map[string]any{"KEY": prefix, "KIND": uint8(1), "NUMBER_VALUE": n})
		}
		return nil
	}
	if err := walk("", config); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if len(str(e["KEY"])) > 255 || len(str(e["STRING_VALUE"])) > 255 {
			return nil, typedErr("too-large", "plugin.getConfig: %s does not fit a ConfigEntry (255 bytes)", str(e["KEY"]))
		}
	}
	return entries, nil
}

// decodeHandlerAnswer reads a capability handler's answer: a JSON {"ok","result","error"} or a pre-encoded envelope
// whose binary values are {"$bin": n} references into segments.
func decodeHandlerAnswer(response []byte) (any, [][]byte, error) {
	var meta []byte
	var segments [][]byte
	if isPreEncodedEnvelope(response) {
		envelope := response[len(preEncodedEnvelopeMagic):]
		if len(envelope) < 8 {
			return nil, nil, typedErr("failed", "handler answer envelope is truncated")
		}
		metaLen := int(binary.LittleEndian.Uint32(envelope))
		if 4+metaLen+4 > len(envelope) {
			return nil, nil, typedErr("failed", "handler answer envelope is truncated")
		}
		meta = envelope[4 : 4+metaLen]
		_, segs, err := decodeHostcallEnvelope(envelope)
		if err != nil {
			return nil, nil, typedErr("failed", "%v", err)
		}
		segments = segs
	} else {
		meta = response
	}
	decoder := json.NewDecoder(bytes.NewReader(meta))
	decoder.UseNumber()
	var envelope struct {
		OK     bool            `json:"ok"`
		Result any             `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		return nil, nil, typedErr("failed", "handler answer: %v", err)
	}
	if !envelope.OK {
		var e struct {
			Message string `json:"message"`
			Sandbox string `json:"sandbox"`
			Code    string `json:"code"`
		}
		json.Unmarshal(envelope.Error, &e) //nolint:errcheck
		return nil, nil, &hostcallError{code: classifyHandlerError(e.Message, e.Sandbox), message: e.Message}
	}
	return envelope.Result, segments, nil
}

// classifyHandlerError gives a handler's refusal its HOSTCALL_ERROR_CODES code.
func classifyHandlerError(message, sandbox string) string {
	m := strings.ToLower(message)
	switch {
	case sandbox != "":
		return "sandbox"
	case strings.Contains(m, "capability grant") || strings.Contains(m, "not granted") || strings.Contains(m, "not approved") || strings.Contains(m, "not permitted"):
		return "not-authorized"
	case strings.Contains(m, "not supported") || strings.HasPrefix(m, "unknown ") && strings.Contains(m, "operation"):
		return "unsupported"
	case strings.Contains(m, "exceeds the") && strings.Contains(m, "limit") || strings.Contains(m, "too large") || strings.Contains(m, "must be 1-"):
		return "too-large"
	case strings.Contains(m, "timed out") || strings.Contains(m, "deadline exceeded"):
		return "timeout"
	case strings.HasPrefix(m, "missing ") || strings.HasPrefix(m, "invalid ") || strings.Contains(m, " must be ") || strings.Contains(m, " requires "):
		return "invalid-params"
	case strings.Contains(m, "refus"):
		return "refused"
	}
	return "failed"
}

// ---- value helpers -----------------------------------------------------------------------------------------------

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case []byte:
		return string(t)
	}
	return fmt.Sprint(v)
}

func num(v any) float64 {
	f, _ := toFloat(v)
	return f
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint8:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	}
	return 0, false
}

// integer reads an integer answer exactly (json.Number keeps 64-bit values whole).
func integer(v any) int64 {
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	f, _ := toFloat(v)
	return int64(f)
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// bytesOf reads a handler's binary answer: a {"$bin": n} segment, a {"__type":"bytes","base64"} value or base64 text.
func bytesOf(v any, segments [][]byte) ([]byte, bool) {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$bin"]; ok {
			i := int(integer(ref))
			if i < 0 || i >= len(segments) {
				return nil, false
			}
			return segments[i], true
		}
		if t["__type"] == "bytes" {
			decoded, err := base64.StdEncoding.DecodeString(str(t["base64"]))
			return decoded, err == nil
		}
	case string:
		decoded, err := base64.StdEncoding.DecodeString(t)
		return decoded, err == nil
	}
	return nil, false
}

// textRawPorts are raw ports whose bytes are text the handlers take as strings (UTF-8).
var textRawPorts = map[string]bool{"url": true, "sql": true, "path": true, "to_path": true, "value": true, "secret": true}

// defaultTypedRequest: the record's set fields as lower-case keys (zero = not given), raw byte ports as base64,
// raw text ports as strings.
func defaultTypedRequest(call *typedCall) (map[string]any, error) {
	request := map[string]any{}
	for field, value := range call.params {
		if isZero(value) {
			continue
		}
		request[strings.ToLower(field)] = value
	}
	for port, data := range call.raw {
		if textRawPorts[port] {
			request[port] = string(data)
		} else {
			request[port] = b64(data)
		}
	}
	return request, nil
}

func isZero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	f, ok := toFloat(v)
	return ok && f == 0
}

// defaultTypedAnswer: an object answer becomes the result record (its keys upper-cased), arrays of objects the
// list ports of the same names, binary values the raw ports of the same names.
func defaultTypedAnswer(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
	answer := &typedAnswer{lists: map[string][]map[string]any{}, raw: map[string][]byte{}}
	object, isObject := result.(map[string]any)
	if !isObject {
		if data, ok := bytesOf(result, segments); ok && contains(call.spec.RawOut, "data") {
			answer.raw["data"] = data
			return answer, nil
		}
		if b, ok := result.(bool); ok && call.spec.Result == "BoolValue" {
			answer.result = map[string]any{"VALUE": b}
		}
		return answer, nil
	}
	if call.spec.Result != "" {
		answer.result = recordFromObject(call.spec.Result, object)
	}
	for port, root := range call.spec.ListsOut {
		if items, ok := object[port].([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					answer.lists[port] = append(answer.lists[port], recordFromObject(root, m))
				}
			}
		}
	}
	for _, port := range call.spec.RawOut {
		if data, ok := bytesOf(object[port], segments); ok {
			answer.raw[port] = data
		}
	}
	return answer, nil
}

// recordFromObject keeps the object's scalar values whose upper-cased keys the record declares.
func recordFromObject(root string, object map[string]any) map[string]any {
	declared := map[string]bool{}
	for _, f := range hostops.Fields(root) {
		declared[f] = true
	}
	out := map[string]any{}
	for key, value := range object {
		field := strings.ToUpper(key)
		if !declared[field] {
			continue
		}
		switch v := value.(type) {
		case json.Number:
			if i, err := v.Int64(); err == nil {
				out[field] = i
			} else if f, err := v.Float64(); err == nil {
				out[field] = f
			}
		case string, bool:
			out[field] = v
		}
	}
	return out
}
