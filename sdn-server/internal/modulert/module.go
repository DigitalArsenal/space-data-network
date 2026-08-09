package modulert

import (
	"context"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

var log = logging.Logger("modulert")

// Default per-invocation resource limits for module-sdk WASM guests (loop
// B3 — defensive hardening, fail closed). Every wasmrt.Module.Execute call
// on a loaded module (plugin_invoke_stream, malloc/free, manifest reads,
// _initialize, ...) is subject to both: WasmEdge aborts the guest itself
// once either budget is spent, hard-interrupting it and leaving the runtime
// usable for the next call. The module-sdk manifest (Manifest, manifest.go)
// deliberately still has no field to override these — a module must never be
// able to self-grant a bigger budget (that would be an untrusted-CPU DoS
// vector). Where a larger budget is legitimately needed, the HOST grants it
// per-call: scheduled/cron invocations pick up the wider budget below via the
// InvokeCron seam (wasmrt.WithExecBudget), operator-tunable through config
// (modules.scheduled_invoke_timeout). Interactive invocations keep these
// tight defaults.
const (
	// defaultInvokeTimeout is the wall-clock budget per Execute call,
	// hard-enforced via WasmEdge async-execute + cancel (modules here are
	// never created WithDedicatedThread, so the hard-interrupt path always
	// applies).
	defaultInvokeTimeout = 30 * time.Second
	// defaultInvokeCostLimit is the WasmEdge instruction-cost (fuel)
	// budget per Execute call. WasmEdge's default per-instruction cost is 1
	// unit (no custom cost table configured here), so this is approximately
	// an instruction-count ceiling — generous headroom for legitimate
	// module work while deterministically bounding a hot loop regardless of
	// host CPU speed.
	defaultInvokeCostLimit = 4_000_000_000
)

// Scheduled-invocation budgets. The interactive defaults above (30s / 4e9)
// are the fail-closed baseline for request-scoped calls (protocol/HTTP
// handlers, which pass a ctx deadline). SCHEDULED work — a cron ticker fire
// or the run-now admin action, both of which reach the guest through
// InvokeCron — may legitimately run for minutes on a slow production host,
// blowing past the 30s interactive cap.
// The larger budget is granted per-call by the HOST at the InvokeCron seam
// (see InvokeCron), never by the module manifest — a module cannot self-grant
// unbounded CPU. A ctx deadline still narrows it (ExecuteContext semantics
// preserved). Operators on especially slow hosts can raise the wall-clock
// budget via config (modules.scheduled_invoke_timeout), which auto-scales the
// fuel budget proportionally.
const (
	// defaultScheduledInvokeTimeout is the per-call wall-clock budget for a
	// scheduled/cron/run-now module invocation. The 10m default comfortably
	// covers a full multi-thousand-record ephemeris pull-parse-sign-publish
	// cycle on a 1 vCPU host while still hard-bounding a runaway guest (the
	// async interrupt fires at the budget; modulert modules are never
	// dedicated-thread, so the wall-clock bound is always hard-enforced).
	defaultScheduledInvokeTimeout = 10 * time.Minute
	// defaultScheduledInvokeCostLimit scales the fuel budget proportionally to
	// the wall-clock ratio (10m / 30s = 20x) so a large in-guest parse
	// (per-record CID/sign over thousands of records) keeps the same
	// instructions-per-second ceiling it had under the 30s interactive budget,
	// just over a longer window — rather than trading a wall-clock failure for
	// a fuel-exhaustion one. A pure-compute runaway is still ultimately bounded
	// by the 10m wall-clock interrupt even if it somehow outran this.
	defaultScheduledInvokeCostLimit uint = defaultInvokeCostLimit * 20
)

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
	// contentHash is the lowercase hex SHA-256 digest of wasmBytes — the
	// capability policy identity (loop B1). Set once instantiateWASM reads
	// the manifest.
	contentHash string
	// signatureStatus is the publication-trailer signature verification
	// outcome for this artifact (loop I1). Set before contentHash, always
	// populated once instantiateWASM has run (even when
	// NodeContext.ModuleSignaturePolicy is nil, i.e. enforcement not
	// configured — Signed/Verified will simply reflect the artifact as-is).
	signatureStatus ModuleSignatureStatus
	// uiURL is the module's UI page URL, assigned via SetUIURL. WASM modules
	// have no self-known UI location the way a Go-native plugin does, so
	// this is populated from outside (see internal/appmanifest.AppManifest's
	// UI entry, resolved and pushed in by node/cmd startup wiring through
	// plugins.Manager.SetModuleUIURL). Empty until assigned, so
	// UIDescriptor().URL stays "" for modules with no declared UI, exactly
	// as before this field existed (H1 loop).
	uiURL string

	// scheduledInvokeTimeout is the per-call wall-clock budget granted to
	// SCHEDULED invocations (cron + run-now) at the InvokeCron seam. Zero
	// selects defaultScheduledInvokeTimeout. Populated from
	// NodeContext.ScheduledInvokeTimeout at construction, which an operator
	// can tune via config (modules.scheduled_invoke_timeout) for slow hosts.
	// Interactive invokes never consult this — they keep defaultInvokeTimeout.
	scheduledInvokeTimeout time.Duration
	// scheduledInvokeCostLimit is the per-call fuel budget for scheduled
	// invocations. Zero selects a value scaled proportionally to the resolved
	// wall-clock budget (see scheduledBudget), keeping the interactive
	// instructions-per-second ceiling over the longer scheduled window.
	scheduledInvokeCostLimit uint

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
//
// Most capabilities are registered by their EXACT name. A few form an open
// FAMILY whose members are not knowable at boot — today only the credential
// lanes ("secrets:<lane>"), where an operator may define a lane for any service
// at runtime (see internal/credstore). Those register once by prefix
// (RegisterFamily) and resolve for every present and future member.
type CapabilityRegistry struct {
	mu        sync.RWMutex
	factories map[string]BridgeCapFactory
	// families maps a capability-name PREFIX to the factory serving every
	// capability that starts with it. Exact registrations always win; among
	// families the longest matching prefix wins, so a family can never shadow
	// a more specific one.
	families map[string]BridgeCapFactory
}

// CapFactory creates a CapHandler for a module that declared a given capability.
type CapFactory func(mod *Module) CapHandler

// BridgeCapFactory is a CapFactory that additionally receives the module
// instance's hostcall bridge — handlers that register out-of-band results
// (body references, loop C.5c) need the bridge's registry. mod may be nil
// (flow instances have no *Module wrapper).
type BridgeCapFactory func(mod *Module, bridge *HostBridge) CapHandler

// NewCapabilityRegistry creates an empty registry.
func NewCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{
		factories: make(map[string]BridgeCapFactory),
		families:  make(map[string]BridgeCapFactory),
	}
}

// Register adds a capability factory.
func (r *CapabilityRegistry) Register(capability string, factory CapFactory) {
	r.RegisterBridgeAware(capability, func(mod *Module, _ *HostBridge) CapHandler {
		return factory(mod)
	})
}

// RegisterBridgeAware adds a capability factory that receives the hostcall
// bridge alongside the module.
func (r *CapabilityRegistry) RegisterBridgeAware(capability string, factory BridgeCapFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[capability] = factory
}

// RegisterFamily adds a factory serving EVERY capability whose name begins with
// prefix. It exists for capability families whose membership is open-ended at
// boot — the credential lanes ("secrets:<lane>"), where an operator may define
// a lane for any service at runtime and the daemon must not need a restart to
// serve it.
//
// THIS IS NOT A WIDENING OF PRIVILEGE. Resolving a factory is the LAST step of
// provisioning, and it happens only for capabilities the module already got
// past checkCapabilityPolicy with (see module.go instantiateWASM and
// capability_provision.go ProvisionBridge — the policy gate runs first in both).
// For the secrets family every member is sensitive by prefix
// (IsSensitiveCapability), so each lane still requires its own operator
// approval recorded against the module's content hash. What this changes is
// only the failure mode for an APPROVED module naming an operator-defined lane:
// previously "operation not supported" until the daemon restarted, now served.
//
// Exact registrations take precedence; among families the longest matching
// prefix wins.
func (r *CapabilityRegistry) RegisterFamily(prefix string, factory BridgeCapFactory) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		// A blank prefix would match every capability name — refuse rather than
		// silently install a catch-all factory.
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.families[prefix] = factory
}

// lookupLocked resolves a capability to its factory. The caller must hold at
// least the read lock.
func (r *CapabilityRegistry) lookupLocked(capability string) (BridgeCapFactory, bool) {
	if factory, ok := r.factories[capability]; ok {
		return factory, true
	}
	var (
		best     BridgeCapFactory
		bestLen  = -1
		bestSeen bool
	)
	for prefix, factory := range r.families {
		if strings.HasPrefix(capability, prefix) && len(prefix) > bestLen {
			best, bestLen, bestSeen = factory, len(prefix), true
		}
	}
	return best, bestSeen
}

// NewModule loads a module-sdk WASM binary, reads its manifest, creates a
// per-module host bridge, and provisions declared capabilities.
func NewModule(wasmBytes []byte, capReg *CapabilityRegistry, nodeCtx *NodeContext) (*Module, error) {
	m := &Module{
		wasmBytes: append([]byte(nil), wasmBytes...),
		nodeCtx:   nodeCtx,
		capReg:    capReg,
	}
	if nodeCtx != nil {
		// Operator-tunable scheduled budget (config
		// modules.scheduled_invoke_timeout → NodeContext). Zero means "use the
		// built-in default", applied by scheduledBudget().
		m.scheduledInvokeTimeout = nodeCtx.ScheduledInvokeTimeout
	}

	if err := m.Load(context.Background()); err != nil {
		return nil, err
	}

	return m, nil
}

// scheduledBudget resolves the per-call resource budget granted to a
// SCHEDULED invocation (cron ticker + run-now admin, both via InvokeCron).
// The wall-clock budget is the operator override (NodeContext) when set, else
// defaultScheduledInvokeTimeout. The fuel budget is the operator override when
// set, else it is scaled proportionally to the resolved wall-clock budget so a
// hand-tuned longer timeout also gets proportionally more fuel — keeping the
// interactive instructions-per-second ceiling over the longer window instead
// of trading a wall-clock failure for a fuel-exhaustion one.
func (m *Module) scheduledBudget() wasmrt.ExecBudget {
	timeout := m.scheduledInvokeTimeout
	if timeout <= 0 {
		timeout = defaultScheduledInvokeTimeout
	}
	cost := m.scheduledInvokeCostLimit
	if cost == 0 {
		if timeout == defaultScheduledInvokeTimeout {
			cost = defaultScheduledInvokeCostLimit
		} else {
			// Scale fuel ∝ wall-clock: cost = defaultInvokeCostLimit *
			// (timeout / defaultInvokeTimeout). Computed in float64 to avoid
			// the uint64 overflow of the intermediate product
			// (defaultInvokeCostLimit * timeout is ~e21 for multi-minute
			// timeouts, well past uint64's ~1.8e19 ceiling); the operand
			// magnitudes and the result are all exactly representable.
			ratio := float64(timeout) / float64(defaultInvokeTimeout)
			cost = uint(float64(defaultInvokeCostLimit) * ratio)
		}
	}
	return wasmrt.ExecBudget{Timeout: timeout, CostLimit: cost}
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
	// Publication-trailer signature gate (loop I1 — defensive hardening,
	// FAIL CLOSED once configured). This MUST run before any wasm bytes are
	// handed to the runtime: an unsigned or badly-signed artifact is
	// refused here, before _initialize, before the manifest is even parsed
	// — a tampered artifact's self-reported manifest is not trustworthy
	// evidence about itself. See publication_signature.go for the
	// verification scheme (reconciles with publish_protocol.go's Ed25519
	// signer-key model) and ModuleSignaturePolicy for the nil-policy /
	// allowlist semantics. This call also strips the SDS $REC publication
	// trailer unconditionally (previously missing here entirely — wasmBytes
	// went straight to wasmrt.NewModule trailer and all), so the runtime
	// only ever compiles the portable wasm payload.
	var sigPolicy *ModuleSignaturePolicy
	if m.nodeCtx != nil {
		sigPolicy = m.nodeCtx.ModuleSignaturePolicy
	}
	portableBytes, sigStatus, sigErr := enforceModuleSignaturePolicy(sigPolicy, wasmBytes)
	if sigErr != nil {
		return nil, nil, nil, sigErr
	}
	wasmBytes = portableBytes
	m.mu.Lock()
	m.signatureStatus = sigStatus
	m.mu.Unlock()

	// Create the per-module host bridge (needs manifest first for capabilities,
	// but we need the WASM loaded to read the manifest — chicken-and-egg.
	// Solution: create bridge with all capabilities initially, then restrict after manifest parse.)
	bridge := NewHostBridge(m.nodeCtx, nil)

	// Build WasmEdge module with shared host functions + per-module SDK hostcall bridge.
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
		wasmrt.WithExecTimeout(defaultInvokeTimeout),
		wasmrt.WithCostLimit(defaultInvokeCostLimit),
		wasmrt.WithMallocName("plugin_alloc"),
		wasmrt.WithFreeName("plugin_alloc"), // dummy — use SecureDeallocate
		wasmrt.WithSecureDealloc("plugin_free"),
		wasmrt.WithHostModule("sdn", wasmrt.SharedHostFuncs("sdn", logFunc)),
		wasmrt.WithHostModule("env", wasmrt.SharedHostFuncs("env", logFunc)),
		wasmrt.WithHostModule(HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create WASM module: %w", err)
	}

	// Run a guest initializer when the artifact declares one. Initialization
	// failures are load failures; an uninitialized module must never be admitted.
	switch {
	case mod.HasFunction("_initialize"):
		if _, initErr := mod.Execute("_initialize"); initErr != nil {
			mod.Release()
			return nil, nil, nil, fmt.Errorf("run WASI reactor initialization: %w", initErr)
		}
	case mod.HasFunction("__wasm_call_ctors"):
		if _, initErr := mod.Execute("__wasm_call_ctors"); initErr != nil {
			mod.Release()
			return nil, nil, nil, fmt.Errorf("run command module constructors: %w", initErr)
		}
	}

	// Read manifest
	manifest, err := ReadManifest(mod)
	if err != nil {
		mod.Release()
		return nil, nil, nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// Operator capability policy gate (loop B1 — defensive hardening, FAIL
	// CLOSED). A module's manifest is self-declared, so the manifest alone
	// is not authorization: sensitive capabilities require an explicit
	// recorded operator approval keyed by the module's content hash (not
	// the spoofable manifest PluginID), or the WHOLE module is refused —
	// no partial silent grant. This is independent of, and runs before,
	// the host-can-satisfy provisioning below (which stays tolerant of
	// capabilities the host has no factory for, e.g. "clock"/"logging" —
	// that is a capacity gap, not a trust decision).
	// wasmBytes is already the portable (trailer-stripped) payload at this
	// point (stripped by enforceModuleSignaturePolicy above), so this is
	// exactly sigStatus.ContentHash — reuse it directly rather than
	// re-hashing, guaranteeing the capability-policy identity and the
	// publication-signature identity can never drift apart.
	contentHash := sigStatus.ContentHash
	m.mu.Lock()
	m.contentHash = contentHash
	m.mu.Unlock()
	var policy *CapabilityPolicyStore
	if m.nodeCtx != nil {
		policy = m.nodeCtx.CapabilityPolicy
	}
	if err := checkCapabilityPolicy(policy, contentHash, manifest.PluginID, manifest.Capabilities); err != nil {
		mod.Release()
		return nil, nil, nil, err
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
			if factory, ok := m.capReg.lookupLocked(cap); ok {
				handler := factory(m, bridge)
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
	// Credential-keystore lanes: every "secrets:<id>" capability is served by
	// the single "secrets" hostcall handler (caps/secrets.go), which re-checks
	// the exact lane per call — the same one-handler/many-capabilities shape as
	// the storage_* family below.
	if strings.HasPrefix(cap, SecretsCapabilityPrefix) {
		return "secrets"
	}
	switch cap {
	case "protocol_handle", "protocol_dial":
		return "protocol"
	case "wallet_sign":
		return "keyslot"
	case "storage_query", "storage_write", "storage_adapter", "storage_ingest":
		return "storage"
	case "crypto_hash", "crypto_sign", "crypto_verify", "crypto_encrypt",
		"crypto_decrypt", "crypto_key_agreement", "crypto_kdf":
		return "crypto"
	case "context_read", "context_write":
		return "context"
	case "schedule_cron":
		return "schedule"
	case "p2p_read":
		return "p2p"
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

// ApplyRuntimeModuleInputs applies dashboard-saved method input values after a
// restart. Values are grouped by method and passed through the SDK stream
// invocation ABI as port frames.
func (m *Module) ApplyRuntimeModuleInputs(ctx context.Context, values []plugins.RuntimeModuleInputValue) error {
	if len(values) == 0 {
		return nil
	}
	grouped := make(map[string][]InvokeInputFrame)
	order := make([]string, 0)
	for _, value := range values {
		methodID := strings.TrimSpace(value.MethodID)
		if methodID == "" {
			return errors.New("runtime input method id is required")
		}
		payload, err := decodeRuntimeModuleInputPayload(value)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", value.MethodID, value.PortID, err)
		}
		if _, exists := grouped[methodID]; !exists {
			order = append(order, methodID)
		}
		grouped[methodID] = append(grouped[methodID], InvokeInputFrame{
			PortID:         value.PortID,
			Payload:        payload,
			SchemaName:     value.SchemaName,
			FileIdentifier: value.FileIdentifier,
			RootTypeName:   value.RootType,
			WireFormat:     runtimeModuleInputWireFormat(value),
		})
	}
	for _, methodID := range order {
		if _, err := m.InvokeMethodFrames(ctx, methodID, grouped[methodID]); err != nil {
			return fmt.Errorf("apply runtime input method %q: %w", methodID, err)
		}
	}
	return nil
}

func decodeRuntimeModuleInputPayload(value plugins.RuntimeModuleInputValue) ([]byte, error) {
	raw := strings.TrimSpace(value.Value)
	switch strings.ToLower(strings.TrimSpace(value.Encoding)) {
	case "base64":
		return base64.StdEncoding.DecodeString(raw)
	case "hex":
		return hex.DecodeString(raw)
	case "json", "text", "":
		return []byte(raw), nil
	default:
		return nil, fmt.Errorf("unsupported encoding %q", value.Encoding)
	}
}

func runtimeModuleInputWireFormat(value plugins.RuntimeModuleInputValue) byte {
	normalized := strings.ToUpper(strings.TrimSpace(value.WireFormat))
	if strings.Contains(normalized, "ALIGNED") || strings.Contains(normalized, "JSON") || strings.Contains(normalized, "TEXT") {
		return payloadWireFormatAlignedBinary
	}
	return payloadWireFormatFlatbuffer
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
	// Scheduled seam. Both the cron ticker (Manager.scheduleCronMethods) and
	// the run-now admin path (Manager.RunRuntimeModuleScheduleNow) reach the
	// guest exclusively through InvokeCron, so this is the single place the
	// HOST grants the larger scheduled budget. The manifest never reaches this
	// — a module cannot self-grant it. Interactive paths call InvokeMethod /
	// InvokeMethodFrames directly and keep the tight defaultInvokeTimeout. A
	// caller-supplied ctx deadline still narrows the budget (see
	// wasmrt.effectiveTimeout / ExecuteContext).
	ctx = wasmrt.WithExecBudget(ctx, m.scheduledBudget())
	output, err := m.InvokeMethod(ctx, method, input)
	m.recordTimerResult(err)
	return output, err
}

// --- plugins.UIProvider / plugins.UIURLSetter interfaces ---

func (m *Module) UIDescriptor() plugins.UIDescriptor {
	m.mu.Lock()
	url := m.uiURL
	m.mu.Unlock()
	return plugins.UIDescriptor{
		Title:       m.manifest.Name,
		Description: fmt.Sprintf("%s v%s", m.manifest.PluginID, m.manifest.Version),
		Icon:        "📦",
		Color:       "#6366f1",
		TextColor:   "#ffffff",
		URL:         url,
	}
}

// SetUIURL assigns the module's UI page URL, implementing
// plugins.UIURLSetter. A module-sdk WASM module has no self-known UI
// location (unlike a Go-native plugin such as ailogplugin, which hardcodes
// its own dashboard path); the URL instead comes from an app manifest's UI
// entry (internal/appmanifest.AppManifest), resolved and pushed in by
// node/cmd startup wiring via plugins.Manager.SetModuleUIURL once this
// module is registered. Safe to call at any time — UIDescriptor() always
// reflects the latest value. Never calling it leaves UIDescriptor().URL
// empty, matching pre-H1 behavior for modules with no declared UI.
func (m *Module) SetUIURL(url string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uiURL = url
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

	// ExecuteContext (not plain Execute): narrows the module's default B3
	// wall-clock budget (defaultInvokeTimeout) to the caller's ctx deadline
	// when it is sooner, so an HTTP/protocol-handler-scoped timeout can cut
	// a guest invocation short before the default 30s.
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

// ContentHash returns the lowercase hex SHA-256 digest of the module's raw
// WASM artifact — the canonical identity used by the capability policy
// (loop B1). Empty until Load()/NewModule() completes manifest parsing.
func (m *Module) ContentHash() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.contentHash
}

// SignatureStatus returns the publication-trailer signature verification
// outcome recorded at load time (loop I1) — observable evidence for
// operators/audit tooling of whether an artifact was signed, verified, or
// loaded via the AllowUnsignedByContentHash bypass. Zero value before
// Load()/NewModule() completes.
func (m *Module) SignatureStatus() ModuleSignatureStatus {
	if m == nil {
		return ModuleSignatureStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.signatureStatus
}

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
		remote := ""
		if conn := s.Conn(); conn != nil {
			remote = conn.RemotePeer().String()
		}
		license.AuditDeliveryAborted("read_failed", reqBytes, remote, err)
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

	// Audit tap. The host decides nothing here and changes no byte: it reads
	// the frame identifier of what crosses its boundary and, for licensing
	// frames, writes one access line (module, requester FINGERPRINT, policy,
	// outcome). Grant decisions are the key server's; observing them is the
	// connector's. See internal/license/grant_audit.go.
	remotePeer := ""
	if conn := s.Conn(); conn != nil {
		remotePeer = conn.RemotePeer().String()
	}
	license.AuditDeliveryFrame("request", reqBytes, remotePeer)

	// Invoke the module's method
	resp, err := m.InvokeMethod(context.Background(), methodID, reqBytes)
	if err != nil {
		// A request this node ADMITTED and then answered with silence. Say so
		// in the delivery audit, naming the module and the requester
		// fingerprint, or the only trace is a bare WARN here and a
		// "Read aborted" in a browser nobody can correlate it with.
		license.AuditDeliveryAborted("invoke_failed", reqBytes, remotePeer, err)
		log.Warnf("Module %q protocol %s: invoke error: %v", m.manifest.PluginID, methodID, err)
		return
	}
	license.AuditDeliveryFrame("response", resp, remotePeer)

	if len(resp) == 0 {
		license.AuditDeliveryAborted("empty_response", reqBytes, remotePeer, nil)
		return
	}

	s.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := s.Write(resp); err != nil {
		// The answer existed and never landed: from the requester's side this
		// is indistinguishable from a provider that refused.
		license.AuditDeliveryAborted("write_failed", reqBytes, remotePeer, err)
		log.Debugf("Module %q protocol %s: write error: %v", m.manifest.PluginID, methodID, err)
	}
}

// --- JSON helper for debug output ---
var _ = json.Marshal
