// Package config provides configuration management for the SDN server.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"gopkg.in/yaml.v3"
)

// Config represents the SDN server configuration.
type Config struct {
	Mode       string           `yaml:"mode"` // "full" or "edge"
	Network    NetworkConfig    `yaml:"network"`
	Storage    StorageConfig    `yaml:"storage"`
	Schemas    SchemaConfig     `yaml:"schemas"`
	Security   SecurityConfig   `yaml:"security"`
	Tor        TorConfig        `yaml:"tor"`
	Peers      PeersConfig      `yaml:"peers"`
	Admin      AdminConfig      `yaml:"admin"`
	Setup      SetupConfig      `yaml:"setup"`
	Users      []UserEntry      `yaml:"users"`
	Blockchain BlockchainConfig `yaml:"blockchain"`
	Publishing PublishingConfig `yaml:"publishing"`
	Flows      FlowsConfig      `yaml:"flows"`
	Policies   PoliciesConfig   `yaml:"policies"`
	Ingest     IngestConfig     `yaml:"ingest"`
	Gateway    GatewayConfig    `yaml:"gateway"`
	TipQueue   TipQueueConfig   `yaml:"tip_queue"`
	AssetPins  AssetPinConfig   `yaml:"asset_pins"`
}

// AssetPinConfig controls the opt-in GitHub Actions asset pin capability.
type AssetPinConfig struct {
	Enabled           bool          `yaml:"enabled"`
	Issuer            string        `yaml:"issuer"`
	Audience          string        `yaml:"audience"`
	Repository        string        `yaml:"repository"`
	Ref               string        `yaml:"ref"`
	PinWorkflow       string        `yaml:"pin_workflow"`
	DecisionWorkflow  string        `yaml:"decision_workflow"`
	GatewayURL        string        `yaml:"gateway_url"`
	KuboRepoPath      string        `yaml:"kubo_repo_path"`
	MaxUploadBytes    int64         `yaml:"max_upload_bytes"`
	MinFreeBytes      int64         `yaml:"min_free_bytes"`
	RetentionInterval time.Duration `yaml:"retention_interval"`
}

// Built-in defaults for AssetPinConfig's bounded resource controls.
const (
	DefaultAssetPinMaxUploadBytes    int64         = 10_000_000
	DefaultAssetPinMinFreeBytes      int64         = 10 << 30
	DefaultAssetPinRetentionInterval time.Duration = time.Hour
)

// EffectiveMaxUploadBytes returns the configured upload limit or the safe
// default when the configured value is not positive.
func (c AssetPinConfig) EffectiveMaxUploadBytes() int64 {
	if c.MaxUploadBytes > 0 {
		return c.MaxUploadBytes
	}
	return DefaultAssetPinMaxUploadBytes
}

// EffectiveMinFreeBytes returns the configured free-space floor or the safe
// default when the configured value is not positive.
func (c AssetPinConfig) EffectiveMinFreeBytes() int64 {
	if c.MinFreeBytes > 0 {
		return c.MinFreeBytes
	}
	return DefaultAssetPinMinFreeBytes
}

// EffectiveRetentionInterval returns the configured retention cadence or the
// safe default when the configured value is not positive.
func (c AssetPinConfig) EffectiveRetentionInterval() time.Duration {
	if c.RetentionInterval > 0 {
		return c.RetentionInterval
	}
	return DefaultAssetPinRetentionInterval
}

// TipQueueConfig makes the pubsub.TipQueue resource caps (Task D4:
// per-item auto-fetch size, in-flight fetch concurrency, and minimum
// spacing between fetch starts) YAML-tunable. Zero values keep
// pubsub.NewTipQueueConfig()'s built-in defaults (DefaultMaxFetchBytes,
// DefaultMaxConcurrentFetches, DefaultMinFetchInterval) — see node.go's
// newTipQueueConfig, which only overrides a field when it is > 0.
type TipQueueConfig struct {
	// MaxFetchBytes caps a single auto-fetched PNM content item. 0 keeps
	// pubsub.DefaultMaxFetchBytes (64 MiB).
	MaxFetchBytes int64 `yaml:"max_fetch_bytes"`

	// MaxConcurrentFetches bounds in-flight auto-fetches. 0 keeps
	// pubsub.DefaultMaxConcurrentFetches.
	MaxConcurrentFetches int `yaml:"max_concurrent_fetches"`

	// MinFetchInterval is the minimum spacing TipQueue enforces between the
	// start of consecutive fetches. 0 keeps pubsub.DefaultMinFetchInterval.
	MinFetchInterval time.Duration `yaml:"min_fetch_interval"`
}

// GatewayConfig tunes the network-gateway API surface (docs/gateway-api.md).
type GatewayConfig struct {
	// Anonymous adjusts the anonymous-access allowlist fed by mounted
	// flows' api.routes[].anonymous declarations (gateway loop G.2): a
	// mounted route is admitted anonymously iff it declares anonymous:true
	// AND no Deny entry matches; Allow entries extend read (GET/HEAD)
	// access to extra paths. Entries are exact paths, "{param}" templates,
	// or trailing-slash prefixes. Deny wins over everything, including the
	// host's built-in static allowlist.
	Anonymous GatewayAnonymousConfig `yaml:"anonymous"`

	// Pin is the OPT-IN dataset pin list for the provider-scoped gateway
	// surface (gateway loop G.4, docs/gateway-api.md §10). Pinning is NEVER
	// a default: GET /api/v1/peers/{peerId}/{standard}/latest serves a
	// remote provider's newest published dataset ONLY when the
	// (peer, standard) pair is listed here (a node always may serve its own
	// publications). A pinned pair additionally gets SUPERSEDE lifecycle
	// management: when a newer publication batch from the pinned provider
	// finishes materializing, the previously pinned batch's records are
	// evicted from the store so pins do not accumulate history.
	//
	// Pinning does NOT create the materialization path: dataset feed heads
	// are still imported only from TRUSTED peers (trust registry), exactly
	// as before G.4. A pin for a peer that is not trusted never
	// materializes, and /latest answers 503 with the newest PNM pointer.
	Pin []GatewayPinEntry `yaml:"pin,omitempty"`

	// Query tunes the sandboxed public query surface (/api/v1/query,
	// gateway loop G.5). The caps are enforced IN-WASM by the FlatSQL
	// engine per statement; zero values take the built-in defaults below —
	// there is deliberately NO way to configure "unlimited".
	Query GatewayQueryConfig `yaml:"query"`
}

// GatewayQueryConfig caps one sandboxed public query execution.
type GatewayQueryConfig struct {
	// TimeoutMs bounds statement execution (progress-handler deadline).
	// Default 5000.
	TimeoutMs int `yaml:"timeout_ms,omitempty"`
	// MaxRows rejects (never truncates) results beyond this row count.
	// Default 200000 — comfortably above a full-catalog per-object query.
	MaxRows int64 `yaml:"max_rows,omitempty"`
	// MaxBytes rejects results whose payload exceeds this byte budget.
	// Default 134217728 (128 MiB).
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
}

// Built-in defaults for GatewayQueryConfig (also the floor semantics: a
// zero/negative knob means "use the default", never "unlimited").
const (
	DefaultGatewayQueryTimeoutMs = 5000
	DefaultGatewayQueryMaxRows   = 200000
	DefaultGatewayQueryMaxBytes  = 128 << 20
)

// EffectiveTimeoutMs returns the configured statement timeout or the default.
func (q GatewayQueryConfig) EffectiveTimeoutMs() int {
	if q.TimeoutMs > 0 {
		return q.TimeoutMs
	}
	return DefaultGatewayQueryTimeoutMs
}

// EffectiveMaxRows returns the configured row cap or the default.
func (q GatewayQueryConfig) EffectiveMaxRows() int64 {
	if q.MaxRows > 0 {
		return q.MaxRows
	}
	return DefaultGatewayQueryMaxRows
}

// EffectiveMaxBytes returns the configured byte cap or the default.
func (q GatewayQueryConfig) EffectiveMaxBytes() int64 {
	if q.MaxBytes > 0 {
		return q.MaxBytes
	}
	return DefaultGatewayQueryMaxBytes
}

// GatewayAnonymousConfig is the operator veto/extension for the anonymous
// allowlist.
type GatewayAnonymousConfig struct {
	Allow []string `yaml:"allow,omitempty"`
	Deny  []string `yaml:"deny,omitempty"`
}

// GatewayPinEntry pins one provider's published standard(s) for gateway
// serving. Standard is an SDS standard name ("OMM", "omm", or "OMM.fbs");
// empty, "*", or "all" pins every standard the peer publishes.
type GatewayPinEntry struct {
	Peer     string `yaml:"peer"`
	Standard string `yaml:"standard,omitempty"`
}

// normalizePinStandard maps a standard spelling to the bare upper-case
// standard name ("omm" / "OMM.fbs" -> "OMM"). ""/"*"/"all" -> "" (the
// config-side wildcard).
func normalizePinStandard(standard string) string {
	standard = strings.TrimSpace(standard)
	if fbs := strings.LastIndexByte(standard, '.'); fbs > 0 && strings.EqualFold(standard[fbs:], ".fbs") {
		standard = standard[:fbs]
	}
	if standard == "" || standard == "*" || strings.EqualFold(standard, "all") {
		return ""
	}
	return strings.ToUpper(standard)
}

// PinnedStandard reports whether gateway.pin opts the (peer, standard) pair
// into pinned gateway serving. schemaName accepts "OMM.fbs" / "OMM" / "omm";
// wildcards are config-side only, so a concrete standard is required here.
func (g GatewayConfig) PinnedStandard(peerID, schemaName string) bool {
	peerID = strings.TrimSpace(peerID)
	want := normalizePinStandard(schemaName)
	if peerID == "" || want == "" {
		return false
	}
	for _, entry := range g.Pin {
		if strings.TrimSpace(entry.Peer) != peerID {
			continue
		}
		pinned := normalizePinStandard(entry.Standard)
		if pinned == "" || pinned == want {
			return true
		}
	}
	return false
}

// PinnedPeers lists the distinct peer ids whose gateway.pin entries cover
// schemaName (used by the supersede evaluation hooks).
func (g GatewayConfig) PinnedPeers(schemaName string) []string {
	want := normalizePinStandard(schemaName)
	if want == "" {
		return nil
	}
	seen := make(map[string]bool, len(g.Pin))
	peers := make([]string, 0, len(g.Pin))
	for _, entry := range g.Pin {
		peer := strings.TrimSpace(entry.Peer)
		if peer == "" || seen[peer] {
			continue
		}
		pinned := normalizePinStandard(entry.Standard)
		if pinned == "" || pinned == want {
			seen[peer] = true
			peers = append(peers, peer)
		}
	}
	return peers
}

// IngestConfig runs the CelesTrak/Space-Track/UDL source-sync workers
// INSIDE the daemon process, against the daemon's own store handle.
//
// This is the single-writer topology (loop C.6b): the FlatSQL v2 store
// (in-process engine + compact record metadata + stream appenders) admits
// exactly one writer process, so a separate `spacedatanetwork ingest` service
// can no longer share a running daemon's storage path — it now fails with a
// store-lock error instead of corrupting the store. Enable this section on
// provider nodes instead of the standalone ingest unit. The standalone
// `ingest` verb remains supported for offline/standalone stores only.
//
// Credentials are intentionally NOT configurable here: Space-Track and UDL
// logins come from the SPACETRACK_IDENTITY/SPACETRACK_PASSWORD and
// UDL_USERNAME/UDL_PASSWORD environment variables (set them in the daemon
// service unit), so secrets never land in config files.
type IngestConfig struct {
	// Enabled starts the in-daemon ingest workers (full nodes only).
	Enabled bool `yaml:"enabled"`

	// RawPath is the raw source-snapshot archive directory.
	// Default: <storage-parent>/raw.
	RawPath string `yaml:"raw_path,omitempty"`

	// Sync cadences (Go duration strings). The CelesTrak cadences are
	// clamped to the 3h public-API minimum by the runner.
	CelestrakInterval    string `yaml:"celestrak_interval,omitempty"`     // default 3h
	SatcatInterval       string `yaml:"satcat_interval,omitempty"`        // default 24h
	SpaceWeatherInterval string `yaml:"space_weather_interval,omitempty"` // default 3h

	// Source URL overrides (defaults are the public CelesTrak endpoints).
	CelestrakCatalogURL      string `yaml:"celestrak_catalog_url,omitempty"`
	CelestrakSatcatURL       string `yaml:"celestrak_satcat_url,omitempty"`
	CelestrakSatcatCSVURL    string `yaml:"celestrak_satcat_csv_url,omitempty"`
	CelestrakSpaceWeatherURL string `yaml:"celestrak_space_weather_url,omitempty"`

	// Space-Track gap-fill worker (credentials via env, see above).
	SpaceTrackEnabled      bool   `yaml:"spacetrack_enabled"`
	SpaceTrackStartDay     string `yaml:"spacetrack_start_day,omitempty"`
	SpaceTrackBatchDays    int    `yaml:"spacetrack_batch_days,omitempty"`
	SpaceTrackBatchSleep   string `yaml:"spacetrack_batch_sleep,omitempty"`
	SpaceTrackPollInterval string `yaml:"spacetrack_poll_interval,omitempty"`
	SpaceTrackLoginURL     string `yaml:"spacetrack_login_url,omitempty"`
	SpaceTrackQueryTmpl    string `yaml:"spacetrack_query_template,omitempty"`

	// Supplemental Space-Track lanes (App 2 / A2.2c-ST). Both ride the same
	// spacetrack_enabled master switch; each can be individually opted out
	// (unset = enabled when spacetrack_enabled is true). Credentials via env,
	// as above.
	//   - publicfiles: operator-ephemeris CCSDS OEM (ISS/NASA-JSC today; new
	//     providers such as Kuiper/SpaceX onboard automatically when Space-Track
	//     shares files — no code change).
	//   - current-gp: full-catalog CCSDS OMM JSON snapshots (feeds A2.7).
	SpaceTrackPublicFilesEnabled *bool  `yaml:"spacetrack_publicfiles_enabled,omitempty"`
	SpaceTrackCurrentGPEnabled   *bool  `yaml:"spacetrack_current_gp_enabled,omitempty"`
	SpaceTrackSupplementalPoll   string `yaml:"spacetrack_supplemental_poll_interval,omitempty"` // default 6h
	SpaceTrackCurrentGPQueryURL  string `yaml:"spacetrack_current_gp_query_url,omitempty"`
	SpaceTrackPublicFilesBaseURL string `yaml:"spacetrack_publicfiles_base_url,omitempty"`

	// Unified Data Library sync worker (credentials via env, see above).
	UDLEnabled      bool   `yaml:"udl_enabled"`
	UDLBaseURL      string `yaml:"udl_base_url,omitempty"`
	UDLStartDay     string `yaml:"udl_start_day,omitempty"`
	UDLBatchDays    int    `yaml:"udl_batch_days,omitempty"`
	UDLBatchSleep   string `yaml:"udl_batch_sleep,omitempty"`
	UDLPollInterval string `yaml:"udl_poll_interval,omitempty"`
	UDLMaxResults   int    `yaml:"udl_max_results,omitempty"`

	// HTTPTimeout bounds each source fetch (default 90s; raise for the
	// full-catalog CelesTrak GP fetch on slow links, e.g. 900s).
	HTTPTimeout string `yaml:"http_timeout,omitempty"`

	// MinFreeDiskGB refuses to start a sync cycle below this free-disk
	// floor (0 = built-in 5 GiB default).
	MinFreeDiskGB float64 `yaml:"min_free_disk_gb,omitempty"`

	// DatasetPublishURL is the local admin dataset-publication endpoint
	// called after successful CelesTrak syncs (usually this daemon's own
	// admin listener). Env override: SDN_DATASET_PUBLISH_URL.
	DatasetPublishURL string `yaml:"dataset_publish_url,omitempty"`
}

// PoliciesConfig configures the ABAC policy engine.
type PoliciesConfig struct {
	// Enabled activates the ABAC policy engine.  When false (the default) the
	// existing trust-level checks are the sole gate and all policy evaluation is
	// skipped — behaviour is identical to a pre-ABAC deployment.
	Enabled bool `yaml:"enabled"`

	// DefaultEffect is "allow" or "deny".  Applied when no rule matches a
	// request.  Defaults to "deny" when empty.
	DefaultEffect string `yaml:"default_effect,omitempty"`

	// Path is the filesystem path to a YAML policy file.  When set, rules are
	// loaded from the file at startup.  InlineRules (below) are appended after
	// the file rules.
	Path string `yaml:"path,omitempty"`

	// InlineRules are policy rules specified directly in the config file.
	// Useful for simple deployments that do not need a separate policy file.
	// Each entry is stored as a raw YAML node so it can be decoded by the abac
	// package without introducing an import cycle in config.
	InlineRules []map[string]interface{} `yaml:"inline_rules,omitempty"`
}

// FlowsConfig controls the flow orchestration runtime.
type FlowsConfig struct {
	// Enabled enables flow loading and execution.
	Enabled bool `yaml:"enabled"`

	// StoragePath is the directory for installed flow artifacts.
	// Default: {storage.path}/flows
	StoragePath string `yaml:"storage_path"`

	// MaxFlows is the maximum number of concurrently running flows.
	MaxFlows int `yaml:"max_flows"`

	// MaxMemoryPages is the WasmEdge memory limit per flow (in 64KB pages).
	MaxMemoryPages uint32 `yaml:"max_memory_pages"`

	// EditorEnabled serves the SDN runtime editor on the admin interface.
	EditorEnabled bool `yaml:"editor_enabled"`

	// EditorPath is the URL base path for the editor (default: /flow-editor).
	EditorPath string `yaml:"editor_path"`

	// Mounts maps HTTP listener paths to flow modules. Daemon startup only
	// registers lazy handlers; each handler loads the existing compiled WASM
	// artifact on first request. The HTTP handler is pure socket plumbing
	// ($HTQ request frames in, $HTR response frames out) with zero
	// request-level decisions in the host.
	Mounts []FlowMount `yaml:"mounts,omitempty"`

	// Services are timer-served flows (loop C.8a ingest-as-flow): each entry
	// loads a compiled flow bundle whose triggers are cron timers and
	// registers it with the plugin manager's cron scheduler. Which flow runs
	// on which schedule with which node CONFIG is configuration, never Go
	// code.
	Services []FlowService `yaml:"services,omitempty"`
}

// FlowService declares one timer-served flow.
type FlowService struct {
	// Flow references the flow module: an installed flow program ID, or a
	// filesystem path to a compiled flow bundle directory (containing
	// runtime.wasm + flow.json) or directly to a .wasm artifact.
	Flow string `yaml:"flow"`

	// MemoryPages caps the instance's linear memory (64KB pages).
	// Default (0): the flows.max_memory_pages global.
	MemoryPages uint32 `yaml:"memory_pages,omitempty"`

	// Config is the node-config block served to the flow's nodes through the
	// builtin plugin.getConfig hostcall (e.g. celestrak_gp_url overrides).
	Config map[string]interface{} `yaml:"config,omitempty"`

	// Intervals overrides trigger intervals by trigger id (Go duration
	// strings, e.g. timer-gp: "6h"). Default: the flow.json trigger
	// defaults.
	Intervals map[string]string `yaml:"intervals,omitempty"`
}

// FlowMount binds one HTTP listener path to one flow module. Route → module
// mapping is configuration, never Go code.
type FlowMount struct {
	// Path is the HTTP mux pattern the flow owns (e.g. "/api/v1/data/").
	Path string `yaml:"path"`

	// Flow references the flow module: an installed flow program ID, or a
	// filesystem path to a compiled flow bundle directory (containing
	// runtime.wasm) or directly to a .wasm artifact.
	Flow string `yaml:"flow"`

	// Pool is the number of flow instances serving this mount. Requests are
	// serialized per instance (one linear memory each), so the pool size
	// bounds concurrently served requests. Default (<= 0): 4.
	Pool int `yaml:"pool,omitempty"`

	// MemoryPages caps each pooled instance's linear memory (64KB pages).
	// Default (0): the flows.max_memory_pages global.
	MemoryPages uint32 `yaml:"memory_pages,omitempty"`
}

// PublishingConfig controls remote data publishing via the API.
type PublishingConfig struct {
	// Enabled enables the data publish endpoint.
	Enabled bool `yaml:"enabled"`

	// AllowedSchemas restricts which schemas can be published. Empty = all.
	AllowedSchemas []string `yaml:"allowed_schemas"`

	// MaxRecordBytes is the maximum size of a single record (default: 10MB).
	MaxRecordBytes int `yaml:"max_record_bytes"`

	// DefaultQuotaBytes is the per-peer storage quota (default: 100MB).
	DefaultQuotaBytes int64 `yaml:"default_quota_bytes"`

	// MinTrustLevel is the minimum peer trust level for publishing (default: "standard").
	MinTrustLevel string `yaml:"min_trust_level"`
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
	// XPub is a standard BIP-32 extended public key (Base58Check, starts with "xpub").
	// Generate with: spacedatanetwork derive-xpub
	XPub string `yaml:"xpub"`

	// SigningPubKeyHex is an optional Ed25519 public key (32 bytes hex).
	// When omitted, the signing key is bound on first wallet login (TOFU).
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
	Path string `yaml:"path"`

	// MaxSize is the disk-quota cap enforced by FlatSQLStore.
	// GarbageCollectToQuota (Task D3). Two forms:
	//   - a percentage of the filesystem holding Path, e.g. "90%" — the
	//     default (DefaultStorageMaxSizePercent) when MaxSize is empty.
	//   - an absolute size, e.g. "10GB", "500MB", "2TB", or a bare integer
	//     byte count. Units are binary (1GB = 1<<30 bytes, matching this
	//     codebase's other byte-size constants, e.g.
	//     DefaultGatewayQueryMaxBytes = 128 << 20); KiB/MiB/GiB/TiB are
	//     accepted as explicit synonyms.
	// Resolve with ResolveMaxSizeBytes (percentages need Statfs against a
	// real path, so resolution is lazy — done at node startup, not here).
	MaxSize string `yaml:"max_size"`

	// GCInterval is a Go duration string (e.g. "1h") controlling how often
	// the periodic storage-quota GC loop runs (node.go's
	// runStorageQuotaGC). Empty resolves to 1h (ResolveGCInterval).
	GCInterval string `yaml:"gc_interval"`

	// EngineHotWindow bounds the records resident per schema in the in-memory
	// FlatSQL-WASM engine (the query hot window). Older records are evicted
	// from the ENGINE only — stream files, compact metadata, and datasync
	// cursors keep the full history. 0 = built-in default (400K records,
	// sized against the engine's 4 GiB wasm32 ceiling).
	EngineHotWindow int `yaml:"engine_hot_window,omitempty"`
}

// DefaultStorageMaxSizePercent is the disk-quota percentage StorageConfig
// resolves to when MaxSize is empty (or explicitly "90%" as Default()
// sets it): 90% of the filesystem holding storage.path.
const DefaultStorageMaxSizePercent = 90

// DefaultStorageGCInterval is the periodic storage-quota GC cadence used
// when storage.gc_interval is empty.
const DefaultStorageGCInterval = time.Hour

// storageMaxSizeSpec is the parsed (but not yet disk-resolved) form of
// StorageConfig.MaxSize.
type storageMaxSizeSpec struct {
	isPercent bool
	percent   float64 // (0, 100]
	bytes     int64   // absolute byte count, only meaningful when !isPercent
}

// resolve turns a parsed spec into an absolute byte cap. A percentage spec
// needs Statfs against a real, ideally-existing path — nearestExistingDir
// walks up to the closest existing ancestor so this still works before
// storage.path itself has been created (e.g. first-run config validation).
func (spec storageMaxSizeSpec) resolve(storagePath string) (int64, error) {
	if !spec.isPercent {
		return spec.bytes, nil
	}
	total, err := diskTotalBytes(nearestExistingDir(storagePath))
	if err != nil {
		return 0, fmt.Errorf("statfs %q: %w", storagePath, err)
	}
	return int64(float64(total) * spec.percent / 100.0), nil
}

// parseStorageMaxSizeSpec validates and parses a storage.max_size string
// WITHOUT touching the filesystem — this is what Config.validate() calls so
// a malformed value (typo'd unit, out-of-range percentage, ...) fails
// config Load() with a clear error instead of surfacing as a confusing
// runtime failure the first time the quota GC loop resolves it.
func parseStorageMaxSizeSpec(spec string) (storageMaxSizeSpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = fmt.Sprintf("%d%%", DefaultStorageMaxSizePercent)
	}
	if strings.HasSuffix(spec, "%") {
		pctStr := strings.TrimSpace(strings.TrimSuffix(spec, "%"))
		pct, err := strconv.ParseFloat(pctStr, 64)
		if err != nil {
			return storageMaxSizeSpec{}, fmt.Errorf("invalid percentage %q: %w", spec, err)
		}
		if pct <= 0 || pct > 100 {
			return storageMaxSizeSpec{}, fmt.Errorf("percentage %q out of range (0,100]", spec)
		}
		return storageMaxSizeSpec{isPercent: true, percent: pct}, nil
	}
	b, err := parseByteSize(spec)
	if err != nil {
		return storageMaxSizeSpec{}, err
	}
	if b <= 0 {
		return storageMaxSizeSpec{}, fmt.Errorf("size %q must be positive", spec)
	}
	return storageMaxSizeSpec{bytes: b}, nil
}

// byteSizeUnits maps size-string suffixes to their byte multiplier, binary
// (1024-based) throughout to match this codebase's other byte-size
// constants (e.g. DefaultGatewayQueryMaxBytes = 128 << 20). Longer/more
// specific suffixes are listed first so e.g. "10GB" matches "GB" — not the
// generic trailing "B" — via the first-match-wins scan in parseByteSize.
var byteSizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"TIB", 1 << 40}, {"TB", 1 << 40},
	{"GIB", 1 << 30}, {"GB", 1 << 30},
	{"MIB", 1 << 20}, {"MB", 1 << 20},
	{"KIB", 1 << 10}, {"KB", 1 << 10},
	{"B", 1},
}

// parseByteSize parses an absolute size string ("10GB", "512MiB", "100",
// ...) into a byte count. A bare integer (no unit suffix) is interpreted
// as a byte count directly.
func parseByteSize(spec string) (int64, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	upper := strings.ToUpper(trimmed)
	for _, u := range byteSizeUnits {
		if !strings.HasSuffix(upper, u.suffix) {
			continue
		}
		numPart := strings.TrimSpace(trimmed[:len(trimmed)-len(u.suffix)])
		if numPart == "" {
			continue
		}
		val, err := strconv.ParseFloat(numPart, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q: %w", spec, err)
		}
		if val < 0 {
			return 0, fmt.Errorf("invalid size %q: negative", spec)
		}
		return int64(val * float64(u.mult)), nil
	}
	val, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: no recognized unit suffix (B/KB/MB/GB/TB, optionally KiB/MiB/GiB/TiB) and not a bare byte integer", spec)
	}
	return val, nil
}

// ResolveMaxSizeBytes resolves StorageConfig.MaxSize against the
// filesystem holding storagePath: an absolute size ("10GB") parses
// directly, a percentage ("90%", or the default when MaxSize is empty)
// resolves against Statfs-reported total filesystem capacity. Call at node
// startup (after storage.path exists), not from Config.validate() — that
// only checks syntax (parseStorageMaxSizeSpec), since a percentage cannot
// be resolved without touching the filesystem.
func (c StorageConfig) ResolveMaxSizeBytes(storagePath string) (int64, error) {
	spec, err := parseStorageMaxSizeSpec(c.MaxSize)
	if err != nil {
		return 0, fmt.Errorf("storage.max_size: %w", err)
	}
	bytes, err := spec.resolve(storagePath)
	if err != nil {
		return 0, fmt.Errorf("storage.max_size: %w", err)
	}
	return bytes, nil
}

// resolveGCInterval parses storage.gc_interval as a Go duration string,
// defaulting to DefaultStorageGCInterval when empty.
func resolveGCInterval(spec string) (time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return DefaultStorageGCInterval, nil
	}
	d, err := time.ParseDuration(spec)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", spec, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %q", spec)
	}
	return d, nil
}

// ResolveGCInterval parses StorageConfig.GCInterval, defaulting to
// DefaultStorageGCInterval (1h) when empty.
func (c StorageConfig) ResolveGCInterval() (time.Duration, error) {
	d, err := resolveGCInterval(c.GCInterval)
	if err != nil {
		return 0, fmt.Errorf("storage.gc_interval: %w", err)
	}
	return d, nil
}

// nearestExistingDir walks up from path until it finds a directory that
// exists (bounded to avoid spinning on a pathological input), so Statfs
// can resolve a percentage-of-disk quota even before storage.path itself
// has been created (e.g. first-run config validation).
func nearestExistingDir(path string) string {
	p := path
	for i := 0; i < 64; i++ {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
	return p
}

// SchemaConfig contains schema validation settings.
type SchemaConfig struct {
	Validate bool `yaml:"validate"`
	Strict   bool `yaml:"strict"`
}

// SecurityConfig contains security-related settings.
type SecurityConfig struct {
	// KeyPassword is the password used to encrypt/decrypt the mnemonic at rest.
	// If empty, a machine-derived password is used (hostname + arch + OS via Argon2).
	// Can also be set via SDN_KEY_PASSWORD environment variable.
	KeyPassword string `yaml:"key_password,omitempty"`

	// RequireSignedFeedHeads rejects dataset feed-head announcements that do
	// not carry an Ed25519 payload signature. Invalid signatures are always
	// rejected; this flag additionally rejects unsigned announcements, for
	// networks where every publisher has upgraded to signed feed heads.
	RequireSignedFeedHeads bool `yaml:"require_signed_feed_heads,omitempty"`
}

// TorConfig contains local TOR runtime settings.
type TorConfig struct {
	// Enabled starts a local tor daemon and routes outbound HTTP through it.
	Enabled bool `yaml:"enabled"`

	// BinaryPath points to the tor executable (default: "tor" in PATH).
	BinaryPath string `yaml:"binary_path"`

	// DataDir is the base directory for tor state (defaults to <storage-parent>/tor).
	DataDir string `yaml:"data_dir"`

	// SocksAddress is the local SOCKS listener, e.g. "127.0.0.1:9050".
	SocksAddress string `yaml:"socks_address"`

	// StartTimeout controls how long to wait for tor bootstrap/startup.
	StartTimeout string `yaml:"start_timeout"`

	// HiddenServiceEnabled publishes the node website as a v3 onion service.
	HiddenServiceEnabled bool `yaml:"hidden_service_enabled"`

	// HiddenServicePort is the virtual onion service port (80 or 443).
	HiddenServicePort int `yaml:"hidden_service_port"`

	// HiddenServiceTarget overrides local forward target (host:port).
	// If empty, admin.listen_addr is used with loopback host normalization.
	HiddenServiceTarget string `yaml:"hidden_service_target"`

	// BypassLocalAddresses preserves direct localhost access for local-only services.
	BypassLocalAddresses bool `yaml:"bypass_local_addresses"`
}

// PeersConfig contains peer trust registry settings.
type PeersConfig struct {
	// StrictMode only allows connections to/from peers in the trusted registry.
	// When disabled, unknown peers get Standard trust level by default.
	StrictMode bool `yaml:"strict_mode"`

	// RegistryPath is an optional JSON snapshot path for explicit
	// operator-managed peer registry persistence. Production nodes store peer
	// registry state as SDS PRR/PGM records in the node FlatSQL store.
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

// DefaultIPFSAPIURL is the Kubo RPC API endpoint AdminConfig.IPFSAPIURL
// defaults to: the embedded/local Kubo node's RPC address. Pinning-gated
// paths (dataset publication, trusted-PNM materialization, demo payload
// pinning) key off AdminConfig.IPFSAPIURL being non-empty, so defaulting it
// here — rather than to "" — turns pinning on by default. Operators can
// still opt out by setting `ipfs_api_url: ""` explicitly in config.yaml.
const DefaultIPFSAPIURL = "http://127.0.0.1:5001"

// AdminConfig contains admin interface settings.
type AdminConfig struct {
	// Enabled enables the admin web interface.
	Enabled bool `yaml:"enabled"`

	// ListenAddr is the address for the admin interface (default: 127.0.0.1:5001).
	ListenAddr string `yaml:"listen_addr"`

	// HTTPChallengeAddr is the HTTP listener used for ACME HTTP-01 challenges
	// and HTTPS redirects when managed TLS is enabled.
	HTTPChallengeAddr string `yaml:"http_challenge_addr"`

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

	// TLSMode selects native server TLS behavior: disabled, static, or managed.
	// When empty, the legacy tls_enabled + cert/key fields are backfilled.
	TLSMode string `yaml:"tls_mode"`

	// TLSCacheDir is the writable directory used for bootstrap and ACME TLS state.
	TLSCacheDir string `yaml:"tls_cache_dir"`

	// TLSHosts is the list of explicit hostnames allowed for managed certificate issuance.
	TLSHosts []string `yaml:"tls_hosts"`

	// FrontendPath is the filesystem path to the public-facing frontend directory.
	// This directory is served at "/" as a static file server with SPA fallback.
	// Default: "" (resolved at runtime to a built sdn-js/ui/dist when available,
	// then ~/.spacedatanetwork/frontend/).
	// The fallback directory is created automatically with a default page if it
	// doesn't exist.
	// Override with SDN_FRONTEND_PATH env var or set explicitly in config.
	FrontendPath string `yaml:"frontend_path"`

	// AdminUIPath is the filesystem path to the built isomorphic SDN admin client.
	// When set and valid, it is served at "/admin" instead of the legacy inline admin UI.
	// Override with SDN_ADMIN_UI_PATH env var or set explicitly in config.
	AdminUIPath string `yaml:"admin_ui_path"`

	// HomepageFile is an optional single-file HTML app served at "/" and "/index.html".
	// Deprecated: use frontend_path instead. If frontend_path is set, this is ignored.
	// If empty, the built-in default landing page is served.
	HomepageFile string `yaml:"homepage_file"`

	// WebuiPath is the filesystem path to an IPFS WebUI build directory (webui/build).
	// When set, the IPFS WebUI is served at "/webui".
	// If empty, the admin panel uses the built-in admin UI and /webui is not mounted.
	WebuiPath string `yaml:"webui_path"`

	// IPFSAPIURL is the base URL of an upstream Kubo RPC API endpoint (no path),
	// e.g. "http://127.0.0.1:5001". When set, the admin server reverse-proxies
	// requests to "/api/v0/*" to this endpoint so the React WebUI can talk to IPFS
	// through the authenticated SDN admin server. It also gates record pinning
	// (dataset publication, trusted-PNM materialization, demo payload pinning):
	// those paths are opt-in only when this is non-empty.
	//
	// Defaults to the embedded/local Kubo RPC ("http://127.0.0.1:5001") so
	// pinning is on out of the box — see Default(). Set to an explicit empty
	// string in config.yaml (ipfs_api_url: "") to disable pinning entirely;
	// Load() starts from Default() and overlays the YAML document, so an
	// explicit empty value in the file overrides the default and is
	// preserved, while simply omitting the key keeps the default.
	IPFSAPIURL string `yaml:"ipfs_api_url"`

	// IPFSGatewayURL is the base URL of an upstream Kubo HTTP gateway (no path),
	// e.g. "http://127.0.0.1:8080". When set, the admin server reverse-proxies
	// requests to "/ipfs/*" so the WebUI can fetch IPFS content without needing
	// direct access to the Kubo gateway port.
	IPFSGatewayURL string `yaml:"ipfs_gateway_url"`

	// WalletUIPath is the filesystem path to the hd-wallet-ui dist directory.
	// If empty, the login page loads wallet UI from CDN (unpkg.com/hd-wallet-ui).
	WalletUIPath string `yaml:"wallet_ui_path"`

	// TrustedProxy is the IP address of a trusted reverse proxy. When set,
	// the server will trust X-Forwarded-Proto from this IP for cookie Secure flag.
	// Set to "loopback" to trust any loopback address (127.0.0.0/8, ::1).
	TrustedProxy string `yaml:"trusted_proxy"`
}

// EffectiveTLSMode backfills the new tls_mode setting from legacy config.
func (a AdminConfig) EffectiveTLSMode() string {
	mode := strings.ToLower(strings.TrimSpace(a.TLSMode))
	if mode != "" {
		return mode
	}
	if !a.TLSEnabled {
		return "disabled"
	}
	if strings.TrimSpace(a.TLSCertFile) != "" && strings.TrimSpace(a.TLSKeyFile) != "" {
		return "static"
	}
	return "managed"
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
				"/ip4/0.0.0.0/udp/4003/webrtc-direct",
			},
			Bootstrap:      bootstrap.DefaultBootstrapAddresses(),
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
			Path: dataPath,
			// 90% of the filesystem holding Path (DefaultStorageMaxSizePercent) —
			// an explicit "10GB"-style absolute size still works (back-compat).
			MaxSize:    fmt.Sprintf("%d%%", DefaultStorageMaxSizePercent),
			GCInterval: "1h",
		},
		Schemas: SchemaConfig{
			Validate: true,
			Strict:   true,
		},
		Security: SecurityConfig{},
		Tor: TorConfig{
			Enabled:              false,
			BinaryPath:           "tor",
			DataDir:              "",
			SocksAddress:         "127.0.0.1:9050",
			StartTimeout:         "30s",
			HiddenServiceEnabled: false,
			HiddenServicePort:    0, // auto: 80 (HTTP) or 443 (HTTPS)
			HiddenServiceTarget:  "",
			BypassLocalAddresses: true,
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
			Enabled:           true,
			ListenAddr:        "127.0.0.1:5001",
			HTTPChallengeAddr: "127.0.0.1:5080",
			RequireAuth:       true, // Require authentication by default
			SessionExpiry:     "24h",
			TOTPRequired:      false,
			TLSEnabled:        false,
			TLSCertFile:       "",
			TLSKeyFile:        "",
			TLSMode:           "",
			TLSCacheDir:       filepath.Join(dataPath, "tls"),
			TLSHosts:          nil,
			FrontendPath:      "",
			AdminUIPath:       "",
			HomepageFile:      "",
			WebuiPath:         "",
			IPFSAPIURL:        DefaultIPFSAPIURL,
			WalletUIPath:      "",
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
		Publishing: PublishingConfig{
			Enabled:           true,
			AllowedSchemas:    []string{},
			MaxRecordBytes:    10 * 1024 * 1024,  // 10MB
			DefaultQuotaBytes: 100 * 1024 * 1024, // 100MB
			MinTrustLevel:     "standard",
		},
		AssetPins: AssetPinConfig{
			Enabled:          false,
			Issuer:           "https://token.actions.githubusercontent.com",
			Audience:         "sdn-asset-models",
			Repository:       "DigitalArsenal/asset-models",
			Ref:              "refs/heads/main",
			PinWorkflow:      "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main",
			DecisionWorkflow: "DigitalArsenal/asset-models/.github/workflows/review-decision.yml@refs/heads/main",
			GatewayURL:       "https://sdn.spaceaware.io/ipfs",
			KuboRepoPath:     "/mnt/volume_nyc3_01/ipfs",
		},
		Flows: FlowsConfig{
			Enabled:        true,
			StoragePath:    filepath.Join(dataPath, "flows"),
			MaxFlows:       50,
			MaxMemoryPages: 1024, // 64MB per flow
			EditorEnabled:  false,
			EditorPath:     "/flow-editor",
			// The public /api/v1/data/* record-retrieval surface is served by
			// the data-retrieval FLOW module (loop C.4 cutover): all routing,
			// param parsing, profile resolution, format selection and
			// ETag/304 logic live inside the wasm flow. Exact-match native
			// routes (health, summary, datasync scan/stream/query, records/)
			// still take mux precedence over this subtree mount. The flow
			// reference is the installed flow program ID (delivered via SDN
			// module delivery); the route is registered at startup and returns
			// 503 until the artifact is installed.
			Mounts: []FlowMount{
				{
					Path:        "/api/v1/data/",
					Flow:        DataRetrievalFlowID,
					Pool:        4,
					MemoryPages: 8192, // 512MB per instance: bulk streams + JSON encode live in flow memory
				},
			},
		},
	}
}

// DataRetrievalFlowID is the program ID of the flow bundle serving
// /api/v1/data/* (space-data-network-modules flows/data-retrieval).
const DataRetrievalFlowID = "com.digitalarsenal.flows.data-retrieval"

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

// DefaultAdminUIPath returns the standard admin UI directory path.
func DefaultAdminUIPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".spacedatanetwork", "admin-ui")
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
			cfg := Default()
			if verr := cfg.validate(); verr != nil {
				return nil, verr
			}
			return cfg, nil
		}
		return nil, err
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate performs configuration checks that cannot be expressed as struct
// tags, and that must fail the load rather than silently start the server
// with a security-relevant misconfiguration. It runs unconditionally at the
// end of Load, on both the default config and a config file that was
// successfully parsed.
func (c *Config) validate() error {
	if c.AssetPins.MaxUploadBytes < 0 {
		return fmt.Errorf("asset_pins.max_upload_bytes must not be negative")
	}
	if c.AssetPins.MinFreeBytes < 0 {
		return fmt.Errorf("asset_pins.min_free_bytes must not be negative")
	}
	if c.AssetPins.RetentionInterval < 0 {
		return fmt.Errorf("asset_pins.retention_interval must not be negative")
	}
	if c.AssetPins.Enabled {
		required := []struct {
			path  string
			value string
		}{
			{path: "asset_pins.issuer", value: c.AssetPins.Issuer},
			{path: "asset_pins.audience", value: c.AssetPins.Audience},
			{path: "asset_pins.repository", value: c.AssetPins.Repository},
			{path: "asset_pins.ref", value: c.AssetPins.Ref},
			{path: "asset_pins.pin_workflow", value: c.AssetPins.PinWorkflow},
			{path: "asset_pins.decision_workflow", value: c.AssetPins.DecisionWorkflow},
			{path: "asset_pins.gateway_url", value: c.AssetPins.GatewayURL},
			{path: "asset_pins.kubo_repo_path", value: c.AssetPins.KuboRepoPath},
		}
		for _, field := range required {
			if strings.TrimSpace(field.value) == "" {
				return fmt.Errorf("%s is required when asset_pins.enabled is true", field.path)
			}
		}
	}
	if strings.TrimSpace(c.AssetPins.GatewayURL) != "" {
		gatewayURL, err := url.Parse(c.AssetPins.GatewayURL)
		if err != nil || !strings.EqualFold(gatewayURL.Scheme, "https") || gatewayURL.Hostname() == "" {
			return fmt.Errorf("asset_pins.gateway_url must be an absolute HTTPS URL")
		}
	}
	for _, mount := range c.Flows.Mounts {
		if !strings.HasPrefix(mount.Path, "/api/") {
			return fmt.Errorf(
				"flows.mounts: path %q must begin with \"/api/\" — the admin auth wall (cmd/spacedatanetwork/main.go's isAPIOrPlugin check) only evaluates a mounted flow's declared anonymous route policy for paths under /api/; a mount outside that prefix would be served without the wall ever enforcing auth for it",
				mount.Path,
			)
		}
	}
	// Syntax-only checks (Task D3): percentage resolution needs Statfs
	// against a real path, so full ResolveMaxSizeBytes stays lazy (node
	// startup) — but a malformed spec should fail Load() immediately
	// rather than surface as a confusing runtime error later.
	if _, err := parseStorageMaxSizeSpec(c.Storage.MaxSize); err != nil {
		return fmt.Errorf("storage.max_size: %w", err)
	}
	if _, err := resolveGCInterval(c.Storage.GCInterval); err != nil {
		return fmt.Errorf("storage.gc_interval: %w", err)
	}
	return nil
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
