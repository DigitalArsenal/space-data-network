package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	ID          string            `json:"id"`
	Version     string            `json:"version,omitempty"`
	Status      string            `json:"status"`
	Description string            `json:"description,omitempty"`
	UI          *UIDescriptor     `json:"ui,omitempty"`
	Cron        []CronMethodSpec  `json:"cron,omitempty"`
}

// ─── Manager ───────────────────────────────────────────────────────────────

// Manager coordinates plugin lifecycle, route registration, and cron scheduling.
type Manager struct {
	plugins []Plugin

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
	for _, existing := range m.plugins {
		if existing.ID() == id {
			return fmt.Errorf("plugin %q already registered", id)
		}
	}
	m.plugins = append(m.plugins, plugin)
	return nil
}

// StartAll starts all registered plugins, then schedules cron methods for
// plugins that implement CronProvider.
func (m *Manager) StartAll(ctx context.Context, runtime RuntimeContext) error {
	if m == nil {
		return nil
	}
	var errs []error
	for _, plugin := range m.plugins {
		if err := plugin.Start(ctx, runtime); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", plugin.ID(), err))
			continue
		}
	}

	// Start cron methods in a shared cancellable context.
	cronCtx, cancel := context.WithCancel(ctx)
	m.cronCancel = cancel

	for _, plugin := range m.plugins {
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
	for _, plugin := range m.plugins {
		plugin.RegisterRoutes(mux)
	}
}

// Get returns a registered plugin by ID.
func (m *Manager) Get(id string) Plugin {
	if m == nil {
		return nil
	}
	for _, plugin := range m.plugins {
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
	entries := make([]PluginManifestEntry, 0, len(m.plugins))
	for _, p := range m.plugins {
		entry := PluginManifestEntry{
			ID:     p.ID(),
			Status: "running",
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
	for i := len(m.plugins) - 1; i >= 0; i-- {
		if err := m.plugins[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.plugins[i].ID(), err))
		}
	}
	return errors.Join(errs...)
}
