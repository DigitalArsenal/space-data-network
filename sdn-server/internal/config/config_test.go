package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProductionIngestConfigHasNoDirectPublicCatalogFields(t *testing.T) {
	forbiddenSource := strings.Join([]string{"celes", "trak"}, "")
	typ := reflect.TypeOf(IngestConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if strings.Contains(strings.ToLower(field.Name+" "+field.Tag.Get("yaml")), forbiddenSource) {
			t.Fatalf("production ingest config exposes forbidden direct source field %s (%s)", field.Name, field.Tag.Get("yaml"))
		}
	}
}

func TestModulesConfigDefaultsToUnsetScheduledTimeout(t *testing.T) {
	// Unset (0) means the module runtime applies its own built-in default
	// (10m) — config must not silently invent a different one.
	if cfg := Default(); cfg.Modules.ScheduledInvokeTimeout != 0 {
		t.Fatalf("Default().Modules.ScheduledInvokeTimeout = %s, want 0 (runtime default applies)", cfg.Modules.ScheduledInvokeTimeout)
	}
}

func TestLoadModulesScheduledInvokeTimeoutFromYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	yamlDoc := "modules:\n  scheduled_invoke_timeout: 20m\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got := cfg.Modules.ScheduledInvokeTimeout; got != 20*time.Minute {
		t.Fatalf("Modules.ScheduledInvokeTimeout = %s, want 20m", got)
	}
}

func TestDefaultAssetPinConfigIsSecureAndBounded(t *testing.T) {
	cfg := Default()

	if cfg.AssetPins.Enabled {
		t.Fatal("default asset pin capability must be disabled")
	}
	if got := cfg.AssetPins.EffectiveMaxUploadBytes(); got != DefaultAssetPinMaxUploadBytes {
		t.Fatalf("EffectiveMaxUploadBytes() = %d, want %d", got, DefaultAssetPinMaxUploadBytes)
	}
	if got := cfg.AssetPins.EffectiveMinFreeBytes(); got != DefaultAssetPinMinFreeBytes {
		t.Fatalf("EffectiveMinFreeBytes() = %d, want %d", got, DefaultAssetPinMinFreeBytes)
	}
	if got := cfg.AssetPins.EffectiveRetentionInterval(); got != DefaultAssetPinRetentionInterval {
		t.Fatalf("EffectiveRetentionInterval() = %v, want %v", got, DefaultAssetPinRetentionInterval)
	}
}

func TestDefaultAssetPinConfigProductionFields(t *testing.T) {
	cfg := Default().AssetPins
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "issuer", got: cfg.Issuer, want: "https://token.actions.githubusercontent.com"},
		{name: "audience", got: cfg.Audience, want: "sdn-asset-models"},
		{name: "repository", got: cfg.Repository, want: "DigitalArsenal/asset-models"},
		{name: "ref", got: cfg.Ref, want: "refs/heads/main"},
		{name: "pin workflow", got: cfg.PinWorkflow, want: "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main"},
		{name: "decision workflow", got: cfg.DecisionWorkflow, want: "DigitalArsenal/asset-models/.github/workflows/review-decision.yml@refs/heads/main"},
		{name: "gateway URL", got: cfg.GatewayURL, want: "https://sdn.spaceaware.io/ipfs"},
		{name: "Kubo repo path", got: cfg.KuboRepoPath, want: "/mnt/volume_nyc3_01/ipfs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestAssetPinConfigEffectiveValuesUsePositiveOverrides(t *testing.T) {
	cfg := AssetPinConfig{
		MaxUploadBytes:    42,
		MinFreeBytes:      84,
		RetentionInterval: 90 * time.Minute,
	}

	if got := cfg.EffectiveMaxUploadBytes(); got != 42 {
		t.Fatalf("EffectiveMaxUploadBytes() = %d, want 42", got)
	}
	if got := cfg.EffectiveMinFreeBytes(); got != 84 {
		t.Fatalf("EffectiveMinFreeBytes() = %d, want 84", got)
	}
	if got := cfg.EffectiveRetentionInterval(); got != 90*time.Minute {
		t.Fatalf("EffectiveRetentionInterval() = %v, want %v", got, 90*time.Minute)
	}
}

func TestLoadAssetPinConfigFromYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	yamlDoc := `asset_pins:
  enabled: true
  issuer: https://issuer.example
  audience: example-audience
  repository: Example/assets
  ref: refs/heads/release
  pin_workflow: Example/assets/.github/workflows/pin.yml@refs/heads/release
  decision_workflow: Example/assets/.github/workflows/decision.yml@refs/heads/release
  gateway_url: https://gateway.example/ipfs
  kubo_repo_path: /srv/ipfs
  max_upload_bytes: 123456
  min_free_bytes: 987654
  retention_interval: 45m
`
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	want := AssetPinConfig{
		Enabled:           true,
		Issuer:            "https://issuer.example",
		Audience:          "example-audience",
		Repository:        "Example/assets",
		Ref:               "refs/heads/release",
		PinWorkflow:       "Example/assets/.github/workflows/pin.yml@refs/heads/release",
		DecisionWorkflow:  "Example/assets/.github/workflows/decision.yml@refs/heads/release",
		GatewayURL:        "https://gateway.example/ipfs",
		KuboRepoPath:      "/srv/ipfs",
		MaxUploadBytes:    123456,
		MinFreeBytes:      987654,
		RetentionInterval: 45 * time.Minute,
	}
	if cfg.AssetPins != want {
		t.Fatalf("AssetPins = %+v, want %+v", cfg.AssetPins, want)
	}
}

func TestAssetPinConfigYAMLRoundTrip(t *testing.T) {
	want := Default()
	want.AssetPins.Enabled = true
	want.AssetPins.MaxUploadBytes = 7_000_000
	want.AssetPins.MinFreeBytes = 12 << 30
	want.AssetPins.RetentionInterval = 2 * time.Hour

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(configPath, want); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	got, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.AssetPins != want.AssetPins {
		t.Fatalf("AssetPins after Save/Load = %+v, want %+v", got.AssetPins, want.AssetPins)
	}
}

func TestAssetPinConfigExampleLoadsDisabled(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.example.yaml")
	if _, err := os.Stat(examplePath); err != nil {
		t.Fatalf("stat config example: %v", err)
	}

	cfg, err := Load(examplePath)
	if err != nil {
		t.Fatalf("Load(config.example.yaml) = %v", err)
	}
	if cfg.AssetPins.Enabled {
		t.Fatal("config.example.yaml must leave asset pins disabled")
	}
}

func TestLoadRejectsNegativeAssetPinBounds(t *testing.T) {
	tests := []struct {
		name      string
		yamlLine  string
		errorPath string
	}{
		{name: "max upload bytes", yamlLine: "max_upload_bytes: -1", errorPath: "asset_pins.max_upload_bytes"},
		{name: "minimum free bytes", yamlLine: "min_free_bytes: -1", errorPath: "asset_pins.min_free_bytes"},
		{name: "retention interval", yamlLine: "retention_interval: -1s", errorPath: "asset_pins.retention_interval"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			yamlDoc := "asset_pins:\n  " + tt.yamlLine + "\n"
			if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := Load(configPath)
			if err == nil {
				t.Fatal("Load() succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.errorPath) {
				t.Fatalf("Load() error = %q, want it to name %s", err, tt.errorPath)
			}
		})
	}
}

func TestLoadRejectsMissingEnabledAssetPinFields(t *testing.T) {
	tests := []struct {
		name      string
		errorPath string
		clear     func(*AssetPinConfig)
	}{
		{name: "issuer", errorPath: "asset_pins.issuer", clear: func(c *AssetPinConfig) { c.Issuer = "" }},
		{name: "audience", errorPath: "asset_pins.audience", clear: func(c *AssetPinConfig) { c.Audience = "" }},
		{name: "repository", errorPath: "asset_pins.repository", clear: func(c *AssetPinConfig) { c.Repository = "" }},
		{name: "ref", errorPath: "asset_pins.ref", clear: func(c *AssetPinConfig) { c.Ref = "" }},
		{name: "pin workflow", errorPath: "asset_pins.pin_workflow", clear: func(c *AssetPinConfig) { c.PinWorkflow = "" }},
		{name: "decision workflow", errorPath: "asset_pins.decision_workflow", clear: func(c *AssetPinConfig) { c.DecisionWorkflow = "" }},
		{name: "gateway URL", errorPath: "asset_pins.gateway_url", clear: func(c *AssetPinConfig) { c.GatewayURL = "" }},
		{name: "Kubo repo path", errorPath: "asset_pins.kubo_repo_path", clear: func(c *AssetPinConfig) { c.KuboRepoPath = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.AssetPins.Enabled = true
			tt.clear(&cfg.AssetPins)

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := Save(configPath, cfg); err != nil {
				t.Fatalf("Save() = %v", err)
			}
			_, err := Load(configPath)
			if err == nil {
				t.Fatal("Load() succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.errorPath) {
				t.Fatalf("Load() error = %q, want it to name %s", err, tt.errorPath)
			}
		})
	}
}

func TestLoadRejectsNonHTTPSAssetPinGateway(t *testing.T) {
	for _, tt := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled", enabled: false},
		{name: "enabled", enabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.AssetPins.Enabled = tt.enabled
			cfg.AssetPins.GatewayURL = "http://gateway.example/ipfs"

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := Save(configPath, cfg); err != nil {
				t.Fatalf("Save() = %v", err)
			}
			_, err := Load(configPath)
			if err == nil {
				t.Fatal("Load() succeeded, want an error")
			}
			if !strings.Contains(err.Error(), "asset_pins.gateway_url") || !strings.Contains(err.Error(), "HTTPS") {
				t.Fatalf("Load() error = %q, want it to require HTTPS for asset_pins.gateway_url", err)
			}
		})
	}
}

func TestLoadRejectsHostlessAssetPinGateway(t *testing.T) {
	cfg := Default()
	cfg.AssetPins.Enabled = true
	cfg.AssetPins.GatewayURL = "https://:443"

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(configPath, cfg); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "asset_pins.gateway_url") {
		t.Fatalf("Load() error = %q, want it to name asset_pins.gateway_url", err)
	}
}

func TestDefaultConfigDoesNotRequireLocalTorRuntime(t *testing.T) {
	cfg := Default()
	if cfg.Tor.Enabled {
		t.Fatal("default config should not start a local tor runtime")
	}
	if cfg.Tor.HiddenServiceEnabled {
		t.Fatal("default config should not publish a tor hidden service")
	}
}

// TestLoadMissingFileReturnsValidatedDefault locks in that Load's
// file-not-found fallback path still runs validate() — the default config's
// flows.mounts entry ("/api/v1/data/") must satisfy the same /api/ prefix
// rule a loaded file is held to, so the guarantee applies unconditionally.
func TestLoadMissingFileReturnsValidatedDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load(missing) = %v, want default config with no error", err)
	}
	if len(cfg.Flows.Mounts) == 0 {
		t.Fatal("expected default config to declare the built-in data-retrieval flow mount")
	}
	for _, m := range cfg.Flows.Mounts {
		if !strings.HasPrefix(m.Path, "/api/") {
			t.Fatalf("default flow mount path %q does not begin with /api/", m.Path)
		}
	}
}

// TestLoadRejectsFlowMountPathOutsideAPIPrefix is the load-bearing guarantee
// behind gap B10.2: a flows.mounts[].path outside /api/ would be registered
// verbatim by RegisterLazyFlowMounts with no auth-wall coverage (the wall's
// isAPIOrPlugin check in cmd/spacedatanetwork/main.go only inspects /api/
// and /orbpro-key-broker/ paths), so such a config must fail to load rather
// than silently create an unauthenticated surface.
func TestLoadRejectsFlowMountPathOutsideAPIPrefix(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "flows:\n  mounts:\n    - path: /unsafe/mount/\n      flow: com.example.test-flow\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load with a non-/api/ flow mount path succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "/unsafe/mount/") || !strings.Contains(err.Error(), "/api/") {
		t.Fatalf("Load error = %q, want it to name the offending path and the /api/ requirement", err.Error())
	}
}

// TestLoadAcceptsFlowMountPathUnderAPIPrefix is the positive counterpart to
// TestLoadRejectsFlowMountPathOutsideAPIPrefix: a properly namespaced mount
// must still load cleanly.
func TestLoadAcceptsFlowMountPathUnderAPIPrefix(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "flows:\n  mounts:\n    - path: /api/v1/custom/\n      flow: com.example.test-flow\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load with an /api/-prefixed flow mount path failed: %v", err)
	}
	if len(cfg.Flows.Mounts) != 1 || cfg.Flows.Mounts[0].Path != "/api/v1/custom/" {
		t.Fatalf("unexpected mounts after load: %+v", cfg.Flows.Mounts)
	}
}

// flows.storage_path must FOLLOW a moved storage.path unless the operator set
// it explicitly. Before this rule a node with a custom store silently kept its
// flow bundles under ~/.spacedatanetwork/data/flows and served whatever stale
// artifact sat there (dev 2026-08-29: a July data-retrieval flow shadowed the
// current installed bundle; same class as sdn-data-retrieval-flow-not-installed).
func TestLoadFlowsStorageFollowsMovedStoragePath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "storage:\n  path: \"" + filepath.Join(dir, "store") + "\"\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got, want := cfg.Flows.StoragePath, filepath.Join(dir, "store", "flows"); got != want {
		t.Fatalf("Flows.StoragePath = %q, want %q", got, want)
	}
}

func TestLoadExplicitFlowsStoragePathIsKept(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	explicit := filepath.Join(dir, "elsewhere", "flow-bundles")
	yamlDoc := "storage:\n  path: \"" + filepath.Join(dir, "store") + "\"\nflows:\n  storage_path: \"" + explicit + "\"\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.Flows.StoragePath != explicit {
		t.Fatalf("Flows.StoragePath = %q, want explicit %q", cfg.Flows.StoragePath, explicit)
	}
}
