package modulert

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/second-state/WasmEdge-go/wasmedge"
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
	Config            map[string]interface{}

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

// InvokeOutputFrame is one independently owned output emitted by a child
// module invocation. Payload is copied out of the child response arena before
// plugin_invoke_stream returns to its caller.
type InvokeOutputFrame struct {
	PortID            string
	Payload           []byte
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

// InvokeFrameSetResult preserves the complete multi-port result of a child
// module invocation. It is the application-blind seam a flow host uses when a
// signed parent runtime dispatches to independently instantiated signed nodes.
type InvokeFrameSetResult struct {
	StatusCode       int32
	Yielded          bool
	BacklogRemaining uint32
	Outputs          []InvokeOutputFrame
}

// BoundIdentity returns immutable identity fields after Module.Load. Unlike
// ID and ContentHash it deliberately takes no Module mutex, so capability
// handlers may call it while plugin_invoke_stream already holds that mutex.
// Callers must only use it for a loaded module passed to its own provisioned
// capability handler.
func (m *Module) BoundIdentity() (contentHash, pluginID string) {
	if m == nil {
		return "", ""
	}
	if m.instanceArtifactHash != "" && m.instanceNodeID != "" {
		return m.instanceArtifactHash, m.instanceNodeID
	}
	contentHash = m.contentHash
	if m.manifest != nil {
		pluginID = m.manifest.PluginID
	}
	return contentHash, pluginID
}

// HostcallImportModule is the SDK-owned sync hostcall import module.
const HostcallImportModule = "space_data_module_host"

// HostBridge is a per-module hostcall dispatcher. It holds the module's
// granted capabilities and routes hostcall operations to the appropriate handler.
type HostBridge struct {
	granted     map[string]bool
	capHandlers map[string]CapHandler // capability prefix → handler
	nodeCtx     *NodeContext

	// invokeCtx is the current scheduled invocation context. Capability
	// handlers derive cancellable work from it; nil means no invocation.
	invokeCtxMu sync.Mutex
	invokeCtx   context.Context

	// Response buffer for the sync hostcall protocol.
	responseGate chan struct{}
	responseMu   sync.RWMutex
	lastStatus   int32
	responseBuf  []byte
	responseLive bool
	responseRead bool

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

// InvokeMethodFrameSet calls plugin_invoke_stream while preserving every
// output port. Module.InvokeMethodFrames intentionally selects one preferred
// output for request/response callers; a flow host must instead forward the
// whole frame set selected by the signed graph.
func (m *Module) InvokeMethodFrameSet(ctx context.Context, methodID string, inputFrames []InvokeInputFrame) (result *InvokeFrameSetResult, err error) {
	started := time.Now()
	defer func() {
		m.recordInvokeResult(started, err)
	}()

	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mod == nil {
		return nil, fmt.Errorf("module not loaded")
	}
	if m.paused {
		return nil, fmt.Errorf("module paused")
	}

	req, err := encodeFrameSetInvokeRequest(methodID, inputFrames)
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
		int32(reqPtr), int32(len(req)), int32(responseLenPtr))
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
		return nil, fmt.Errorf("decode invoke response: %w", err)
	}
	return invokeFrameSetFromResponseReader(response, m.mod.ReadMemory)
}

// InvokeScheduledMethodFrameSet is the multi-port form of InvokeCron: it grants
// the operator-bounded scheduled execution budget while preserving every
// output frame for the parent flow runtime.
func (m *Module) InvokeScheduledMethodFrameSet(ctx context.Context, methodID string, inputFrames []InvokeInputFrame) (*InvokeFrameSetResult, error) {
	if m == nil {
		return nil, errors.New("module is nil")
	}
	ctx = wasmrt.WithExecBudget(ctx, m.scheduledBudget())
	if m.bridge != nil {
		m.bridge.SetFireContext(ctx)
		defer m.bridge.SetFireContext(nil)
	}
	result, err := m.InvokeMethodFrameSet(ctx, methodID, inputFrames)
	m.recordTimerResult(err)
	return result, err
}

func encodeFrameSetInvokeRequest(methodID string, inputFrames []InvokeInputFrame) ([]byte, error) {
	if len(inputFrames) > 0 {
		return encodePluginInvokeRequestFrames(methodID, inputFrames)
	}
	if methodID == "" {
		return nil, errors.New("method id is required")
	}
	// A generic wakeup callback may declare every input optional. Preserve
	// that declaration as a real zero-frame PIV request instead of fabricating
	// a schema-bearing body in the host.
	builder := flatbuffers.NewBuilder(128)
	method := builder.CreateString(methodID)
	piv.PIVRequestStart(builder)
	piv.PIVRequestAddMethodId(builder, method)
	request := piv.PIVRequestEnd(builder)
	piv.PIVStart(builder)
	piv.PIVAddRequest(builder, request)
	root := piv.PIVEnd(builder)
	piv.FinishPIVBuffer(builder, root)
	return builder.FinishedBytes(), nil
}

func invokeFrameSetFromResponse(response *pluginInvokeResponse) (*InvokeFrameSetResult, error) {
	return invokeFrameSetFromResponseReader(response, nil)
}

func invokeFrameSetFromResponseReader(response *pluginInvokeResponse, readGuest func(uint32, uint32) ([]byte, error)) (*InvokeFrameSetResult, error) {
	if response == nil {
		return nil, errors.New("plugin invoke response is required")
	}
	if response.StatusCode != 0 || response.ErrorCode != "" || response.ErrorMessage != "" {
		if response.ErrorMessage != "" {
			return nil, fmt.Errorf("plugin invoke failed (%d): %s", response.StatusCode, response.ErrorMessage)
		}
		if response.ErrorCode != "" {
			return nil, fmt.Errorf("plugin invoke failed (%d): %s", response.StatusCode, response.ErrorCode)
		}
		return nil, fmt.Errorf("plugin invoke failed with status %d", response.StatusCode)
	}
	result := &InvokeFrameSetResult{
		StatusCode:       response.StatusCode,
		Yielded:          response.Yielded,
		BacklogRemaining: response.BacklogRemaining,
	}
	result.Outputs = make([]InvokeOutputFrame, 0, len(response.OutputFrames))
	for _, frame := range response.OutputFrames {
		if err := validateInvokeFrameLayout(frame, len(response.PayloadArena)); err != nil {
			return nil, err
		}
		if frame.Mutability != byte(piv.EnumValuesbufferMutability["IMMUTABLE"]) &&
			frame.Ownership != byte(piv.EnumValuesbufferOwnership["TRANSFERRED"]) {
			return nil, fmt.Errorf("plugin invoke output port %q mutable bytes are not transferred", frame.PortID)
		}
		end := uint64(frame.Offset) + uint64(frame.Size)
		var payload []byte
		if end <= uint64(len(response.PayloadArena)) {
			payload = append([]byte(nil), response.PayloadArena[frame.Offset:uint32(end)]...)
		} else {
			// Reactor modules may return plugin-owned frames addressed directly
			// in their linear memory and omit PAYLOAD_ARENA. Copy them while the
			// child instance is still locked and alive.
			if readGuest == nil {
				return nil, fmt.Errorf(
					"plugin invoke output port %q exceeds payload arena: offset=%d size=%d arena=%d",
					frame.PortID, frame.Offset, frame.Size, len(response.PayloadArena),
				)
			}
			if frame.Offset%frame.Alignment != 0 ||
				(frame.WireFormat == payloadWireFormatAlignedBinary && frame.Offset%uint32(frame.RequiredAlignment) != 0) {
				return nil, fmt.Errorf("plugin-owned output port %q address %d violates alignment %d/%d", frame.PortID, frame.Offset, frame.Alignment, frame.RequiredAlignment)
			}
			guestPayload, err := readGuest(frame.Offset, frame.Size)
			if err != nil {
				return nil, fmt.Errorf("read plugin-owned output port %q: %w", frame.PortID, err)
			}
			payload = append([]byte(nil), guestPayload...)
		}
		result.Outputs = append(result.Outputs, InvokeOutputFrame{
			PortID:            frame.PortID,
			Payload:           payload,
			Alignment:         frame.Alignment,
			WireFormat:        frame.WireFormat,
			SchemaName:        frame.SchemaName,
			FileIdentifier:    frame.FileIdentifier,
			SchemaVersion:     frame.SchemaVersion,
			SchemaHash:        append([]byte(nil), frame.SchemaHash...),
			RootTypeName:      frame.RootTypeName,
			FixedStringLength: frame.FixedStringLength,
			ByteLength:        frame.ByteLength,
			RequiredAlignment: frame.RequiredAlignment,
			Ownership:         byte(piv.EnumValuesbufferOwnership["HOST_OWNED"]),
			Mutability:        byte(piv.EnumValuesbufferMutability["IMMUTABLE"]),
			FrameID:           frame.FrameID,
		})
	}
	return result, nil
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

// SetFireContext installs the current scheduled invocation context.
func (hb *HostBridge) SetFireContext(ctx context.Context) {
	if hb == nil {
		return
	}
	hb.invokeCtxMu.Lock()
	hb.invokeCtx = ctx
	hb.invokeCtxMu.Unlock()
}

// FireContext returns the current scheduled invocation context, or a
// background context when no invocation is active.
func (hb *HostBridge) FireContext() context.Context {
	if hb == nil {
		return context.Background()
	}
	hb.invokeCtxMu.Lock()
	ctx := hb.invokeCtx
	hb.invokeCtxMu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// NewHostBridge creates a per-module host bridge.
func NewHostBridge(nodeCtx *NodeContext, grantedCaps []string) *HostBridge {
	granted := make(map[string]bool, len(grantedCaps))
	for _, c := range grantedCaps {
		granted[c] = true
	}
	hb := &HostBridge{
		granted:      granted,
		capHandlers:  make(map[string]CapHandler),
		nodeCtx:      nodeCtx,
		responseGate: make(chan struct{}, 1),
		lastStatus:   0,
		responseBuf:  encodeHostcallEnvelope(map[string]interface{}{"ok": true, "result": nil}, nil),
	}
	return hb
}

// beginResponse publishes one completed sync-hostcall exchange. Dispatch work
// happens before this method, so concurrent requests can perform network I/O in
// parallel; only the short response_len/read/status/clear sequence is
// serialized. This is required by the SDK ABI, which has no request identifier
// on its response accessors.
func (hb *HostBridge) beginResponse(status int32, response []byte) {
	hb.responseGate <- struct{}{}
	hb.responseMu.Lock()
	hb.lastStatus = status
	hb.responseBuf = append(hb.responseBuf[:0], response...)
	hb.responseLive = true
	hb.responseRead = false
	hb.responseMu.Unlock()
}

func (hb *HostBridge) responseLength() int32 {
	hb.responseMu.RLock()
	defer hb.responseMu.RUnlock()
	return int32(len(hb.responseBuf))
}

func (hb *HostBridge) responseBytes(limit uint32) []byte {
	hb.responseMu.Lock()
	defer hb.responseMu.Unlock()
	n := len(hb.responseBuf)
	if uint64(n) > uint64(limit) {
		n = int(limit)
	}
	if hb.responseLive {
		hb.responseRead = true
	}
	return append([]byte(nil), hb.responseBuf[:n]...)
}

func (hb *HostBridge) responseStatus() int32 {
	hb.responseMu.RLock()
	defer hb.responseMu.RUnlock()
	return hb.lastStatus
}

func (hb *HostBridge) clearResponse() {
	hb.responseMu.Lock()
	// SDK guests clear once before beginning a call and once after copying the
	// response. Concurrent workers may issue the first clear while another
	// completed call owns the single response slot. Only a response that has
	// actually been copied may be released; a pre-call clear cannot steal it.
	if hb.responseLive && !hb.responseRead {
		hb.responseMu.Unlock()
		return
	}
	release := hb.responseLive
	hb.lastStatus = 0
	hb.responseBuf = encodeHostcallEnvelope(map[string]interface{}{"ok": true, "result": nil}, nil)
	hb.responseLive = false
	hb.responseRead = false
	hb.responseMu.Unlock()
	if !release {
		return
	}
	select {
	case <-hb.responseGate:
	default:
	}
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
			hb.beginResponse(1, encodeHostcallEnvelope(map[string]interface{}{
				"ok":    false,
				"error": map[string]string{"message": "no memory"},
			}, nil))
			return []interface{}{int32(1)}, wasmedge.Result_Success
		}

		opBytes, err := mem.GetData(uint(opPtr), uint(opLen))
		if err != nil {
			hb.beginResponse(1, encodeHostcallEnvelope(map[string]interface{}{
				"ok":    false,
				"error": map[string]string{"message": "failed to read operation"},
			}, nil))
			return []interface{}{int32(1)}, wasmedge.Result_Success
		}

		var payloadBytes []byte
		if payloadLen > 0 {
			payloadBytes, err = mem.GetData(uint(payloadPtr), uint(payloadLen))
			if err != nil {
				hb.beginResponse(1, encodeHostcallEnvelope(map[string]interface{}{
					"ok":    false,
					"error": map[string]string{"message": "failed to read payload"},
				}, nil))
				return []interface{}{int32(1)}, wasmedge.Result_Success
			}
		}

		jsonPayload, err := decodeHostcallEnvelopePayload(payloadBytes)
		if err != nil {
			hb.beginResponse(1, encodeHostcallEnvelope(map[string]interface{}{
				"ok":    false,
				"error": map[string]string{"message": err.Error()},
			}, nil))
			return []interface{}{int32(1)}, wasmedge.Result_Success
		}

		hb.beginResponse(0, encodeHostcallJSONResponse(hb.Dispatch(string(opBytes), jsonPayload)))
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
				return []interface{}{hb.responseLength()}, wasmedge.Result_Success
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
				response := hb.responseBytes(dstLen)
				if len(response) > 0 {
					mem.SetData(response, uint(dstPtr), uint(len(response)))
				}
				return []interface{}{int32(len(response))}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "clear_response",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				hb.clearResponse()
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "last_status_code",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{hb.responseStatus()}, wasmedge.Result_Success
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

func encodeHostcallEnvelope(meta interface{}, segments [][]byte) []byte {
	metaBytes, _ := json.Marshal(meta)
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
