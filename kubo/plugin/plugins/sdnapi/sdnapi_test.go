package sdnapi

import (
	"testing"

	plugin "github.com/ipfs/kubo/plugin"
)

func TestInitDefaults(t *testing.T) {
	p := &sdnAPIPlugin{}
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init(nil): %v", err)
	}
	if !p.enabled {
		t.Error("SDN API must be enabled by default")
	}
	if p.addr != DefaultAddr {
		t.Errorf("addr = %q, want default %q", p.addr, DefaultAddr)
	}
	if !isLoopbackAddr(p.addr) {
		t.Errorf("default addr %q must be loopback-bound", p.addr)
	}
}

func TestInitConfigOverride(t *testing.T) {
	env := &plugin.Environment{Config: map[string]interface{}{
		"Enabled": false,
		"Addr":    "127.0.0.1:6060",
	}}
	p := &sdnAPIPlugin{}
	if err := p.Init(env); err != nil {
		t.Fatalf("Init(env): %v", err)
	}
	if p.enabled {
		t.Error("Enabled=false must disable the plugin")
	}
	if p.addr != "127.0.0.1:6060" {
		t.Errorf("addr = %q, want 127.0.0.1:6060", p.addr)
	}
}

func TestLoopbackClassification(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5020":    true,
		"[::1]:5020":        true,
		"localhost:5020":    true,
		"0.0.0.0:5020":      false,
		"[::]:5020":         false,
		"192.168.1.10:5020": false,
	}
	for addr, want := range cases {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestImplementsPluginDaemonInternal(t *testing.T) {
	var _ plugin.PluginDaemonInternal = (*sdnAPIPlugin)(nil)
}
