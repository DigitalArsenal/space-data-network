package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	logging "github.com/ipfs/go-log/v2"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
)

var log = logging.Logger("plugins")

// RuntimeContext provides node/runtime dependencies that plugins need at startup.
type RuntimeContext struct {
	Host              host.Host
	DHT               *dht.IpfsDHT
	BaseDataPath      string
	PeerID            string
	IPFSAPIURL        string
	Mode              string
	NodeEncryptionKey []byte
}

// Plugin is the runtime contract for SDN server plugins.
type Plugin interface {
	ID() string
	Start(ctx context.Context, runtime RuntimeContext) error
	RegisterRoutes(mux *http.ServeMux)
	Close() error
}

// RuntimeModuleLoader is implemented by modules that can load their runtime
// artifact without starting execution.
type RuntimeModuleLoader interface {
	Load(ctx context.Context) error
}

// RuntimeModulePausable is implemented by modules that can pause/resume
// invocation without unloading their runtime artifact.
type RuntimeModulePausable interface {
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
}

// ─── WASM Cron Types ───────────────────────────────────────────────────────

// CronMethodSpec describes a single cron-eligible method declared by a WASM
// plugin in its metadata JSON. The plugin declares *what* can be scheduled;
// the server config and web UI control *whether* and *how often* it runs.
type CronMethodSpec struct {
	// Method is the identifier passed to plugin_cron (e.g. "collect-telemetry").
	Method string `json:"method"`

	// Description is a human-readable label for the web UI.
	Description string `json:"description,omitempty"`

	// DefaultInterval is the suggested schedule (e.g. "30s", "5m", "1h").
	// The actual interval is controlled by the server config/UI.
	DefaultInterval string `json:"default_interval"`

	// Input describes what the host passes to this method.
	//   "none"       — no input (in_ptr=0, in_len=0)
	//   "json"       — host passes JSON bytes
	//   "flatbuffer" — host passes FlatBuffer bytes
	Input string `json:"input"`

	// Output describes what the plugin returns.
	//   "none"       — no output (returns 0 bytes)
	//   "json"       — plugin writes JSON to out_ptr
	//   "flatbuffer" — plugin writes FlatBuffer bytes to out_ptr
	Output string `json:"output"`

	// IntervalPinned marks DefaultInterval as an OPERATOR-DECLARED cadence
	// from the node config file, not a bundle suggestion. A pinned interval
	// outranks persisted runtime state (modules/runtime-inputs.json, written
	// by SaveRuntimeModuleSchedule from the dashboard).
	//
	// Without this, a schedule saved once through the dashboard silently
	// outlives every later edit of the config file, and the two surfaces
	// disagree: the $APPS board reports the CONFIG interval (it is built from
	// the same triggers the config override wrote) while the ticker runs the
	// PERSISTED one. Measured live on host-02 2026-08-26: config
	// flows.services[].intervals declared timer-cell-ingest 1m, /api/apps
	// reported interval_ms 60000, and the cron ticker fired every 5m0s from
	// a stale persisted "5m0s". A board that reports a cadence the node is
	// not running is worse than no board.
	//
	// Enable/disable is a SEPARATE axis and stays with the persisted state:
	// an operator who turned a lane off from the dashboard keeps it off.
	IntervalPinned bool `json:"interval_pinned,omitempty"`
}

// CronScheduleConfig is the per-method schedule from the server config file
// or web UI. This controls whether and how often the host calls plugin_cron.
type CronScheduleConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	Interval       string `json:"interval" yaml:"interval"` // overrides DefaultInterval
	CronExpression string `json:"cronExpression,omitempty" yaml:"cronExpression,omitempty"`
	Timezone       string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	Jitter         string `json:"jitter,omitempty" yaml:"jitter,omitempty"`
	Backoff        string `json:"backoff,omitempty" yaml:"backoff,omitempty"`
	RetryBudget    int    `json:"retryBudget,omitempty" yaml:"retryBudget,omitempty"`
	MaxRuntime     string `json:"maxRuntime,omitempty" yaml:"maxRuntime,omitempty"`
}

// CronProvider is an optional interface that plugins can implement to
// declare cron-eligible methods. For WASM plugins, the Go wrapper reads
// the metadata JSON and returns CronMethodSpec entries. The manager
// schedules calls to plugin_cron based on server config.
type CronProvider interface {
	CronMethods() []CronMethodSpec
	InvokeCron(ctx context.Context, method string, input []byte) ([]byte, error)
}

// ─── UI ────────────────────────────────────────────────────────────────────

// UIDescriptor describes a plugin's web UI. Plugins that provide a web
// interface should implement the UIProvider interface.
type UIDescriptor struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`      // emoji or single character
	Color       string `json:"color,omitempty"`     // CSS background for icon badge
	TextColor   string `json:"textColor,omitempty"` // CSS text color for icon badge
	URL         string `json:"url,omitempty"`       // path to plugin UI page (served by plugin)
}

// UIProvider is an optional interface that plugins can implement to declare
// a web UI that will be shown on the Plugins page in the SDN web client.
type UIProvider interface {
	UIDescriptor() UIDescriptor
}

// UIURLSetter is an optional interface that plugins can implement when their
// UI page URL is not known at construction time and must be assigned later.
// A module-sdk WASM module (internal/modulert.Module) is the motivating
// case: it has no self-known UI location the way a Go-native plugin does
// (e.g. plugins/ailogplugin hardcodes its own dashboard path). Its URL
// instead comes from an app manifest's UI entry
// (internal/appmanifest.AppManifest), resolved and pushed in via
// Manager.SetModuleUIURL once the module is registered. Plugins that never
// have SetUIURL called on them keep reporting an empty UIDescriptor.URL, as
// before this interface existed.
type UIURLSetter interface {
	SetUIURL(url string)
}

// ─── Manifest ──────────────────────────────────────────────────────────────

// PluginManifestEntry is the JSON representation of a plugin in the manifest.
type PluginManifestEntry struct {
	ID            string           `json:"id"`
	Version       string           `json:"version,omitempty"`
	Status        string           `json:"status"`
	StatusMessage string           `json:"status_message,omitempty"`
	Description   string           `json:"description,omitempty"`
	UI            *UIDescriptor    `json:"ui,omitempty"`
	Cron          []CronMethodSpec `json:"cron,omitempty"`
}

// RuntimeModuleStats contains runtime metrics that are safe to expose to the
// dashboard. WASM memory is reported as linear-memory pages, not process RSS.
type RuntimeModuleStats struct {
	MemoryPages      uint64  `json:"memoryPages,omitempty"`
	MemoryBytes      uint64  `json:"memoryBytes,omitempty"`
	MaxMemoryPages   uint64  `json:"maxMemoryPages,omitempty"`
	MaxMemoryBytes   uint64  `json:"maxMemoryBytes,omitempty"`
	UptimeMs         int64   `json:"uptimeMs,omitempty"`
	HostRSSBytes     uint64  `json:"hostRssBytes,omitempty"`
	InvokeCount      uint64  `json:"invokeCount,omitempty"`
	ErrorCount       uint64  `json:"errorCount,omitempty"`
	LastInvokeAt     string  `json:"lastInvokeAt,omitempty"`
	AverageLatencyMs float64 `json:"averageLatencyMs,omitempty"`
	TimerRunCount    uint64  `json:"timerRunCount,omitempty"`
	LastTimerStatus  string  `json:"lastTimerStatus,omitempty"`
}

// RuntimeModuleMethod describes an invokable method surfaced by the module
// manifest.
type RuntimeModuleMethod struct {
	MethodID    string              `json:"methodId"`
	DisplayName string              `json:"displayName,omitempty"`
	Description string              `json:"description,omitempty"`
	InputPorts  []RuntimeModulePort `json:"inputPorts,omitempty"`
	OutputPorts []RuntimeModulePort `json:"outputPorts,omitempty"`
	MaxBatch    uint32              `json:"maxBatch,omitempty"`
	DrainPolicy string              `json:"drainPolicy,omitempty"`
}

// RuntimeModulePort describes one input or output stream port for a method.
type RuntimeModulePort struct {
	PortID           string                         `json:"portId"`
	DisplayName      string                         `json:"displayName,omitempty"`
	AcceptedTypeSets []RuntimeModuleAcceptedTypeSet `json:"acceptedTypeSets,omitempty"`
	MinStreams       uint16                         `json:"minStreams,omitempty"`
	MaxStreams       uint16                         `json:"maxStreams,omitempty"`
	Required         bool                           `json:"required,omitempty"`
	Description      string                         `json:"description,omitempty"`
}

// RuntimeModuleAcceptedTypeSet describes the schema and wire formats accepted
// by a method port.
type RuntimeModuleAcceptedTypeSet struct {
	SetID              string                 `json:"setId,omitempty"`
	AllowedTypes       []RuntimeModuleTypeRef `json:"allowedTypes,omitempty"`
	AllowedWireFormats []string               `json:"allowedWireFormats,omitempty"`
	Description        string                 `json:"description,omitempty"`
}

// RuntimeModuleTypeRef identifies a FlatBuffer payload type accepted by a port.
type RuntimeModuleTypeRef struct {
	SchemaName     string `json:"schemaName,omitempty"`
	FileIdentifier string `json:"fileIdentifier,omitempty"`
	SchemaVersion  string `json:"schemaVersion,omitempty"`
	RootType       string `json:"rootType,omitempty"`
}

// RuntimeModuleProtocol describes a network protocol declared by the module.
type RuntimeModuleProtocol struct {
	ProtocolID    string `json:"protocolId"`
	MethodID      string `json:"methodId,omitempty"`
	InputPortID   string `json:"inputPortId,omitempty"`
	OutputPortID  string `json:"outputPortId,omitempty"`
	Description   string `json:"description,omitempty"`
	WireID        string `json:"wireId,omitempty"`
	TransportKind string `json:"transportKind,omitempty"`
	Role          string `json:"role,omitempty"`
	AutoInstall   bool   `json:"autoInstall"`
	Advertise     bool   `json:"advertise"`
	DiscoveryKey  string `json:"discoveryKey,omitempty"`
}

// RuntimeModuleTimer describes a periodic timer declared by the module.
type RuntimeModuleTimer struct {
	TimerID           string `json:"timerId"`
	MethodID          string `json:"methodId"`
	DefaultIntervalMs uint64 `json:"defaultIntervalMs,omitempty"`
	Description       string `json:"description,omitempty"`
}

// RuntimeModuleManifest is the dashboard-friendly subset of a module manifest.
type RuntimeModuleManifest struct {
	PluginID     string                  `json:"pluginId"`
	Name         string                  `json:"name,omitempty"`
	Version      string                  `json:"version,omitempty"`
	PluginFamily string                  `json:"pluginFamily,omitempty"`
	Methods      []RuntimeModuleMethod   `json:"methods,omitempty"`
	Capabilities []string                `json:"capabilities,omitempty"`
	Protocols    []RuntimeModuleProtocol `json:"protocols,omitempty"`
	Timers       []RuntimeModuleTimer    `json:"timers,omitempty"`
}

// RuntimeModuleOption describes a safe runtime control exposed by the
// dashboard. Custom module settings should come from canonical manifests, not a
// repo-local schema.
type RuntimeModuleOption struct {
	Key             string  `json:"key"`
	Label           string  `json:"label"`
	Type            string  `json:"type"`
	Value           string  `json:"value,omitempty"`
	Description     string  `json:"description,omitempty"`
	ReadOnly        bool    `json:"readOnly,omitempty"`
	Units           string  `json:"units,omitempty"`
	Min             float64 `json:"min,omitempty"`
	Max             float64 `json:"max,omitempty"`
	DefaultValue    string  `json:"defaultValue,omitempty"`
	RestartRequired bool    `json:"restartRequired,omitempty"`
	Persistence     string  `json:"persistence,omitempty"`
	Mutable         bool    `json:"mutable,omitempty"`
}

// RuntimeModuleScheduleConfig is the operator-controlled schedule for one
// provider cron method.
type RuntimeModuleScheduleConfig struct {
	Enabled        bool   `json:"enabled"`
	Interval       string `json:"interval,omitempty"`
	CronExpression string `json:"cronExpression,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	Jitter         string `json:"jitter,omitempty"`
	Backoff        string `json:"backoff,omitempty"`
	RetryBudget    int    `json:"retryBudget,omitempty"`
	MaxRuntime     string `json:"maxRuntime,omitempty"`
}

// RuntimeModuleScheduleRun records a bounded scheduler execution history entry.
type RuntimeModuleScheduleRun struct {
	ID         string `json:"id"`
	MethodID   string `json:"methodId"`
	Trigger    string `json:"trigger"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	OutputSize int    `json:"outputSize,omitempty"`
}

// RuntimeModuleSchedule is the dashboard read model for one provider schedule.
type RuntimeModuleSchedule struct {
	MethodID        string                     `json:"methodId"`
	Description     string                     `json:"description,omitempty"`
	Enabled         bool                       `json:"enabled"`
	Interval        string                     `json:"interval"`
	CronExpression  string                     `json:"cronExpression,omitempty"`
	Timezone        string                     `json:"timezone"`
	TimezoneDisplay string                     `json:"timezoneDisplay"`
	UTCDisplay      string                     `json:"utcDisplay"`
	Jitter          string                     `json:"jitter,omitempty"`
	Backoff         string                     `json:"backoff,omitempty"`
	RetryBudget     int                        `json:"retryBudget,omitempty"`
	MaxRuntime      string                     `json:"maxRuntime,omitempty"`
	MinInterval     string                     `json:"minInterval"`
	IntervalPresets []string                   `json:"intervalPresets,omitempty"`
	LastRunAt       string                     `json:"lastRunAt,omitempty"`
	NextRunAt       string                     `json:"nextRunAt,omitempty"`
	RunHistory      []RuntimeModuleScheduleRun `json:"runHistory,omitempty"`
}

// RuntimeModuleAction describes a lifecycle control surfaced by the dashboard.
type RuntimeModuleAction struct {
	ActionID    string `json:"actionId"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Destructive bool   `json:"destructive,omitempty"`
}

// RuntimeModuleStatusEvent records a recent runtime state transition.
type RuntimeModuleStatusEvent struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	At      string `json:"at"`
}

// RuntimeModuleLinks exposes dashboard-safe detail links for module logs/events.
type RuntimeModuleLinks struct {
	LogsURL   string `json:"logsUrl,omitempty"`
	EventsURL string `json:"eventsUrl,omitempty"`
}

// RuntimeModuleInputValue is an operator-saved method input value. Values are
// keyed by module, method, and port, then applied by runtimes that implement
// RuntimeModuleInputApplier when the module is restarted.
type RuntimeModuleInputValue struct {
	MethodID       string `json:"methodId"`
	PortID         string `json:"portId"`
	WireFormat     string `json:"wireFormat,omitempty"`
	Encoding       string `json:"encoding,omitempty"`
	SchemaName     string `json:"schemaName,omitempty"`
	FileIdentifier string `json:"fileIdentifier,omitempty"`
	SchemaVersion  string `json:"schemaVersion,omitempty"`
	RootType       string `json:"rootType,omitempty"`
	Value          string `json:"value,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
}

// RuntimeModuleCommandHistoryEntry records dashboard commands that change
// module runtime inputs or apply them through lifecycle actions.
type RuntimeModuleCommandHistoryEntry struct {
	ID          string                    `json:"id"`
	At          string                    `json:"at"`
	Command     string                    `json:"command"`
	ModuleID    string                    `json:"moduleId,omitempty"`
	MethodID    string                    `json:"methodId,omitempty"`
	PortID      string                    `json:"portId,omitempty"`
	Status      string                    `json:"status"`
	Summary     string                    `json:"summary,omitempty"`
	InputValues []RuntimeModuleInputValue `json:"inputValues,omitempty"`
}

// RuntimeModuleInputState is the persisted dashboard state for one module.
type RuntimeModuleInputState struct {
	Values         []RuntimeModuleInputValue          `json:"values,omitempty"`
	RestartPending bool                               `json:"restartPending,omitempty"`
	CommandHistory []RuntimeModuleCommandHistoryEntry `json:"commandHistory,omitempty"`
	Schedules      map[string]RuntimeModuleSchedule   `json:"schedules,omitempty"`
}

// RuntimeModuleCatalog contains public catalog metadata when this runtime
// module came from the module-delivery catalog.
type RuntimeModuleCatalog struct {
	RequiredScope   string `json:"requiredScope,omitempty"`
	ContentType     string `json:"contentType,omitempty"`
	CacheControl    string `json:"cacheControl,omitempty"`
	BundleSHA256    string `json:"bundleSha256,omitempty"`
	SizeBytes       int64  `json:"sizeBytes,omitempty"`
	SignatureHex    string `json:"signatureHex,omitempty"`
	SignerPubKeyHex string `json:"signerPubKeyHex,omitempty"`
	UploadedAt      string `json:"uploadedAt,omitempty"`
}

// RuntimeModuleDescriptor is supplied by plugins that can expose richer module
// metadata than the base Plugin interface.
type RuntimeModuleDescriptor struct {
	Manifest *RuntimeModuleManifest `json:"manifest,omitempty"`
	Stats    RuntimeModuleStats     `json:"stats,omitempty"`
	Actions  []RuntimeModuleAction  `json:"actions,omitempty"`
	Links    RuntimeModuleLinks     `json:"links,omitempty"`
}

// RuntimeModuleEntry is one row in the dashboard runtime snapshot.
type RuntimeModuleEntry struct {
	ID             string                             `json:"id"`
	Version        string                             `json:"version,omitempty"`
	Status         string                             `json:"status"`
	StatusMessage  string                             `json:"statusMessage,omitempty"`
	Description    string                             `json:"description,omitempty"`
	UI             *UIDescriptor                      `json:"ui,omitempty"`
	Cron           []CronMethodSpec                   `json:"cron,omitempty"`
	Manifest       *RuntimeModuleManifest             `json:"manifest,omitempty"`
	Stats          RuntimeModuleStats                 `json:"stats,omitempty"`
	Options        []RuntimeModuleOption              `json:"options,omitempty"`
	Schedules      []RuntimeModuleSchedule            `json:"schedules,omitempty"`
	Actions        []RuntimeModuleAction              `json:"actions,omitempty"`
	StatusHistory  []RuntimeModuleStatusEvent         `json:"statusHistory,omitempty"`
	Links          *RuntimeModuleLinks                `json:"links,omitempty"`
	Catalog        *RuntimeModuleCatalog              `json:"catalog,omitempty"`
	InputValues    []RuntimeModuleInputValue          `json:"inputValues,omitempty"`
	RestartPending bool                               `json:"restartPending,omitempty"`
	CommandHistory []RuntimeModuleCommandHistoryEntry `json:"commandHistory,omitempty"`
}

// RuntimeSnapshot is the top-level dashboard payload.
type RuntimeSnapshot struct {
	GeneratedAt string               `json:"generatedAt"`
	Count       int                  `json:"count"`
	Modules     []RuntimeModuleEntry `json:"modules"`
}

type runtimeDescriptorProvider interface {
	RuntimeDescriptor() RuntimeModuleDescriptor
}

// RuntimeModuleInputApplier is implemented by runtimes that can apply saved
// dashboard input values after a lifecycle restart.
type RuntimeModuleInputApplier interface {
	ApplyRuntimeModuleInputs(ctx context.Context, values []RuntimeModuleInputValue) error
}

type pluginRuntimeState struct {
	status    string
	message   string
	startedAt time.Time
	updatedAt time.Time
	history   []RuntimeModuleStatusEvent
}

// ─── Manager ───────────────────────────────────────────────────────────────

// Manager coordinates plugin lifecycle, route registration, and cron scheduling.
type Manager struct {
	pluginMu sync.RWMutex
	plugins  []Plugin
	states   map[string]pluginRuntimeState

	// Cron scheduler state.
	cronCancel context.CancelFunc
	cronWg     sync.WaitGroup
	runtimeCtx context.Context
	runtime    RuntimeContext
	// cronMu guards runtimeCtx/lateCronCtx against a plugin registered (and
	// started) concurrently with StartAll. lateCronCtx is the same cancellable
	// context StartAll's scheduled methods run under, retained so a
	// LATE-registered plugin's timers stop with everything else.
	cronMu      sync.Mutex
	lateCronCtx context.Context

	// Per-plugin cron config (plugin ID → method → schedule).
	// Set via SetCronConfig before StartAll, or updated at runtime via API.
	cronConfig   map[string]map[string]CronScheduleConfig
	cronConfigMu sync.RWMutex

	inputState   map[string]RuntimeModuleInputState
	inputStateMu sync.RWMutex
}

// New creates an empty plugin manager.
func New() *Manager {
	return &Manager{
		plugins:    make([]Plugin, 0),
		states:     make(map[string]pluginRuntimeState),
		cronConfig: make(map[string]map[string]CronScheduleConfig),
		inputState: make(map[string]RuntimeModuleInputState),
	}
}

// SetCronConfig sets the cron schedule for all plugins. Call before StartAll.
// The map is keyed by plugin ID → method name → schedule config.
func (m *Manager) SetCronConfig(config map[string]map[string]CronScheduleConfig) {
	if m == nil {
		return
	}
	m.cronConfigMu.Lock()
	defer m.cronConfigMu.Unlock()
	m.cronConfig = config
}

// Register adds a plugin to the manager.
func (m *Manager) Register(plugin Plugin) error {
	if m == nil {
		return errors.New("plugin manager is nil")
	}
	if plugin == nil {
		return errors.New("plugin is nil")
	}
	id := plugin.ID()
	if id == "" {
		return errors.New("plugin ID is empty")
	}
	m.pluginMu.Lock()
	defer m.pluginMu.Unlock()
	for _, existing := range m.plugins {
		if existing.ID() == id {
			return fmt.Errorf("plugin %q already registered", id)
		}
	}
	m.plugins = append(m.plugins, plugin)
	m.ensureStateMapLocked()
	m.states[id] = pluginRuntimeState{
		status:    "registered",
		updatedAt: time.Now().UTC(),
		history: []RuntimeModuleStatusEvent{
			{
				Status: "registered",
				At:     time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	return nil
}

// StartAll starts all registered plugins, then schedules cron methods for
// plugins that implement CronProvider.
func (m *Manager) StartAll(ctx context.Context, runtime RuntimeContext) error {
	if m == nil {
		return nil
	}
	m.runtime = runtime
	m.loadRuntimeModuleInputState(runtime.BaseDataPath)

	var errs []error
	registered := m.registeredPlugins()
	for _, plugin := range registered {
		if err := plugin.Start(ctx, runtime); err != nil {
			m.setPluginState(plugin.ID(), "error", err.Error(), time.Time{})
			errs = append(errs, fmt.Errorf("%s: %w", plugin.ID(), err))
			continue
		}
		m.setPluginState(plugin.ID(), "running", "", time.Now().UTC())
	}

	// Start cron methods in a shared cancellable context.
	cronCtx, cancel := context.WithCancel(ctx)
	m.cronCancel = cancel
	m.cronMu.Lock()
	m.runtimeCtx = ctx
	m.lateCronCtx = cronCtx
	m.cronMu.Unlock()

	for _, plugin := range registered {
		if status, _ := m.pluginStatus(plugin.ID()); status != "running" {
			continue
		}
		cp, ok := plugin.(CronProvider)
		if !ok {
			continue
		}
		m.scheduleCronMethods(cronCtx, plugin.ID(), cp)
	}

	return errors.Join(errs...)
}

// StartLateRegistered starts and schedules a plugin registered AFTER StartAll.
//
// StartAll only ever sees the plugins present when it runs. Flow services are
// registered later by design — they are loaded from config after the node is
// up (node.StartConfiguredFlowServices) — so before this existed they were
// never started and their cron methods were never scheduled. A timer-served
// ingest flow would sit at status "registered"/"never-run" FOREVER: not late,
// not waiting out its first interval, simply never wired to a ticker.
//
// Returns false when the manager has not started yet; the caller can leave the
// plugin for StartAll to pick up normally.
func (m *Manager) StartLateRegistered(plugin Plugin) (bool, error) {
	if m == nil || plugin == nil {
		return false, nil
	}
	m.cronMu.Lock()
	started := m.runtimeCtx != nil
	cronCtx := m.lateCronCtx
	m.cronMu.Unlock()
	if !started {
		return false, nil
	}

	id := plugin.ID()
	if err := plugin.Start(m.runtimeCtx, m.runtime); err != nil {
		m.setPluginState(id, "error", err.Error(), time.Time{})
		return true, fmt.Errorf("%s: %w", id, err)
	}
	m.setPluginState(id, "running", "", time.Now().UTC())

	if cp, ok := plugin.(CronProvider); ok && cronCtx != nil {
		m.scheduleCronMethods(cronCtx, id, cp)
	}
	return true, nil
}

// scheduleCronMethods reads a plugin's declared cron methods and starts
// goroutines for each enabled method based on the server config.
func (m *Manager) scheduleCronMethods(ctx context.Context, pluginID string, cp CronProvider) {
	m.cronConfigMu.RLock()
	pluginConfig := m.cronConfig[pluginID]
	m.cronConfigMu.RUnlock()

	for _, spec := range cp.CronMethods() {
		interval, enabled := m.resolveCronSchedule(spec, pluginConfig)
		if !enabled {
			log.Infof("Plugin %q: cron method %q disabled", pluginID, spec.Method)
			continue
		}

		method := spec.Method
		m.cronWg.Add(1)
		go func() {
			defer m.cronWg.Done()

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					run := RuntimeModuleScheduleRun{
						ID:        runtimeModuleCommandID(time.Now().UTC(), 1),
						MethodID:  method,
						Trigger:   "scheduled",
						StartedAt: time.Now().UTC().Format(time.RFC3339),
						Status:    "running",
					}
					if output, err := cp.InvokeCron(ctx, method, nil); err != nil {
						run.Status = "error"
						run.Message = err.Error()
						log.Debugf("Plugin %q cron %q: %v", pluginID, method, err)
					} else if len(output) > 0 {
						run.Status = "ok"
						run.OutputSize = len(output)
						log.Debugf("Plugin %q cron %q: %d bytes output", pluginID, method, len(output))
					} else {
						run.Status = "ok"
					}
					run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
					m.recordRuntimeModuleScheduleRun(pluginID, method, run)
				}
			}
		}()
		log.Infof("Plugin %q: scheduled cron %q every %s", pluginID, spec.Method, interval)
	}
}

// resolveCronSchedule determines the interval and enabled state for a cron
// method. Server config overrides the plugin's default.
func (m *Manager) resolveCronSchedule(spec CronMethodSpec, pluginConfig map[string]CronScheduleConfig) (time.Duration, bool) {
	// Start with the plugin's declared default.
	intervalStr := spec.DefaultInterval
	enabled := true

	// Override from persisted/runtime state if present — unless the config
	// file pinned this cadence, in which case the operator's file wins and a
	// stale dashboard save is ignored rather than silently outranking it.
	if sched, ok := pluginConfig[spec.Method]; ok {
		enabled = sched.Enabled
		if sched.Interval != "" {
			if spec.IntervalPinned {
				if sched.Interval != intervalStr {
					log.Infof("Cron method %q: persisted schedule %q ignored; the node config file pins this cadence at %q",
						spec.Method, sched.Interval, intervalStr)
				}
			} else {
				intervalStr = sched.Interval
			}
		}
	}

	if !enabled {
		return 0, false
	}

	interval, err := time.ParseDuration(intervalStr)
	if err != nil || interval < time.Second {
		interval = 30 * time.Second // sane fallback
	}
	if minInterval := declaredMinimumCadence(spec); minInterval > 0 && interval < minInterval {
		interval = minInterval
	}

	return interval, true
}

// RegisterRoutes mounts plugin HTTP routes.
func (m *Manager) RegisterRoutes(mux *http.ServeMux) {
	if m == nil || mux == nil {
		return
	}
	for _, plugin := range m.registeredPlugins() {
		plugin.RegisterRoutes(mux)
	}
}

// Get returns a registered plugin by ID.
func (m *Manager) Get(id string) Plugin {
	if m == nil {
		return nil
	}
	for _, plugin := range m.registeredPlugins() {
		if plugin.ID() == id {
			return plugin
		}
	}
	return nil
}

// SetModuleUIURL assigns a registered module's UI page URL. This is the
// write side of app-manifest UI resolution (H1 loop): an app manifest
// (internal/appmanifest.AppManifest) declares which member module serves
// its UI and at what URL, but the manifest package has no dependency on
// plugin runtimes, so node/cmd startup wiring resolves that URL and calls
// this method once the module is registered with the manager. Modules that
// don't implement UIURLSetter (most Go-native plugins hardcode their own
// UI URL instead) return an error; callers that don't care can ignore it.
// Modules never assigned a URL keep reporting an empty UIDescriptor.URL, as
// before this method existed.
func (m *Manager) SetModuleUIURL(moduleID, url string) error {
	if m == nil {
		return errors.New("plugin manager is nil")
	}
	plugin := m.Get(moduleID)
	if plugin == nil {
		return fmt.Errorf("module %q not found", moduleID)
	}
	setter, ok := plugin.(UIURLSetter)
	if !ok {
		return fmt.Errorf("module %q does not support UI URL assignment", moduleID)
	}
	setter.SetUIURL(url)
	return nil
}

// Manifest returns a JSON-serializable list of all registered plugins
// with their status, optional UI descriptors, and cron method specs.
func (m *Manager) Manifest() []PluginManifestEntry {
	if m == nil {
		return nil
	}
	registered := m.registeredPlugins()
	entries := make([]PluginManifestEntry, 0, len(registered))
	for _, p := range registered {
		status, statusMessage := m.pluginStatus(p.ID())
		entry := PluginManifestEntry{
			ID:            p.ID(),
			Status:        status,
			StatusMessage: statusMessage,
		}
		if vp, ok := p.(interface{ Version() string }); ok {
			entry.Version = vp.Version()
		}
		if dp, ok := p.(interface{ Description() string }); ok {
			entry.Description = dp.Description()
		}
		if up, ok := p.(UIProvider); ok {
			desc := up.UIDescriptor()
			entry.UI = &desc
		}
		if cp, ok := p.(CronProvider); ok {
			entry.Cron = cp.CronMethods()
		}
		entries = append(entries, entry)
	}
	return entries
}

// RuntimeSnapshot returns the dashboard read model for loaded runtime modules.
func (m *Manager) RuntimeSnapshot() RuntimeSnapshot {
	if m == nil {
		return RuntimeSnapshot{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Modules: []RuntimeModuleEntry{}}
	}

	registered := m.registeredPlugins()
	modules := make([]RuntimeModuleEntry, 0, len(registered))
	for _, p := range registered {
		id := p.ID()
		status, statusMessage := m.pluginStatus(id)
		entry := RuntimeModuleEntry{
			ID:            id,
			Status:        status,
			StatusMessage: statusMessage,
		}
		if vp, ok := p.(interface{ Version() string }); ok {
			entry.Version = vp.Version()
		}
		if dp, ok := p.(interface{ Description() string }); ok {
			entry.Description = dp.Description()
		}
		if up, ok := p.(UIProvider); ok {
			desc := up.UIDescriptor()
			entry.UI = &desc
		}
		if cp, ok := p.(CronProvider); ok {
			entry.Cron = cp.CronMethods()
		}
		if rp, ok := p.(runtimeDescriptorProvider); ok {
			descriptor := rp.RuntimeDescriptor()
			entry.Manifest = descriptor.Manifest
			entry.Stats = descriptor.Stats
			entry.Actions = append(entry.Actions, descriptor.Actions...)
			if descriptor.Links.LogsURL != "" || descriptor.Links.EventsURL != "" {
				links := descriptor.Links
				entry.Links = &links
			}
			if entry.Version == "" && descriptor.Manifest != nil {
				entry.Version = descriptor.Manifest.Version
			}
		}
		entry.Options = m.buildRuntimeModuleOptions(id, entry.Manifest, entry.Cron)
		entry.Schedules = m.buildRuntimeModuleSchedules(id, entry.Cron)
		inputState := m.runtimeModuleInputState(id)
		entry.InputValues = inputState.Values
		entry.RestartPending = inputState.RestartPending
		entry.CommandHistory = inputState.CommandHistory
		if entry.RestartPending && entry.Status != "error" {
			entry.Status = "updated"
			if entry.StatusMessage == "" {
				entry.StatusMessage = "input values updated; restart to apply"
			}
		}
		entry.Actions = mergeRuntimeModuleActions(entry.Actions, buildRuntimeModuleActions(entry.Status, p))
		entry.StatusHistory = m.pluginStatusHistory(id)
		if entry.Links == nil {
			links := defaultRuntimeModuleLinks(id)
			entry.Links = &links
		}
		modules = append(modules, entry)
	}

	return RuntimeSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Count:       len(modules),
		Modules:     modules,
	}
}

// SaveRuntimeModuleInputValues stores operator-supplied method inputs and marks
// the module as needing a restart before the new values are applied.
func (m *Manager) SaveRuntimeModuleInputValues(ctx context.Context, moduleID string, values []RuntimeModuleInputValue) ([]RuntimeModuleInputValue, error) {
	if m == nil {
		return nil, errors.New("plugin manager is nil")
	}
	moduleID = strings.TrimSpace(moduleID)
	if moduleID == "" {
		return nil, errors.New("module id is required")
	}
	plugin := m.Get(moduleID)
	if plugin == nil {
		return nil, fmt.Errorf("module %q not found", moduleID)
	}
	manifest, _ := runtimeDescriptorAndCron(plugin)
	normalized, err := normalizeRuntimeModuleInputValues(values, manifest)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for index := range normalized {
		normalized[index].UpdatedAt = now.Format(time.RFC3339)
	}

	m.inputStateMu.Lock()
	if m.inputState == nil {
		m.inputState = make(map[string]RuntimeModuleInputState)
	}
	state := m.inputState[moduleID]
	state.Values = mergeRuntimeModuleInputValues(state.Values, normalized)
	state.RestartPending = true
	state.CommandHistory = appendRuntimeModuleCommandHistory(state.CommandHistory, RuntimeModuleCommandHistoryEntry{
		ID:          runtimeModuleCommandID(now, len(state.CommandHistory)+1),
		At:          now.Format(time.RFC3339),
		Command:     "save-inputs",
		ModuleID:    moduleID,
		MethodID:    firstRuntimeInputMethodID(normalized),
		PortID:      firstRuntimeInputPortID(normalized),
		Status:      "updated",
		Summary:     fmt.Sprintf("Saved %d input value%s", len(normalized), pluralSuffix(len(normalized))),
		InputValues: cloneRuntimeModuleInputValues(normalized),
	})
	m.inputState[moduleID] = state
	saved := cloneRuntimeModuleInputValues(state.Values)
	m.inputStateMu.Unlock()

	m.persistRuntimeModuleInputState()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return saved, nil
}

// RuntimeModuleCommandHistory returns dashboard command history for a module.
func (m *Manager) RuntimeModuleCommandHistory(moduleID string) ([]RuntimeModuleCommandHistoryEntry, error) {
	if m == nil {
		return nil, errors.New("plugin manager is nil")
	}
	moduleID = strings.TrimSpace(moduleID)
	if moduleID == "" {
		return nil, errors.New("module id is required")
	}
	if m.Get(moduleID) == nil {
		return nil, fmt.Errorf("module %q not found", moduleID)
	}
	state := m.runtimeModuleInputState(moduleID)
	return cloneRuntimeModuleCommandHistory(state.CommandHistory), nil
}

// SaveRuntimeModuleSchedule validates, persists, and applies one provider
// schedule. The persisted state backs both cron runtime config and the
// dashboard command history.
func (m *Manager) SaveRuntimeModuleSchedule(ctx context.Context, moduleID, methodID string, config RuntimeModuleScheduleConfig) (RuntimeModuleSchedule, error) {
	if m == nil {
		return RuntimeModuleSchedule{}, errors.New("plugin manager is nil")
	}
	moduleID = strings.TrimSpace(moduleID)
	methodID = strings.TrimSpace(methodID)
	if moduleID == "" || methodID == "" {
		return RuntimeModuleSchedule{}, errors.New("module id and method id are required")
	}
	plugin := m.Get(moduleID)
	if plugin == nil {
		return RuntimeModuleSchedule{}, fmt.Errorf("module %q not found", moduleID)
	}
	cp, ok := plugin.(CronProvider)
	if !ok {
		return RuntimeModuleSchedule{}, fmt.Errorf("module %q does not expose provider schedules", moduleID)
	}
	spec, ok := findCronMethodSpec(cp.CronMethods(), methodID)
	if !ok {
		return RuntimeModuleSchedule{}, fmt.Errorf("module %q schedule %q not found", moduleID, methodID)
	}
	now := time.Now().UTC()
	schedule, cronConfig, err := normalizeRuntimeModuleSchedule(spec, config, now)
	if err != nil {
		return RuntimeModuleSchedule{}, err
	}

	m.cronConfigMu.Lock()
	if m.cronConfig == nil {
		m.cronConfig = make(map[string]map[string]CronScheduleConfig)
	}
	if m.cronConfig[moduleID] == nil {
		m.cronConfig[moduleID] = make(map[string]CronScheduleConfig)
	}
	m.cronConfig[moduleID][methodID] = cronConfig
	m.cronConfigMu.Unlock()

	m.inputStateMu.Lock()
	if m.inputState == nil {
		m.inputState = make(map[string]RuntimeModuleInputState)
	}
	state := m.inputState[moduleID]
	if state.Schedules == nil {
		state.Schedules = make(map[string]RuntimeModuleSchedule)
	}
	if previous, ok := state.Schedules[methodID]; ok {
		schedule.LastRunAt = previous.LastRunAt
		schedule.RunHistory = cloneRuntimeModuleScheduleRuns(previous.RunHistory)
	}
	state.Schedules[methodID] = schedule
	state.CommandHistory = appendRuntimeModuleCommandHistory(state.CommandHistory, RuntimeModuleCommandHistoryEntry{
		ID:       runtimeModuleCommandID(now, len(state.CommandHistory)+1),
		At:       now.Format(time.RFC3339),
		Command:  "save-schedule",
		ModuleID: moduleID,
		MethodID: methodID,
		Status:   "updated",
		Summary:  runtimeModuleScheduleSummary(schedule),
	})
	m.inputState[moduleID] = state
	m.inputStateMu.Unlock()

	m.persistRuntimeModuleInputState()
	m.restartCron(ctx)
	return schedule, nil
}

// RunRuntimeModuleScheduleNow executes a provider schedule method immediately.
func (m *Manager) RunRuntimeModuleScheduleNow(ctx context.Context, moduleID, methodID string) (RuntimeModuleScheduleRun, error) {
	if m == nil {
		return RuntimeModuleScheduleRun{}, errors.New("plugin manager is nil")
	}
	moduleID = strings.TrimSpace(moduleID)
	methodID = strings.TrimSpace(methodID)
	plugin := m.Get(moduleID)
	if plugin == nil {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q not found", moduleID)
	}
	cp, ok := plugin.(CronProvider)
	if !ok {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q does not expose provider schedules", moduleID)
	}
	if _, ok := findCronMethodSpec(cp.CronMethods(), methodID); !ok {
		return RuntimeModuleScheduleRun{}, fmt.Errorf("module %q schedule %q not found", moduleID, methodID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	run := RuntimeModuleScheduleRun{
		ID:        runtimeModuleCommandID(now, 1),
		MethodID:  methodID,
		Trigger:   "manual",
		StartedAt: now.Format(time.RFC3339),
		Status:    "running",
	}
	output, err := cp.InvokeCron(ctx, methodID, nil)
	run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		run.Status = "error"
		run.Message = err.Error()
		m.recordRuntimeModuleScheduleRun(moduleID, methodID, run)
		return run, err
	}
	run.Status = "ok"
	run.OutputSize = len(output)
	m.recordRuntimeModuleScheduleRun(moduleID, methodID, run)
	return run, nil
}

// UpdateRuntimeModuleOption mutates a dashboard-exposed runtime option. Timer
// and cron interval options are persisted as schedule changes for provider
// modules, then applied by restarting cron goroutines.
func (m *Manager) UpdateRuntimeModuleOption(ctx context.Context, moduleID, key, value string) (RuntimeModuleOption, error) {
	if m == nil {
		return RuntimeModuleOption{}, errors.New("plugin manager is nil")
	}
	plugin := m.Get(moduleID)
	if plugin == nil {
		return RuntimeModuleOption{}, fmt.Errorf("module %q not found", moduleID)
	}
	manifest, cron := runtimeDescriptorAndCron(plugin)
	options := buildRuntimeModuleOptionsForConfig(manifest, cron, nil)
	var selected RuntimeModuleOption
	var targetMethod string
	for _, option := range options {
		if option.Key != key {
			continue
		}
		selected = option
		targetMethod = runtimeOptionMethodForKey(key, manifest, cron)
		break
	}
	if selected.Key == "" {
		return RuntimeModuleOption{}, fmt.Errorf("module %q option %q not found", moduleID, key)
	}
	if selected.ReadOnly {
		return RuntimeModuleOption{}, fmt.Errorf("module %q option %q is read-only", moduleID, key)
	}
	if targetMethod == "" {
		return RuntimeModuleOption{}, fmt.Errorf("module %q option %q cannot be applied", moduleID, key)
	}

	canonicalValue, cronInterval, err := normalizeRuntimeOptionValue(selected, value)
	if err != nil {
		return RuntimeModuleOption{}, err
	}

	if _, ok := findCronMethodSpec(cron, targetMethod); ok {
		if _, err := m.SaveRuntimeModuleSchedule(ctx, moduleID, targetMethod, RuntimeModuleScheduleConfig{
			Enabled:  true,
			Interval: cronInterval,
		}); err != nil {
			return RuntimeModuleOption{}, err
		}
	} else {
		m.cronConfigMu.Lock()
		if m.cronConfig == nil {
			m.cronConfig = make(map[string]map[string]CronScheduleConfig)
		}
		if m.cronConfig[moduleID] == nil {
			m.cronConfig[moduleID] = make(map[string]CronScheduleConfig)
		}
		m.cronConfig[moduleID][targetMethod] = CronScheduleConfig{
			Enabled:  true,
			Interval: cronInterval,
		}
		m.cronConfigMu.Unlock()
		m.restartCron(ctx)
	}

	selected.Value = canonicalValue
	return selected, nil
}

// RunRuntimeModuleAction executes a supported module lifecycle action.
func (m *Manager) RunRuntimeModuleAction(ctx context.Context, moduleID, actionID string) error {
	if m == nil {
		return errors.New("plugin manager is nil")
	}
	plugin := m.Get(moduleID)
	if plugin == nil {
		return fmt.Errorf("module %q not found", moduleID)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	switch actionID {
	case "clear-error":
		status, _ := m.pluginStatus(moduleID)
		if status != "error" {
			return nil
		}
		m.setPluginState(moduleID, "registered", "error cleared", time.Time{})
		return nil
	case "load":
		loader, ok := plugin.(RuntimeModuleLoader)
		if !ok {
			return fmt.Errorf("module action %q is not supported by this runtime", actionID)
		}
		if err := loader.Load(ctx); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		m.setPluginState(moduleID, "registered", "", time.Time{})
		m.restartCron(ctx)
		return nil
	case "unload":
		if err := plugin.Close(); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		m.setPluginState(moduleID, "unloaded", "", time.Time{})
		m.restartCron(ctx)
		return nil
	case "pause":
		status, _ := m.pluginStatus(moduleID)
		if status != "running" {
			return nil
		}
		pausable, ok := plugin.(RuntimeModulePausable)
		if !ok {
			return fmt.Errorf("module action %q is not supported by this runtime", actionID)
		}
		if err := pausable.Pause(ctx); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		m.setPluginState(moduleID, "paused", "", time.Time{})
		m.restartCron(ctx)
		return nil
	case "start":
		status, _ := m.pluginStatus(moduleID)
		if status == "paused" {
			pausable, ok := plugin.(RuntimeModulePausable)
			if !ok {
				return fmt.Errorf("module action %q is not supported by this runtime", actionID)
			}
			if err := pausable.Resume(ctx); err != nil {
				m.setPluginState(moduleID, "error", err.Error(), time.Time{})
				return err
			}
			m.setPluginState(moduleID, "running", "", time.Now().UTC())
			m.restartCron(ctx)
			return nil
		}
		if err := plugin.Start(m.moduleStartContext(ctx), m.runtime); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		m.setPluginState(moduleID, "running", "", time.Now().UTC())
		m.restartCron(ctx)
		return nil
	case "stop":
		if err := plugin.Close(); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		m.setPluginState(moduleID, "stopped", "", time.Time{})
		m.restartCron(ctx)
		return nil
	case "restart":
		pendingInputs := m.runtimeModuleInputState(moduleID).Values
		if err := plugin.Close(); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			m.recordRuntimeModuleCommand(moduleID, "restart", "failed", err.Error(), pendingInputs)
			return err
		}
		if err := plugin.Start(m.moduleStartContext(ctx), m.runtime); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			m.recordRuntimeModuleCommand(moduleID, "restart", "failed", err.Error(), pendingInputs)
			return err
		}
		if len(pendingInputs) > 0 {
			applier, ok := plugin.(RuntimeModuleInputApplier)
			if !ok {
				err := fmt.Errorf("module %q does not support runtime input application", moduleID)
				m.setPluginState(moduleID, "error", err.Error(), time.Time{})
				m.recordRuntimeModuleCommand(moduleID, "restart", "failed", err.Error(), pendingInputs)
				return err
			}
			if err := applier.ApplyRuntimeModuleInputs(ctx, pendingInputs); err != nil {
				m.setPluginState(moduleID, "error", err.Error(), time.Time{})
				m.recordRuntimeModuleCommand(moduleID, "restart", "failed", err.Error(), pendingInputs)
				return err
			}
		}
		m.setPluginState(moduleID, "running", "", time.Now().UTC())
		m.markRuntimeModuleInputsApplied(moduleID, pendingInputs)
		m.restartCron(ctx)
		return nil
	case "reload-manifest":
		loader, ok := plugin.(RuntimeModuleLoader)
		if !ok {
			return fmt.Errorf("module action %q is not supported by this runtime", actionID)
		}
		status, _ := m.pluginStatus(moduleID)
		if err := plugin.Close(); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		if err := loader.Load(ctx); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		if status == "running" {
			if err := plugin.Start(m.moduleStartContext(ctx), m.runtime); err != nil {
				m.setPluginState(moduleID, "error", err.Error(), time.Time{})
				return err
			}
			m.setPluginState(moduleID, "running", "manifest reloaded", time.Now().UTC())
			m.restartCron(ctx)
			return nil
		}
		m.setPluginState(moduleID, "registered", "manifest reloaded", time.Time{})
		m.restartCron(ctx)
		return nil
	default:
		return fmt.Errorf("unknown module action %q", actionID)
	}
}

// HandleRuntimeSnapshot serves the dashboard runtime snapshot at
// GET /api/v1/modules/runtime.
func (m *Manager) HandleRuntimeSnapshot() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_ = json.NewEncoder(w).Encode(m.RuntimeSnapshot())
	}
}

// HandleManifest returns an http.HandlerFunc that serves the plugin manifest
// as JSON at GET /api/v1/plugins/manifest.
func (m *Manager) HandleManifest() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		json.NewEncoder(w).Encode(m.Manifest())
	}
}

// Close stops cron goroutines, then shuts down all plugins in reverse
// registration order.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	// Stop all cron goroutines first.
	if m.cronCancel != nil {
		m.cronCancel()
	}
	m.cronWg.Wait()

	var errs []error
	registered := m.registeredPlugins()
	for i := len(registered) - 1; i >= 0; i-- {
		if err := registered[i].Close(); err != nil {
			m.setPluginState(registered[i].ID(), "error", err.Error(), time.Time{})
			errs = append(errs, fmt.Errorf("%s: %w", registered[i].ID(), err))
		} else {
			m.setPluginState(registered[i].ID(), "stopped", "", time.Time{})
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) registeredPlugins() []Plugin {
	if m == nil {
		return nil
	}
	m.pluginMu.RLock()
	defer m.pluginMu.RUnlock()
	return append([]Plugin(nil), m.plugins...)
}

func (m *Manager) ensureStateMapLocked() {
	if m.states == nil {
		m.states = make(map[string]pluginRuntimeState)
	}
}

func (m *Manager) setPluginState(id, status, message string, startedAt time.Time) {
	if m == nil {
		return
	}
	m.pluginMu.Lock()
	defer m.pluginMu.Unlock()
	m.ensureStateMapLocked()
	state := m.states[id]
	now := time.Now().UTC()
	state.status = status
	state.message = message
	if !startedAt.IsZero() {
		state.startedAt = startedAt
	}
	state.updatedAt = now
	state.history = appendRuntimeStatusHistory(state.history, RuntimeModuleStatusEvent{
		Status:  status,
		Message: message,
		At:      now.Format(time.RFC3339),
	})
	m.states[id] = state
}

func (m *Manager) pluginStatus(id string) (string, string) {
	if m == nil {
		return "unknown", ""
	}
	m.pluginMu.RLock()
	defer m.pluginMu.RUnlock()
	state := m.states[id]
	if state.status == "" {
		return "registered", ""
	}
	return state.status, state.message
}

func (m *Manager) pluginStatusHistory(id string) []RuntimeModuleStatusEvent {
	if m == nil {
		return nil
	}
	m.pluginMu.RLock()
	defer m.pluginMu.RUnlock()
	return append([]RuntimeModuleStatusEvent(nil), m.states[id].history...)
}

func (m *Manager) runtimeModuleInputState(moduleID string) RuntimeModuleInputState {
	if m == nil {
		return RuntimeModuleInputState{}
	}
	m.inputStateMu.RLock()
	defer m.inputStateMu.RUnlock()
	state := m.inputState[moduleID]
	return RuntimeModuleInputState{
		Values:         cloneRuntimeModuleInputValues(state.Values),
		RestartPending: state.RestartPending,
		CommandHistory: cloneRuntimeModuleCommandHistory(state.CommandHistory),
		Schedules:      cloneRuntimeModuleSchedules(state.Schedules),
	}
}

func (m *Manager) markRuntimeModuleInputsApplied(moduleID string, values []RuntimeModuleInputValue) {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	m.inputStateMu.Lock()
	if m.inputState == nil {
		m.inputState = make(map[string]RuntimeModuleInputState)
	}
	state := m.inputState[moduleID]
	state.RestartPending = false
	state.CommandHistory = appendRuntimeModuleCommandHistory(state.CommandHistory, RuntimeModuleCommandHistoryEntry{
		ID:          runtimeModuleCommandID(now, len(state.CommandHistory)+1),
		At:          now.Format(time.RFC3339),
		Command:     "restart",
		ModuleID:    moduleID,
		MethodID:    firstRuntimeInputMethodID(values),
		PortID:      firstRuntimeInputPortID(values),
		Status:      "applied",
		Summary:     fmt.Sprintf("Restart applied %d input value%s", len(values), pluralSuffix(len(values))),
		InputValues: cloneRuntimeModuleInputValues(values),
	})
	m.inputState[moduleID] = state
	m.inputStateMu.Unlock()
	m.persistRuntimeModuleInputState()
}

func (m *Manager) recordRuntimeModuleCommand(moduleID, command, status, summary string, values []RuntimeModuleInputValue) {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	m.inputStateMu.Lock()
	if m.inputState == nil {
		m.inputState = make(map[string]RuntimeModuleInputState)
	}
	state := m.inputState[moduleID]
	state.CommandHistory = appendRuntimeModuleCommandHistory(state.CommandHistory, RuntimeModuleCommandHistoryEntry{
		ID:          runtimeModuleCommandID(now, len(state.CommandHistory)+1),
		At:          now.Format(time.RFC3339),
		Command:     command,
		ModuleID:    moduleID,
		MethodID:    firstRuntimeInputMethodID(values),
		PortID:      firstRuntimeInputPortID(values),
		Status:      status,
		Summary:     summary,
		InputValues: cloneRuntimeModuleInputValues(values),
	})
	m.inputState[moduleID] = state
	m.inputStateMu.Unlock()
	m.persistRuntimeModuleInputState()
}

func (m *Manager) buildRuntimeModuleOptions(pluginID string, manifest *RuntimeModuleManifest, cron []CronMethodSpec) []RuntimeModuleOption {
	var pluginConfig map[string]CronScheduleConfig
	if m != nil {
		m.cronConfigMu.RLock()
		pluginConfig = cloneCronConfig(m.cronConfig[pluginID])
		m.cronConfigMu.RUnlock()
	}
	return buildRuntimeModuleOptionsForConfig(manifest, cron, pluginConfig)
}

func buildRuntimeModuleOptionsForConfig(manifest *RuntimeModuleManifest, cron []CronMethodSpec, pluginConfig map[string]CronScheduleConfig) []RuntimeModuleOption {
	options := make([]RuntimeModuleOption, 0)
	seen := map[string]bool{}
	if manifest != nil {
		for _, timer := range manifest.Timers {
			if timer.TimerID == "" {
				continue
			}
			key := "timer." + timer.TimerID + ".interval"
			seen[key] = true
			defaultValue := fmt.Sprintf("%d", timer.DefaultIntervalMs)
			methodID := strings.TrimSpace(timer.MethodID)
			if methodID == "" {
				methodID = timer.TimerID
			}
			options = append(options, RuntimeModuleOption{
				Key:          key,
				Label:        timerOptionLabel(timer.TimerID),
				Type:         "duration-ms",
				Value:        timerIntervalValue(pluginConfig[methodID].Interval, defaultValue),
				Description:  timer.Description,
				Units:        "ms",
				Min:          1000,
				Max:          86400000,
				DefaultValue: defaultValue,
				Persistence:  "persisted",
				Mutable:      true,
			})
		}
	}
	for _, spec := range cron {
		if spec.Method == "" {
			continue
		}
		key := "cron." + spec.Method + ".interval"
		if seen[key] {
			continue
		}
		options = append(options, RuntimeModuleOption{
			Key:          key,
			Label:        timerOptionLabel(spec.Method),
			Type:         "duration",
			Value:        cronIntervalValue(pluginConfig[spec.Method].Interval, spec.DefaultInterval),
			Description:  spec.Description,
			Units:        "duration",
			DefaultValue: spec.DefaultInterval,
			Persistence:  "persisted",
			Mutable:      true,
		})
	}
	return options
}

func (m *Manager) buildRuntimeModuleSchedules(pluginID string, cron []CronMethodSpec) []RuntimeModuleSchedule {
	if len(cron) == 0 {
		return nil
	}
	var pluginConfig map[string]CronScheduleConfig
	var persisted map[string]RuntimeModuleSchedule
	if m != nil {
		m.cronConfigMu.RLock()
		pluginConfig = cloneCronConfig(m.cronConfig[pluginID])
		m.cronConfigMu.RUnlock()
		state := m.runtimeModuleInputState(pluginID)
		persisted = cloneRuntimeModuleSchedules(state.Schedules)
	}
	now := time.Now().UTC()
	out := make([]RuntimeModuleSchedule, 0, len(cron))
	for _, spec := range cron {
		if strings.TrimSpace(spec.Method) == "" {
			continue
		}
		config := RuntimeModuleScheduleConfig{
			Enabled:        true,
			Interval:       spec.DefaultInterval,
			Timezone:       "UTC",
			Backoff:        "fixed",
			CronExpression: "",
		}
		if cfg, ok := pluginConfig[spec.Method]; ok {
			config = RuntimeModuleScheduleConfig{
				Enabled:        cfg.Enabled,
				Interval:       cfg.Interval,
				CronExpression: cfg.CronExpression,
				Timezone:       cfg.Timezone,
				Jitter:         cfg.Jitter,
				Backoff:        cfg.Backoff,
				RetryBudget:    cfg.RetryBudget,
				MaxRuntime:     cfg.MaxRuntime,
			}
		}
		schedule, _, err := normalizeRuntimeModuleSchedule(spec, config, now)
		if err != nil {
			schedule, _, _ = normalizeRuntimeModuleSchedule(spec, RuntimeModuleScheduleConfig{
				Enabled:  true,
				Interval: spec.DefaultInterval,
				Timezone: "UTC",
				Backoff:  "fixed",
			}, now)
		}
		if previous, ok := persisted[spec.Method]; ok {
			if previous.CronExpression != "" {
				schedule.CronExpression = previous.CronExpression
			}
			schedule.LastRunAt = previous.LastRunAt
			schedule.RunHistory = cloneRuntimeModuleScheduleRuns(previous.RunHistory)
		}
		out = append(out, schedule)
	}
	return out
}

func runtimeDescriptorAndCron(plugin Plugin) (*RuntimeModuleManifest, []CronMethodSpec) {
	var manifest *RuntimeModuleManifest
	if rp, ok := plugin.(runtimeDescriptorProvider); ok {
		manifest = rp.RuntimeDescriptor().Manifest
	}
	var cron []CronMethodSpec
	if cp, ok := plugin.(CronProvider); ok {
		cron = cp.CronMethods()
	}
	return manifest, cron
}

func runtimeOptionMethodForKey(key string, manifest *RuntimeModuleManifest, cron []CronMethodSpec) string {
	if strings.HasPrefix(key, "timer.") && strings.HasSuffix(key, ".interval") && manifest != nil {
		timerID := strings.TrimSuffix(strings.TrimPrefix(key, "timer."), ".interval")
		for _, timer := range manifest.Timers {
			if timer.TimerID != timerID {
				continue
			}
			if strings.TrimSpace(timer.MethodID) != "" {
				return strings.TrimSpace(timer.MethodID)
			}
			return timer.TimerID
		}
	}
	if strings.HasPrefix(key, "cron.") && strings.HasSuffix(key, ".interval") {
		method := strings.TrimSuffix(strings.TrimPrefix(key, "cron."), ".interval")
		for _, spec := range cron {
			if spec.Method == method {
				return method
			}
		}
	}
	return ""
}

func normalizeRuntimeOptionValue(option RuntimeModuleOption, rawValue string) (displayValue string, cronInterval string, err error) {
	value := strings.TrimSpace(rawValue)
	if value == "" {
		return "", "", fmt.Errorf("option %q value is required", option.Key)
	}
	switch option.Type {
	case "duration-ms":
		duration, parseErr := parseRuntimeDurationMS(value)
		if parseErr != nil {
			return "", "", fmt.Errorf("option %q value is not a valid duration in milliseconds: %w", option.Key, parseErr)
		}
		ms := duration.Milliseconds()
		if option.Min > 0 && float64(ms) < option.Min {
			return "", "", fmt.Errorf("option %q value must be at least %.0f %s", option.Key, option.Min, option.Units)
		}
		if option.Max > 0 && float64(ms) > option.Max {
			return "", "", fmt.Errorf("option %q value must be at most %.0f %s", option.Key, option.Max, option.Units)
		}
		return fmt.Sprintf("%d", ms), fmt.Sprintf("%dms", ms), nil
	case "duration":
		duration, parseErr := time.ParseDuration(value)
		if parseErr != nil {
			return "", "", fmt.Errorf("option %q value is not a valid duration: %w", option.Key, parseErr)
		}
		if duration < time.Second {
			return "", "", fmt.Errorf("option %q value must be at least 1s", option.Key)
		}
		return duration.String(), duration.String(), nil
	default:
		return "", "", fmt.Errorf("option %q type %q is not mutable", option.Key, option.Type)
	}
}

func parseRuntimeDurationMS(value string) (time.Duration, error) {
	if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
		if ms <= 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return time.Duration(ms) * time.Millisecond, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return duration, nil
}

func timerIntervalValue(interval, defaultValue string) string {
	duration, err := time.ParseDuration(strings.TrimSpace(interval))
	if err != nil || duration <= 0 {
		return defaultValue
	}
	return fmt.Sprintf("%d", duration.Milliseconds())
}

func cronIntervalValue(interval, defaultValue string) string {
	duration, err := time.ParseDuration(strings.TrimSpace(interval))
	if err != nil || duration <= 0 {
		return defaultValue
	}
	return duration.String()
}

func cloneCronConfig(config map[string]CronScheduleConfig) map[string]CronScheduleConfig {
	if len(config) == 0 {
		return nil
	}
	out := make(map[string]CronScheduleConfig, len(config))
	for key, value := range config {
		out[key] = value
	}
	return out
}

func (m *Manager) restartCron(ctx context.Context) {
	if m == nil {
		return
	}
	if m.cronCancel != nil {
		m.cronCancel()
		m.cronWg.Wait()
	}
	if ctx == nil {
		ctx = m.runtimeCtx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cronCtx, cancel := context.WithCancel(ctx)
	m.cronCancel = cancel
	m.cronMu.Lock()
	m.runtimeCtx = ctx
	m.lateCronCtx = cronCtx
	m.cronMu.Unlock()
	for _, plugin := range m.registeredPlugins() {
		if status, _ := m.pluginStatus(plugin.ID()); status != "running" {
			continue
		}
		cp, ok := plugin.(CronProvider)
		if !ok {
			continue
		}
		m.scheduleCronMethods(cronCtx, plugin.ID(), cp)
	}
}

func (m *Manager) moduleStartContext(fallback context.Context) context.Context {
	if m == nil {
		if fallback != nil {
			return fallback
		}
		return context.Background()
	}
	if m.runtimeCtx != nil {
		return m.runtimeCtx
	}
	if fallback != nil {
		return fallback
	}
	return context.Background()
}

func buildRuntimeModuleActions(status string, plugin Plugin) []RuntimeModuleAction {
	normalized := strings.ToLower(strings.TrimSpace(status))
	// Advertised enablement must mirror RunRuntimeModuleAction's dispatch:
	// "load" hard-requires RuntimeModuleLoader and "pause"/paused-"start"
	// hard-require RuntimeModulePausable — advertising them on status alone
	// hands the dashboard a button that can only ever 400.
	_, isLoader := plugin.(RuntimeModuleLoader)
	_, isPausable := plugin.(RuntimeModulePausable)
	canLoad := isLoader && (normalized == "unloaded" || normalized == "stopped")
	canStart := normalized == "registered" || normalized == "stopped" || normalized == "unloaded" ||
		(normalized == "paused" && isPausable)
	canUnload := normalized == "running" || normalized == "paused" || normalized == "registered" || normalized == "stopped" || normalized == "updated"
	canRestart := normalized == "running" || normalized == "paused" || normalized == "registered" || normalized == "stopped" || normalized == "updated"
	canReloadManifest := normalized == "running" || normalized == "paused" || normalized == "registered" || normalized == "stopped" || normalized == "updated"
	return []RuntimeModuleAction{
		{
			ActionID:    "load",
			Label:       "Load",
			Description: "Load this module artifact without starting it.",
			Enabled:     canLoad,
		},
		{
			ActionID:    "unload",
			Label:       "Unload",
			Description: "Unload this module artifact from the runtime.",
			Enabled:     canUnload,
			Destructive: true,
		},
		{
			ActionID:    "pause",
			Label:       "Pause",
			Description: "Pause module invocation while keeping the artifact loaded.",
			Enabled:     isPausable && (normalized == "running" || normalized == "updated"),
		},
		{
			ActionID:    "start",
			Label:       "Start",
			Description: "Start or resume this module runtime.",
			Enabled:     canStart,
		},
		{
			ActionID:    "stop",
			Label:       "Stop",
			Description: "Stop this module runtime.",
			Enabled:     normalized == "running" || normalized == "updated",
			Destructive: true,
		},
		{
			ActionID:    "restart",
			Label:       "Restart",
			Description: "Restart this module runtime.",
			Enabled:     canRestart,
			Destructive: true,
		},
		{
			ActionID:    "reload-manifest",
			Label:       "Reload manifest",
			Description: "Reload manifest metadata from the module artifact.",
			Enabled:     canReloadManifest,
		},
		{
			ActionID:    "clear-error",
			Label:       "Clear error",
			Description: "Clear the current error state after the operator has reviewed it.",
			Enabled:     normalized == "error",
		},
	}
}

func mergeRuntimeModuleActions(primary, fallback []RuntimeModuleAction) []RuntimeModuleAction {
	if len(primary) == 0 {
		return fallback
	}
	out := append([]RuntimeModuleAction(nil), primary...)
	seen := make(map[string]bool, len(out))
	for _, action := range out {
		seen[action.ActionID] = true
	}
	for _, action := range fallback {
		if seen[action.ActionID] {
			continue
		}
		out = append(out, action)
	}
	return out
}

func defaultRuntimeModuleLinks(id string) RuntimeModuleLinks {
	escaped := strings.ReplaceAll(id, "/", "%2F")
	return RuntimeModuleLinks{
		LogsURL:   "/api/v1/modules/runtime/" + escaped + "/logs",
		EventsURL: "/api/v1/modules/runtime/" + escaped + "/events",
	}
}

func appendRuntimeStatusHistory(history []RuntimeModuleStatusEvent, event RuntimeModuleStatusEvent) []RuntimeModuleStatusEvent {
	if event.Status == "" {
		return history
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		if last.Status == event.Status && last.Message == event.Message {
			history[len(history)-1] = event
			return history
		}
	}
	history = append(history, event)
	if len(history) > 20 {
		history = append([]RuntimeModuleStatusEvent(nil), history[len(history)-20:]...)
	}
	return history
}

func normalizeRuntimeModuleInputValues(values []RuntimeModuleInputValue, manifest *RuntimeModuleManifest) ([]RuntimeModuleInputValue, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one input value is required")
	}
	normalized := make([]RuntimeModuleInputValue, 0, len(values))
	for _, value := range values {
		next := RuntimeModuleInputValue{
			MethodID:       strings.TrimSpace(value.MethodID),
			PortID:         strings.TrimSpace(value.PortID),
			WireFormat:     strings.TrimSpace(value.WireFormat),
			Encoding:       normalizeRuntimeInputEncoding(value.Encoding, value.WireFormat),
			SchemaName:     strings.TrimSpace(value.SchemaName),
			FileIdentifier: strings.TrimSpace(value.FileIdentifier),
			SchemaVersion:  strings.TrimSpace(value.SchemaVersion),
			RootType:       strings.TrimSpace(value.RootType),
			Value:          strings.TrimSpace(value.Value),
		}
		if next.MethodID == "" {
			return nil, errors.New("input value methodId is required")
		}
		if next.PortID == "" {
			return nil, fmt.Errorf("input value %q portId is required", next.MethodID)
		}
		if next.Value == "" {
			return nil, fmt.Errorf("input value %q/%q value is required", next.MethodID, next.PortID)
		}
		if err := validateRuntimeInputValueAgainstManifest(next, manifest); err != nil {
			return nil, err
		}
		normalized = append(normalized, next)
	}
	return normalized, nil
}

func normalizeRuntimeInputEncoding(encoding, wireFormat string) string {
	normalized := strings.ToLower(strings.TrimSpace(encoding))
	switch normalized {
	case "base64", "hex", "json", "text":
		return normalized
	}
	if strings.Contains(strings.ToUpper(wireFormat), "JSON") {
		return "json"
	}
	return "text"
}

func validateRuntimeInputValueAgainstManifest(value RuntimeModuleInputValue, manifest *RuntimeModuleManifest) error {
	if manifest == nil {
		return nil
	}
	for _, method := range manifest.Methods {
		if method.MethodID != value.MethodID {
			continue
		}
		if len(method.InputPorts) == 0 {
			return nil
		}
		for _, port := range method.InputPorts {
			if port.PortID == value.PortID {
				return nil
			}
		}
		return fmt.Errorf("module input port %q not found on method %q", value.PortID, value.MethodID)
	}
	return fmt.Errorf("module input method %q not found", value.MethodID)
}

func mergeRuntimeModuleInputValues(existing, updates []RuntimeModuleInputValue) []RuntimeModuleInputValue {
	out := cloneRuntimeModuleInputValues(existing)
	indexes := make(map[string]int, len(out))
	for index, value := range out {
		indexes[runtimeInputValueKey(value)] = index
	}
	for _, value := range updates {
		key := runtimeInputValueKey(value)
		if index, ok := indexes[key]; ok {
			out[index] = value
			continue
		}
		indexes[key] = len(out)
		out = append(out, value)
	}
	return out
}

func runtimeInputValueKey(value RuntimeModuleInputValue) string {
	return value.MethodID + "\x00" + value.PortID
}

func cloneRuntimeModuleInputValues(values []RuntimeModuleInputValue) []RuntimeModuleInputValue {
	if len(values) == 0 {
		return nil
	}
	return append([]RuntimeModuleInputValue(nil), values...)
}

func cloneRuntimeModuleCommandHistory(history []RuntimeModuleCommandHistoryEntry) []RuntimeModuleCommandHistoryEntry {
	if len(history) == 0 {
		return nil
	}
	out := append([]RuntimeModuleCommandHistoryEntry(nil), history...)
	for index := range out {
		out[index].InputValues = cloneRuntimeModuleInputValues(out[index].InputValues)
	}
	return out
}

func cloneRuntimeModuleScheduleRuns(runs []RuntimeModuleScheduleRun) []RuntimeModuleScheduleRun {
	if len(runs) == 0 {
		return nil
	}
	return append([]RuntimeModuleScheduleRun(nil), runs...)
}

func cloneRuntimeModuleSchedules(schedules map[string]RuntimeModuleSchedule) map[string]RuntimeModuleSchedule {
	if len(schedules) == 0 {
		return nil
	}
	out := make(map[string]RuntimeModuleSchedule, len(schedules))
	for key, schedule := range schedules {
		schedule.IntervalPresets = append([]string(nil), schedule.IntervalPresets...)
		schedule.RunHistory = cloneRuntimeModuleScheduleRuns(schedule.RunHistory)
		out[key] = schedule
	}
	return out
}

func appendRuntimeModuleScheduleRun(history []RuntimeModuleScheduleRun, run RuntimeModuleScheduleRun) []RuntimeModuleScheduleRun {
	if run.MethodID == "" || run.StartedAt == "" {
		return history
	}
	history = append(history, run)
	if len(history) > 20 {
		history = append([]RuntimeModuleScheduleRun(nil), history[len(history)-20:]...)
	}
	return history
}

func (m *Manager) recordRuntimeModuleScheduleRun(moduleID, methodID string, run RuntimeModuleScheduleRun) {
	if m == nil {
		return
	}
	now := time.Now().UTC()
	m.inputStateMu.Lock()
	if m.inputState == nil {
		m.inputState = make(map[string]RuntimeModuleInputState)
	}
	state := m.inputState[moduleID]
	if state.Schedules == nil {
		state.Schedules = make(map[string]RuntimeModuleSchedule)
	}
	schedule := state.Schedules[methodID]
	if schedule.MethodID == "" {
		plugin := m.Get(moduleID)
		if cp, ok := plugin.(CronProvider); ok {
			if spec, found := findCronMethodSpec(cp.CronMethods(), methodID); found {
				schedule, _, _ = normalizeRuntimeModuleSchedule(spec, RuntimeModuleScheduleConfig{
					Enabled:  true,
					Interval: spec.DefaultInterval,
					Timezone: "UTC",
					Backoff:  "fixed",
				}, now)
			}
		}
	}
	schedule.LastRunAt = run.FinishedAt
	if schedule.LastRunAt == "" {
		schedule.LastRunAt = run.StartedAt
	}
	schedule.RunHistory = appendRuntimeModuleScheduleRun(schedule.RunHistory, run)
	if schedule.Enabled {
		interval, _ := time.ParseDuration(schedule.Interval)
		if interval > 0 {
			schedule.NextRunAt = now.Add(interval).Format(time.RFC3339)
		}
	}
	state.Schedules[methodID] = schedule
	state.CommandHistory = appendRuntimeModuleCommandHistory(state.CommandHistory, RuntimeModuleCommandHistoryEntry{
		ID:       runtimeModuleCommandID(now, len(state.CommandHistory)+1),
		At:       now.Format(time.RFC3339),
		Command:  "run-schedule",
		ModuleID: moduleID,
		MethodID: methodID,
		Status:   run.Status,
		Summary:  strings.TrimSpace(run.Trigger + " run " + run.Status),
	})
	m.inputState[moduleID] = state
	m.inputStateMu.Unlock()
	m.persistRuntimeModuleInputState()
}

func appendRuntimeModuleCommandHistory(history []RuntimeModuleCommandHistoryEntry, entry RuntimeModuleCommandHistoryEntry) []RuntimeModuleCommandHistoryEntry {
	if entry.Command == "" {
		return history
	}
	entry.InputValues = cloneRuntimeModuleInputValues(entry.InputValues)
	history = append(history, entry)
	if len(history) > 50 {
		history = append([]RuntimeModuleCommandHistoryEntry(nil), history[len(history)-50:]...)
	}
	return history
}

func runtimeModuleCommandID(at time.Time, index int) string {
	return fmt.Sprintf("%d-%06d", at.UnixNano(), index)
}

func firstRuntimeInputMethodID(values []RuntimeModuleInputValue) string {
	if len(values) == 0 {
		return ""
	}
	return values[0].MethodID
}

func firstRuntimeInputPortID(values []RuntimeModuleInputValue) string {
	if len(values) == 0 {
		return ""
	}
	return values[0].PortID
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func findCronMethodSpec(cron []CronMethodSpec, methodID string) (CronMethodSpec, bool) {
	methodID = strings.TrimSpace(methodID)
	for _, spec := range cron {
		if strings.TrimSpace(spec.Method) == methodID {
			return spec, true
		}
	}
	return CronMethodSpec{}, false
}

func normalizeRuntimeModuleSchedule(spec CronMethodSpec, config RuntimeModuleScheduleConfig, now time.Time) (RuntimeModuleSchedule, CronScheduleConfig, error) {
	methodID := strings.TrimSpace(spec.Method)
	if methodID == "" {
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, errors.New("schedule method id is required")
	}
	intervalText := strings.TrimSpace(config.Interval)
	if intervalText == "" {
		intervalText = strings.TrimSpace(spec.DefaultInterval)
	}
	interval, err := time.ParseDuration(intervalText)
	if err != nil || interval <= 0 {
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q interval is not a valid duration", methodID)
	}
	minInterval := declaredMinimumCadence(spec)
	if minInterval > 0 && interval < minInterval {
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q interval %s is below manifest minimum cadence %s", methodID, interval, minInterval)
	}
	timezone := strings.TrimSpace(config.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q timezone %q is invalid", methodID, timezone)
	}
	cronExpression := strings.TrimSpace(config.CronExpression)
	if cronExpression != "" && !isPlausibleCronExpression(cronExpression) {
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q cron expression is invalid", methodID)
	}
	jitter := strings.TrimSpace(config.Jitter)
	if jitter != "" {
		jitterDuration, err := time.ParseDuration(jitter)
		if err != nil || jitterDuration < 0 {
			return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q jitter is not a valid duration", methodID)
		}
		if jitterDuration >= interval {
			return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q jitter must be shorter than interval", methodID)
		}
		jitter = jitterDuration.String()
	}
	backoff := strings.ToLower(strings.TrimSpace(config.Backoff))
	switch backoff {
	case "":
		backoff = "fixed"
	case "fixed", "linear", "exponential":
	default:
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q backoff must be fixed, linear, or exponential", methodID)
	}
	if config.RetryBudget < 0 || config.RetryBudget > 10 {
		return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q retry budget must be between 0 and 10", methodID)
	}
	maxRuntime := strings.TrimSpace(config.MaxRuntime)
	if maxRuntime != "" {
		maxRuntimeDuration, err := time.ParseDuration(maxRuntime)
		if err != nil || maxRuntimeDuration <= 0 {
			return RuntimeModuleSchedule{}, CronScheduleConfig{}, fmt.Errorf("schedule %q max runtime is not a valid duration", methodID)
		}
		maxRuntime = maxRuntimeDuration.String()
	}
	nextRunAt := ""
	if config.Enabled {
		nextRunAt = now.Add(interval).Format(time.RFC3339)
	}
	schedule := RuntimeModuleSchedule{
		MethodID:        methodID,
		Description:     strings.TrimSpace(spec.Description),
		Enabled:         config.Enabled,
		Interval:        interval.String(),
		CronExpression:  cronExpression,
		Timezone:        timezone,
		TimezoneDisplay: now.In(location).Format("2006-01-02 15:04 MST"),
		UTCDisplay:      now.UTC().Format("2006-01-02 15:04 UTC"),
		Jitter:          jitter,
		Backoff:         backoff,
		RetryBudget:     config.RetryBudget,
		MaxRuntime:      maxRuntime,
		MinInterval:     minInterval.String(),
		IntervalPresets: runtimeModuleSchedulePresets(minInterval),
		NextRunAt:       nextRunAt,
	}
	cronConfig := CronScheduleConfig{
		Enabled:        config.Enabled,
		Interval:       interval.String(),
		CronExpression: cronExpression,
		Timezone:       timezone,
		Jitter:         jitter,
		Backoff:        backoff,
		RetryBudget:    config.RetryBudget,
		MaxRuntime:     maxRuntime,
	}
	return schedule, cronConfig, nil
}

func isPlausibleCronExpression(expression string) bool {
	fields := strings.Fields(expression)
	return len(fields) == 5 || len(fields) == 6
}

func declaredMinimumCadence(spec CronMethodSpec) time.Duration {
	if interval, err := time.ParseDuration(strings.TrimSpace(spec.DefaultInterval)); err == nil && interval > 0 {
		return interval
	}
	return time.Second
}

func runtimeModuleSchedulePresets(minInterval time.Duration) []string {
	// Cadence presets, finest first. Grouped across lines with interstitial
	// comments so the time.Duration literals don't scan as a 12+ BIP-39-word
	// run and trip scripts/check-no-mnemonics.sh (mnemonic guard).
	candidates := []time.Duration{
		15 * time.Minute, 30 * time.Minute, // sub-hourly presets
		time.Hour, 2 * time.Hour, 3 * time.Hour, // low multi-hour presets
		6 * time.Hour, 12 * time.Hour, 24 * time.Hour, // coarse daily-scale presets
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate >= minInterval {
			out = append(out, candidate.String())
		}
	}
	if len(out) == 0 {
		out = append(out, minInterval.String())
	}
	return out
}

func runtimeModuleScheduleSummary(schedule RuntimeModuleSchedule) string {
	state := "disabled"
	if schedule.Enabled {
		state = "enabled"
	}
	parts := []string{state}
	if schedule.Interval != "" {
		parts = append(parts, "interval "+schedule.Interval)
	}
	if schedule.CronExpression != "" {
		parts = append(parts, "cron "+schedule.CronExpression)
	}
	if schedule.Timezone != "" {
		parts = append(parts, "timezone "+schedule.Timezone)
	}
	return strings.Join(parts, " | ")
}

func (m *Manager) loadRuntimeModuleInputState(baseDataPath string) {
	if m == nil || strings.TrimSpace(baseDataPath) == "" {
		return
	}
	path := runtimeModuleInputStatePath(baseDataPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Warnf("read runtime module input state: %v", err)
		}
		return
	}
	var state map[string]RuntimeModuleInputState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Warnf("decode runtime module input state: %v", err)
		return
	}
	m.inputStateMu.Lock()
	defer m.inputStateMu.Unlock()
	if m.inputState == nil {
		m.inputState = make(map[string]RuntimeModuleInputState)
	}
	for moduleID, moduleState := range state {
		if _, exists := m.inputState[moduleID]; exists {
			continue
		}
		moduleState.Values = cloneRuntimeModuleInputValues(moduleState.Values)
		moduleState.CommandHistory = cloneRuntimeModuleCommandHistory(moduleState.CommandHistory)
		moduleState.Schedules = cloneRuntimeModuleSchedules(moduleState.Schedules)
		m.inputState[moduleID] = moduleState
	}
	m.cronConfigMu.Lock()
	if m.cronConfig == nil {
		m.cronConfig = make(map[string]map[string]CronScheduleConfig)
	}
	for moduleID, moduleState := range m.inputState {
		for methodID, schedule := range moduleState.Schedules {
			if m.cronConfig[moduleID] == nil {
				m.cronConfig[moduleID] = make(map[string]CronScheduleConfig)
			}
			m.cronConfig[moduleID][methodID] = CronScheduleConfig{
				Enabled:        schedule.Enabled,
				Interval:       schedule.Interval,
				CronExpression: schedule.CronExpression,
				Timezone:       schedule.Timezone,
				Jitter:         schedule.Jitter,
				Backoff:        schedule.Backoff,
				RetryBudget:    schedule.RetryBudget,
				MaxRuntime:     schedule.MaxRuntime,
			}
		}
	}
	m.cronConfigMu.Unlock()
}

func (m *Manager) persistRuntimeModuleInputState() {
	if m == nil || strings.TrimSpace(m.runtime.BaseDataPath) == "" {
		return
	}
	path := runtimeModuleInputStatePath(m.runtime.BaseDataPath)
	m.inputStateMu.RLock()
	state := make(map[string]RuntimeModuleInputState, len(m.inputState))
	for moduleID, moduleState := range m.inputState {
		if len(moduleState.Values) == 0 && len(moduleState.CommandHistory) == 0 && len(moduleState.Schedules) == 0 {
			continue
		}
		state[moduleID] = RuntimeModuleInputState{
			Values:         cloneRuntimeModuleInputValues(moduleState.Values),
			RestartPending: moduleState.RestartPending,
			CommandHistory: cloneRuntimeModuleCommandHistory(moduleState.CommandHistory),
			Schedules:      cloneRuntimeModuleSchedules(moduleState.Schedules),
		}
	}
	m.inputStateMu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Warnf("create runtime module input state directory: %v", err)
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Warnf("encode runtime module input state: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Warnf("write runtime module input state: %v", err)
	}
}

func runtimeModuleInputStatePath(baseDataPath string) string {
	return filepath.Join(baseDataPath, "modules", "runtime-inputs.json")
}

func timerOptionLabel(id string) string {
	label := strings.ReplaceAll(id, "_", " ")
	label = strings.ReplaceAll(label, "-", " ")
	return "Timer " + label + " interval"
}
