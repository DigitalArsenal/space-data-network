package modulert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/host"
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
	mod       *wasmrt.Module
	wasmBytes []byte
	manifest  *Manifest
	bridge    *HostBridge
	nodeCtx   *NodeContext
	capReg    *CapabilityRegistry
	host      host.Host
	paused    bool
	mu        sync.Mutex

	ctx       context.Context
	cancel    context.CancelFunc
	startedAt time.Time
	wg        sync.WaitGroup

	invokeCount     uint64
	errorCount      uint64
	totalLatency    time.Duration
	lastInvokeAt    time.Time
	timerRunCount   uint64
	lastTimerStatus string
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
		wasmBytes: append([]byte(nil), wasmBytes...),
		nodeCtx:   nodeCtx,
		capReg:    capReg,
	}

	if err := m.Load(context.Background()); err != nil {
		return nil, err
	}

	return m, nil
}

// Load instantiates the module artifact and refreshes the manifest without
// starting protocol handlers or timers.
func (m *Module) Load(ctx context.Context) error {
	if m == nil {
		return errors.New("module is nil")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if m.mod != nil {
		m.mu.Unlock()
		return nil
	}
	wasmBytes := append([]byte(nil), m.wasmBytes...)
	m.mu.Unlock()
	if len(wasmBytes) == 0 {
		return errors.New("module artifact is empty")
	}

	mod, bridge, manifest, err := m.instantiateWASM(wasmBytes)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.mod != nil {
		m.mu.Unlock()
		mod.Release()
		return nil
	}
	m.mod = mod
	m.bridge = bridge
	m.manifest = manifest
	m.paused = false
	m.mu.Unlock()

	log.Infof("Module loaded: %s v%s (%s) — %d methods, %d protocols, %d capabilities",
		manifest.PluginID, manifest.Version, manifest.PluginFamily,
		len(manifest.Methods), len(manifest.Protocols), len(manifest.Capabilities))

	return nil
}

func (m *Module) instantiateWASM(wasmBytes []byte) (*wasmrt.Module, *HostBridge, *Manifest, error) {
	// Create the per-module host bridge (needs manifest first for capabilities,
	// but we need the WASM loaded to read the manifest — chicken-and-egg.
	// Solution: create bridge with all capabilities initially, then restrict after manifest parse.)
	bridge := NewHostBridge(m.nodeCtx, nil)

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
		return nil, nil, nil, fmt.Errorf("failed to create WASM module: %w", err)
	}

	// Call _initialize
	mod.Execute("_initialize")

	// Read manifest
	manifest, err := ReadManifest(mod)
	if err != nil {
		mod.Release()
		return nil, nil, nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// Now restrict the bridge to only granted capabilities
	granted := make(map[string]bool, len(manifest.Capabilities))
	for _, c := range manifest.Capabilities {
		granted[c] = true
	}
	bridge.granted = granted

	// Provision capabilities from registry
	if m.capReg != nil {
		m.capReg.mu.RLock()
		for _, cap := range manifest.Capabilities {
			if factory, ok := m.capReg.factories[cap]; ok {
				handler := factory(m)
				bridge.RegisterCapHandler(capPrefixFromName(cap), handler)
				log.Debugf("Module %q: provisioned capability %q", manifest.PluginID, cap)
			}
		}
		m.capReg.mu.RUnlock()
	}

	return mod, bridge, manifest, nil
}

// capPrefixFromName maps a capability string to the hostcall operation prefix.
func capPrefixFromName(cap string) string {
	switch cap {
	case "protocol_handle", "protocol_dial":
		return "protocol"
	case "wallet_sign":
		return "keyslot"
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
	if m == nil {
		return errors.New("module is nil")
	}
	m.mu.Lock()
	loaded := m.mod != nil
	m.mu.Unlock()
	if !loaded {
		if err := m.Load(ctx); err != nil {
			return err
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.ctx = ctx
	m.cancel = cancel
	m.host = runtime.Host
	m.paused = false
	m.startedAt = time.Now().UTC()
	manifest := m.manifest
	m.mu.Unlock()
	if manifest == nil {
		return errors.New("module manifest is not loaded")
	}

	// Register libp2p stream handlers for declared protocols
	if runtime.Host != nil {
		for _, proto := range manifest.Protocols {
			if !proto.AutoInstall {
				continue
			}
			streamProtocolID := strings.TrimSpace(proto.WireID)
			if streamProtocolID == "" {
				streamProtocolID = proto.ProtocolID
			}
			pid := protocol.ID(streamProtocolID)
			methodID := proto.MethodID
			runtime.Host.SetStreamHandler(pid, func(s network.Stream) {
				m.handleProtocolStream(s, methodID)
			})
			log.Infof(
				"Module %q: registered protocol handler %s (%s) → %s",
				manifest.PluginID,
				streamProtocolID,
				proto.ProtocolID,
				methodID,
			)
		}
	}

	log.Infof("Module %q started", manifest.PluginID)
	return nil
}

func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	// Modules can declare HTTP routes in their manifest in the future.
	// For now, no HTTP routes are auto-registered.
}

func (m *Module) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	cancel := m.cancel
	mod := m.mod
	runtimeHost := m.host
	manifest := m.manifest
	m.cancel = nil
	m.ctx = nil
	m.mod = nil
	m.host = nil
	m.paused = false
	m.startedAt = time.Time{}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	if runtimeHost != nil && manifest != nil {
		for _, proto := range manifest.Protocols {
			if !proto.AutoInstall {
				continue
			}
			streamProtocolID := strings.TrimSpace(proto.WireID)
			if streamProtocolID == "" {
				streamProtocolID = proto.ProtocolID
			}
			if streamProtocolID != "" {
				runtimeHost.RemoveStreamHandler(protocol.ID(streamProtocolID))
			}
		}
	}
	if mod != nil {
		mod.Release()
	}
	if manifest != nil {
		log.Infof("Module %q closed", manifest.PluginID)
	}
	return nil
}

// Pause prevents method invocation while keeping the runtime artifact loaded.
func (m *Module) Pause(ctx context.Context) error {
	if m == nil {
		return errors.New("module is nil")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mod == nil {
		return errors.New("module not loaded")
	}
	m.paused = true
	return nil
}

// Resume allows method invocation after a pause. If the artifact was unloaded,
// it is loaded first but protocol handlers are left to Start.
func (m *Module) Resume(ctx context.Context) error {
	if m == nil {
		return errors.New("module is nil")
	}
	m.mu.Lock()
	loaded := m.mod != nil
	m.mu.Unlock()
	if !loaded {
		if err := m.Load(ctx); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.paused = false
	m.mu.Unlock()
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
	output, err := m.InvokeMethod(ctx, method, input)
	m.recordTimerResult(err)
	return output, err
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
	return m.InvokeMethodFrames(ctx, methodID, []InvokeInputFrame{
		{
			PortID:  "request",
			Payload: payload,
		},
	})
}

// InvokeMethodFrames calls plugin_invoke_stream with an SDK-style multi-port input request.
func (m *Module) InvokeMethodFrames(ctx context.Context, methodID string, inputFrames []InvokeInputFrame) (payload []byte, err error) {
	started := time.Now()
	defer func() {
		m.recordInvokeResult(started, err)
	}()

	m.mu.Lock()
	defer m.mu.Unlock()

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

	results, err := m.mod.Execute("plugin_invoke_stream",
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
		return nil, fmt.Errorf("decode invoke response: %w", err)
	}

	return extractPluginInvokePayload(response, "response")
}

// Manifest returns the parsed manifest.
func (m *Module) Manifest() *Manifest { return m.manifest }

// RuntimeDescriptor returns a dashboard-safe summary of this module.
func (m *Module) RuntimeDescriptor() plugins.RuntimeModuleDescriptor {
	descriptor := plugins.RuntimeModuleDescriptor{
		Manifest: runtimeManifestDescriptor(m.manifest),
	}
	if m != nil && m.mod != nil {
		if stats, err := m.mod.MemoryStats(); err == nil {
			descriptor.Stats.MemoryPages = stats.Pages
			descriptor.Stats.MemoryBytes = stats.Bytes
			descriptor.Stats.MaxMemoryPages = stats.MaxPages
			descriptor.Stats.MaxMemoryBytes = stats.MaxBytes
		}
	}
	if m != nil {
		m.mu.Lock()
		startedAt := m.startedAt
		invokeCount := m.invokeCount
		errorCount := m.errorCount
		totalLatency := m.totalLatency
		lastInvokeAt := m.lastInvokeAt
		timerRunCount := m.timerRunCount
		lastTimerStatus := m.lastTimerStatus
		m.mu.Unlock()
		if !startedAt.IsZero() {
			descriptor.Stats.UptimeMs = time.Since(startedAt).Milliseconds()
		}
		descriptor.Stats.InvokeCount = invokeCount
		descriptor.Stats.ErrorCount = errorCount
		if !lastInvokeAt.IsZero() {
			descriptor.Stats.LastInvokeAt = lastInvokeAt.UTC().Format(time.RFC3339)
		}
		if invokeCount > 0 {
			descriptor.Stats.AverageLatencyMs = float64(totalLatency.Microseconds()) / 1000.0 / float64(invokeCount)
		}
		descriptor.Stats.TimerRunCount = timerRunCount
		descriptor.Stats.LastTimerStatus = lastTimerStatus
	}
	return descriptor
}

func (m *Module) recordInvokeResult(started time.Time, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invokeCount++
	m.totalLatency += time.Since(started)
	m.lastInvokeAt = time.Now().UTC()
	if err != nil {
		m.errorCount++
	}
}

func (m *Module) recordTimerResult(err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timerRunCount++
	if err != nil {
		m.lastTimerStatus = "error"
		return
	}
	m.lastTimerStatus = "ok"
}

// Mod returns the underlying wasmrt.Module.
func (m *Module) Mod() *wasmrt.Module { return m.mod }

// RuntimeHost returns the module's bound libp2p host once Start() has run.
func (m *Module) RuntimeHost() host.Host {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.host
}

// NodeContext returns the module's bound node context.
func (m *Module) NodeContext() *NodeContext { return m.nodeCtx }

func runtimeManifestDescriptor(manifest *Manifest) *plugins.RuntimeModuleManifest {
	if manifest == nil {
		return nil
	}
	out := &plugins.RuntimeModuleManifest{
		PluginID:     manifest.PluginID,
		Name:         manifest.Name,
		Version:      manifest.Version,
		PluginFamily: manifest.PluginFamily,
		Capabilities: append([]string(nil), manifest.Capabilities...),
	}
	for _, method := range manifest.Methods {
		out.Methods = append(out.Methods, plugins.RuntimeModuleMethod{
			MethodID:    method.MethodID,
			DisplayName: method.DisplayName,
			Description: method.Description,
			InputPorts:  runtimeManifestPorts(method.InputPorts),
			OutputPorts: runtimeManifestPorts(method.OutputPorts),
			MaxBatch:    method.MaxBatch,
			DrainPolicy: method.DrainPolicy,
		})
	}
	for _, protocolDecl := range manifest.Protocols {
		out.Protocols = append(out.Protocols, plugins.RuntimeModuleProtocol{
			ProtocolID:    protocolDecl.ProtocolID,
			MethodID:      protocolDecl.MethodID,
			InputPortID:   protocolDecl.InputPortID,
			OutputPortID:  protocolDecl.OutputPortID,
			Description:   protocolDecl.Description,
			WireID:        protocolDecl.WireID,
			TransportKind: protocolDecl.TransportKind,
			Role:          protocolDecl.Role,
			AutoInstall:   protocolDecl.AutoInstall,
			Advertise:     protocolDecl.Advertise,
			DiscoveryKey:  protocolDecl.DiscoveryKey,
		})
	}
	for _, timer := range manifest.Timers {
		out.Timers = append(out.Timers, plugins.RuntimeModuleTimer{
			TimerID:           timer.TimerID,
			MethodID:          timer.MethodID,
			DefaultIntervalMs: timer.DefaultIntervalMs,
			Description:       timer.Description,
		})
	}
	return out
}

func runtimeManifestPorts(ports []ManifestPort) []plugins.RuntimeModulePort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]plugins.RuntimeModulePort, 0, len(ports))
	for _, port := range ports {
		out = append(out, plugins.RuntimeModulePort{
			PortID:           port.PortID,
			DisplayName:      port.DisplayName,
			AcceptedTypeSets: runtimeManifestAcceptedTypeSets(port.AcceptedTypeSets),
			MinStreams:       port.MinStreams,
			MaxStreams:       port.MaxStreams,
			Required:         port.Required,
			Description:      port.Description,
		})
	}
	return out
}

func runtimeManifestAcceptedTypeSets(sets []ManifestAcceptedTypeSet) []plugins.RuntimeModuleAcceptedTypeSet {
	if len(sets) == 0 {
		return nil
	}
	out := make([]plugins.RuntimeModuleAcceptedTypeSet, 0, len(sets))
	for _, set := range sets {
		out = append(out, plugins.RuntimeModuleAcceptedTypeSet{
			SetID:              set.SetID,
			AllowedTypes:       runtimeManifestTypeRefs(set.AllowedTypes),
			AllowedWireFormats: append([]string(nil), set.AllowedWireFormats...),
			Description:        set.Description,
		})
	}
	return out
}

func runtimeManifestTypeRefs(typeRefs []ManifestFlatBufferTypeRef) []plugins.RuntimeModuleTypeRef {
	if len(typeRefs) == 0 {
		return nil
	}
	out := make([]plugins.RuntimeModuleTypeRef, 0, len(typeRefs))
	for _, typeRef := range typeRefs {
		out = append(out, plugins.RuntimeModuleTypeRef{
			SchemaName:     typeRef.SchemaName,
			FileIdentifier: typeRef.FileIdentifier,
			SchemaVersion:  typeRef.SchemaVersion,
			RootType:       typeRef.RootType,
		})
	}
	return out
}

// --- Generic protocol stream handler ---

func (m *Module) handleProtocolStream(s network.Stream, methodID string) {
	defer s.Close()

	s.SetReadDeadline(time.Now().Add(15 * time.Second))

	// Read the full bounded request payload before invoking the module.
	reqBytes, err := io.ReadAll(io.LimitReader(s, 16385))
	if err != nil {
		log.Debugf("Module %q protocol %s: read error: %v", m.manifest.PluginID, methodID, err)
		return
	}
	if len(reqBytes) == 0 {
		return
	}
	if len(reqBytes) > 16384 {
		log.Debugf("Module %q protocol %s: request exceeds 16KB limit", m.manifest.PluginID, methodID)
		return
	}

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
