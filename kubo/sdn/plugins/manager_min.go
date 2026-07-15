package plugins

import (
	"errors"
	"fmt"
	"sync"
)

// Manager is the MINIMAL plugin registry ported for Phase 2c (flow runtime).
//
// The full sdn-server plugins.Manager (cron scheduler, per-plugin runtime
// state history, dashboard input-state persistence, HTTP route mounting, DHT
// wiring) was intentionally left behind in the Phase 2b trim (see the package
// doc in manager.go). FlowManager (sdn/flowrt/manager.go) needs only enough of
// it to register a running flow as a plugin: construction (New) and de-dup'd
// registration (Register). This minimal type provides exactly that. Re-port the
// full Manager runtime when the node runtime is rebased onto kubo.
type Manager struct {
	mu      sync.RWMutex
	plugins []Plugin
}

// New creates an empty plugin manager.
func New() *Manager {
	return &Manager{plugins: make([]Plugin, 0)}
}

// Register adds a plugin to the manager, rejecting nil plugins, empty IDs, and
// duplicate IDs (matching the full Manager.Register contract).
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.plugins {
		if existing.ID() == id {
			return fmt.Errorf("plugin %q already registered", id)
		}
	}
	m.plugins = append(m.plugins, plugin)
	return nil
}

// Plugins returns a snapshot of the registered plugins.
func (m *Manager) Plugins() []Plugin {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Plugin, len(m.plugins))
	copy(out, m.plugins)
	return out
}

// Get returns a registered plugin by ID, or nil when none matches.
func (m *Manager) Get(id string) Plugin {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.plugins {
		if p.ID() == id {
			return p
		}
	}
	return nil
}
