// Package plugins provides the runtime types and interfaces that the SDN
// module runtime (sdn/modulert) implements and consumes.
//
// TRIMMED PORT (kubo rebase Phase 2b): this file was ported from
// sdn-server/plugins/manager.go but reduced to ONLY the types and interfaces
// that sdn/modulert needs to compile — RuntimeContext, the Plugin /
// CronProvider / UIProvider / UIURLSetter / RuntimeModuleInputApplier
// interfaces, and the RuntimeModule* dashboard data structs. The full plugin
// Manager runtime (lifecycle coordination, HTTP route mounting, DHT wiring,
// cron scheduler) was intentionally left behind: it drags in heavy node deps
// (net/http mounting, go-libp2p-kad-dht scheduling) that are off the
// module-load/invoke path this checkpoint proves. Re-port the Manager in a
// later phase when the node runtime is rebased onto kubo.
package plugins

import (
	"context"
	"net/http"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
)

// RuntimeContext provides node/runtime dependencies that plugins need at startup.
type RuntimeContext struct {
	Host              host.Host
	DHT               *dht.IpfsDHT
	BaseDataPath      string
	PeerID            string
	IPFSAPIURL        string
	Mode              string
	NodeEncryptionKey []byte
}

// Plugin is the runtime contract for SDN server plugins.
type Plugin interface {
	ID() string
	Start(ctx context.Context, runtime RuntimeContext) error
	RegisterRoutes(mux *http.ServeMux)
	Close() error
}

// RuntimeModuleLoader is implemented by modules that can load their runtime
// artifact without starting execution.
type RuntimeModuleLoader interface {
	Load(ctx context.Context) error
}

// RuntimeModulePausable is implemented by modules that can pause/resume
// invocation without unloading their runtime artifact.
type RuntimeModulePausable interface {
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
}

// ─── WASM Cron Types ───────────────────────────────────────────────────────

// CronMethodSpec describes a single cron-eligible method declared by a WASM
// plugin in its metadata JSON. The plugin declares *what* can be scheduled;
// the server config and web UI control *whether* and *how often* it runs.
type CronMethodSpec struct {
	// Method is the identifier passed to plugin_cron (e.g. "collect-telemetry").
	Method string `json:"method"`

	// Description is a human-readable label for the web UI.
	Description string `json:"description,omitempty"`

	// DefaultInterval is the suggested schedule (e.g. "30s", "5m", "1h").
	// The actual interval is controlled by the server config/UI.
	DefaultInterval string `json:"default_interval"`

	// Input describes what the host passes to this method.
	//   "none"       — no input (in_ptr=0, in_len=0)
	//   "json"       — host passes JSON bytes
	//   "flatbuffer" — host passes FlatBuffer bytes
	Input string `json:"input"`

	// Output describes what the plugin returns.
	//   "none"       — no output (returns 0 bytes)
	//   "json"       — plugin writes JSON to out_ptr
	//   "flatbuffer" — plugin writes FlatBuffer bytes to out_ptr
	Output string `json:"output"`
}

// CronScheduleConfig is the per-method schedule from the server config file
// or web UI. This controls whether and how often the host calls plugin_cron.
type CronScheduleConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	Interval       string `json:"interval" yaml:"interval"` // overrides DefaultInterval
	CronExpression string `json:"cronExpression,omitempty" yaml:"cronExpression,omitempty"`
	Timezone       string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	Jitter         string `json:"jitter,omitempty" yaml:"jitter,omitempty"`
	Backoff        string `json:"backoff,omitempty" yaml:"backoff,omitempty"`
	RetryBudget    int    `json:"retryBudget,omitempty" yaml:"retryBudget,omitempty"`
	MaxRuntime     string `json:"maxRuntime,omitempty" yaml:"maxRuntime,omitempty"`
}

// CronProvider is an optional interface that plugins can implement to
// declare cron-eligible methods. For WASM plugins, the Go wrapper reads
// the metadata JSON and returns CronMethodSpec entries. The manager
// schedules calls to plugin_cron based on server config.
type CronProvider interface {
	CronMethods() []CronMethodSpec
	InvokeCron(ctx context.Context, method string, input []byte) ([]byte, error)
}

// ─── UI ────────────────────────────────────────────────────────────────────

// UIDescriptor describes a plugin's web UI. Plugins that provide a web
// interface should implement the UIProvider interface.
type UIDescriptor struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`      // emoji or single character
	Color       string `json:"color,omitempty"`     // CSS background for icon badge
	TextColor   string `json:"textColor,omitempty"` // CSS text color for icon badge
	URL         string `json:"url,omitempty"`       // path to plugin UI page (served by plugin)
}

// UIProvider is an optional interface that plugins can implement to declare
// a web UI that will be shown on the Plugins page in the SDN web client.
type UIProvider interface {
	UIDescriptor() UIDescriptor
}

// UIURLSetter is an optional interface that plugins can implement when their
// UI page URL is not known at construction time and must be assigned later.
// A module-sdk WASM module (internal/modulert.Module) is the motivating
// case: it has no self-known UI location the way a Go-native plugin does
// (e.g. plugins/ailogplugin hardcodes its own dashboard path). Its URL
// instead comes from an app manifest's UI entry
// (internal/appmanifest.AppManifest), resolved and pushed in via
// Manager.SetModuleUIURL once the module is registered. Plugins that never
// have SetUIURL called on them keep reporting an empty UIDescriptor.URL, as
// before this interface existed.
type UIURLSetter interface {
	SetUIURL(url string)
}

// ─── Manifest ──────────────────────────────────────────────────────────────

// PluginManifestEntry is the JSON representation of a plugin in the manifest.
type PluginManifestEntry struct {
	ID            string           `json:"id"`
	Version       string           `json:"version,omitempty"`
	Status        string           `json:"status"`
	StatusMessage string           `json:"status_message,omitempty"`
	Description   string           `json:"description,omitempty"`
	UI            *UIDescriptor    `json:"ui,omitempty"`
	Cron          []CronMethodSpec `json:"cron,omitempty"`
}

// RuntimeModuleStats contains runtime metrics that are safe to expose to the
// dashboard. WASM memory is reported as linear-memory pages, not process RSS.
type RuntimeModuleStats struct {
	MemoryPages      uint64  `json:"memoryPages,omitempty"`
	MemoryBytes      uint64  `json:"memoryBytes,omitempty"`
	MaxMemoryPages   uint64  `json:"maxMemoryPages,omitempty"`
	MaxMemoryBytes   uint64  `json:"maxMemoryBytes,omitempty"`
	UptimeMs         int64   `json:"uptimeMs,omitempty"`
	HostRSSBytes     uint64  `json:"hostRssBytes,omitempty"`
	InvokeCount      uint64  `json:"invokeCount,omitempty"`
	ErrorCount       uint64  `json:"errorCount,omitempty"`
	LastInvokeAt     string  `json:"lastInvokeAt,omitempty"`
	AverageLatencyMs float64 `json:"averageLatencyMs,omitempty"`
	TimerRunCount    uint64  `json:"timerRunCount,omitempty"`
	LastTimerStatus  string  `json:"lastTimerStatus,omitempty"`
}

// RuntimeModuleMethod describes an invokable method surfaced by the module
// manifest.
type RuntimeModuleMethod struct {
	MethodID    string              `json:"methodId"`
	DisplayName string              `json:"displayName,omitempty"`
	Description string              `json:"description,omitempty"`
	InputPorts  []RuntimeModulePort `json:"inputPorts,omitempty"`
	OutputPorts []RuntimeModulePort `json:"outputPorts,omitempty"`
	MaxBatch    uint32              `json:"maxBatch,omitempty"`
	DrainPolicy string              `json:"drainPolicy,omitempty"`
}

// RuntimeModulePort describes one input or output stream port for a method.
type RuntimeModulePort struct {
	PortID           string                         `json:"portId"`
	DisplayName      string                         `json:"displayName,omitempty"`
	AcceptedTypeSets []RuntimeModuleAcceptedTypeSet `json:"acceptedTypeSets,omitempty"`
	MinStreams       uint16                         `json:"minStreams,omitempty"`
	MaxStreams       uint16                         `json:"maxStreams,omitempty"`
	Required         bool                           `json:"required,omitempty"`
	Description      string                         `json:"description,omitempty"`
}

// RuntimeModuleAcceptedTypeSet describes the schema and wire formats accepted
// by a method port.
type RuntimeModuleAcceptedTypeSet struct {
	SetID              string                 `json:"setId,omitempty"`
	AllowedTypes       []RuntimeModuleTypeRef `json:"allowedTypes,omitempty"`
	AllowedWireFormats []string               `json:"allowedWireFormats,omitempty"`
	Description        string                 `json:"description,omitempty"`
}

// RuntimeModuleTypeRef identifies a FlatBuffer payload type accepted by a port.
type RuntimeModuleTypeRef struct {
	SchemaName     string `json:"schemaName,omitempty"`
	FileIdentifier string `json:"fileIdentifier,omitempty"`
	SchemaVersion  string `json:"schemaVersion,omitempty"`
	RootType       string `json:"rootType,omitempty"`
}

// RuntimeModuleProtocol describes a network protocol declared by the module.
type RuntimeModuleProtocol struct {
	ProtocolID    string `json:"protocolId"`
	MethodID      string `json:"methodId,omitempty"`
	InputPortID   string `json:"inputPortId,omitempty"`
	OutputPortID  string `json:"outputPortId,omitempty"`
	Description   string `json:"description,omitempty"`
	WireID        string `json:"wireId,omitempty"`
	TransportKind string `json:"transportKind,omitempty"`
	Role          string `json:"role,omitempty"`
	AutoInstall   bool   `json:"autoInstall"`
	Advertise     bool   `json:"advertise"`
	DiscoveryKey  string `json:"discoveryKey,omitempty"`
}

// RuntimeModuleTimer describes a periodic timer declared by the module.
type RuntimeModuleTimer struct {
	TimerID           string `json:"timerId"`
	MethodID          string `json:"methodId"`
	DefaultIntervalMs uint64 `json:"defaultIntervalMs,omitempty"`
	Description       string `json:"description,omitempty"`
}

// RuntimeModuleManifest is the dashboard-friendly subset of a module manifest.
type RuntimeModuleManifest struct {
	PluginID     string                  `json:"pluginId"`
	Name         string                  `json:"name,omitempty"`
	Version      string                  `json:"version,omitempty"`
	PluginFamily string                  `json:"pluginFamily,omitempty"`
	Methods      []RuntimeModuleMethod   `json:"methods,omitempty"`
	Capabilities []string                `json:"capabilities,omitempty"`
	Protocols    []RuntimeModuleProtocol `json:"protocols,omitempty"`
	Timers       []RuntimeModuleTimer    `json:"timers,omitempty"`
}

// RuntimeModuleOption describes a safe runtime control exposed by the
// dashboard. Custom module settings should come from canonical manifests, not a
// repo-local schema.
type RuntimeModuleOption struct {
	Key             string  `json:"key"`
	Label           string  `json:"label"`
	Type            string  `json:"type"`
	Value           string  `json:"value,omitempty"`
	Description     string  `json:"description,omitempty"`
	ReadOnly        bool    `json:"readOnly,omitempty"`
	Units           string  `json:"units,omitempty"`
	Min             float64 `json:"min,omitempty"`
	Max             float64 `json:"max,omitempty"`
	DefaultValue    string  `json:"defaultValue,omitempty"`
	RestartRequired bool    `json:"restartRequired,omitempty"`
	Persistence     string  `json:"persistence,omitempty"`
	Mutable         bool    `json:"mutable,omitempty"`
}

// RuntimeModuleScheduleConfig is the operator-controlled schedule for one
// provider cron method.
type RuntimeModuleScheduleConfig struct {
	Enabled        bool   `json:"enabled"`
	Interval       string `json:"interval,omitempty"`
	CronExpression string `json:"cronExpression,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	Jitter         string `json:"jitter,omitempty"`
	Backoff        string `json:"backoff,omitempty"`
	RetryBudget    int    `json:"retryBudget,omitempty"`
	MaxRuntime     string `json:"maxRuntime,omitempty"`
}

// RuntimeModuleScheduleRun records a bounded scheduler execution history entry.
type RuntimeModuleScheduleRun struct {
	ID         string `json:"id"`
	MethodID   string `json:"methodId"`
	Trigger    string `json:"trigger"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	OutputSize int    `json:"outputSize,omitempty"`
}

// RuntimeModuleSchedule is the dashboard read model for one provider schedule.
type RuntimeModuleSchedule struct {
	MethodID        string                     `json:"methodId"`
	Description     string                     `json:"description,omitempty"`
	Enabled         bool                       `json:"enabled"`
	Interval        string                     `json:"interval"`
	CronExpression  string                     `json:"cronExpression,omitempty"`
	Timezone        string                     `json:"timezone"`
	TimezoneDisplay string                     `json:"timezoneDisplay"`
	UTCDisplay      string                     `json:"utcDisplay"`
	Jitter          string                     `json:"jitter,omitempty"`
	Backoff         string                     `json:"backoff,omitempty"`
	RetryBudget     int                        `json:"retryBudget,omitempty"`
	MaxRuntime      string                     `json:"maxRuntime,omitempty"`
	MinInterval     string                     `json:"minInterval"`
	IntervalPresets []string                   `json:"intervalPresets,omitempty"`
	LastRunAt       string                     `json:"lastRunAt,omitempty"`
	NextRunAt       string                     `json:"nextRunAt,omitempty"`
	RunHistory      []RuntimeModuleScheduleRun `json:"runHistory,omitempty"`
}

// RuntimeModuleAction describes a lifecycle control surfaced by the dashboard.
type RuntimeModuleAction struct {
	ActionID    string `json:"actionId"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Destructive bool   `json:"destructive,omitempty"`
}

// RuntimeModuleStatusEvent records a recent runtime state transition.
type RuntimeModuleStatusEvent struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	At      string `json:"at"`
}

// RuntimeModuleLinks exposes dashboard-safe detail links for module logs/events.
type RuntimeModuleLinks struct {
	LogsURL   string `json:"logsUrl,omitempty"`
	EventsURL string `json:"eventsUrl,omitempty"`
}

// RuntimeModuleInputValue is an operator-saved method input value. Values are
// keyed by module, method, and port, then applied by runtimes that implement
// RuntimeModuleInputApplier when the module is restarted.
type RuntimeModuleInputValue struct {
	MethodID       string `json:"methodId"`
	PortID         string `json:"portId"`
	WireFormat     string `json:"wireFormat,omitempty"`
	Encoding       string `json:"encoding,omitempty"`
	SchemaName     string `json:"schemaName,omitempty"`
	FileIdentifier string `json:"fileIdentifier,omitempty"`
	SchemaVersion  string `json:"schemaVersion,omitempty"`
	RootType       string `json:"rootType,omitempty"`
	Value          string `json:"value,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
}

// RuntimeModuleCommandHistoryEntry records dashboard commands that change
// module runtime inputs or apply them through lifecycle actions.
type RuntimeModuleCommandHistoryEntry struct {
	ID          string                    `json:"id"`
	At          string                    `json:"at"`
	Command     string                    `json:"command"`
	ModuleID    string                    `json:"moduleId,omitempty"`
	MethodID    string                    `json:"methodId,omitempty"`
	PortID      string                    `json:"portId,omitempty"`
	Status      string                    `json:"status"`
	Summary     string                    `json:"summary,omitempty"`
	InputValues []RuntimeModuleInputValue `json:"inputValues,omitempty"`
}

// RuntimeModuleInputState is the persisted dashboard state for one module.
type RuntimeModuleInputState struct {
	Values         []RuntimeModuleInputValue          `json:"values,omitempty"`
	RestartPending bool                               `json:"restartPending,omitempty"`
	CommandHistory []RuntimeModuleCommandHistoryEntry `json:"commandHistory,omitempty"`
	Schedules      map[string]RuntimeModuleSchedule   `json:"schedules,omitempty"`
}

// RuntimeModuleCatalog contains public catalog metadata when this runtime
// module came from the module-delivery catalog.
type RuntimeModuleCatalog struct {
	RequiredScope   string `json:"requiredScope,omitempty"`
	ContentType     string `json:"contentType,omitempty"`
	CacheControl    string `json:"cacheControl,omitempty"`
	BundleSHA256    string `json:"bundleSha256,omitempty"`
	SizeBytes       int64  `json:"sizeBytes,omitempty"`
	SignatureHex    string `json:"signatureHex,omitempty"`
	SignerPubKeyHex string `json:"signerPubKeyHex,omitempty"`
	UploadedAt      string `json:"uploadedAt,omitempty"`
}

// RuntimeModuleDescriptor is supplied by plugins that can expose richer module
// metadata than the base Plugin interface.
type RuntimeModuleDescriptor struct {
	Manifest *RuntimeModuleManifest `json:"manifest,omitempty"`
	Stats    RuntimeModuleStats     `json:"stats,omitempty"`
	Actions  []RuntimeModuleAction  `json:"actions,omitempty"`
	Links    RuntimeModuleLinks     `json:"links,omitempty"`
}

// RuntimeModuleEntry is one row in the dashboard runtime snapshot.
type RuntimeModuleEntry struct {
	ID             string                             `json:"id"`
	Version        string                             `json:"version,omitempty"`
	Status         string                             `json:"status"`
	StatusMessage  string                             `json:"statusMessage,omitempty"`
	Description    string                             `json:"description,omitempty"`
	UI             *UIDescriptor                      `json:"ui,omitempty"`
	Cron           []CronMethodSpec                   `json:"cron,omitempty"`
	Manifest       *RuntimeModuleManifest             `json:"manifest,omitempty"`
	Stats          RuntimeModuleStats                 `json:"stats,omitempty"`
	Options        []RuntimeModuleOption              `json:"options,omitempty"`
	Schedules      []RuntimeModuleSchedule            `json:"schedules,omitempty"`
	Actions        []RuntimeModuleAction              `json:"actions,omitempty"`
	StatusHistory  []RuntimeModuleStatusEvent         `json:"statusHistory,omitempty"`
	Links          *RuntimeModuleLinks                `json:"links,omitempty"`
	Catalog        *RuntimeModuleCatalog              `json:"catalog,omitempty"`
	InputValues    []RuntimeModuleInputValue          `json:"inputValues,omitempty"`
	RestartPending bool                               `json:"restartPending,omitempty"`
	CommandHistory []RuntimeModuleCommandHistoryEntry `json:"commandHistory,omitempty"`
}

// RuntimeSnapshot is the top-level dashboard payload.
type RuntimeSnapshot struct {
	GeneratedAt string               `json:"generatedAt"`
	Count       int                  `json:"count"`
	Modules     []RuntimeModuleEntry `json:"modules"`
}

type runtimeDescriptorProvider interface {
	RuntimeDescriptor() RuntimeModuleDescriptor
}

// RuntimeModuleInputApplier is implemented by runtimes that can apply saved
// dashboard input values after a lifecycle restart.
type RuntimeModuleInputApplier interface {
	ApplyRuntimeModuleInputs(ctx context.Context, values []RuntimeModuleInputValue) error
}
