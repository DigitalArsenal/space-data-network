package modulert

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// NodeContext holds node-level info that any module can access via hostcalls.
type NodeContext struct {
	PeerID        string
	PublicKeyHex  string
	EncryptionKey []byte
	Config        map[string]interface{}
}

// CapHandler is a function that handles a capability-gated hostcall operation.
type CapHandler func(operation string, payload []byte) ([]byte, error)

// HostBridge is a per-module sdn_host dispatcher. It holds the module's
// granted capabilities and routes hostcall operations to the appropriate handler.
type HostBridge struct {
	granted     map[string]bool
	capHandlers map[string]CapHandler // capability prefix → handler
	nodeCtx     *NodeContext

	// Response buffer for the sdn_host protocol
	lastStatus  int32
	responseBuf []byte
}

// NewHostBridge creates a per-module host bridge.
func NewHostBridge(nodeCtx *NodeContext, grantedCaps []string) *HostBridge {
	granted := make(map[string]bool, len(grantedCaps))
	for _, c := range grantedCaps {
		granted[c] = true
	}
	hb := &HostBridge{
		granted:     granted,
		capHandlers: make(map[string]CapHandler),
		nodeCtx:     nodeCtx,
		lastStatus:  0,
		responseBuf: []byte(`{"ok":true,"result":null}`),
	}
	return hb
}

// RegisterCapHandler registers a handler for operations with the given prefix.
func (hb *HostBridge) RegisterCapHandler(prefix string, handler CapHandler) {
	hb.capHandlers[prefix] = handler
}

// HasCapability returns true if the module was granted the given capability.
func (hb *HostBridge) HasCapability(cap string) bool {
	return hb.granted[cap]
}

// Dispatch handles a hostcall operation and returns a JSON response.
func (hb *HostBridge) Dispatch(operation string, payload []byte) []byte {
	// Built-in operations (always available)
	switch operation {
	case "clock.now":
		return okJSON(time.Now().UnixMilli())
	case "clock.nowIso":
		return okJSON(time.Now().UTC().Format(time.RFC3339Nano))
	case "clock.monotonicNow":
		return okJSON(time.Now().UnixNano() / 1e6)
	case "random.bytes":
		return hb.handleRandomBytes(payload)
	case "host.runtimeTarget":
		return okJSON("server")
	case "host.listCapabilities":
		caps := make([]string, 0, len(hb.granted))
		for c := range hb.granted {
			caps = append(caps, c)
		}
		return okJSON(caps)
	case "host.listSupportedCapabilities":
		return okJSON([]string{
			"clock", "random",
			"protocol_handle", "protocol_dial",
			"pubsub",
			"crypto_hash", "crypto_sign", "crypto_verify",
			"crypto_encrypt", "crypto_decrypt", "crypto_key_agreement", "crypto_kdf",
			"ipfs",
			"storage_query", "storage_write", "storage_adapter",
			"http",
			"schedule_cron",
		})
	case "host.hasCapability":
		var p struct {
			Capability string `json:"capability"`
		}
		json.Unmarshal(payload, &p)
		return okJSON(hb.granted[p.Capability])
	case "host.listOperations":
		operations := []string{
			"clock.now",
			"clock.nowIso",
			"clock.monotonicNow",
			"random.bytes",
			"host.runtimeTarget",
			"host.listCapabilities",
			"host.hasCapability",
			"host.listOperations",
			"node.publicKey",
			"node.peerId",
			"plugin.getConfig",
		}
		if _, ok := hb.capHandlers["protocol"]; ok {
			operations = append(operations, "protocol.request")
		}
		return okJSON(operations)
	case "node.publicKey":
		if hb.nodeCtx != nil && hb.nodeCtx.PublicKeyHex != "" {
			return okJSON(hb.nodeCtx.PublicKeyHex)
		}
		return errJSON("node public key not available")
	case "node.peerId":
		if hb.nodeCtx != nil && hb.nodeCtx.PeerID != "" {
			return okJSON(hb.nodeCtx.PeerID)
		}
		return errJSON("node peer ID not available")
	case "plugin.getConfig":
		if hb.nodeCtx != nil && hb.nodeCtx.Config != nil {
			return okJSON(hb.nodeCtx.Config)
		}
		return okJSON(map[string]interface{}{})
	}

	// Capability-gated operations — route by prefix
	for prefix, handler := range hb.capHandlers {
		if len(operation) > len(prefix) && operation[:len(prefix)+1] == prefix+"." {
			resp, err := handler(operation, payload)
			if err != nil {
				return errJSON(err.Error())
			}
			return resp
		}
	}

	return errJSON(fmt.Sprintf("operation %q not supported", operation))
}

func (hb *HostBridge) handleRandomBytes(payload []byte) []byte {
	n := 32
	if len(payload) > 0 {
		var p struct {
			Length int `json:"length"`
		}
		if json.Unmarshal(payload, &p) == nil && p.Length > 0 {
			n = p.Length
		}
	}
	if n > 8192 {
		n = 8192
	}
	buf := make([]byte, n)
	rand.Read(buf)
	// Return as JSON with base64 — use standard encoding
	return okJSON(map[string]interface{}{
		"__type": "bytes",
		"base64": encodeBase64(buf),
	})
}

// BuildWasmEdgeHostFuncs returns the sdn_host WasmEdge host functions bound to this bridge.
func (hb *HostBridge) BuildWasmEdgeHostFuncs() []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }

	return []wasmrt.HostFunc{
		{
			Name: "call_json",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				opPtr := uint32(params[0].(int32))
				opLen := uint32(params[1].(int32))
				payloadPtr := uint32(params[2].(int32))
				payloadLen := uint32(params[3].(int32))

				mem := cf.GetMemoryByIndex(0)
				if mem == nil {
					hb.lastStatus = 1
					hb.responseBuf = errJSON("no memory")
					return []interface{}{int32(1)}, wasmedge.Result_Success
				}

				opBytes, err := mem.GetData(uint(opPtr), uint(opLen))
				if err != nil {
					hb.lastStatus = 1
					hb.responseBuf = errJSON("failed to read operation")
					return []interface{}{int32(1)}, wasmedge.Result_Success
				}

				var payloadBytes []byte
				if payloadLen > 0 {
					payloadBytes, err = mem.GetData(uint(payloadPtr), uint(payloadLen))
					if err != nil {
						hb.lastStatus = 1
						hb.responseBuf = errJSON("failed to read payload")
						return []interface{}{int32(1)}, wasmedge.Result_Success
					}
				}

				hb.responseBuf = hb.Dispatch(string(opBytes), payloadBytes)
				hb.lastStatus = 0
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32(), i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "response_len",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{int32(len(hb.responseBuf))}, wasmedge.Result_Success
			},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "read_response",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				dstPtr := uint32(params[0].(int32))
				dstLen := uint32(params[1].(int32))
				mem := cf.GetMemoryByIndex(0)
				if mem == nil {
					return []interface{}{int32(0)}, wasmedge.Result_Success
				}
				toCopy := uint32(len(hb.responseBuf))
				if toCopy > dstLen {
					toCopy = dstLen
				}
				if toCopy > 0 {
					mem.SetData(hb.responseBuf[:toCopy], uint(dstPtr), uint(toCopy))
				}
				return []interface{}{int32(toCopy)}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "clear_response",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				hb.lastStatus = 0
				hb.responseBuf = []byte(`{"ok":true,"result":null}`)
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "last_status_code",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{hb.lastStatus}, wasmedge.Result_Success
			},
			Returns: []*wasmedge.ValType{i32()},
		},
	}
}

// JSON helpers

func okJSON(result interface{}) []byte {
	r, _ := json.Marshal(map[string]interface{}{"ok": true, "result": result})
	return r
}

func errJSON(msg string) []byte {
	r, _ := json.Marshal(map[string]interface{}{
		"ok":    false,
		"error": map[string]string{"message": msg},
	})
	return r
}

func encodeBase64(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	result := make([]byte, ((len(data)+2)/3)*4)
	di, si := 0, 0
	n := (len(data) / 3) * 3
	for si < n {
		val := uint(data[si])<<16 | uint(data[si+1])<<8 | uint(data[si+2])
		result[di] = alphabet[val>>18&0x3F]
		result[di+1] = alphabet[val>>12&0x3F]
		result[di+2] = alphabet[val>>6&0x3F]
		result[di+3] = alphabet[val&0x3F]
		si += 3
		di += 4
	}
	remain := len(data) - si
	if remain == 2 {
		val := uint(data[si])<<16 | uint(data[si+1])<<8
		result[di] = alphabet[val>>18&0x3F]
		result[di+1] = alphabet[val>>12&0x3F]
		result[di+2] = alphabet[val>>6&0x3F]
		result[di+3] = '='
	} else if remain == 1 {
		val := uint(data[si]) << 16
		result[di] = alphabet[val>>18&0x3F]
		result[di+1] = alphabet[val>>12&0x3F]
		result[di+2] = '='
		result[di+3] = '='
	}
	return string(result)
}
