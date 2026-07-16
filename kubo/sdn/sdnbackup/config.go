package sdnbackup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup configuration is operator-only node-local state that references secret
// LANES (never secrets), the same shape as the per-module cron config and the
// module registry — so it lives as a home-dir JSON file, not a network-published
// SDS record (spec D.1). Promoting it to a signed, fleet-shareable $BUC record
// is a flagged owner decision (spec F-10), not built here.

// DefaultConfigPath returns <homeDir>/sdn/backup/config.json (spec D.1),
// mirroring the installed.json / per-module cron config convention.
func DefaultConfigPath(homeDir string) string {
	return filepath.Join(homeDir, "sdn", "backup", "config.json")
}

// AdapterConfig binds one configured adapter: which reference provider, which
// tier, and which secrets LANE (a lane name, never a secret). Meta carries
// provider addressing (repo, bucket, region, base dir).
type AdapterConfig struct {
	ID             string            `json:"id"`
	Module         string            `json:"module,omitempty"` // WASM adapter module id (informational for Go refs)
	Provider       string            `json:"provider"`         // local | github | s3 | ...
	Tier           string            `json:"tier"`             // primary | secondary
	CredentialLane string            `json:"credentialLane,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
	EncryptAtRest  bool              `json:"encryptAtRest,omitempty"`
}

// Retention configures version keep policy (spec F-11). KeepVersions nil =
// keep-all (content-addressed, cheap).
type Retention struct {
	KeepVersions *int `json:"keepVersions"`
}

// Config is the home-dir backup configuration (spec D.1).
type Config struct {
	Schedule             string          `json:"schedule"`
	EncryptAtRestDefault bool            `json:"encryptAtRestDefault"`
	Adapters             []AdapterConfig `json:"adapters"`
	Retention            Retention       `json:"retention"`
}

// DefaultConfig is the empty starting config: a 6h cadence and no adapters.
func DefaultConfig() Config {
	return Config{Schedule: "6h", Adapters: []AdapterConfig{}}
}

// LoadConfig reads the backup config. A missing file is NOT an error — it
// returns DefaultConfig (a node with backup unconfigured), mirroring the
// registry/cron config stores.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, fmt.Errorf("sdnbackup: read config %q: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return DefaultConfig(), nil
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("sdnbackup: parse config %q: %w", path, err)
	}
	if c.Schedule == "" {
		c.Schedule = "6h"
	}
	return c, nil
}

// Save writes the config atomically at 0600 (it names secret lanes, so it is
// operator-only like the other home-dir state).
func (c Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("sdnbackup: create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("sdnbackup: encode config: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("sdnbackup: write config: %w", err)
	}
	return os.Chmod(path, 0o600)
}

// Validate checks tier values and lane presence for adapters that need creds.
func (c Config) Validate() error {
	for _, a := range c.Adapters {
		if a.ID == "" {
			return fmt.Errorf("sdnbackup: adapter config has empty id")
		}
		switch a.Tier {
		case "primary", "secondary":
		default:
			return fmt.Errorf("sdnbackup: adapter %q has invalid tier %q (want primary|secondary)", a.ID, a.Tier)
		}
	}
	return nil
}
