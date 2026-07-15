// Package flowconfig carries the MINIMAL configuration surface the SDN flow
// runtime (sdn/flowrt) needs, ported onto the kubo base as part of Phase 2c
// (flow runtime core).
//
// TRIMMED PORT: this is NOT the full sdn-server internal/config package. Only
// FlowsConfig is ported here, and only the four fields FlowManager
// (sdn/flowrt/manager.go) actually reads — Enabled, StoragePath, MaxFlows,
// MaxMemoryPages. The remaining FlowsConfig fields in sdn-server
// (EditorEnabled/EditorPath and the Mounts []FlowMount / Services
// []FlowService slices) were intentionally left behind: they are consumed only
// by the DEFERRED serving files (httpmount.go, cronmount.go) and the discarded
// editor/ subdir, and porting them would drag in the FlowMount / FlowService
// nested types those files own. Re-add them (and the nested types) when the
// httpmount / cronmount serving layer is brought over in a later phase.
package flowconfig

// FlowsConfig controls the flow orchestration runtime. See the package doc for
// the fields trimmed relative to sdn-server's internal/config.FlowsConfig.
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
}
