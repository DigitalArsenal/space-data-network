// Package config provides configuration management for the SDN server.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the SDN server configuration.
type Config struct {
	Mode    string        `yaml:"mode"`    // "full" or "edge"
	Network NetworkConfig `yaml:"network"`
	Storage StorageConfig `yaml:"storage"`
	Schemas SchemaConfig  `yaml:"schemas"`
}

// NetworkConfig contains network-related settings.
type NetworkConfig struct {
	Listen      []string `yaml:"listen"`
	Bootstrap   []string `yaml:"bootstrap"`
	EdgeRelays  []string `yaml:"edge_relays"`
	MaxConns    int      `yaml:"max_connections"`
	EnableRelay bool     `yaml:"enable_relay"`
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
			EdgeRelays:  []string{},
			MaxConns:    1000,
			EnableRelay: true,
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
