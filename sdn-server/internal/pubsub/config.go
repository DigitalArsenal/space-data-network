// Package pubsub provides PubSub topic management and tip/queue configuration.
package pubsub

import (
	"sync"
	"time"
)

// Default configuration values.
const (
	DefaultAutoFetch    = false
	DefaultAutoPin      = false
	DefaultTTL          = 24 * time.Hour
	DefaultMaxQueueSize = 1000
	DefaultFetchTimeout = 30 * time.Second
	DefaultPriority     = 0
)

// TipQueueConfig holds system-wide defaults and per-source/per-schema overrides.
type TipQueueConfig struct {
	// System-wide defaults
	DefaultAutoFetch bool          `yaml:"default_auto_fetch" json:"default_auto_fetch"`
	DefaultAutoPin   bool          `yaml:"default_auto_pin" json:"default_auto_pin"`
	DefaultTTL       time.Duration `yaml:"default_ttl" json:"default_ttl"`
	MaxQueueSize     int           `yaml:"max_queue_size" json:"max_queue_size"`
	FetchTimeout     time.Duration `yaml:"fetch_timeout" json:"fetch_timeout"`

	// Per-schema defaults (schema name -> config)
	SchemaDefaults map[string]*SchemaConfig `yaml:"schema_defaults" json:"schema_defaults"`

	// Per-source overrides (peer ID -> config)
	SourceOverrides map[string]*SourceConfig `yaml:"source_overrides" json:"source_overrides"`

	mu sync.RWMutex
}

// SchemaConfig holds per-message-type settings.
type SchemaConfig struct {
	AutoFetch bool          `yaml:"auto_fetch" json:"auto_fetch"`
	AutoPin   bool          `yaml:"auto_pin" json:"auto_pin"`
	TTL       time.Duration `yaml:"ttl" json:"ttl"`
	Priority  int           `yaml:"priority" json:"priority"` // Higher = more important
}

// SourceConfig holds per-peer/source settings.
type SourceConfig struct {
	Trusted   bool           `yaml:"trusted" json:"trusted"`
	AutoFetch *bool          `yaml:"auto_fetch,omitempty" json:"auto_fetch,omitempty"` // nil = use default
	AutoPin   *bool          `yaml:"auto_pin,omitempty" json:"auto_pin,omitempty"`     // nil = use default
	TTL       *time.Duration `yaml:"ttl,omitempty" json:"ttl,omitempty"`               // nil = use default

	// Per-schema per-source overrides
	SchemaOverrides map[string]*SchemaConfig `yaml:"schema_overrides" json:"schema_overrides"`
}

// ResolvedConfig holds the final resolved configuration for a specific source+schema.
type ResolvedConfig struct {
	AutoFetch bool
	AutoPin   bool
	TTL       time.Duration
	Priority  int
	Trusted   bool
}

// NewTipQueueConfig creates a TipQueueConfig with sensible defaults.
func NewTipQueueConfig() *TipQueueConfig {
	return &TipQueueConfig{
		DefaultAutoFetch: DefaultAutoFetch,
		DefaultAutoPin:   DefaultAutoPin,
		DefaultTTL:       DefaultTTL,
		MaxQueueSize:     DefaultMaxQueueSize,
		FetchTimeout:     DefaultFetchTimeout,
		SchemaDefaults:   make(map[string]*SchemaConfig),
		SourceOverrides:  make(map[string]*SourceConfig),
	}
}

// ResolveConfig resolves the configuration for a specific source and schema.
// Resolution order:
//  1. SourceOverrides[peerID].SchemaOverrides[schemaType] - highest priority
//  2. SourceOverrides[peerID] - source-level defaults
//  3. SchemaDefaults[schemaType] - schema-level defaults
//  4. System Default* values - lowest priority
func (c *TipQueueConfig) ResolveConfig(peerID, schemaType string) ResolvedConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Start with system defaults
	resolved := ResolvedConfig{
		AutoFetch: c.DefaultAutoFetch,
		AutoPin:   c.DefaultAutoPin,
		TTL:       c.DefaultTTL,
		Priority:  DefaultPriority,
		Trusted:   false,
	}

	// Apply schema defaults (priority 3)
	if schemaConfig, ok := c.SchemaDefaults[schemaType]; ok && schemaConfig != nil {
		resolved.AutoFetch = schemaConfig.AutoFetch
		resolved.AutoPin = schemaConfig.AutoPin
		resolved.TTL = schemaConfig.TTL
		resolved.Priority = schemaConfig.Priority
	}

	// Apply source overrides (priority 2)
	sourceConfig, hasSource := c.SourceOverrides[peerID]
	if hasSource && sourceConfig != nil {
		resolved.Trusted = sourceConfig.Trusted

		if sourceConfig.AutoFetch != nil {
			resolved.AutoFetch = *sourceConfig.AutoFetch
		}
		if sourceConfig.AutoPin != nil {
			resolved.AutoPin = *sourceConfig.AutoPin
		}
		if sourceConfig.TTL != nil {
			resolved.TTL = *sourceConfig.TTL
		}

		// Apply source+schema overrides (priority 1 - highest)
		if schemaOverride, ok := sourceConfig.SchemaOverrides[schemaType]; ok && schemaOverride != nil {
			resolved.AutoFetch = schemaOverride.AutoFetch
			resolved.AutoPin = schemaOverride.AutoPin
			resolved.TTL = schemaOverride.TTL
			resolved.Priority = schemaOverride.Priority
		}
	}

	return resolved
}

// SetSchemaDefault sets the default configuration for a schema type.
func (c *TipQueueConfig) SetSchemaDefault(schemaType string, config *SchemaConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.SchemaDefaults == nil {
		c.SchemaDefaults = make(map[string]*SchemaConfig)
	}
	c.SchemaDefaults[schemaType] = config
}

// SetSourceOverride sets the configuration override for a source peer.
func (c *TipQueueConfig) SetSourceOverride(peerID string, config *SourceConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.SourceOverrides == nil {
		c.SourceOverrides = make(map[string]*SourceConfig)
	}
	c.SourceOverrides[peerID] = config
}

// SetSourceSchemaOverride sets a schema-specific override for a source peer.
func (c *TipQueueConfig) SetSourceSchemaOverride(peerID, schemaType string, config *SchemaConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.SourceOverrides == nil {
		c.SourceOverrides = make(map[string]*SourceConfig)
	}

	sourceConfig, ok := c.SourceOverrides[peerID]
	if !ok || sourceConfig == nil {
		sourceConfig = &SourceConfig{
			SchemaOverrides: make(map[string]*SchemaConfig),
		}
		c.SourceOverrides[peerID] = sourceConfig
	}

	if sourceConfig.SchemaOverrides == nil {
		sourceConfig.SchemaOverrides = make(map[string]*SchemaConfig)
	}

	sourceConfig.SchemaOverrides[schemaType] = config
}

// GetSourceConfig returns the source configuration for a peer.
func (c *TipQueueConfig) GetSourceConfig(peerID string) (*SourceConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	config, ok := c.SourceOverrides[peerID]
	return config, ok
}

// GetSchemaDefault returns the default configuration for a schema type.
func (c *TipQueueConfig) GetSchemaDefault(schemaType string) (*SchemaConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	config, ok := c.SchemaDefaults[schemaType]
	return config, ok
}

// TrustSource marks a source as trusted.
func (c *TipQueueConfig) TrustSource(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.SourceOverrides == nil {
		c.SourceOverrides = make(map[string]*SourceConfig)
	}

	if sourceConfig, ok := c.SourceOverrides[peerID]; ok && sourceConfig != nil {
		sourceConfig.Trusted = true
	} else {
		c.SourceOverrides[peerID] = &SourceConfig{Trusted: true}
	}
}

// UntrustSource marks a source as untrusted.
func (c *TipQueueConfig) UntrustSource(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if sourceConfig, ok := c.SourceOverrides[peerID]; ok && sourceConfig != nil {
		sourceConfig.Trusted = false
	}
}

// IsTrusted checks if a source is trusted.
func (c *TipQueueConfig) IsTrusted(peerID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if sourceConfig, ok := c.SourceOverrides[peerID]; ok && sourceConfig != nil {
		return sourceConfig.Trusted
	}
	return false
}

// ListTrustedSources returns all trusted peer IDs.
func (c *TipQueueConfig) ListTrustedSources() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var trusted []string
	for peerID, config := range c.SourceOverrides {
		if config != nil && config.Trusted {
			trusted = append(trusted, peerID)
		}
	}
	return trusted
}

// BoolPtr is a helper to create a pointer to a bool.
func BoolPtr(b bool) *bool {
	return &b
}

// DurationPtr is a helper to create a pointer to a duration.
func DurationPtr(d time.Duration) *time.Duration {
	return &d
}
