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
				MemoryPages:    7,
				MemoryBytes:    7 * 65536,
				MaxMemoryPages: 1024,
				MaxMemoryBytes: 1024 * 65536,
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
	if len(snapshot.Modules[0].Options) != 1 || snapshot.Modules[0].Options[0].Key != "timer.refresh-grants.interval" {
		t.Fatalf("options = %#v, want timer interval option", snapshot.Modules[0].Options)
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
