package plugins

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/libp2p/go-libp2p/core/host"
)

// RuntimeContext provides node/runtime dependencies that plugins need at startup.
type RuntimeContext struct {
	Host         host.Host
	BaseDataPath string
	PeerID       string
	Mode         string
}

// Plugin is the runtime contract for SDN server plugins.
type Plugin interface {
	ID() string
	Start(ctx context.Context, runtime RuntimeContext) error
	RegisterRoutes(mux *http.ServeMux)
	Close() error
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
