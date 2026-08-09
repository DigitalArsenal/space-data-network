package modulert

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/second-state/WasmEdge-go/wasmedge"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Key-slot algorithm declarations (loop B9.5 — defensive hardening).
// Every entry in NodeContext.KeySlots MUST have a matching entry in
// NodeContext.KeySlotAlgorithms declaring the single algorithm the slot's
// key material may be used with. The keyslot hostcall family fails closed
// on an undeclared slot or an algorithm mismatch, so one 32-byte slot can
// never be used as both an Ed25519 seed (keyslot.sign) and an X25519
// scalar (keyslot.unwrap) — cross-protocol key reuse.
const (
	KeySlotAlgorithmEd25519   = "ed25519"
	KeySlotAlgorithmSecp256k1 = "secp256k1"
	KeySlotAlgorithmX25519    = "x25519"
)

// NodeContext holds node-level info that any module can access via hostcalls.
type NodeContext struct {
	PeerID        string
	PublicKeyHex  string
	EncryptionKey []byte
	KeySlots      map[string][]byte
	// KeySlotAlgorithms domain-separates KeySlots by algorithm: slot ID →
	// one of the KeySlotAlgorithm* constants. A slot absent from this map
	// is unusable by any keyslot operation (fail closed). See the constant
	// block above.
	KeySlotAlgorithms map[string]string

	// KeySlotRefusals records slots the HOST DELIBERATELY DID NOT PROVISION,
	// slot ID → operator-readable reason. It is the difference between "this
	// node has no such key" and "this node has the key and refuses to hand it
	// to this slot", which are the same absence to a consumer and very
	// different facts to an operator.
	//
	// A refused slot is simply absent from KeySlots, so nothing can sign with
	// it — the refusal is already enforced by absence. This map exists so the
	// lane that needed the slot can fail closed with the REAL reason instead of
	// a generic "slot missing", and so the reason survives into a log line an
	// operator can act on. First user: the grant-signing / update-root key
	// domain separation guard (owner ruling 2026-08-07,
	// graph/tasks/sdn-grant-verifier-key-domain-separation.md).
	KeySlotRefusals map[string]string

	Config map[string]interface{}

	// CapabilityPolicy is the operator-controlled capability allowlist
	// consulted at module load/provision time (loop B1 — defensive
	// hardening, FAIL CLOSED). May be nil, which is equivalent to an empty
	// policy store: every sensitive capability is denied. See
	// capability_policy.go.
	CapabilityPolicy *CapabilityPolicyStore

	// ScheduledInvokeTimeout overrides the per-call wall-clock budget for
	// SCHEDULED module invocations (cron ticker + run-now admin, both routed
	// through Module.InvokeCron). Zero selects the modulert built-in default
	// (defaultScheduledInvokeTimeout, 10m). INTERACTIVE invocations
	// (protocol/HTTP handlers) are never affected — they keep the tight 30s
	// defaultInvokeTimeout. Operator-tunable via the node config field
	// modules.scheduled_invoke_timeout, threaded in by node.go's
	// buildModuleNodeContext for slow (e.g. single-vCPU) hosts. The fuel
	// budget scales proportionally with this value (see Module.scheduledBudget).
	ScheduledInvokeTimeout time.Duration

	// ModuleSignaturePolicy is the operator-controlled publication-trailer
	// signature trust policy consulted at module load time, before the
	// module is admitted (loop I1 — defensive hardening, FAIL CLOSED once
	// configured). A nil value means signature enforcement is not
	// configured for this node — the trailer is still stripped, but no
	// signature is required (see ModuleSignaturePolicy's doc in
	// publication_signature.go for why, and what a production deployment
	// must set here). Mirrors CapabilityPolicy's nil-is-a-real-choice
	// shape.
	ModuleSignaturePolicy *ModuleSignaturePolicy
}

// CapHandler is a function that handles a capability-gated hostcall operation.
type CapHandler func(operation string, payload []byte) ([]byte, error)

// HostcallImportModule is the SDK-owned sync hostcall import module.
const HostcallImportModule = "space_data_module_host"

// HostBridge is a per-module hostcall dispatcher. It holds the module's
// granted capabilities and routes hostcall operations to the appropriate handler.
type HostBridge struct {
	granted     map[string]bool
	capHandlers map[string]CapHandler // capability prefix → handler
	nodeCtx     *NodeContext

	// producerID identifies the artifact this bridge serves — a module's
	// manifest plugin id, or a flow bundle's program id. Set by
	// ProvisionBridge. Capability handlers use it for record provenance, never
	// for an access decision (the grant set above is the only authority).
	producerID string

	// Response buffer for the sync hostcall protocol.
	lastStatus  int32
	responseBuf []byte

	// Body-reference registry (loop C.5c near-zero-copy egress): capability
	// handlers answering in "deliver":"ref" mode register the result buffer
	// here and return only a token; the host's egress sink resolves $HTR
	// BODY_REF_TOKEN against the SAME bridge — references never cross module
	// instances. Guarded for host-side use (the egress may run on a request
	// goroutine while guest calls are serialized elsewhere).
	bodyRefMu   sync.Mutex
	bodyRefs    map[uint64][]byte
	bodyRefNext uint64
}

// ProducerID returns the artifact identity this bridge serves (module plugin
// id or flow program id), or "" when the bridge was provisioned without one.
func (hb *HostBridge) ProducerID() string {
	if hb == nil {
		return ""
	}
	return hb.producerID
}

// PutBodyRef registers a byte buffer for out-of-band body delivery and
// returns its token. Buffers may be shared (e.g. mirror-cached streams) —
// nothing here mutates or frees them.
func (hb *HostBridge) PutBodyRef(b []byte) uint64 {
	hb.bodyRefMu.Lock()
	defer hb.bodyRefMu.Unlock()
	if hb.bodyRefs == nil {
		hb.bodyRefs = make(map[uint64][]byte)
	}
	hb.bodyRefNext++
	token := hb.bodyRefNext
	hb.bodyRefs[token] = b
	return token
}

// TakeBodyRef resolves and removes a registered body reference. Tokens are
// single-use.
func (hb *HostBridge) TakeBodyRef(token uint64) ([]byte, bool) {
	hb.bodyRefMu.Lock()
	defer hb.bodyRefMu.Unlock()
	b, ok := hb.bodyRefs[token]
	if ok {
		delete(hb.bodyRefs, token)
	}
	return b, ok
}

// ResetBodyRefs drops every outstanding reference (end-of-exchange cleanup —
// e.g. a 304 or an error path never consumes the reference it caused).
func (hb *HostBridge) ResetBodyRefs() {
	hb.bodyRefMu.Lock()
	defer hb.bodyRefMu.Unlock()
	hb.bodyRefs = nil
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
		responseBuf: encodeHostcallEnvelope(map[string]interface{}{"ok": true, "result": nil}, nil),
	}
	return hb
}

// NodeContext returns the node identity/config context this bridge was built
// with, or nil when there is none.
//
// It exists for BridgeCapFactory implementations that need the node context on
// the FLOW path, where there is no *Module to read it from (ProvisionBridge
// passes mod == nil for flow bundles). The bridge is the only place that
// context exists for such a caller — it is what node.publicKey and secrets.*
// already read — so a capability that resolves it from mod alone is dead in
// every flow. Nil-safe: a nil bridge reports no context rather than panicking,
// because a cap handler must degrade to a refusal, never take the daemon down.
func (hb *HostBridge) NodeContext() *NodeContext {
	if hb == nil {
		return nil
	}
	return hb.nodeCtx
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
			"wallet_sign",
			"ipfs",
			"storage_query", "storage_write", "storage_adapter", "storage_ingest",
			"http",
			"schedule_cron",
			"p2p_read",
			"node_status_read",
			"node_activity_read",
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
		if _, ok := hb.capHandlers["ipfs"]; ok {
			operations = append(operations, "ipfs.add", "ipfs.cat")
		}
		if _, ok := hb.capHandlers["keyslot"]; ok {
			// keyslot is a host-side crypto oracle: sign/unwrap return only
			// derived outputs (a signature, or the plaintext of a wrapped
			// payload) — never the slot's private key. There is no raw-get
			// operation; do not add one back.
			operations = append(operations, "keyslot.sign", "keyslot.unwrap")
		}
		if _, ok := hb.capHandlers["p2p"]; ok {
			operations = append(operations, "p2p.peers_snapshot", "p2p.standards_snapshot")
		}
		if _, ok := hb.capHandlers["node_status_read"]; ok {
			// node_status_read has no capPrefixFromName override (module.go
			// is out of scope for this change), so — mirroring the OTHER
			// unmapped capabilities above (ipfs, keyslot's wallet_sign
			// aside) — its hostcall prefix is the capability name itself.
			// See caps/nodestatus.go's package doc for the full rationale.
			operations = append(operations, "node_status_read.status")
		}
		if _, ok := hb.capHandlers["node_activity_read"]; ok {
			// node_activity_read has no capPrefixFromName override either
			// (same unmapped-prefix convention as node_status_read just
			// above) — see caps/nodeactivity.go's package doc for the full
			// rationale.
			operations = append(operations, "node_activity_read.activity")
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

// BuildWasmEdgeHostFuncs returns the WasmEdge host functions bound to this bridge.
func (hb *HostBridge) BuildWasmEdgeHostFuncs() []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	call := func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
		opPtr := uint32(params[0].(int32))
		opLen := uint32(params[1].(int32))
		payloadPtr := uint32(params[2].(int32))
		payloadLen := uint32(params[3].(int32))

		mem := cf.GetMemoryByIndex(0)
		if mem == nil {
			hb.lastStatus = 1
			hb.responseBuf = encodeHostcallEnvelope(map[string]interface{}{
				"ok":    false,
				"error": map[string]string{"message": "no memory"},
			}, nil)
			return []interface{}{int32(1)}, wasmedge.Result_Success
		}

		opBytes, err := mem.GetData(uint(opPtr), uint(opLen))
		if err != nil {
			hb.lastStatus = 1
			hb.responseBuf = encodeHostcallEnvelope(map[string]interface{}{
				"ok":    false,
				"error": map[string]string{"message": "failed to read operation"},
			}, nil)
			return []interface{}{int32(1)}, wasmedge.Result_Success
		}

		var payloadBytes []byte
		if payloadLen > 0 {
			payloadBytes, err = mem.GetData(uint(payloadPtr), uint(payloadLen))
			if err != nil {
				hb.lastStatus = 1
				hb.responseBuf = encodeHostcallEnvelope(map[string]interface{}{
					"ok":    false,
					"error": map[string]string{"message": "failed to read payload"},
				}, nil)
				return []interface{}{int32(1)}, wasmedge.Result_Success
			}
		}

		jsonPayload, err := decodeHostcallEnvelopePayload(payloadBytes)
		if err != nil {
			hb.lastStatus = 1
			hb.responseBuf = encodeHostcallEnvelope(map[string]interface{}{
				"ok":    false,
				"error": map[string]string{"message": err.Error()},
			}, nil)
			return []interface{}{int32(1)}, wasmedge.Result_Success
		}

		hb.responseBuf = encodeHostcallJSONResponse(hb.Dispatch(string(opBytes), jsonPayload))
		hb.lastStatus = 0
		return []interface{}{int32(0)}, wasmedge.Result_Success
	}

	return []wasmrt.HostFunc{
		{
			Name:    "call",
			Func:    call,
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
				hb.responseBuf = encodeHostcallEnvelope(map[string]interface{}{"ok": true, "result": nil}, nil)
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

func decodeHostcallEnvelopePayload(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	meta, segments, err := decodeHostcallEnvelope(payload)
	if err != nil {
		return nil, err
	}
	attached, err := attachHostcallBinaryRefs(meta, segments)
	if err != nil {
		return nil, err
	}
	return json.Marshal(attached)
}

func decodeHostcallEnvelope(payload []byte) (interface{}, [][]byte, error) {
	if len(payload) < 8 {
		return nil, nil, fmt.Errorf("hostcall envelope is truncated")
	}
	offset := 0
	metaLen := int(binary.LittleEndian.Uint32(payload[offset:]))
	offset += 4
	if offset+metaLen+4 > len(payload) {
		return nil, nil, fmt.Errorf("hostcall envelope meta exceeds envelope bounds")
	}
	var meta interface{}
	if err := json.Unmarshal(payload[offset:offset+metaLen], &meta); err != nil {
		return nil, nil, fmt.Errorf("decode hostcall envelope meta: %w", err)
	}
	offset += metaLen
	segmentCount := int(binary.LittleEndian.Uint32(payload[offset:]))
	offset += 4
	segments := make([][]byte, 0, segmentCount)
	for i := 0; i < segmentCount; i++ {
		if offset+4 > len(payload) {
			return nil, nil, fmt.Errorf("hostcall envelope segment table is truncated")
		}
		segmentLen := int(binary.LittleEndian.Uint32(payload[offset:]))
		offset += 4
		if offset+segmentLen > len(payload) {
			return nil, nil, fmt.Errorf("hostcall envelope segment exceeds envelope bounds")
		}
		segments = append(segments, payload[offset:offset+segmentLen])
		offset += segmentLen
	}
	return meta, segments, nil
}

func attachHostcallBinaryRefs(value interface{}, segments [][]byte) (interface{}, error) {
	switch v := value.(type) {
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, entry := range v {
			attached, err := attachHostcallBinaryRefs(entry, segments)
			if err != nil {
				return nil, err
			}
			out[i] = attached
		}
		return out, nil
	case map[string]interface{}:
		if len(v) == 1 {
			if ref, ok := v["$bin"]; ok {
				index, ok := ref.(float64)
				if !ok || index < 0 || index != float64(int(index)) || int(index) >= len(segments) {
					return nil, fmt.Errorf("hostcall envelope references missing binary segment %v", ref)
				}
				return base64.StdEncoding.EncodeToString(segments[int(index)]), nil
			}
		}
		out := make(map[string]interface{}, len(v))
		for key, entry := range v {
			attached, err := attachHostcallBinaryRefs(entry, segments)
			if err != nil {
				return nil, err
			}
			out[key] = attached
		}
		return out, nil
	default:
		return value, nil
	}
}

// preEncodedEnvelopeMagic prefixes CapHandler responses that are ALREADY
// encoded as the binary hostcall envelope ([u32 metaLen][meta JSON]
// [u32 segCount]([u32 segLen][segment])...). encodeHostcallJSONResponse
// strips the marker and forwards the envelope untouched — large binary
// results (aligned FlatBuffer streams) skip the base64→JSON→parse→base64
// round-trip entirely. The marker's leading NUL cannot collide with a JSON
// response. Pure copy elimination: the envelope bytes the guest reads are
// identical either way.
var preEncodedEnvelopeMagic = []byte{0x00, 'S', 'D', 'N', 'E', 'N', 'V', '1'}

// PreEncodedEnvelope builds a marked, fully-encoded hostcall envelope for a
// CapHandler to return. meta is the JSON metadata (binary values referenced
// as {"$bin": segmentIndex}); segments carry the raw bytes verbatim.
func PreEncodedEnvelope(meta interface{}, segments [][]byte) []byte {
	envelope := encodeHostcallEnvelope(meta, segments)
	out := make([]byte, 0, len(preEncodedEnvelopeMagic)+len(envelope))
	out = append(out, preEncodedEnvelopeMagic...)
	return append(out, envelope...)
}

func isPreEncodedEnvelope(response []byte) bool {
	return len(response) >= len(preEncodedEnvelopeMagic) &&
		string(response[:len(preEncodedEnvelopeMagic)]) == string(preEncodedEnvelopeMagic)
}

func encodeHostcallJSONResponse(response []byte) []byte {
	if isPreEncodedEnvelope(response) {
		return response[len(preEncodedEnvelopeMagic):]
	}
	var envelope interface{}
	if err := json.Unmarshal(response, &envelope); err != nil {
		envelope = map[string]interface{}{
			"ok":    false,
			"error": map[string]string{"message": err.Error()},
		}
	}
	segments := make([][]byte, 0)
	meta := detachHostcallBinaryValues(envelope, &segments)
	return encodeHostcallEnvelope(meta, segments)
}

func detachHostcallBinaryValues(value interface{}, segments *[][]byte) interface{} {
	switch v := value.(type) {
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, entry := range v {
			out[i] = detachHostcallBinaryValues(entry, segments)
		}
		return out
	case map[string]interface{}:
		if typ, ok := v["__type"].(string); ok && typ == "bytes" {
			if encoded, ok := v["base64"].(string); ok {
				decoded, err := base64.StdEncoding.DecodeString(encoded)
				if err == nil {
					index := len(*segments)
					*segments = append(*segments, decoded)
					return map[string]interface{}{"$bin": index}
				}
			}
		}
		out := make(map[string]interface{}, len(v))
		for key, entry := range v {
			out[key] = detachHostcallBinaryValues(entry, segments)
		}
		return out
	default:
		return value
	}
}

// encodeHostcallEnvelope frames the hostcall response the guest reads:
// [u32 metaLen][meta JSON][u32 segCount]([u32 segLen][segment])*
//
// The meta JSON is encoded WITHOUT HTML escaping. This is the final encoder on
// every hostcall response — encodeHostcallJSONResponse re-marshals through it
// after detaching binary segments — so escaping here would re-corrupt values
// that Dispatch already encoded correctly. A guest reading a config URL must
// receive the operator's bytes, not a browser-safe transcription of them; see
// hostbridge_config_escaping_test.go.
func encodeHostcallEnvelope(meta interface{}, segments [][]byte) []byte {
	metaBytes := marshalJSONNoHTMLEscape(meta)
	total := 4 + len(metaBytes) + 4
	for _, segment := range segments {
		total += 4 + len(segment)
	}
	out := make([]byte, total)
	offset := 0
	binary.LittleEndian.PutUint32(out[offset:], uint32(len(metaBytes)))
	offset += 4
	copy(out[offset:], metaBytes)
	offset += len(metaBytes)
	binary.LittleEndian.PutUint32(out[offset:], uint32(len(segments)))
	offset += 4
	for _, segment := range segments {
		binary.LittleEndian.PutUint32(out[offset:], uint32(len(segment)))
		offset += 4
		copy(out[offset:], segment)
		offset += len(segment)
	}
	return out
}

// JSON helpers

// okJSON encodes a successful hostcall response.
//
// HTML escaping is DISABLED. encoding/json escapes &, < and > as & etc.
// by default — a browser-safety measure that is meaningless on a wasm hostcall
// and actively harmful on it: a guest reading a config value would receive
// "GROUP=stations&FORMAT=csv" where the host holds
// "GROUP=stations&FORMAT=csv". That silently corrupted every configured source
// URL carrying a query string with more than one parameter — the exact shape
// of a CelesTrak GP request — turning an operator's URL override into a
// request for a host that does not exist.
func okJSON(result interface{}) []byte {
	return marshalJSONNoHTMLEscape(map[string]interface{}{"ok": true, "result": result})
}

// marshalJSONNoHTMLEscape encodes v as JSON with HTML escaping DISABLED.
//
// encoding/json escapes &, < and > by default — a browser-safety measure that
// is meaningless on a wasm hostcall and actively harmful on it. A guest reading
// a configured value would receive the HTML entity form where the host holds
// the raw character, which silently corrupted every source URL carrying a
// multi-parameter query string (the exact shape of a CelesTrak GP request):
// "GROUP=stations&FORMAT=csv" reached the guest as
// "GROUP=stationsu0026FORMAT=csv" — the guest's minimal JSON reader does not
// decode \u escapes — and the node then spent its whole timeout dialling a
// host that does not exist.
func marshalJSONNoHTMLEscape(v interface{}) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		fallback, _ := json.Marshal(map[string]interface{}{
			"ok":    false,
			"error": map[string]string{"message": "failed to encode response: " + err.Error()},
		})
		return fallback
	}
	// Encode appends a newline; hostcall payloads are length-prefixed, so trim
	// it rather than shipping a stray byte to the guest.
	return bytes.TrimRight(buf.Bytes(), "\n")
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
