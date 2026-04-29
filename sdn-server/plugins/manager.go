package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	MemoryPages    uint64 `json:"memoryPages,omitempty"`
	MemoryBytes    uint64 `json:"memoryBytes,omitempty"`
	MaxMemoryPages uint64 `json:"maxMemoryPages,omitempty"`
	MaxMemoryBytes uint64 `json:"maxMemoryBytes,omitempty"`
	UptimeMs       int64  `json:"uptimeMs,omitempty"`
}

// RuntimeModuleMethod describes an invokable method surfaced by the module
// manifest.
type RuntimeModuleMethod struct {
	MethodID    string `json:"methodId"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
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
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
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
}

// RuntimeModuleEntry is one row in the dashboard runtime snapshot.
type RuntimeModuleEntry struct {
	ID            string                 `json:"id"`
	Version       string                 `json:"version,omitempty"`
	Status        string                 `json:"status"`
	StatusMessage string                 `json:"statusMessage,omitempty"`
	Description   string                 `json:"description,omitempty"`
	UI            *UIDescriptor          `json:"ui,omitempty"`
	Cron          []CronMethodSpec       `json:"cron,omitempty"`
	Manifest      *RuntimeModuleManifest `json:"manifest,omitempty"`
	Stats         RuntimeModuleStats     `json:"stats,omitempty"`
	Options       []RuntimeModuleOption  `json:"options,omitempty"`
	Catalog       *RuntimeModuleCatalog  `json:"catalog,omitempty"`
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
			if entry.Version == "" && descriptor.Manifest != nil {
				entry.Version = descriptor.Manifest.Version
			}
		}
		entry.Options = buildRuntimeModuleOptions(entry.Manifest, entry.Cron)
		modules = append(modules, entry)
	}

	return RuntimeSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Count:       len(modules),
		Modules:     modules,
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
	state.status = status
	state.message = message
	if !startedAt.IsZero() {
		state.startedAt = startedAt
	}
	state.updatedAt = time.Now().UTC()
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

func buildRuntimeModuleOptions(manifest *RuntimeModuleManifest, cron []CronMethodSpec) []RuntimeModuleOption {
	options := make([]RuntimeModuleOption, 0)
	seen := map[string]bool{}
	if manifest != nil {
		for _, timer := range manifest.Timers {
			if timer.TimerID == "" {
				continue
			}
			key := "timer." + timer.TimerID + ".interval"
			seen[key] = true
			options = append(options, RuntimeModuleOption{
				Key:         key,
				Label:       timerOptionLabel(timer.TimerID),
				Type:        "duration-ms",
				Value:       fmt.Sprintf("%d", timer.DefaultIntervalMs),
				Description: timer.Description,
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
			Key:         key,
			Label:       timerOptionLabel(spec.Method),
			Type:        "duration",
			Value:       spec.DefaultInterval,
			Description: spec.Description,
		})
	}
	return options
}

func timerOptionLabel(id string) string {
	label := strings.ReplaceAll(id, "_", " ")
	label = strings.ReplaceAll(label, "-", " ")
	return "Timer " + label + " interval"
}
