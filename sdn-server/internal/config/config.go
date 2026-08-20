// Package config provides configuration management for the SDN server.
package config

import (
	"errors"
	"fmt"
	"net"
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
	// SourcePath is the config file this Config was loaded from, recorded so
	// surfaces that tell an operator "change it in the config" can name the
	// REAL file. Never serialized: it is where the config came from, not part
	// of it. Empty when the defaults were used (no file on disk).
	SourcePath string `yaml:"-"`

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
	Modules    ModulesConfig    `yaml:"modules"`
	GeoIP      GeoIPConfig      `yaml:"geoip"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	WalletWasm WalletWasmConfig `yaml:"wallet_wasm"`
	Status     StatusConfig     `yaml:"status"`
	Apps       AppsConfig       `yaml:"apps"`
	Update     UpdateConfig     `yaml:"update"`
}

// UpdateConfig governs whether this install acts on pushed update signals.
//
// OWNER RULING 2026-08-09: "We should be building locally and then pushing an
// update signal to all installs to upgrade in place". Enabled is therefore
// DEFAULT TRUE — the ruling is about all installs, and a default of false would
// mean every box needed a config edit before the mechanism the ruling describes
// did anything, which is the manual step it exists to remove.
//
// That default is safe because it is not the only gate. A signal is acted on
// only if it is signed by a key in this bundle's trust roots, targets this
// platform/arch/kind and channel, carries a sequence above the installed one,
// does not declare a source-lineage rollback, and names an update this box has
// not already tried and reversed. Everything it points at is then re-fetched
// over HTTPS and verified against the signed manifest before a byte moves.
//
// A box opts out with `update: {enabled: false}`. That is the recorded way to
// hold a box back — not silence on the publisher side, which would hold back
// the whole fleet.
type UpdateConfig struct {
	// Enabled acts on update signals. Default true.
	Enabled bool `yaml:"enabled"`

	// Topic overrides the signal topic. Default: the pubsubTopic declared by
	// this bundle's own manifest.json, so the bundle stays the source of truth
	// for where its channel talks.
	Topic string `yaml:"topic,omitempty"`

	// Channel overrides the channel this box accepts signals for. Default: the
	// channel in this bundle's manifest.json.
	Channel string `yaml:"channel,omitempty"`

	// HealthTimeoutSeconds bounds the helper's post-restart health wait.
	// Default 600. The helper's own default is 60 s, which is correct for an
	// 18-second-boot VM and WRONG for a store-heavy node whose boot replays the
	// record catalog for minutes — a 60 s gate already rolled back a healthy
	// update on host-02 once (graph: ops-update-lane-restart-policy-preflight).
	// "Takes as long as it takes" is an owner rule; the gate must be able to
	// say it too.
	HealthTimeoutSeconds int `yaml:"health_timeout_seconds,omitempty"`

	// MinIntervalSeconds is the floor between two self-upgrades on this box.
	// Default 300. It bounds the blast radius of a publisher that signals in a
	// loop: a box will decline to swap itself more often than this regardless
	// of how many valid signals arrive.
	MinIntervalSeconds int `yaml:"min_interval_seconds,omitempty"`

	// MaxDelaySeconds spreads a fleet-wide roll over a window instead of
	// restarting every box at once. Zero (the default) means act immediately,
	// which is right for a fleet whose boxes hold different roles; set it on a
	// fleet where several boxes serve the same surface.
	MaxDelaySeconds int `yaml:"max_delay_seconds,omitempty"`
}

// HealthTimeout is the resolved post-restart health budget.
func (u UpdateConfig) HealthTimeout() time.Duration {
	if u.HealthTimeoutSeconds > 0 {
		return time.Duration(u.HealthTimeoutSeconds) * time.Second
	}
	return 600 * time.Second
}

// MinInterval is the resolved floor between self-upgrades.
func (u UpdateConfig) MinInterval() time.Duration {
	if u.MinIntervalSeconds > 0 {
		return time.Duration(u.MinIntervalSeconds) * time.Second
	}
	return 300 * time.Second
}

// AppsConfig declares the node's DEFAULT $APP per runtime class (owner ruling
// 2026-08-04: "there needs to be a default $APP for the SDN node software
// (server or browser), it's the Dashboard for the server and the
// orbital-console for the browser").
//
// This is DEPLOYED DATA, not code — exactly like the $PMM catalog. WHICH app a
// given node opens is an operator ruling that varies by node, so it arrives in
// config and never as a table compiled into the daemon. Leaving the whole
// section out is the shipped default: the node registers its own embedded
// dashboard as the server-class default and advertises no browser app.
type AppsConfig struct {
	// DefaultServerApp / DefaultBrowserApp name the $APP ID each runtime class
	// opens. Empty means "resolve automatically": if exactly one app of that
	// class is registered it is the default, otherwise the node reports no
	// default rather than guessing between candidates.
	// YAML: apps.default_server_app / apps.default_browser_app.
	DefaultServerApp  string `yaml:"default_server_app,omitempty"`
	DefaultBrowserApp string `yaml:"default_browser_app,omitempty"`

	// Declared lists apps this node ADVERTISES but holds no $APP record for —
	// the browser console published elsewhere is the motivating case. A
	// declared entry is a pointer (id + where to open it), never a claim about
	// content the node cannot show: the defaults surface marks it
	// state=declared and serves no record for it.
	// YAML: apps.declared.
	Declared []DeclaredAppConfig `yaml:"declared,omitempty"`

	// Installed lists $APP records this node LOADS FROM DISK and serves at
	// /api/v1/apps/records/<ID> — the operator-deployed-record tier between
	// the Go-minted dashboard and the future pulled-record install lane. The
	// record file is operator-deployed data with the same trust posture as
	// this config file itself. YAML: apps.installed.
	Installed []InstalledAppConfig `yaml:"installed,omitempty"`
}

// InstalledAppConfig is one entry of apps.installed.
type InstalledAppConfig struct {
	// ID must equal the record's own $APP ID — a cross-check that the file
	// on disk is the record the operator meant (mistakes must be seen).
	ID string `yaml:"id"`
	// RuntimeClass is "server" or "browser" (NODE/PAGE accepted as synonyms).
	RuntimeClass string `yaml:"runtime_class"`
	// URL is where the runtime opens the app.
	URL string `yaml:"url"`
	// RecordPath is the on-disk size-prefixed $APP FlatBuffer.
	RecordPath string `yaml:"record_path"`
}

// DeclaredAppConfig is one entry of apps.declared.
type DeclaredAppConfig struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name,omitempty"`
	Version     string `yaml:"version,omitempty"`
	Description string `yaml:"description,omitempty"`
	// RuntimeClass is "server" or "browser" (the $APP schema's NODE / PAGE
	// appRuntimeTarget names are accepted as synonyms).
	RuntimeClass string `yaml:"runtime_class"`
	// URL is where the runtime opens the app.
	URL string `yaml:"url"`
}

// ModulesConfig tunes the module-sdk WASM runtime (internal/modulert).
type ModulesConfig struct {
	// ScheduledInvokeTimeout is the per-call wall-clock budget for a SCHEDULED
	// module method run — a cron ticker fire or the run-now admin action.
	// INTERACTIVE (protocol/HTTP) invokes are unaffected and keep the runtime's
	// tight built-in default. Raise this on slow hosts (e.g. a single-vCPU
	// node) where a data-source pull — fetch → parse ephemeris → per-record
	// CID + sign → publish — legitimately needs minutes; production evidence
	// showed every catalog cron pull tripping the 30s interactive cap. When
	// unset (0) the module runtime applies its built-in default (10m). The fuel
	// budget scales proportionally with this value. YAML:
	// modules.scheduled_invoke_timeout, e.g. "10m", "20m".
	ScheduledInvokeTimeout time.Duration `yaml:"scheduled_invoke_timeout"`

	// EgressMinInterval is the minimum spacing between OUTBOUND module HTTP
	// requests to a destination host (hostname → Go duration string, e.g.
	// "celestrak.org": "5s"). This is host egress policy, not application
	// configuration: the host owns the network hook, so the host decides how
	// hard that hook may hit a third party, and a wasm module can neither see
	// nor bypass it.
	//
	// Compiled-in floors always apply on top, so this setting can only make a
	// node POLITER, never ruder — in particular the binding CelesTrak fetch
	// policy (2.5 s serial) is enforced whether or not it is configured here.
	// YAML: modules.egress_min_interval.
	EgressMinInterval map[string]string `yaml:"egress_min_interval,omitempty"`
}

// EffectiveEgressMinIntervals parses the configured per-host egress spacing,
// skipping entries that do not parse (they are logged by the caller). Compiled
// -in floors are applied by the pacer itself, not here.
func (c ModulesConfig) EffectiveEgressMinIntervals() (map[string]time.Duration, []string) {
	if len(c.EgressMinInterval) == 0 {
		return nil, nil
	}
	out := make(map[string]time.Duration, len(c.EgressMinInterval))
	var invalid []string
	for host, raw := range c.EgressMinInterval {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || d <= 0 {
			invalid = append(invalid, host)
			continue
		}
		out[host] = d
	}
	return out, invalid
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

// IngestConfig runs credentialed Space-Track and UDL source-sync workers
// inside the daemon process, against the daemon's own store handle.
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

	// Space-Track gap-fill worker (credentials via env, see above).
	SpaceTrackEnabled      bool   `yaml:"spacetrack_enabled"`
	SpaceTrackStartDay     string `yaml:"spacetrack_start_day,omitempty"`
	SpaceTrackBatchDays    int    `yaml:"spacetrack_batch_days,omitempty"`
	SpaceTrackBatchSleep   string `yaml:"spacetrack_batch_sleep,omitempty"`
	SpaceTrackPollInterval string `yaml:"spacetrack_poll_interval,omitempty"`
	SpaceTrackLoginURL     string `yaml:"spacetrack_login_url,omitempty"`
	SpaceTrackQueryTmpl    string `yaml:"spacetrack_query_template,omitempty"`

	// Unified Data Library sync worker (credentials via env, see above).
	UDLEnabled      bool   `yaml:"udl_enabled"`
	UDLBaseURL      string `yaml:"udl_base_url,omitempty"`
	UDLStartDay     string `yaml:"udl_start_day,omitempty"`
	UDLBatchDays    int    `yaml:"udl_batch_days,omitempty"`
	UDLBatchSleep   string `yaml:"udl_batch_sleep,omitempty"`
	UDLPollInterval string `yaml:"udl_poll_interval,omitempty"`
	UDLMaxResults   int    `yaml:"udl_max_results,omitempty"`

	// HTTPTimeout bounds each credentialed source request (default 90s).
	HTTPTimeout string `yaml:"http_timeout,omitempty"`

	// MinFreeDiskGB refuses to start a sync cycle below this free-disk
	// floor (0 = built-in 5 GiB default).
	MinFreeDiskGB float64 `yaml:"min_free_disk_gb,omitempty"`
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

	// FirstFireWhenDue runs a timer-served flow's triggers once shortly after
	// it starts, but ONLY when the node's retrieval ledger says its sources are
	// past their debounce window (or have never been retrieved).
	//
	// A cron ticker fires one interval AFTER it starts, so without this a node
	// that boots with a 3 h GP timer publishes nothing for three hours — and a
	// node restarted more often than its interval publishes nothing ever.
	// Firing unconditionally would be the opposite failure (a crash loop
	// becomes a fetch storm), which is why the debounce ledger is the gate.
	// YAML: flows.first_fire_when_due. Default true.
	FirstFireWhenDue bool `yaml:"first_fire_when_due"`

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
	// builtin plugin.getConfig hostcall (for example fixture URL overrides).
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

	// LocalPublishAddr optionally starts a SEPARATE, loopback-only HTTP listener
	// carrying only the publish routes, with no HTTP authentication, for a data
	// pipeline running ON this host (e.g. the constellation OD pipeline writing
	// its fitted OMM/OCM records into this node's own store).
	//
	// This exists because the daemon's single admin/API listener is reverse-proxied
	// to the public internet by nginx, so every public request already arrives at
	// the daemon from 127.0.0.1. Authenticating by client IP on THAT listener would
	// therefore expose writes to the whole internet. A second socket, bound to a
	// loopback address the proxy does not forward to, is the only safe local lane.
	//
	// Hard requirements, enforced at startup by ValidateLoopbackListenAddr:
	//   - must be a literal loopback IP (127.0.0.0/8 or ::1) with a port;
	//     0.0.0.0, ::, a hostname, or a routable IP is a fatal config error;
	//   - the reverse proxy must NEVER be configured to forward to this port.
	//
	// Empty (the default) disables the lane entirely.
	LocalPublishAddr string `yaml:"local_publish_addr"`

	// AutoPublish declares which INGESTED source lanes are republished to the
	// network as dataset publications, without an operator typing a curl.
	//
	// WHY THIS EXISTS (2026-08-04, sdn-rfb-publish-to-consumer-node). A
	// producer node can ingest a batch perfectly and still be invisible: the
	// $RFB SatNOGS batch (5,289 records) landed on host-02 and never reached
	// host-01, because nothing ever fired the publication. The CelesTrak
	// lanes fire it from INSIDE the flow (a publish_request node POSTing the
	// loopback admin route, gated on flow config), which is why they publish
	// and every other ingest lane does not — and that in-flow trigger is the
	// same shape that deadlocked the GP flow for 100 minutes with zero store
	// writes. A lane declared here fires OFF the flow's thread instead: the
	// wasm ingest call returns immediately and the export/pin/announce runs
	// on the host's own queue.
	//
	// This is connector wiring, not application logic. The decision of WHAT
	// is republished is configuration (this list); the mechanism is the same
	// dataset publication the admin route already performs.
	//
	// FAIL-CLOSED: an empty list publishes nothing. Absence of configuration
	// is never permission to republish somebody else's data — which matters
	// most for share-alike sources (SatNOGS DB is CC-BY-SA-4.0).
	AutoPublish []AutoPublishLane `yaml:"auto_publish,omitempty"`
}

// AutoPublishLane is one ingest lane whose batches are republished as dataset
// publications when they land.
//
// Schema is REQUIRED; ProviderID/SourceName are optional narrowing filters
// (empty = any). Matching is case-insensitive and the schema may be written
// either as the standard code ("RFB") or the schema file ("RFB.fbs").
type AutoPublishLane struct {
	// Schema is the SDS schema whose batches publish, e.g. "RFB.fbs".
	Schema string `yaml:"schema"`

	// ProviderID narrows the lane to one provider (the provider_id the
	// ingesting module declared), e.g. "space-data-network-02".
	ProviderID string `yaml:"provider_id,omitempty"`

	// SourceName narrows the lane to one source of that provider, e.g.
	// "satnogs-db".
	SourceName string `yaml:"source_name,omitempty"`

	// MinInterval rate-limits this lane: a batch that lands sooner than this
	// after the lane's last publication is skipped. Zero uses the built-in
	// default (5m). A publication exports, pins and announces a whole shard,
	// so a misconfigured 1-minute ingest timer must not turn into a
	// publication storm.
	MinInterval time.Duration `yaml:"min_interval,omitempty"`

	// MaxShardBytes caps ONE published shard in bytes, in addition to the
	// record-count window. Zero uses the node default (64 MiB,
	// api.DefaultDatasetPublicationMaxShardBytes); a negative value publishes
	// unbounded shards, which is only ever right for reproducing a shard that
	// was cut before byte budgets existed.
	//
	// This is the operator surface for the fix in
	// graph/tasks/sdn-sharding-not-length-aware.md: without it a lane of large
	// records produced a "250-record" shard of arbitrary size (250 x 128 MiB =
	// 32 GiB), because buffer length never entered the boundary decision.
	MaxShardBytes int64 `yaml:"max_shard_bytes,omitempty"`
}

// ErrListenAddrNotLoopback marks a listen address that is not loopback-only.
var ErrListenAddrNotLoopback = errors.New("listen address must be a literal loopback IP")

// ValidateLoopbackListenAddr enforces that addr binds to loopback only.
//
// It deliberately requires a LITERAL loopback IP: an empty host ("" / ":5011")
// binds all interfaces, and a hostname such as "localhost" resolves through
// /etc/hosts and DNS, neither of which is a trustworthy security boundary.
func ValidateLoopbackListenAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("%w: address is empty", ErrListenAddrNotLoopback)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %q is not a host:port address: %v", ErrListenAddrNotLoopback, addr, err)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("%w: %q has no port", ErrListenAddrNotLoopback, addr)
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("%w: %q has no host, which binds every interface", ErrListenAddrNotLoopback, addr)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is a hostname, not a literal IP", ErrListenAddrNotLoopback, host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %q is not loopback", ErrListenAddrNotLoopback, host)
	}

	return nil
}

// ListenLoopback binds a TCP listener on addr, which MUST be loopback-only.
//
// The address is validated before binding AND the real bound address is asserted
// afterwards, so the socket a caller receives is guaranteed loopback. A listener
// that somehow came up on a routable interface is closed rather than served: for
// an unauthenticated lane the failure mode must be closed, never "listening on
// 0.0.0.0 and hoping the firewall covers it" (ufw is inactive on the prod hosts,
// so the bind IS the boundary).
func ListenLoopback(addr string) (net.Listener, error) {
	if err := ValidateLoopbackListenAddr(addr); err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", strings.TrimSpace(addr))
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", addr, err)
	}

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddr.IP.IsLoopback() {
		listener.Close()
		return nil, fmt.Errorf("%w: bound to %s", ErrListenAddrNotLoopback, listener.Addr())
	}

	return listener, nil
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

	// EthereumAddress optionally links an external EVM wallet address to this
	// account so surfaces can RECOGNIZE a connected wallet (owner 2026-08-20;
	// intended for local/dev nodes during development). Identification only,
	// never authentication: no session is ever minted from an address claim —
	// signed admission belongs to the external-wallet verification lane.
	EthereumAddress string `yaml:"ethereum_address,omitempty"`
}

// NetworkConfig contains network-related settings.
type NetworkConfig struct {
	Listen     []string `yaml:"listen"`
	Bootstrap  []string `yaml:"bootstrap"`
	EdgeRelays []string `yaml:"edge_relays"`
	MaxConns   int      `yaml:"max_connections"`

	// EnableRelay controls whether this node RUNS a public circuit-relay v2
	// HOP service — i.e. donates its bandwidth and CPU to relay traffic
	// between arbitrary internet peers.
	//
	// THIS FIELD WAS DECLARED AND NEVER READ until 2026-08-08. host-01's
	// module-delivery sidecar has carried `enable_relay: false` since it was
	// written, and the node ran `libp2p.EnableRelayService()` unconditionally
	// anyway: a live identify against the box returned
	// `/libp2p/circuit/relay/0.2.0/hop`, and it sat at 98.5% CPU of 2 vCPUs
	// with 780 inbound connections from ~700 distinct internet IPs while its
	// actual job — serving encrypted modules to browsers — lost every race for
	// the CPU. An operator-set "false" that the process ignores is worse than
	// no knob at all, because it reads as a decision that was made.
	//
	// This never gates the CLIENT side: a node can always DIAL through someone
	// else's relay (libp2p.EnableRelay) regardless of this setting.
	EnableRelay bool `yaml:"enable_relay"`

	// DHTServer controls whether this node serves the PUBLIC Amino/IPFS
	// Kademlia DHT (dht.ModeAutoServer) instead of using it as a client
	// (dht.ModeClient).
	//
	// Defaults to FALSE. A DHT SERVER answers routing queries for the whole
	// IPFS network, which is an unbounded public workload with no relationship
	// to anything this node is for. It was hardcoded to ModeAutoServer until
	// 2026-08-08 and produced 2105 kad-dht handler warnings in 70 minutes on
	// host-01 alongside the relay load above. Client mode keeps everything the
	// node actually needs — it still QUERIES the DHT and still PROVIDES its own
	// records (module-delivery provider discovery is unaffected) — it simply
	// stops serving strangers' lookups off a 2-vCPU box.
	//
	// Set true only on a node that is deliberately deployed as public DHT
	// infrastructure and sized for it.
	DHTServer bool `yaml:"dht_server"`

	// Announce lists EXTRA multiaddrs this node advertises for itself, on top
	// of what it actually binds. It exists because the browser-reachable
	// address of this node is NOT a libp2p listener: TLS terminates on the
	// node's own :443 HTTPS server, which reverse-proxies /p2p/<peerid>
	// websocket upgrades to the loopback libp2p listener
	// (see newAdminUpgradeRouter). libp2p therefore has no way to discover
	// that `/dns4/<host>/tcp/443/wss` reaches it, and advertised NOTHING a
	// browser on an HTTPS origin could legally dial — only `/ws`, which is
	// mixed content and is dropped client-side before a dial is even attempted.
	//
	// Entries are advertised verbatim (a trailing /p2p/<peerid> is optional and
	// is appended by consumers), so they flow into identify, into delegated
	// routing, and into /api/module-delivery/provider — which is what lets a
	// browser get a dialable address from the CONTRACT instead of from a
	// hardcoded constant in one client library.
	Announce []string `yaml:"announce"`

	MaxMessageSize int `yaml:"max_message_size"` // Maximum message size in bytes (default: 10MB)
	MaxSchemaName  int `yaml:"max_schema_name"`  // Maximum schema name length (default: 256)
	MaxQuerySize   int `yaml:"max_query_size"`   // Maximum query size in bytes (default: 4KB)

	// Rate limiting settings (per peer)
	MaxMessagesPerSecond float64 `yaml:"max_messages_per_second"` // Maximum messages per second per peer (default: 100)
	MaxMessagesPerMinute int     `yaml:"max_messages_per_minute"` // Maximum messages per minute per peer (default: 1000)
	RateLimitBurst       int     `yaml:"rate_limit_burst"`        // Allow burst of messages up to this limit (default: 50)

	// AutoTLS provisions a publicly-trusted certificate for this node's own
	// peer identity so browsers can dial it. See AutoTLSConfig.
	AutoTLS AutoTLSConfig `yaml:"autotls"`

	// Admission governs which connections this node KEEPS when the public
	// swarm floods it. See PeerAdmissionConfig.
	Admission PeerAdmissionConfig `yaml:"admission"`
}

// PeerAdmissionConfig configures the peer admission policy: the trim band the
// libp2p connection manager works in, the headroom reserved below the ceiling
// so pinned and trusted peers can always get in, and the window an inbound
// peer has to prove it speaks an SDN protocol before it becomes a trim
// candidate.
//
// WHY THIS EXISTS (measured on host-01, task sdn-inbound-junk-flood-policy):
// ~1095 distinct IPs refilled inbound connections to the ceiling in ~65
// minutes. That is not abuse — at ~1.11 connections per distinct IP it is
// organic public-IPFS/libp2p DHT churn, which this node inherits because it is
// a kubo fork. Per-IP connection caps free nothing against that shape, and
// raising the ceiling is a treadmill. The fix is to make the connection
// manager keep the RIGHT connections:
//
//	low_water < high_water < max_connections
//
// Before this config existed the node built its connection manager as
// `NewConnManager(1000, network.max_connections)` — a hard-coded low water
// against a configured high water. On a default config that is low == high:
// no band at all, so the node lives permanently AT its watermark. On a node
// configured below 1000 (celestrak.eth runs max_connections: 64) it is
// low > high, and go-libp2p does not validate that — getConnsToClose returns
// early whenever connCount <= lowWater, so the connection manager silently
// never trims anything at all.
type PeerAdmissionConfig struct {
	// Disabled turns the policy off: no reserved headroom, no protection of
	// pinned/trusted peers, no SDN-protocol reputation tagging.
	//
	// Spelled as an opt-OUT on purpose. `Enabled bool` would make the zero
	// value (every programmatically constructed config, every test, every
	// caller that does not go through Default()) silently run with NO
	// admission policy, which is exactly the failure this file is about.
	Disabled bool `yaml:"disabled"`

	// HighWater is the connection count at which the connection manager starts
	// trimming. 0 (default) derives it as max_connections - reserved_headroom.
	// Values above max_connections are clamped: a high water at or above the
	// ceiling is the degenerate configuration described above.
	HighWater int `yaml:"high_water"`

	// LowWater is the count trimming targets. 0 (default) derives it as 75% of
	// the high water, which gives the manager a real band to work in instead of
	// re-trimming on every tick.
	LowWater int `yaml:"low_water"`

	// ReservedHeadroom is how many connection slots are kept free BELOW the
	// ceiling so a pinned peer, a browser /p2p/ upgrade or the module-publish
	// lane can always be admitted while the generic pool is saturated.
	// Default 128, clamped to at most a quarter of max_connections so small
	// nodes stay sane.
	ReservedHeadroom int `yaml:"reserved_headroom"`

	// GracePeriod is how long a newly opened connection is immune from
	// trimming — the window in which an inbound peer may prove it speaks an
	// SDN protocol. Go duration; default 30s.
	GracePeriod string `yaml:"grace_period"`

	// SilencePeriod is how often the connection manager checks whether it is
	// over the high water. Go duration; default 10s.
	SilencePeriod string `yaml:"silence_period"`

	// ProtectTrustLevel is the registry trust level at and above which a peer
	// is PROTECTED from trimming outright. Default "trusted". Config trusted
	// peers, operator pins and configured bootstrap peers are protected
	// regardless of this setting.
	ProtectTrustLevel string `yaml:"protect_trust_level"`
}

// AutoTLSConfig configures libp2p AutoTLS (p2p-forge / libp2p.direct), the
// connector that makes this node BROWSER-DIALABLE without a DNS record, a
// certificate file, or exposing the admin listener.
//
// A browser on an https:// origin cannot open a plain ws:// socket and will not
// accept a pinned self-signed certificate hash, so a node with only
// /tcp + /ws + /quic addresses is unreachable from a web page except through a
// relay. AutoTLS closes that: the forge broker verifies this node is reachable
// at the multiaddrs it claims, solves an ACME DNS-01 challenge on its behalf,
// and Let's Encrypt issues a wildcard certificate for
// `*.<this-node's-peer-id-base36>.libp2p.direct`. The resulting listen address
// shares the existing TCP port, so no new port is opened.
//
// Disabled by default: enabling it publishes this node's IP inside a public DNS
// name and registers with a third-party broker, which is an operator decision.
type AutoTLSConfig struct {
	// Enabled turns the connector on. When true, every plain /ip4|/ip6 + /tcp
	// listen address gains a matching `/tls/sni/*.<domain>/ws` address.
	Enabled bool `yaml:"enabled"`

	// StoragePath is the certmagic storage directory holding the ACME account
	// key and the issued certificate. Defaults to <storage.path>/p2p-forge-certs.
	// It MUST persist across restarts: a wiped directory means a fresh ACME
	// order on every boot, which is how a node ends up rate-limited by the CA.
	StoragePath string `yaml:"storage_path"`

	// DomainSuffix overrides the forge domain (default: libp2p.direct).
	DomainSuffix string `yaml:"domain_suffix"`

	// RegistrationEndpoint overrides the broker (default:
	// https://registration.libp2p.direct).
	RegistrationEndpoint string `yaml:"registration_endpoint"`

	// CAEndpoint overrides the ACME directory (default: Let's Encrypt
	// production). Point it at the staging directory when testing issuance.
	CAEndpoint string `yaml:"ca_endpoint"`

	// RegistrationToken is an optional broker access token (FORGE_ACCESS_TOKEN).
	RegistrationToken string `yaml:"registration_token"`

	// RegistrationDelay delays the first registration attempt (Go duration).
	// Empty means no delay, which is what an operator who explicitly enabled
	// the connector asked for.
	RegistrationDelay string `yaml:"registration_delay"`

	// ShortAddrs controls the SHAPE of the advertised address. Unset means
	// TRUE, and that default is measured, not cosmetic:
	//
	//	short (default): /dns4/<ip-dashed>.<peerid>.libp2p.direct/tcp/<port>/tls/ws
	//	long:            /ip4/<ip>/tcp/<port>/tls/sni/<name>/ws
	//
	// Dialed from the js-libp2p stack sdn-js ships (@libp2p/websockets v8),
	// the LONG form is rejected before a socket is opened — "The dial request
	// has no valid addresses" — while the short form connects (measured
	// 2026-08-06 against a live forge-issued certificate: DIAL_OK in 438 ms,
	// limits null, i.e. direct and non-transient). sdn-js's own
	// isBrowserDialableAddr() also requires a /dns prefix, so the long form
	// would be filtered out of the bootstrap list even if the transport
	// accepted it. A node that advertises only the long form is not
	// browser-dialable, which defeats the entire purpose of the connector.
	//
	// Pointer so "unset" is distinguishable from "explicitly false"; an
	// operator who wants the long form must ask for it.
	ShortAddrs *bool `yaml:"short_addrs"`

	// AllowPrivateAddrs skips the "wait for public reachability" gate and lets
	// private multiaddrs be sent to the broker. TEST ONLY — on a real host it
	// only removes the guard that keeps doomed registrations out of the log.
	AllowPrivateAddrs bool `yaml:"allow_private_addrs"`
}

// GeoIPConfig configures the fail-open GeoLite2-City connector
// (internal/geoip) used to place peers on the status dashboard map.
type GeoIPConfig struct {
	// MMDBPath is the filesystem path to a MaxMind GeoLite2-City .mmdb
	// database. Empty or missing is fine: the connector fail-opens and peers
	// are reported without coordinates. Defaults to <data-dir>/geoip/
	// GeoLite2-City.mmdb.
	MMDBPath string `yaml:"mmdb_path"`
}

// EmbeddingConfig configures the fail-open semantic-search asset surface
// served at /embedding/* for the status dashboard: the quantized sentence-
// embedding model, its tokenizer vocab, and the onnxruntime-web .wasm/.mjs
// runtime artifacts, all same-origin so the dashboard's strict CSP holds.
type EmbeddingConfig struct {
	// AssetsDir is the directory holding the /embedding/* static assets
	// (model.onnx, vocab.txt, ort-wasm-*.wasm/.mjs), staged by
	// deployment/embedding/fetch-model.sh — the same staged-file pattern as
	// the GeoIP mmdb. Empty or missing is fine: the surface 404s and the
	// dashboard fails open to substring search. Defaults to
	// <data-dir>/embedding.
	AssetsDir string `yaml:"assets_dir"`
}

// WalletWasmConfig configures the fail-open same-origin hd-wallet-wasm asset
// surface served at /wallet-wasm/* for the dashboard's wallet sign-in. The
// dashboard's CSP is default-src 'self', so the wallet loader, its runtime ES
// modules and the WASI artifact must all come from this node — never a CDN.
type WalletWasmConfig struct {
	// AssetsDir is the directory holding the /wallet-wasm/* static assets,
	// staged (as a mirror of the hd-wallet-wasm package's dist/ tree) by
	// deployment/wallet-wasm/stage-wallet-wasm.sh — the same staged-file
	// pattern as the GeoIP mmdb and the /embedding/* model. Empty or missing
	// is fine: the surface 404s and the dashboard reports sign-in as
	// unavailable instead of reaching off-origin. Defaults to
	// <data-dir>/wallet-wasm.
	AssetsDir string `yaml:"assets_dir"`

	// UIAssetsDir is the directory holding the /wallet-ui/* static assets: the
	// hd-wallet-ui package's dist tree, which supplies the actual wallet
	// sign-in experience the dashboard mounts IN-PAGE.
	//
	// OWNER LAW 2026-07-27: "we do NOT load anything from a site." The wallet
	// UI must come from this node and nowhere else, so hd-wallet-ui's
	// registered-site client — which opens https://wallet.spacedatanetwork.org
	// — is inadmissible and is not what this serves. Staged alongside
	// AssetsDir by deployment/wallet-wasm/stage-wallet-wasm.sh, at a matching
	// version. Empty or missing is fine: the surface 404s and the dashboard
	// reports sign-in as unavailable. Defaults to <data-dir>/wallet-ui.
	UIAssetsDir string `yaml:"ui_assets_dir"`
}

// StatusConfig configures the public read-only node-status feed
// (internal/status, served at /ws/status).
type StatusConfig struct {
	// AllowedOrigins is the extra cross-origin allowlist for the status
	// WebSocket beyond the always-permitted same-origin and loopback/dev
	// origins. Values are full origins (scheme://host[:port]). The status
	// feed is public read-only telemetry, but a browser handshake still
	// carries the page's Origin, so a central dashboard hosted elsewhere must
	// be listed here to subscribe cross-origin. Operators add their node's
	// public dashboard host(s); the default seeds the primary hosted board.
	AllowedOrigins []string `yaml:"allowed_origins"`
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

	// AuxiliaryReplayChunkBytes bounds ONE auxiliary-journal replay
	// transaction in BYTES as well as in frames. Without a byte bound a
	// 512-frame chunk is unbounded in size: 512 directory rows and 512
	// multi-megabyte payloads were the same "chunk" (b6c21e87). The bound
	// itself already ships with a built-in default; this is the operator
	// knob for the boxes where the default is the wrong number — a
	// 1-vCPU/2GB ingest box wants it smaller, a fat box replaying a long
	// journal wants it bigger.
	//
	// 0 = built-in default (8 MiB, storage.WithAuxiliaryReplayChunkBytes).
	// A single frame larger than the budget is still applied whole: frames
	// are never split, so no value of this knob can wedge a replay.
	AuxiliaryReplayChunkBytes int64 `yaml:"auxiliary_replay_chunk_bytes,omitempty"`
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

	// EPMDir names a directory of operator-managed signed EPM records
	// (*.epm, size-prefixed FlatBuffers). Each file is signature-verified
	// and its peer id extracted at boot, then loaded into the peer registry
	// — so an enrolled fleet peer's full crypto identity (xpub, key paths,
	// EPM signature) is held from provisioning, even while the peer is
	// offline (owner directive 2026-07-31: "on instantiation, once the keys
	// are generated, you have all the info you need"). Empty disables.
	EPMDir string `yaml:"epm_dir"`

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

	// AuthDBPath is the SQLite file holding operator trust-matrix entries and
	// sessions.
	//
	// OWNER DIRECTIVE 2026-07-27, verbatim: "use the entries in an sqlite
	// database to handle that. Also I think it should probably be in a separate
	// database file that the other standards for safety."
	//
	// This is a real SQLite file (flatsqldrv.OpenStandalone opens
	// sql.Open("sqlite", path)), kept deliberately OUT of the standards /
	// FlatSQL record store. Operator credentials and session state have a
	// different blast radius from network record storage: a rebuilt or corrupt
	// standards store must not be able to lock an operator out of their own
	// node, and a record-store bug must not be able to reach auth rows.
	//
	// Empty means the default, <storage.path>/auth.db — unchanged from before
	// this key existed, so no deployed node's store moves. Set it to relocate
	// the auth database; it must never point inside the standards store.
	AuthDBPath string `yaml:"auth_db_path"`

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
			Bootstrap:  bootstrap.DefaultBootstrapAddresses(),
			EdgeRelays: []string{},
			MaxConns:   1000,
			// Donated public infrastructure is OPT-IN, not the default. See the
			// field comments on EnableRelay/DHTServer: both ran unconditionally
			// until 2026-08-08 and pinned host-01 at 98.5% CPU serving the
			// public IPFS DHT and relaying strangers' traffic, while the
			// module-delivery lane it exists for reset streams under load.
			EnableRelay:    false,
			DHTServer:      false,
			Announce:       []string{},
			MaxMessageSize: 10 * 1024 * 1024, // 10MB default
			MaxSchemaName:  256,              // 256 bytes max schema name
			MaxQuerySize:   4 * 1024,         // 4KB max query size

			MaxMessagesPerSecond: 100,  // 100 messages per second per peer
			MaxMessagesPerMinute: 1000, // 1000 messages per minute per peer
			RateLimitBurst:       50,   // Allow burst of 50 messages

			// Admission: the zero value is already the intended policy (see
			// PeerAdmissionConfig — every field derives its default), so this
			// only spells out the two numbers an operator is most likely to
			// want to see written down.
			Admission: PeerAdmissionConfig{
				ReservedHeadroom: 128,
				GracePeriod:      "30s",
			},
		},
		Storage: StorageConfig{
			Path: dataPath,
			// 90% of the filesystem holding Path (DefaultStorageMaxSizePercent) —
			// an explicit "10GB"-style absolute size still works (back-compat).
			MaxSize:    fmt.Sprintf("%d%%", DefaultStorageMaxSizePercent),
			GCInterval: "1h",
		},
		GeoIP: GeoIPConfig{
			MMDBPath: filepath.Join(dataPath, "geoip", "GeoLite2-City.mmdb"),
		},
		Embedding: EmbeddingConfig{
			AssetsDir: filepath.Join(dataPath, "embedding"),
		},
		WalletWasm: WalletWasmConfig{
			AssetsDir:   filepath.Join(dataPath, "wallet-wasm"),
			UIAssetsDir: filepath.Join(dataPath, "wallet-ui"),
		},
		Status: StatusConfig{
			AllowedOrigins: []string{"https://sdn.spaceaware.io"},
		},
		// Default ON: owner ruling 2026-08-09 is that the publisher pushes a
		// signal and ALL installs upgrade in place. See UpdateConfig for the
		// gates that make a default-on push lane safe.
		Update: UpdateConfig{Enabled: true},
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
			// Ingest flows should serve data on the day they are installed,
			// not one full interval later; the debounce ledger keeps that
			// honest. See FirstFireWhenDue.
			FirstFireWhenDue: true,
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
			// CelesTrak retrieval, as flows. Every decision — which URL, how to
			// parse it, what provenance to stamp, which reconcile mode — lives
			// in the wasm nodes; the host contributes the timer, the HTTP
			// egress hook and guarded persistence. There is deliberately NO Go
			// CelesTrak fetcher: the ingest runner carries credentialed sources
			// (Space-Track, UDL) only, and cmd's
			// TestIngestCommandHasNoDirectCelesTrakSourceFlags keeps it that way.
			//
			// Declaring a service here is not the same as running it:
			// LoadFlowServices SKIPS any flow whose bundle is not installed in
			// flows.storage_path, so a node pulls from CelesTrak only after an
			// operator deliberately installs the bundle. Intervals are the
			// flow.json defaults (GP 3 h, SATCAT 24 h, space weather 3 h) — the
			// 3 h GP cadence IS the CelesTrak fetch-policy debounce window.
			//
			// MEMORY: a retrieval flow holds the whole fetched payload, its
			// base64 hostcall envelope, every normalized record stream and the
			// raw archive copy in ONE linear memory. The real CelesTrak GP full
			// catalog is ~4.75 MB of CSV that normalizes into ~13k OMM plus
			// ~13k MPE records, and the 64 MB global default trapped it on
			// host-01 with "Memory grow page failed -- exceeded limit page
			// size: 1024" AFTER a successful 200/4,750,985-byte fetch: the pull
			// reported a clean run and stored nothing. These ceilings are not
			// reservations — WasmEdge grows on demand — so sizing them for the
			// real catalog costs an idle node nothing.
			Services: []FlowService{
				{Flow: CelesTrakGPIngestFlowID, MemoryPages: 8192},
				{Flow: CelesTrakSatcatIngestFlowID, MemoryPages: 8192},
				{Flow: CelesTrakSpaceWeatherIngestFlowID, MemoryPages: 2048},
			},
		},
	}
}

// Program IDs of the CelesTrak retrieval flow bundles
// (space-data-network-modules flows/celestrak-ingest). GP yields OMM + MPE,
// SATCAT yields CAT (both the legacy fixed-width and the CSV snapshot), and
// SPW yields space weather.
const (
	CelesTrakGPIngestFlowID           = "com.digitalarsenal.flows.celestrak-gp-ingest"
	CelesTrakSatcatIngestFlowID       = "com.digitalarsenal.flows.celestrak-satcat-ingest"
	CelesTrakSpaceWeatherIngestFlowID = "com.digitalarsenal.flows.celestrak-spw-ingest"
)

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
	cfg.SourcePath = path
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
