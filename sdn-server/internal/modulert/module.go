package modulert

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

var log = logging.Logger("modulert")

// Module is the generic module-sdk runtime. It loads any space-data-module-sdk
// WASM binary, reads its manifest, provisions declared capabilities, and
// implements the SDN plugin interfaces (Plugin, CronProvider, UIProvider).
type Module struct {
	mod      *wasmrt.Module
	manifest *Manifest
	bridge   *HostBridge
	nodeCtx  *NodeContext
	capReg   *CapabilityRegistry
	mu       sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Context returns the module's lifecycle context. It is set when Start() is called
// and cancelled when Close() is called. Returns context.Background() before Start().
func (m *Module) Context() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// CapabilityRegistry maps capability strings to provisioner functions.
type CapabilityRegistry struct {
	mu        sync.RWMutex
	factories map[string]CapFactory
}

// CapFactory creates a CapHandler for a module that declared a given capability.
type CapFactory func(mod *Module) CapHandler

// NewCapabilityRegistry creates an empty registry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{factories: make(map[string]CapFactory)}
}

// Register adds a capability factory.
func (r *CapabilityRegistry) Register(capability string, factory CapFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[capability] = factory
}

// NewModule loads a module-sdk WASM binary, reads its manifest, creates a
// per-module host bridge, and provisions declared capabilities.
func NewModule(wasmBytes []byte, capReg *CapabilityRegistry, nodeCtx *NodeContext) (*Module, error) {
	m := &Module{
		nodeCtx: nodeCtx,
		capReg:  capReg,
	}

	// Create the per-module host bridge (needs manifest first for capabilities,
	// but we need the WASM loaded to read the manifest — chicken-and-egg.
	// Solution: create bridge with all capabilities initially, then restrict after manifest parse.)
	bridge := NewHostBridge(nodeCtx, nil)
	m.bridge = bridge

	// Build WasmEdge module with shared host functions + per-module sdn_host bridge
	logFunc := func(level int32, msg string) {
		switch {
		case level <= 0:
			log.Debugf("[module] %s", msg)
		case level == 1:
			log.Infof("[module] %s", msg)
		case level == 2:
			log.Warnf("[module] %s", msg)
		default:
			log.Errorf("[module] %s", msg)
		}
	}

	mod, err := wasmrt.NewModule(wasmBytes,
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(1024),
		wasmrt.WithMallocName("plugin_alloc"),
		wasmrt.WithFreeName("plugin_alloc"), // dummy — use SecureDeallocate
		wasmrt.WithSecureDealloc("plugin_free"),
		wasmrt.WithHostModule("sdn", wasmrt.SharedHostFuncs("sdn", logFunc)),
		wasmrt.WithHostModule("env", wasmrt.SharedHostFuncs("env", logFunc)),
		wasmrt.WithHostModule("sdn_host", bridge.BuildWasmEdgeHostFuncs()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create WASM module: %w", err)
	}
	m.mod = mod

	// Call _initialize
	mod.Execute("_initialize")

	// Read manifest
	manifest, err := ReadManifest(mod)
	if err != nil {
		mod.Release()
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}
	m.manifest = manifest

	// Now restrict the bridge to only granted capabilities
	granted := make(map[string]bool, len(manifest.Capabilities))
	for _, c := range manifest.Capabilities {
		granted[c] = true
	}
	bridge.granted = granted

	// Provision capabilities from registry
	if capReg != nil {
		capReg.mu.RLock()
		for _, cap := range manifest.Capabilities {
			if factory, ok := capReg.factories[cap]; ok {
				handler := factory(m)
				bridge.RegisterCapHandler(capPrefixFromName(cap), handler)
				log.Debugf("Module %q: provisioned capability %q", manifest.PluginID, cap)
			}
		}
		capReg.mu.RUnlock()
	}

	log.Infof("Module loaded: %s v%s (%s) — %d methods, %d protocols, %d capabilities",
		manifest.PluginID, manifest.Version, manifest.PluginFamily,
		len(manifest.Methods), len(manifest.Protocols), len(manifest.Capabilities))

	return m, nil
}

// capPrefixFromName maps a capability string to the hostcall operation prefix.
func capPrefixFromName(cap string) string {
	switch cap {
	case "protocol_handle", "protocol_dial":
		return "protocol"
	case "storage_query", "storage_write", "storage_adapter":
		return "storage"
	case "crypto_hash", "crypto_sign", "crypto_verify", "crypto_encrypt",
		"crypto_decrypt", "crypto_key_agreement", "crypto_kdf":
		return "crypto"
	case "context_read", "context_write":
		return "context"
	case "schedule_cron":
		return "schedule"
	default:
		return cap
	}
}

// --- plugins.Plugin interface ---

func (m *Module) ID() string {
	if m.manifest != nil {
		return m.manifest.PluginID
	}
	return "unknown-module"
}

func (m *Module) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.ctx = ctx
	m.cancel = cancel
	m.mu.Unlock()

	// Register libp2p stream handlers for declared protocols
	if runtime.Host != nil {
		for _, proto := range m.manifest.Protocols {
			if !proto.AutoInstall {
				continue
			}
			pid := protocol.ID(proto.ProtocolID)
			methodID := proto.MethodID
			runtime.Host.SetStreamHandler(pid, func(s network.Stream) {
				m.handleProtocolStream(s, methodID)
			})
			log.Infof("Module %q: registered protocol handler %s → %s", m.manifest.PluginID, proto.ProtocolID, methodID)
		}
	}

	log.Infof("Module %q started", m.manifest.PluginID)
	return nil
}

func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	// Modules can declare HTTP routes in their manifest in the future.
	// For now, no HTTP routes are auto-registered.
}

func (m *Module) Close() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	if m.mod != nil {
		m.mod.Release()
		m.mod = nil
	}
	log.Infof("Module %q closed", m.manifest.PluginID)
	return nil
}

// --- plugins.CronProvider interface ---

func (m *Module) CronMethods() []plugins.CronMethodSpec {
	var specs []plugins.CronMethodSpec
	for _, t := range m.manifest.Timers {
		interval := fmt.Sprintf("%dms", t.DefaultIntervalMs)
		if t.DefaultIntervalMs >= 1000 {
			interval = fmt.Sprintf("%ds", t.DefaultIntervalMs/1000)
		}
		specs = append(specs, plugins.CronMethodSpec{
			Method:          t.MethodID,
			Description:     t.Description,
			DefaultInterval: interval,
			Input:           "none",
			Output:          "json",
		})
	}
	return specs
}

func (m *Module) InvokeCron(ctx context.Context, method string, input []byte) ([]byte, error) {
	return m.InvokeMethod(ctx, method, input)
}

// --- plugins.UIProvider interface ---

func (m *Module) UIDescriptor() plugins.UIDescriptor {
	return plugins.UIDescriptor{
		Title:       m.manifest.Name,
		Description: fmt.Sprintf("%s v%s", m.manifest.PluginID, m.manifest.Version),
		Icon:        "📦",
		Color:       "#6366f1",
		TextColor:   "#ffffff",
	}
}

// --- Module-SDK invocation ---

// InvokeMethod calls plugin_invoke_stream with the given method ID and payload.
func (m *Module) InvokeMethod(ctx context.Context, methodID string, payload []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mod == nil {
		return nil, fmt.Errorf("module not loaded")
	}

	// Build a simple invoke envelope: 4-byte method length + method + payload
	// This matches the PluginInvokeRequest convention used by plugin_invoke_stream.
	methodBytes := []byte(methodID)
	reqLen := 4 + len(methodBytes) + len(payload)
	req := make([]byte, reqLen)
	req[0] = byte(len(methodBytes))
	req[1] = byte(len(methodBytes) >> 8)
	req[2] = byte(len(methodBytes) >> 16)
	req[3] = byte(len(methodBytes) >> 24)
	copy(req[4:], methodBytes)
	if len(payload) > 0 {
		copy(req[4+len(methodBytes):], payload)
	}

	reqPtr, err := m.mod.Allocate(req)
	if err != nil {
		return nil, fmt.Errorf("allocate request: %w", err)
	}
	defer m.mod.SecureDeallocate(reqPtr, uint32(len(req)))

	const outCap = 16384
	outPtr, err := m.mod.AllocateSize(outCap)
	if err != nil {
		return nil, fmt.Errorf("allocate output: %w", err)
	}
	defer m.mod.SecureDeallocate(outPtr, outCap)

	results, err := m.mod.Execute("plugin_invoke_stream",
		int32(reqPtr), int32(len(req)), int32(outCap),
	)
	if err != nil {
		return nil, fmt.Errorf("plugin_invoke_stream(%s): %w", methodID, err)
	}

	written := wasmrt.ToInt32(results[0])
	if written <= 0 {
		return nil, nil
	}
	if uint32(written) > outCap {
		return nil, fmt.Errorf("output %d exceeds capacity %d", written, outCap)
	}

	return m.mod.ReadMemory(outPtr, uint32(written))
}

// Manifest returns the parsed manifest.
func (m *Module) Manifest() *Manifest { return m.manifest }

// Mod returns the underlying wasmrt.Module.
func (m *Module) Mod() *wasmrt.Module { return m.mod }

// --- Generic protocol stream handler ---

func (m *Module) handleProtocolStream(s network.Stream, methodID string) {
	defer s.Close()

	s.SetReadDeadline(time.Now().Add(15 * time.Second))

	// Read request bytes from stream (max 16KB)
	reqBytes := make([]byte, 16384)
	n, err := io.ReadAtLeast(s, reqBytes, 1)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		log.Debugf("Module %q protocol %s: read error: %v", m.manifest.PluginID, methodID, err)
		return
	}
	reqBytes = reqBytes[:n]

	// Invoke the module's method
	resp, err := m.InvokeMethod(context.Background(), methodID, reqBytes)
	if err != nil {
		log.Warnf("Module %q protocol %s: invoke error: %v", m.manifest.PluginID, methodID, err)
		return
	}

	if len(resp) > 0 {
		s.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := s.Write(resp); err != nil {
			log.Debugf("Module %q protocol %s: write error: %v", m.manifest.PluginID, methodID, err)
		}
	}
}

// --- JSON helper for debug output ---
var _ = json.Marshal
