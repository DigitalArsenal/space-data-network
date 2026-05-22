package config

import "testing"

func TestDefaultConfigDoesNotRequireLocalTorRuntime(t *testing.T) {
	cfg := Default()
	if cfg.Tor.Enabled {
		t.Fatal("default config should not start a local tor runtime")
	}
	if cfg.Tor.HiddenServiceEnabled {
		t.Fatal("default config should not publish a tor hidden service")
	}
}
