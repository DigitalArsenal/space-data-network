package plugins

import (
	"context"
	"net/http"
	"testing"
)

func TestRuntimeSnapshotReportsModuleDescriptorStatsAndOptions(t *testing.T) {
	mgr := New()
	plugin := &fakeRuntimePlugin{
		id: "licensing",
		descriptor: RuntimeModuleDescriptor{
			Manifest: &RuntimeModuleManifest{
				PluginID:     "licensing",
				Name:         "Licensing",
				Version:      "1.2.3",
				PluginFamily: "INFRASTRUCTURE",
				Methods: []RuntimeModuleMethod{
					{
						MethodID:    "server_handle_message",
						DisplayName: "Handle message",
						Description: "Handle module delivery requests",
					},
				},
				Capabilities: []string{"protocol_handle", "crypto_decrypt"},
				Protocols: []RuntimeModuleProtocol{
					{
						ProtocolID:  "module-delivery",
						WireID:      "/space-data-network/module-delivery/1.0.0",
						MethodID:    "server_handle_message",
						AutoInstall: true,
					},
				},
				Timers: []RuntimeModuleTimer{
					{
						TimerID:           "refresh-grants",
						MethodID:          "refresh_grants",
						DefaultIntervalMs: 30000,
						Description:       "Refresh grant cache",
					},
				},
			},
			Stats: RuntimeModuleStats{
				MemoryPages:      7,
				MemoryBytes:      7 * 65536,
				MaxMemoryPages:   1024,
				MaxMemoryBytes:   1024 * 65536,
				HostRSSBytes:     42 * 1024 * 1024,
				InvokeCount:      11,
				ErrorCount:       2,
				LastInvokeAt:     "2026-04-30T12:00:00Z",
				AverageLatencyMs: 13.5,
				TimerRunCount:    3,
				LastTimerStatus:  "ok",
			},
			Links: RuntimeModuleLinks{
				LogsURL:   "/api/v1/modules/runtime/licensing/logs",
				EventsURL: "/api/v1/modules/runtime/licensing/events",
			},
		},
	}

	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	snapshot := mgr.RuntimeSnapshot()
	if snapshot.Count != 1 {
		t.Fatalf("count = %d, want 1", snapshot.Count)
	}
	if snapshot.Modules[0].ID != "licensing" {
		t.Fatalf("module id = %q, want licensing", snapshot.Modules[0].ID)
	}
	if snapshot.Modules[0].Status != "running" {
		t.Fatalf("status = %q, want running", snapshot.Modules[0].Status)
	}
	if snapshot.Modules[0].Manifest == nil || snapshot.Modules[0].Manifest.Methods[0].MethodID != "server_handle_message" {
		t.Fatalf("manifest = %#v, want method descriptor", snapshot.Modules[0].Manifest)
	}
	if got, want := snapshot.Modules[0].Stats.MemoryBytes, uint64(7*65536); got != want {
		t.Fatalf("memory bytes = %d, want %d", got, want)
	}
	if got, want := snapshot.Modules[0].Stats.HostRSSBytes, uint64(42*1024*1024); got != want {
		t.Fatalf("host RSS bytes = %d, want %d", got, want)
	}
	if got, want := snapshot.Modules[0].Stats.InvokeCount, uint64(11); got != want {
		t.Fatalf("invoke count = %d, want %d", got, want)
	}
	if got, want := snapshot.Modules[0].Stats.AverageLatencyMs, 13.5; got != want {
		t.Fatalf("average latency = %f, want %f", got, want)
	}
	if len(snapshot.Modules[0].Options) != 1 || snapshot.Modules[0].Options[0].Key != "timer.refresh-grants.interval" {
		t.Fatalf("options = %#v, want timer interval option", snapshot.Modules[0].Options)
	}
	option := snapshot.Modules[0].Options[0]
	if option.ReadOnly || option.Persistence != "live-only" || option.Units != "ms" || option.DefaultValue != "30000" {
		t.Fatalf("option metadata = %#v, want mutable live-only millisecond timer option", option)
	}
	if len(snapshot.Modules[0].Actions) == 0 {
		t.Fatalf("actions = %#v, want lifecycle actions", snapshot.Modules[0].Actions)
	}
	if len(snapshot.Modules[0].StatusHistory) == 0 || snapshot.Modules[0].StatusHistory[0].Status != "registered" {
		t.Fatalf("status history = %#v, want registration event", snapshot.Modules[0].StatusHistory)
	}
	if snapshot.Modules[0].Links == nil || snapshot.Modules[0].Links.LogsURL == "" || snapshot.Modules[0].Links.EventsURL == "" {
		t.Fatalf("links = %#v, want log and event links", snapshot.Modules[0].Links)
	}
}

func TestUpdateRuntimeModuleOptionAppliesLiveCronOverride(t *testing.T) {
	mgr := New()
	plugin := &fakeRuntimePlugin{
		id: "licensing",
		descriptor: RuntimeModuleDescriptor{
			Manifest: &RuntimeModuleManifest{
				PluginID: "licensing",
				Timers: []RuntimeModuleTimer{
					{
						TimerID:           "refresh-grants",
						MethodID:          "refresh_grants",
						DefaultIntervalMs: 30000,
					},
				},
			},
		},
	}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	updated, err := mgr.UpdateRuntimeModuleOption(context.Background(), "licensing", "timer.refresh-grants.interval", "45000")
	if err != nil {
		t.Fatalf("UpdateRuntimeModuleOption failed: %v", err)
	}
	if updated.Value != "45000" || updated.Persistence != "live-only" || updated.Units != "ms" {
		t.Fatalf("updated option = %#v, want live millisecond override", updated)
	}

	snapshot := mgr.RuntimeSnapshot()
	if got, want := snapshot.Modules[0].Options[0].Value, "45000"; got != want {
		t.Fatalf("snapshot option value = %q, want %q", got, want)
	}
}

func TestRuntimeModuleInputValuesMarkModuleUpdatedAndRestartAppliesHistory(t *testing.T) {
	mgr := New()
	plugin := &fakeRuntimePlugin{
		id: "licensing",
		descriptor: RuntimeModuleDescriptor{
			Manifest: &RuntimeModuleManifest{
				PluginID: "licensing",
				Methods: []RuntimeModuleMethod{
					{
						MethodID: "server_configure_runtime",
						InputPorts: []RuntimeModulePort{
							{
								PortID:      "request",
								DisplayName: "Request",
								Required:    true,
								AcceptedTypeSets: []RuntimeModuleAcceptedTypeSet{
									{
										SetID:              "licensing-config",
										AllowedWireFormats: []string{"FLATBUFFER_JSON", "JSON"},
										AllowedTypes: []RuntimeModuleTypeRef{
											{
												SchemaName:     "MODULE.fbs",
												FileIdentifier: "MODL",
												SchemaVersion:  "1.0.0",
												RootType:       "ConfigureRuntimeRequest",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	values, err := mgr.SaveRuntimeModuleInputValues(context.Background(), "licensing", []RuntimeModuleInputValue{
		{
			MethodID:       "server_configure_runtime",
			PortID:         "request",
			WireFormat:     "FLATBUFFER_JSON",
			Encoding:       "json",
			SchemaName:     "MODULE.fbs",
			FileIdentifier: "MODL",
			SchemaVersion:  "1.0.0",
			RootType:       "ConfigureRuntimeRequest",
			Value:          `{"refreshIntervalMs":45000}`,
		},
	})
	if err != nil {
		t.Fatalf("SaveRuntimeModuleInputValues failed: %v", err)
	}
	if len(values) != 1 || values[0].UpdatedAt == "" {
		t.Fatalf("saved values = %#v, want timestamped value", values)
	}

	snapshot := mgr.RuntimeSnapshot()
	module := snapshot.Modules[0]
	if got, want := module.Status, "updated"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if !module.RestartPending {
		t.Fatalf("restartPending = false, want true")
	}
	if len(module.InputValues) != 1 || module.InputValues[0].Value == "" {
		t.Fatalf("input values = %#v, want saved input value", module.InputValues)
	}
	if len(module.CommandHistory) == 0 || module.CommandHistory[len(module.CommandHistory)-1].Command != "save-inputs" {
		t.Fatalf("command history = %#v, want save-inputs entry", module.CommandHistory)
	}

	if err := mgr.RunRuntimeModuleAction(context.Background(), "licensing", "restart"); err != nil {
		t.Fatalf("RunRuntimeModuleAction(restart) failed: %v", err)
	}

	snapshot = mgr.RuntimeSnapshot()
	module = snapshot.Modules[0]
	if got, want := module.Status, "running"; got != want {
		t.Fatalf("status after restart = %q, want %q", got, want)
	}
	if module.RestartPending {
		t.Fatalf("restartPending after restart = true, want false")
	}
	if got, want := plugin.appliedInputCalls, 1; got != want {
		t.Fatalf("applied input calls = %d, want %d", got, want)
	}
	if len(plugin.appliedInputs) != 1 || plugin.appliedInputs[0].MethodID != "server_configure_runtime" {
		t.Fatalf("applied inputs = %#v, want saved configure input", plugin.appliedInputs)
	}
	last := module.CommandHistory[len(module.CommandHistory)-1]
	if last.Command != "restart" || last.Status != "applied" {
		t.Fatalf("last command = %#v, want applied restart", last)
	}
}

func TestRunRuntimeModuleActionClearErrorRecordsHistory(t *testing.T) {
	mgr := New()
	plugin := &fakeRuntimePlugin{
		id:       "broken",
		startErr: context.Canceled,
	}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), RuntimeContext{Mode: "test"}); err == nil {
		t.Fatal("StartAll succeeded, want plugin start error")
	}

	if err := mgr.RunRuntimeModuleAction(context.Background(), "broken", "clear-error"); err != nil {
		t.Fatalf("RunRuntimeModuleAction failed: %v", err)
	}
	snapshot := mgr.RuntimeSnapshot()
	if got, want := snapshot.Modules[0].Status, "registered"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if len(snapshot.Modules[0].StatusHistory) < 3 {
		t.Fatalf("status history = %#v, want registration, error, clear-error", snapshot.Modules[0].StatusHistory)
	}
}

func TestRunRuntimeModuleActionControlsModuleLifecycle(t *testing.T) {
	mgr := New()
	plugin := &fakeRuntimePlugin{id: "licensing"}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	for _, actionID := range []string{"pause", "start", "stop", "load", "start", "restart", "reload-manifest", "unload"} {
		if err := mgr.RunRuntimeModuleAction(context.Background(), "licensing", actionID); err != nil {
			t.Fatalf("RunRuntimeModuleAction(%q) failed: %v", actionID, err)
		}
	}

	if got, want := plugin.startCalls, 4; got != want {
		t.Fatalf("start calls = %d, want %d", got, want)
	}
	if got, want := plugin.loadCalls, 2; got != want {
		t.Fatalf("load calls = %d, want %d", got, want)
	}
	if got, want := plugin.pauseCalls, 1; got != want {
		t.Fatalf("pause calls = %d, want %d", got, want)
	}
	if got, want := plugin.resumeCalls, 1; got != want {
		t.Fatalf("resume calls = %d, want %d", got, want)
	}
	if got, want := plugin.closeCalls, 4; got != want {
		t.Fatalf("close calls = %d, want %d", got, want)
	}

	snapshot := mgr.RuntimeSnapshot()
	if got, want := snapshot.Modules[0].Status, "unloaded"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func TestRuntimeSnapshotEnablesLifecycleActionsForCurrentStatus(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		enabled map[string]bool
	}{
		{
			name:   "running",
			status: "running",
			enabled: map[string]bool{
				"pause":           true,
				"stop":            true,
				"restart":         true,
				"reload-manifest": true,
				"unload":          true,
			},
		},
		{
			name:   "paused",
			status: "paused",
			enabled: map[string]bool{
				"start":           true,
				"restart":         true,
				"reload-manifest": true,
				"unload":          true,
			},
		},
		{
			name:   "unloaded",
			status: "unloaded",
			enabled: map[string]bool{
				"load":  true,
				"start": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := buildRuntimeModuleActions(tt.status)
			seen := make(map[string]RuntimeModuleAction, len(actions))
			for _, action := range actions {
				seen[action.ActionID] = action
			}
			for _, actionID := range []string{"load", "unload", "pause", "start", "stop", "restart", "reload-manifest", "clear-error"} {
				action, ok := seen[actionID]
				if !ok {
					t.Fatalf("action %q missing from %#v", actionID, actions)
				}
				if got, want := action.Enabled, tt.enabled[actionID]; got != want {
					t.Fatalf("action %q enabled = %t, want %t for status %q", actionID, got, want, tt.status)
				}
			}
		})
	}
}

type fakeRuntimePlugin struct {
	id         string
	startErr   error
	descriptor RuntimeModuleDescriptor

	loadCalls   int
	startCalls  int
	pauseCalls  int
	resumeCalls int
	closeCalls  int

	appliedInputCalls int
	appliedInputs     []RuntimeModuleInputValue
}

func (p *fakeRuntimePlugin) ID() string { return p.id }

func (p *fakeRuntimePlugin) Load(context.Context) error {
	p.loadCalls++
	return nil
}

func (p *fakeRuntimePlugin) Start(context.Context, RuntimeContext) error {
	p.startCalls++
	return p.startErr
}

func (p *fakeRuntimePlugin) Pause(context.Context) error {
	p.pauseCalls++
	return nil
}

func (p *fakeRuntimePlugin) Resume(context.Context) error {
	p.resumeCalls++
	return nil
}

func (p *fakeRuntimePlugin) RegisterRoutes(*http.ServeMux) {}

func (p *fakeRuntimePlugin) Close() error {
	p.closeCalls++
	return nil
}

func (p *fakeRuntimePlugin) RuntimeDescriptor() RuntimeModuleDescriptor {
	return p.descriptor
}

func (p *fakeRuntimePlugin) ApplyRuntimeModuleInputs(_ context.Context, values []RuntimeModuleInputValue) error {
	p.appliedInputCalls++
	p.appliedInputs = append([]RuntimeModuleInputValue(nil), values...)
	return nil
}
