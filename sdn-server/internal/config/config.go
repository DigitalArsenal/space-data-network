// Package config provides configuration management for the SDN server.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the SDN server configuration.
type Config struct {
	Mode       string           `yaml:"mode"` // "full" or "edge"
	Network    NetworkConfig    `yaml:"network"`
	Storage    StorageConfig    `yaml:"storage"`
	Schemas    SchemaConfig     `yaml:"schemas"`
	Security   SecurityConfig   `yaml:"security"`
	Peers      PeersConfig      `yaml:"peers"`
	Admin      AdminConfig      `yaml:"admin"`
	Setup      SetupConfig      `yaml:"setup"`
	Users      []UserEntry      `yaml:"users"`
	Blockchain BlockchainConfig `yaml:"blockchain"`
}

// BlockchainConfig holds RPC settings for crypto payment verification.
type BlockchainConfig struct {
	Ethereum ChainRPCConfig `yaml:"ethereum"`
	Solana   ChainRPCConfig `yaml:"solana"`
	Bitcoin  ChainRPCConfig `yaml:"bitcoin"`
}

// ChainRPCConfig holds per-chain RPC endpoint and confirmation threshold.
type ChainRPCConfig struct {
	RPCURL                string `yaml:"rpc_url"`
	RequiredConfirmations uint64 `yaml:"required_confirmations"`
}

// UserEntry maps an HD wallet xpub to a trust level for authentication.
type UserEntry struct {
	// XPub is the SDN extended public key at the signing key path
	// m/44'/0'/0'/0'/0'. It uses Base58Check encoding with SDN-specific
	// version bytes and directly embeds the Ed25519 signing public key.
	//
	// The server automatically extracts the Ed25519 signing public key from
	// the xpub, so signing_pubkey_hex is no longer required.
	XPub string `yaml:"xpub"`

	// SigningPubKeyHex is an optional Ed25519 public key (32 bytes hex) override.
	// When omitted, the signing key is extracted from the xpub automatically.
	// This field is retained for backward compatibility.
	SigningPubKeyHex string `yaml:"signing_pubkey_hex,omitempty"`

	// TrustLevel: "untrusted", "limited", "standard", "trusted", "admin".
	TrustLevel string `yaml:"trust_level"`

	// Name is an optional human-readable label.
	Name string `yaml:"name"`
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

// PeersConfig contains peer trust registry settings.
type PeersConfig struct {
	// StrictMode only allows connections to/from peers in the trusted registry.
	// When disabled, unknown peers get Standard trust level by default.
	StrictMode bool `yaml:"strict_mode"`

	// RegistryPath is the path to the peer registry database.
	// If empty, defaults to {storage_path}/peers.db
	RegistryPath string `yaml:"registry_path"`

	// TrustedPeers is a list of peer addresses that should be always connected (like IPFS Peering.Peers).
	// These peers will be added to the registry with Trusted level on startup.
	TrustedPeers []string `yaml:"trusted_peers"`

	// EnableDHT enables DHT-based peer discovery.
	EnableDHT bool `yaml:"enable_dht"`

	// EnableMDNS enables mDNS-based local peer discovery.
	EnableMDNS bool `yaml:"enable_mdns"`

	// TrustBasedRateLimiting adjusts rate limits based on peer trust level.
	TrustBasedRateLimiting bool `yaml:"trust_based_rate_limiting"`
}

// AdminConfig contains admin interface settings.
type AdminConfig struct {
	// Enabled enables the admin web interface.
	Enabled bool `yaml:"enabled"`

	// ListenAddr is the address for the admin interface (default: 127.0.0.1:5001).
	ListenAddr string `yaml:"listen_addr"`

	// RequireAuth requires authentication for the admin interface.
	RequireAuth bool `yaml:"require_auth"`

	// SessionExpiry is the duration for admin session tokens (default: 24h).
	SessionExpiry string `yaml:"session_expiry"`

	// TOTPRequired requires TOTP 2FA for admin login.
	TOTPRequired bool `yaml:"totp_required"`

	// TLSEnabled enables native HTTPS on the admin/API server.
	TLSEnabled bool `yaml:"tls_enabled"`

	// TLSCertFile is the PEM-encoded certificate chain path.
	TLSCertFile string `yaml:"tls_cert_file"`

	// TLSKeyFile is the PEM-encoded private key path.
	TLSKeyFile string `yaml:"tls_key_file"`

	// FrontendPath is the filesystem path to the public-facing frontend directory.
	// This directory is served at "/" as a static file server with SPA fallback.
	// Default: "" (resolved at runtime to ~/.spacedatanetwork/frontend/).
	// The directory is created automatically with a default page if it doesn't exist.
	// Override with SDN_FRONTEND_PATH env var or set explicitly in config.
	FrontendPath string `yaml:"frontend_path"`

	// HomepageFile is an optional single-file HTML app served at "/" and "/index.html".
	// Deprecated: use frontend_path instead. If frontend_path is set, this is ignored.
	// If empty, the built-in default landing page is served.
	HomepageFile string `yaml:"homepage_file"`

	// WebuiPath is the filesystem path to an IPFS WebUI build directory (webui/build).
	// When set, the IPFS WebUI is served at "/admin" behind admin authentication.
	// If empty, the admin panel uses the built-in admin UI.
	WebuiPath string `yaml:"webui_path"`

	// IPFSAPIURL is the base URL of an upstream Kubo RPC API endpoint (no path),
	// e.g. "http://127.0.0.1:5001". When set, the admin server reverse-proxies
	// requests to "/api/v0/*" to this endpoint so the React WebUI can talk to IPFS
	// through the authenticated SDN admin server.
	IPFSAPIURL string `yaml:"ipfs_api_url"`

	// WalletUIPath is the filesystem path to the hd-wallet-ui dist directory.
	// If empty, the login page loads wallet UI from CDN (unpkg.com/hd-wallet-ui).
	WalletUIPath string `yaml:"wallet_ui_path"`

	// TrustedProxy is the IP address of a trusted reverse proxy. When set,
	// the server will trust X-Forwarded-Proto from this IP for cookie Secure flag.
	// Set to "loopback" to trust any loopback address (127.0.0.0/8, ::1).
	TrustedProxy string `yaml:"trusted_proxy"`
}

// SetupConfig contains first-time setup settings.
type SetupConfig struct {
	// TokenExpiry is how long the setup token is valid (default: 10m).
	TokenExpiry string `yaml:"token_expiry"`

	// DataPath is the base path for setup data (default: storage path).
	DataPath string `yaml:"data_path"`
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
				"/dnsaddr/bootstrap.digitalarsenal.io/p2p/QmBootstrap1",
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
		Peers: PeersConfig{
			StrictMode:             false, // Allow unknown peers by default
			RegistryPath:           "",    // Use default path
			TrustedPeers:           []string{},
			EnableDHT:              true,
			EnableMDNS:             true,
			TrustBasedRateLimiting: true,
		},
		Admin: AdminConfig{
			Enabled:       true,
			ListenAddr:    "127.0.0.1:5001",
			RequireAuth:   true, // Require authentication by default
			SessionExpiry: "24h",
			TOTPRequired:  false,
			TLSEnabled:    false,
			TLSCertFile:   "",
			TLSKeyFile:    "",
			FrontendPath:  "",
			HomepageFile:  "",
			WebuiPath:     "",
			IPFSAPIURL:    "",
			WalletUIPath:  "",
		},
		Users: []UserEntry{},
		Setup: SetupConfig{
			TokenExpiry: "10m",
			DataPath:    "", // Use storage path by default
		},
		Blockchain: BlockchainConfig{
			Ethereum: ChainRPCConfig{RequiredConfirmations: 12},
			Solana:   ChainRPCConfig{RequiredConfirmations: 1},
			Bitcoin:  ChainRPCConfig{RequiredConfirmations: 6},
		},
	}
}

// DefaultPath returns the default configuration file path.
func DefaultPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".spacedatanetwork", "config.yaml")
}

// DefaultFrontendPath returns the standard frontend directory path.
func DefaultFrontendPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".spacedatanetwork", "frontend")
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

	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save saves the configuration to a file.
func Save(path string, cfg *Config) error {
	if path == "" {
		path = DefaultPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}
