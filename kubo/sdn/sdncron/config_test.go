package sdncron

import (
	"os"
	"testing"
)

// TestConfigStoreRoundTripAndPerms proves atomic persistence, 0600 perms, a
// missing-file empty-config load, and that opaque module inputs round-trip.
func TestConfigStoreRoundTripAndPerms(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	// Missing file -> empty config, no error.
	cfg, err := store.Load("absent")
	if err != nil {
		t.Fatalf("Load(absent): %v", err)
	}
	if len(cfg) != 0 {
		t.Fatalf("Load(absent) = %+v, want empty", cfg)
	}

	// Save + reload preserves reserved and opaque keys.
	in := ModuleConfig{
		"interval_ms":  float64(60000),
		"timers":       map[string]interface{}{"poll": float64(1000)},
		"custom_input": "keep-me",
	}
	if err := store.Save("mod-a", in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := store.Load("mod-a")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ms, ok := out.intervalMs(); !ok || ms != 60000 {
		t.Fatalf("reloaded interval_ms = %v (ok=%v), want 60000", ms, ok)
	}
	if ms, ok := out.timerIntervalMs("poll"); !ok || ms != 1000 {
		t.Fatalf("reloaded timers[poll] = %v (ok=%v), want 1000", ms, ok)
	}
	if out["custom_input"] != "keep-me" {
		t.Fatalf("opaque input lost: %+v", out)
	}

	info, err := os.Stat(store.Path("mod-a"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perms = %v, want 0600", perm)
	}
}

// TestConfigValidate pins the reserved-key validation the settings API relies on.
func TestConfigValidate(t *testing.T) {
	good := []ModuleConfig{
		nil,
		{},
		{"interval_ms": float64(1)},
		{"timers": map[string]interface{}{"a": float64(5)}},
		{"anything": "opaque"},
	}
	for i, c := range good {
		if err := c.Validate(); err != nil {
			t.Fatalf("good[%d] Validate = %v, want nil", i, err)
		}
	}
	bad := []ModuleConfig{
		{"interval_ms": float64(0)},
		{"interval_ms": float64(-1)},
		{"interval_ms": "not a number"},
		{"interval_ms": 1.5},
		{"timers": "not an object"},
		{"timers": map[string]interface{}{"a": float64(0)}},
		{"timers": map[string]interface{}{"a": "x"}},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Fatalf("bad[%d] (%+v) Validate = nil, want error", i, c)
		}
	}
}

// TestConfigStoreNoPersistenceMode: an empty dir is a valid no-persistence store.
func TestConfigStoreNoPersistenceMode(t *testing.T) {
	store, err := NewConfigStore("")
	if err != nil {
		t.Fatalf("NewConfigStore(\"\"): %v", err)
	}
	if err := store.Save("m", ModuleConfig{"interval_ms": float64(1)}); err != nil {
		t.Fatalf("Save (no-persistence) = %v, want nil no-op", err)
	}
	cfg, err := store.Load("m")
	if err != nil || len(cfg) != 0 {
		t.Fatalf("Load (no-persistence) = %+v, %v; want empty, nil", cfg, err)
	}
	if store.Path("m") != "" {
		t.Fatalf("Path in no-persistence mode = %q, want empty", store.Path("m"))
	}
}
