// Package config provides configuration management for the SDN server.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the SDN server configuration.
type Config struct {
	Mode     string         `yaml:"mode"`     // "full" or "edge"
	Network  NetworkConfig  `yaml:"network"`
	Storage  StorageConfig  `yaml:"storage"`
	Schemas  SchemaConfig   `yaml:"schemas"`
	Security SecurityConfig `yaml:"security"`
}

// NetworkConfig contains network-related settings.
type NetworkConfig struct {
	Listen         []string `yaml:"listen"`
	Bootstrap      []string `yaml:"bootstrap"`
	EdgeRelays     []string `yaml:"edge_relays"`
	MaxConns       int      `yaml:"max_connections"`
	EnableRelay    bool     `yaml:"enable_relay"`
	MaxMessageSize int      `yaml:"max_message_size"` // Maximum message size in bytes (default: 10MB)
	MaxSchemaName  int      `yaml:"max_schema_name"`  // Maximum schema name length (default: 256)
	MaxQuerySize   int      `yaml:"max_query_size"`   // Maximum query size in bytes (default: 4KB)

	// Rate limiting settings (per peer)
	MaxMessagesPerSecond float64 `yaml:"max_messages_per_second"` // Maximum messages per second per peer (default: 100)
	MaxMessagesPerMinute int     `yaml:"max_messages_per_minute"` // Maximum messages per minute per peer (default: 1000)
	RateLimitBurst       int     `yaml:"rate_limit_burst"`        // Allow burst of messages up to this limit (default: 50)
}

// StorageConfig contains storage-related settings.
type StorageConfig struct {
	Path       string `yaml:"path"`
	MaxSize    string `yaml:"max_size"`
	GCInterval string `yaml:"gc_interval"`
}

// SchemaConfig contains schema validation settings.
type SchemaConfig struct {
	Validate bool `yaml:"validate"`
	Strict   bool `yaml:"strict"`
}

// SecurityConfig contains security-related settings.
type SecurityConfig struct {
	// InsecureMode disables mandatory signature verification.
	// WARNING: This should ONLY be used for development and testing.
	// In production, signature verification is REQUIRED for all data operations.
	// When enabled, a warning will be logged at startup.
	InsecureMode bool `yaml:"insecure_mode"`
}

// Default returns a default configuration.
func Default() *Config {
	homeDir, _ := os.UserHomeDir()
	dataPath := filepath.Join(homeDir, ".spacedatanetwork", "data")

	return &Config{
		Mode: "full",
		Network: NetworkConfig{
			Listen: []string{
				"/ip4/0.0.0.0/tcp/4001",
				"/ip4/0.0.0.0/tcp/8080/ws",
				"/ip4/0.0.0.0/udp/4001/quic-v1",
			},
			Bootstrap: []string{
				"/dnsaddr/bootstrap.spacedatanetwork.org/p2p/QmBootstrap1",
			},
			EdgeRelays:     []string{},
			MaxConns:       1000,
			EnableRelay:    true,
			MaxMessageSize: 10 * 1024 * 1024, // 10MB default
			MaxSchemaName:  256,              // 256 bytes max schema name
			MaxQuerySize:   4 * 1024,         // 4KB max query size

			MaxMessagesPerSecond: 100,  // 100 messages per second per peer
			MaxMessagesPerMinute: 1000, // 1000 messages per minute per peer
			RateLimitBurst:       50,   // Allow burst of 50 messages
		},
		Storage: StorageConfig{
			Path:       dataPath,
			MaxSize:    "10GB",
			GCInterval: "1h",
		},
		Schemas: SchemaConfig{
			Validate: true,
			Strict:   true,
		},
		Security: SecurityConfig{
			InsecureMode: false, // Signature verification required by default
		},
	}
}

// DefaultPath returns the default configuration file path.
func DefaultPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".spacedatanetwork", "config.yaml")
}

// Load loads the configuration from a file.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config if file doesn't exist
			return Default(), nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save saves the configuration to a file.
func Save(path string, cfg *Config) error {
	if path == "" {
		path = DefaultPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
