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
	"github.com/ipfs/kubo/sdn/plugins"
	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
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
	defaultScheduledInvokeCostLimit uint   = defaultInvokeCostLimit * 20
	defaultModuleMemoryPages        uint32 = 1024
	maxModuleMemoryPages            uint32 = 16384
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
	maxMemoryPages           uint32

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
	factories map[string]BridgeCapFactory
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
	return &CapabilityRegistry{factories: make(map[string]BridgeCapFactory)}
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

// NewModule loads a module-sdk WASM binary, reads its manifest, creates a
// per-module host bridge, and provisions declared capabilities.
func NewModule(wasmBytes []byte, capReg *CapabilityRegistry, nodeCtx *NodeContext) (*Module, error) {
	return NewModuleWithMaxMemoryPages(wasmBytes, capReg, nodeCtx, 0)
}

// NewModuleWithMaxMemoryPages loads a module under a caller-supplied wasm32
// memory ceiling. The isomorphic bundle installer uses this to give each
// independently instantiated signed child the same bounded resource request
// as its signed parent; ordinary module installs retain the conservative
// default through NewModule.
func NewModuleWithMaxMemoryPages(wasmBytes []byte, capReg *CapabilityRegistry, nodeCtx *NodeContext, requestedPages uint32) (*Module, error) {
	maxMemoryPages, err := resolveModuleMemoryPages(requestedPages)
	if err != nil {
		return nil, err
	}
	m := &Module{
		wasmBytes:      append([]byte(nil), wasmBytes...),
		nodeCtx:        nodeCtx,
		capReg:         capReg,
		maxMemoryPages: maxMemoryPages,
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

func resolveModuleMemoryPages(requested uint32) (uint32, error) {
	if requested == 0 {
		return defaultModuleMemoryPages, nil
	}
	if requested > maxModuleMemoryPages {
		return 0, fmt.Errorf("module memory ceiling %d pages exceeds bounded maximum %d", requested, maxModuleMemoryPages)
	}
	return requested, nil
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

	opts := []wasmrt.Option{
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(m.maxMemoryPages),
		wasmrt.WithExecTimeout(defaultInvokeTimeout),
		wasmrt.WithCostLimit(defaultInvokeCostLimit),
		wasmrt.WithMallocName("plugin_alloc"),
		wasmrt.WithFreeName("plugin_alloc"), // dummy — use SecureDeallocate
		wasmrt.WithSecureDealloc("plugin_free"),
		wasmrt.WithHostModule("sdn", wasmrt.SharedHostFuncs("sdn", logFunc)),
		wasmrt.WithHostModule("env", wasmrt.SharedHostFuncs("env", logFunc)),
		wasmrt.WithHostModule(HostcallImportModule, bridge.BuildWasmEdgeHostFuncs()),
	}
	if moduleDeclaresWASIThreads(wasmBytes) {
		opts = append(opts, wasmrt.WithWASIThreads())
		log.Infof("Module artifact declares the standard wasi-threads contract — enabling shared-memory worker execution")
	}
	mod, err := wasmrt.NewModule(wasmBytes, opts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create WASM module: %w", err)
	}

	// Run the guest's runtime init before the manifest read or any invoke.
	// WASI reactor modules (the module-SDK default) export _initialize, which
	// runs global constructors + sets up the guest heap. Command-style builds
	// instead run their constructors via
	// __wasm_call_ctors. Match the module-SDK's own service-mode runner: prefer
	// _initialize, else __wasm_call_ctors. We deliberately do NOT fall back to
	// _start — that runs a command module's main()/serve-loop and proc_exits the
	// instance, so plugin_invoke_stream could never be driven afterwards.
	// Guarding on HasFunction avoids a "function not found" trap (which otherwise
	// leaves the guest heap uninitialized and makes plugin_alloc fault) on a
	// module that exports neither.
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
			if factory, ok := m.capReg.factories[cap]; ok {
				handler := factory(m, bridge)
				bridge.RegisterCapHandler(capPrefixFromName(cap), handler)
				log.Debugf("Module %q: provisioned capability %q", manifest.PluginID, cap)
			}
		}
		m.capReg.mu.RUnlock()
	}

	return mod, bridge, manifest, nil
}

type moduleThreadFeatures struct {
	sharedImportedMemory bool
	threadSpawnImport    bool
	threadStartExport    bool
	emscriptenHook       bool
}

func moduleDeclaresWASIThreads(wasm []byte) bool {
	features := scanModuleThreadFeatures(wasm)
	return features.sharedImportedMemory && features.threadSpawnImport &&
		features.threadStartExport && !features.emscriptenHook
}

// scanModuleThreadFeatures reads only the standard WASM import/export
// sections needed to recognize the SDK's isomorphic pthread contract. A
// malformed artifact yields no features and therefore never enables threads.
func scanModuleThreadFeatures(wasm []byte) moduleThreadFeatures {
	var features moduleThreadFeatures
	if len(wasm) < 8 || wasm[0] != 0 || wasm[1] != 'a' || wasm[2] != 's' || wasm[3] != 'm' {
		return features
	}
	for cursor := 8; cursor < len(wasm); {
		sectionID := wasm[cursor]
		cursor++
		sectionSize, width := readModuleULEB(wasm, cursor)
		if width == 0 {
			return moduleThreadFeatures{}
		}
		cursor += width
		if sectionSize > uint64(len(wasm)-cursor) {
			return moduleThreadFeatures{}
		}
		body := wasm[cursor : cursor+int(sectionSize)]
		cursor += int(sectionSize)
		switch sectionID {
		case 0x02:
			scanModuleImports(body, &features)
		case 0x07:
			scanModuleExports(body, &features)
		}
	}
	return features
}

func scanModuleImports(section []byte, features *moduleThreadFeatures) {
	cursor := 0
	count, width := readModuleULEB(section, cursor)
	if width == 0 {
		return
	}
	cursor += width
	for index := uint64(0); index < count && cursor < len(section); index++ {
		moduleName, next, ok := readModuleWasmName(section, cursor)
		if !ok {
			return
		}
		cursor = next
		fieldName, next, ok := readModuleWasmName(section, cursor)
		if !ok || next >= len(section) {
			return
		}
		cursor = next
		kind := section[cursor]
		cursor++
		switch kind {
		case 0x00: // function: type index
			_, width = readModuleULEB(section, cursor)
			if width == 0 {
				return
			}
			cursor += width
			if moduleName == "wasi" && fieldName == "thread-spawn" {
				features.threadSpawnImport = true
			}
			if moduleName == "env" && (fieldName == "__pthread_create_js" ||
				fieldName == "pthread_create" || fieldName == "_emscripten_thread_mailbox_await" ||
				fieldName == "__emscripten_init_main_thread_js") {
				features.emscriptenHook = true
			}
		case 0x01: // table: ref type + limits
			if cursor >= len(section) {
				return
			}
			cursor++
			cursor = skipModuleWasmLimits(section, cursor)
		case 0x02: // memory: limits; bit 1 means shared
			if cursor >= len(section) {
				return
			}
			flags := section[cursor]
			cursor++
			if flags&0x02 != 0 && moduleName == "env" && fieldName == "memory" {
				features.sharedImportedMemory = true
			}
			_, width = readModuleULEB(section, cursor)
			if width == 0 {
				return
			}
			cursor += width
			if flags&0x01 != 0 {
				_, width = readModuleULEB(section, cursor)
				if width == 0 {
					return
				}
				cursor += width
			}
		case 0x03: // global: value type + mutability
			if cursor+2 > len(section) {
				return
			}
			cursor += 2
		default:
			return
		}
	}
}

func scanModuleExports(section []byte, features *moduleThreadFeatures) {
	cursor := 0
	count, width := readModuleULEB(section, cursor)
	if width == 0 {
		return
	}
	cursor += width
	for index := uint64(0); index < count && cursor < len(section); index++ {
		name, next, ok := readModuleWasmName(section, cursor)
		if !ok || next >= len(section) {
			return
		}
		cursor = next + 1 // external kind
		_, width = readModuleULEB(section, cursor)
		if width == 0 {
			return
		}
		cursor += width
		if name == "wasi_thread_start" {
			features.threadStartExport = true
		}
	}
}

func skipModuleWasmLimits(data []byte, cursor int) int {
	if cursor >= len(data) {
		return len(data)
	}
	flags := data[cursor]
	cursor++
	_, width := readModuleULEB(data, cursor)
	if width == 0 {
		return len(data)
	}
	cursor += width
	if flags&0x01 != 0 {
		_, width = readModuleULEB(data, cursor)
		if width == 0 {
			return len(data)
		}
		cursor += width
	}
	return cursor
}

func readModuleWasmName(data []byte, cursor int) (string, int, bool) {
	length, width := readModuleULEB(data, cursor)
	if width == 0 {
		return "", cursor, false
	}
	cursor += width
	if length > uint64(len(data)-cursor) {
		return "", cursor, false
	}
	return string(data[cursor : cursor+int(length)]), cursor + int(length), true
}

func readModuleULEB(data []byte, cursor int) (uint64, int) {
	var value uint64
	var shift uint
	for width := 0; width < 10 && cursor+width < len(data); width++ {
		current := data[cursor+width]
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, width + 1
		}
		shift += 7
	}
	return 0, 0
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

// InvokeMethodFrames calls plugin_invoke_stream with an SDK-style multi-port
// input request, returning the "response" output port (falling back to the first
// output frame when no port is named "response").
func (m *Module) InvokeMethodFrames(ctx context.Context, methodID string, inputFrames []InvokeInputFrame) (payload []byte, err error) {
	return m.InvokeMethodFramesPort(ctx, methodID, inputFrames, "response")
}

// InvokeMethodFramesPort calls plugin_invoke_stream with an SDK-style multi-port
// input request and returns the payload of preferredOutputPort (falling back to
// the first output frame when that port is absent). This is the RESIDENT-reactor
// drive path: the module is loaded once (Load ran _initialize/__wasm_call_ctors)
// and every call reuses the live instance — no _start, no per-call process. It
// serializes per-Module (m.mu); run N Modules to fit N objects concurrently.
func (m *Module) InvokeMethodFramesPort(ctx context.Context, methodID string, inputFrames []InvokeInputFrame, preferredOutputPort string) (payload []byte, err error) {
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

	return extractPluginInvokePayload(response, preferredOutputPort)
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
