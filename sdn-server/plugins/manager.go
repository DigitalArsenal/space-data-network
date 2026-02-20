package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
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
}

// New creates an empty plugin manager.
func New() *Manager {
	return &Manager{
		plugins: make([]Plugin, 0),
	}
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

// StartAll starts all registered plugins.
func (m *Manager) StartAll(ctx context.Context, runtime RuntimeContext) error {
	if m == nil {
		return nil
	}
	var errs []error
	for _, plugin := range m.plugins {
		if err := plugin.Start(ctx, runtime); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", plugin.ID(), err))
		}
	}
	return errors.Join(errs...)
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

// Close shuts down all plugins in reverse registration order.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var errs []error
	for i := len(m.plugins) - 1; i >= 0; i-- {
		if err := m.plugins[i].Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.plugins[i].ID(), err))
		}
	}
	return errors.Join(errs...)
}
