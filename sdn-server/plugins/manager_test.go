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

type fakeRuntimePlugin struct {
	id         string
	startErr   error
	descriptor RuntimeModuleDescriptor
}

func (p *fakeRuntimePlugin) ID() string { return p.id }

func (p *fakeRuntimePlugin) Start(context.Context, RuntimeContext) error { return p.startErr }

func (p *fakeRuntimePlugin) RegisterRoutes(*http.ServeMux) {}

func (p *fakeRuntimePlugin) Close() error { return nil }

func (p *fakeRuntimePlugin) RuntimeDescriptor() RuntimeModuleDescriptor {
	return p.descriptor
}
