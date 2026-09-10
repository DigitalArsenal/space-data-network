package modulert

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

func TestRuntimeSnapshotRemainsResponsiveDuringInvocation(t *testing.T) {
	module := &Module{manifest: &Manifest{PluginID: "busy-runtime-fixture", Name: "Busy fixture", Version: "1"}, contentHash: "fixture-hash"}
	module.recordInvokeResult(time.Now(), nil)
	manager := plugins.New()
	if err := manager.Register(module); err != nil {
		t.Fatal(err)
	}

	// InvokeMethodFrames holds this lock across the guest and its host calls.
	// Keep that execution seam blocked until the status handler has returned.
	module.mu.Lock()
	defer module.mu.Unlock()
	done := make(chan []byte, 1)
	go func() {
		module.SetUIURL("/apps/fixture/")
		_ = module.ContentHash()
		_ = module.SignatureStatus()
		response := httptest.NewRecorder()
		manager.HandleRuntimeSnapshot().ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/modules/runtime", nil))
		done <- response.Body.Bytes()
	}()
	select {
	case body := <-done:
		var snapshot plugins.RuntimeSnapshot
		if err := json.Unmarshal(body, &snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Modules) != 1 || snapshot.Modules[0].Stats.InvokeCount != 1 || snapshot.Modules[0].Manifest.Name != "Busy fixture" || snapshot.Modules[0].UI.URL != "/apps/fixture/" {
			t.Fatalf("status lost completed metadata during invocation: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime status waited for the active guest invocation")
	}
}

func TestRuntimeDescriptorCachesRealMemoryAndClearsItOnClose(t *testing.T) {
	// A real VM with one exported memory page, bounded to four pages.
	wasm := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x05, 0x03, 0x01, 0x00, 0x01,
		0x07, 0x0a, 0x01, 0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00,
	}
	vm, err := wasmrt.NewModule(wasm, wasmrt.WithMaxMemoryPages(4))
	if err != nil {
		t.Fatal(err)
	}
	module := &Module{mod: vm, manifest: &Manifest{PluginID: "memory-fixture"}}
	t.Cleanup(func() { _ = module.Close() })
	module.mu.Lock()
	module.refreshMemoryStatsLocked()
	module.mu.Unlock()
	stats := module.RuntimeDescriptor().Stats
	if stats.MemoryPages != 1 || stats.MemoryBytes != 65536 || stats.MaxMemoryPages != 4 || stats.MaxMemoryBytes != 4*65536 {
		t.Fatalf("cached memory differs from the real VM: %+v", stats)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			module.recordInvokeResult(time.Now(), nil)
			module.recordTimerResult(nil)
			module.SetUIURL("/apps/memory-fixture/")
		}
	}()
	for i := 0; i < 100; i++ {
		_ = module.RuntimeDescriptor()
		_ = module.UIDescriptor()
	}
	<-done
	if stats := module.RuntimeDescriptor().Stats; stats.InvokeCount != 100 || stats.TimerRunCount != 100 || stats.LastTimerStatus != "ok" {
		t.Fatalf("concurrent metadata updates were lost: %+v", stats)
	}
	if err := module.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := module.RuntimeDescriptor().Stats; stats.MemoryPages != 0 || stats.MemoryBytes != 0 || stats.UptimeMs != 0 {
		t.Fatalf("closed runtime retained live memory/uptime: %+v", stats)
	}
}
