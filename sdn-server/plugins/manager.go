package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
}

// CronScheduleConfig is the per-method schedule from the server config file
// or web UI. This controls whether and how often the host calls plugin_cron.
type CronScheduleConfig struct {
	Enabled  bool   `json:"enabled" yaml:"enabled"`
	Interval string `json:"interval" yaml:"interval"` // overrides DefaultInterval
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
	ID            string                     `json:"id"`
	Version       string                     `json:"version,omitempty"`
	Status        string                     `json:"status"`
	StatusMessage string                     `json:"statusMessage,omitempty"`
	Description   string                     `json:"description,omitempty"`
	UI            *UIDescriptor              `json:"ui,omitempty"`
	Cron          []CronMethodSpec           `json:"cron,omitempty"`
	Manifest      *RuntimeModuleManifest     `json:"manifest,omitempty"`
	Stats         RuntimeModuleStats         `json:"stats,omitempty"`
	Options       []RuntimeModuleOption      `json:"options,omitempty"`
	Actions       []RuntimeModuleAction      `json:"actions,omitempty"`
	StatusHistory []RuntimeModuleStatusEvent `json:"statusHistory,omitempty"`
	Links         *RuntimeModuleLinks        `json:"links,omitempty"`
	Catalog       *RuntimeModuleCatalog      `json:"catalog,omitempty"`
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

	// Per-plugin cron config (plugin ID → method → schedule).
	// Set via SetCronConfig before StartAll, or updated at runtime via API.
	cronConfig   map[string]map[string]CronScheduleConfig
	cronConfigMu sync.RWMutex
}

// New creates an empty plugin manager.
func New() *Manager {
	return &Manager{
		plugins:    make([]Plugin, 0),
		states:     make(map[string]pluginRuntimeState),
		cronConfig: make(map[string]map[string]CronScheduleConfig),
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
	m.runtimeCtx = ctx
	m.runtime = runtime

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
					if output, err := cp.InvokeCron(ctx, method, nil); err != nil {
						log.Debugf("Plugin %q cron %q: %v", pluginID, method, err)
					} else if len(output) > 0 {
						log.Debugf("Plugin %q cron %q: %d bytes output", pluginID, method, len(output))
					}
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

	// Override from server config if present.
	if sched, ok := pluginConfig[spec.Method]; ok {
		enabled = sched.Enabled
		if sched.Interval != "" {
			intervalStr = sched.Interval
		}
	}

	if !enabled {
		return 0, false
	}

	interval, err := time.ParseDuration(intervalStr)
	if err != nil || interval < time.Second {
		interval = 30 * time.Second // sane fallback
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
		entry.Actions = mergeRuntimeModuleActions(entry.Actions, buildRuntimeModuleActions(entry.Status))
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

// UpdateRuntimeModuleOption mutates a dashboard-exposed runtime option. Timer
// and cron interval options are live-only: they update this manager's in-memory
// schedule config and restart cron goroutines, but they do not rewrite config
// files on disk.
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
		if err := plugin.Close(); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		if err := plugin.Start(m.moduleStartContext(ctx), m.runtime); err != nil {
			m.setPluginState(moduleID, "error", err.Error(), time.Time{})
			return err
		}
		m.setPluginState(moduleID, "running", "", time.Now().UTC())
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
				Persistence:  "live-only",
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
			Persistence:  "live-only",
			Mutable:      true,
		})
	}
	return options
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
	m.runtimeCtx = ctx
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

func buildRuntimeModuleActions(status string) []RuntimeModuleAction {
	normalized := strings.ToLower(strings.TrimSpace(status))
	canLoad := normalized == "unloaded" || normalized == "stopped"
	canStart := normalized == "registered" || normalized == "stopped" || normalized == "unloaded" || normalized == "paused"
	canUnload := normalized == "running" || normalized == "paused" || normalized == "registered" || normalized == "stopped"
	canRestart := normalized == "running" || normalized == "paused" || normalized == "registered" || normalized == "stopped"
	canReloadManifest := normalized == "running" || normalized == "paused" || normalized == "registered" || normalized == "stopped"
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
			Enabled:     normalized == "running",
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
			Enabled:     normalized == "running",
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

func timerOptionLabel(id string) string {
	label := strings.ReplaceAll(id, "_", " ")
	label = strings.ReplaceAll(label, "-", " ")
	return "Timer " + label + " interval"
}
