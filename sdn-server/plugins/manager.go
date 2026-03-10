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
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

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

var log = logging.Logger("plugins")

// PeriodicTaskConfig describes a recurring task that a plugin wants the
// manager to schedule. The manager owns the goroutine and ticker lifetime,
// so plugins don't need to manage their own background loops.
type PeriodicTaskConfig struct {
	// Name is a human-readable label for logging (e.g. "dht-announce").
	Name string

	// Interval is how often the task runs. Minimum enforced: 1 second.
	Interval time.Duration

	// RunOnStart executes the task immediately at plugin start, before the
	// first ticker fires.
	RunOnStart bool

	// Fn is the work to perform. The context is cancelled when the plugin
	// shuts down; Fn should respect cancellation.
	Fn func(ctx context.Context) error
}

// PeriodicTaskProvider is an optional interface that plugins can implement to
// declare periodic (cron-like) tasks. The manager starts a goroutine per task
// after Start() completes and stops them before Close().
type PeriodicTaskProvider interface {
	PeriodicTasks() []PeriodicTaskConfig
}

// StreamHandlerConfig describes a libp2p stream protocol that a plugin wants
// to register. The manager registers/removes the handlers as part of the
// plugin lifecycle.
type StreamHandlerConfig struct {
	// ProtocolID is the libp2p protocol identifier (e.g. "/myapp/data/1.0.0").
	ProtocolID protocol.ID

	// Handler is the stream handler function.
	Handler network.StreamHandler
}

// StreamProvider is an optional interface that plugins can implement to
// declare libp2p stream handlers. The manager registers them after Start()
// and removes them during Close().
type StreamProvider interface {
	StreamHandlers() []StreamHandlerConfig
}

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

// PluginManifestEntry is the JSON representation of a plugin in the manifest.
type PluginManifestEntry struct {
	ID          string        `json:"id"`
	Version     string        `json:"version,omitempty"`
	Status      string        `json:"status"`
	Description string        `json:"description,omitempty"`
	UI          *UIDescriptor `json:"ui,omitempty"`
}

// Manager coordinates plugin lifecycle and route registration.
type Manager struct {
	plugins []Plugin

	// Periodic task scheduler state.
	taskCancel context.CancelFunc
	taskWg     sync.WaitGroup

	// Stream handler tracking for cleanup.
	streamHandlers map[string][]protocol.ID // plugin ID → registered protocol IDs
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

// New creates an empty plugin manager.
func New() *Manager {
	return &Manager{
		plugins:        make([]Plugin, 0),
		streamHandlers: make(map[string][]protocol.ID),
	}
}

// StartAll starts all registered plugins, then schedules periodic tasks
// and registers stream handlers for plugins that implement those interfaces.
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

		// Register stream handlers for StreamProvider plugins.
		if sp, ok := plugin.(StreamProvider); ok && runtime.Host != nil {
			for _, sh := range sp.StreamHandlers() {
				runtime.Host.SetStreamHandler(sh.ProtocolID, sh.Handler)
				m.streamHandlers[plugin.ID()] = append(m.streamHandlers[plugin.ID()], sh.ProtocolID)
				log.Infof("Plugin %q: registered stream handler %s", plugin.ID(), sh.ProtocolID)
			}
		}
	}

	// Start periodic tasks in a shared cancellable context.
	taskCtx, cancel := context.WithCancel(ctx)
	m.taskCancel = cancel

	for _, plugin := range m.plugins {
		tp, ok := plugin.(PeriodicTaskProvider)
		if !ok {
			continue
		}
		for _, task := range tp.PeriodicTasks() {
			m.scheduleTask(taskCtx, plugin.ID(), task)
		}
	}

	return errors.Join(errs...)
}

// scheduleTask starts a single periodic task goroutine.
func (m *Manager) scheduleTask(ctx context.Context, pluginID string, task PeriodicTaskConfig) {
	interval := task.Interval
	if interval < time.Second {
		interval = time.Second
	}

	m.taskWg.Add(1)
	go func() {
		defer m.taskWg.Done()

		if task.RunOnStart {
			if err := task.Fn(ctx); err != nil {
				log.Warnf("Plugin %q task %q (initial): %v", pluginID, task.Name, err)
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := task.Fn(ctx); err != nil {
					log.Debugf("Plugin %q task %q: %v", pluginID, task.Name, err)
				}
			}
		}
	}()
	log.Infof("Plugin %q: scheduled periodic task %q every %s (run_on_start=%v)",
		pluginID, task.Name, interval, task.RunOnStart)
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
// with their status and optional UI descriptors.
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

// Close stops periodic tasks, removes stream handlers, then shuts down all
// plugins in reverse registration order.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	// Stop all periodic task goroutines first.
	if m.taskCancel != nil {
		m.taskCancel()
	}
	m.taskWg.Wait()

	var errs []error
	for i := len(m.plugins) - 1; i >= 0; i-- {
		if err := m.plugins[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.plugins[i].ID(), err))
		}
	}
	return errors.Join(errs...)
}

// RemoveStreamHandlers removes all stream handlers that were registered by
// plugins. Call this with the host before Close() if you need explicit cleanup,
// otherwise plugins should remove their own handlers in Close().
func (m *Manager) RemoveStreamHandlers(h host.Host) {
	if m == nil || h == nil {
		return
	}
	for pluginID, protocols := range m.streamHandlers {
		for _, pid := range protocols {
			h.RemoveStreamHandler(pid)
			log.Debugf("Plugin %q: removed stream handler %s", pluginID, pid)
		}
	}
	m.streamHandlers = make(map[string][]protocol.ID)
}
