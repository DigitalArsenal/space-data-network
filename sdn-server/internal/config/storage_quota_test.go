package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- storage.max_size parsing/resolution (Task D3) ----------------------

func TestResolveMaxSizeBytesPercentageResolvesAgainstStatfs(t *testing.T) {
	dir := t.TempDir()

	total, err := diskTotalBytes(dir)
	if err != nil {
		t.Fatalf("diskTotalBytes(%q) failed: %v", dir, err)
	}
	if total == 0 {
		t.Fatal("diskTotalBytes returned 0; test environment cannot Statfs the temp dir")
	}

	cfg := StorageConfig{MaxSize: "90%"}
	got, err := cfg.ResolveMaxSizeBytes(dir)
	if err != nil {
		t.Fatalf("ResolveMaxSizeBytes(90%%) failed: %v", err)
	}
	want := int64(float64(total) * 90.0 / 100.0)
	if got != want {
		t.Fatalf("ResolveMaxSizeBytes(90%%) = %d, want %d (90%% of statfs total %d)", got, want, total)
	}
}

func TestResolveMaxSizeBytesDefaultIsNinetyPercentNotFixedTenGB(t *testing.T) {
	dir := t.TempDir()

	total, err := diskTotalBytes(dir)
	if err != nil {
		t.Fatalf("diskTotalBytes(%q) failed: %v", dir, err)
	}

	// Unset MaxSize must resolve the same way an explicit "90%" does — i.e.
	// as a percentage of disk, not the old fixed 10GB default.
	empty := StorageConfig{MaxSize: ""}
	gotEmpty, err := empty.ResolveMaxSizeBytes(dir)
	if err != nil {
		t.Fatalf("ResolveMaxSizeBytes(empty) failed: %v", err)
	}
	explicit := StorageConfig{MaxSize: "90%"}
	gotExplicit, err := explicit.ResolveMaxSizeBytes(dir)
	if err != nil {
		t.Fatalf("ResolveMaxSizeBytes(90%%) failed: %v", err)
	}
	if gotEmpty != gotExplicit {
		t.Fatalf("empty MaxSize resolved to %d, want it to match explicit 90%% (%d)", gotEmpty, gotExplicit)
	}
	tenGB := int64(10) << 30
	if int64(total) > tenGB*2 && gotEmpty == tenGB {
		t.Fatalf("empty MaxSize resolved to the OLD fixed 10GB default (%d); want ~90%% of disk", gotEmpty)
	}
}

func TestResolveMaxSizeBytesAbsoluteSizes(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		spec string
		want int64
	}{
		{"10GB", 10 << 30},
		{"10GiB", 10 << 30},
		{"500MB", 500 << 20},
		{"1TB", 1 << 40},
		{"100KB", 100 << 10},
		{"2048", 2048}, // bare integer bytes
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			cfg := StorageConfig{MaxSize: tc.spec}
			got, err := cfg.ResolveMaxSizeBytes(dir)
			if err != nil {
				t.Fatalf("ResolveMaxSizeBytes(%q) failed: %v", tc.spec, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveMaxSizeBytes(%q) = %d, want %d", tc.spec, got, tc.want)
			}
		})
	}
}

func TestResolveMaxSizeBytesBadInputErrorsClearly(t *testing.T) {
	dir := t.TempDir()

	cases := []string{
		"not-a-size",
		"10XB",
		"150%",
		"0%",
		"-5GB",
		"-10%",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			cfg := StorageConfig{MaxSize: spec}
			_, err := cfg.ResolveMaxSizeBytes(dir)
			if err == nil {
				t.Fatalf("ResolveMaxSizeBytes(%q) succeeded, want an error", spec)
			}
			if !strings.Contains(err.Error(), "storage.max_size") {
				t.Fatalf("ResolveMaxSizeBytes(%q) error = %q, want it to name storage.max_size", spec, err.Error())
			}
		})
	}
}

func TestParseStorageMaxSizeSpecIsSyntaxOnlyNoStatfs(t *testing.T) {
	// A percentage must parse without touching the filesystem at all (no
	// path argument here) — this is what Config.validate() relies on to
	// fail Load() fast on a malformed spec without needing storage.path to
	// exist yet.
	if _, err := parseStorageMaxSizeSpec("90%"); err != nil {
		t.Fatalf("parseStorageMaxSizeSpec(90%%) failed: %v", err)
	}
	if _, err := parseStorageMaxSizeSpec("10GB"); err != nil {
		t.Fatalf("parseStorageMaxSizeSpec(10GB) failed: %v", err)
	}
	if _, err := parseStorageMaxSizeSpec("garbage"); err == nil {
		t.Fatal("parseStorageMaxSizeSpec(garbage) succeeded, want an error")
	}
}

func TestDefaultConfigStorageMaxSizeIsPercentSentinel(t *testing.T) {
	cfg := Default()
	if cfg.Storage.MaxSize != "90%" {
		t.Fatalf("Default().Storage.MaxSize = %q, want \"90%%\" (DefaultStorageMaxSizePercent)", cfg.Storage.MaxSize)
	}
}

func TestLoadRejectsMalformedStorageMaxSize(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "storage:\n  max_size: \"not-a-size\"\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(configPath); err == nil {
		t.Fatal("Load with a malformed storage.max_size succeeded, want an error")
	}
}

// --- storage.gc_interval parsing (Task D3) -------------------------------

func TestResolveGCIntervalParsesDuration(t *testing.T) {
	cfg := StorageConfig{GCInterval: "30m"}
	got, err := cfg.ResolveGCInterval()
	if err != nil {
		t.Fatalf("ResolveGCInterval(30m) failed: %v", err)
	}
	if got != 30*time.Minute {
		t.Fatalf("ResolveGCInterval(30m) = %v, want 30m", got)
	}
}

func TestResolveGCIntervalDefaultsToOneHour(t *testing.T) {
	cfg := StorageConfig{GCInterval: ""}
	got, err := cfg.ResolveGCInterval()
	if err != nil {
		t.Fatalf("ResolveGCInterval(empty) failed: %v", err)
	}
	if got != time.Hour {
		t.Fatalf("ResolveGCInterval(empty) = %v, want 1h", got)
	}
}

func TestResolveGCIntervalBadInputErrorsClearly(t *testing.T) {
	cases := []string{"not-a-duration", "0s", "-1h"}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			cfg := StorageConfig{GCInterval: spec}
			_, err := cfg.ResolveGCInterval()
			if err == nil {
				t.Fatalf("ResolveGCInterval(%q) succeeded, want an error", spec)
			}
			if !strings.Contains(err.Error(), "storage.gc_interval") {
				t.Fatalf("ResolveGCInterval(%q) error = %q, want it to name storage.gc_interval", spec, err.Error())
			}
		})
	}
}

func TestLoadRejectsMalformedGCInterval(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "storage:\n  gc_interval: \"not-a-duration\"\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(configPath); err == nil {
		t.Fatal("Load with a malformed storage.gc_interval succeeded, want an error")
	}
}

// --- tip_queue.* tunables (D4 fold-in) -----------------------------------

func TestTipQueueConfigYAMLUnmarshalsMaxFetchBytes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	yamlDoc := "tip_queue:\n  max_fetch_bytes: 1048576\n  max_concurrent_fetches: 2\n  min_fetch_interval: 500ms\n"
	if err := os.WriteFile(configPath, []byte(yamlDoc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.TipQueue.MaxFetchBytes != 1048576 {
		t.Fatalf("TipQueue.MaxFetchBytes = %d, want 1048576", cfg.TipQueue.MaxFetchBytes)
	}
	if cfg.TipQueue.MaxConcurrentFetches != 2 {
		t.Fatalf("TipQueue.MaxConcurrentFetches = %d, want 2", cfg.TipQueue.MaxConcurrentFetches)
	}
	if cfg.TipQueue.MinFetchInterval != 500*time.Millisecond {
		t.Fatalf("TipQueue.MinFetchInterval = %v, want 500ms", cfg.TipQueue.MinFetchInterval)
	}
}

func TestTipQueueConfigDefaultsToZeroValueWhenUnset(t *testing.T) {
	cfg := Default()
	if cfg.TipQueue.MaxFetchBytes != 0 || cfg.TipQueue.MaxConcurrentFetches != 0 || cfg.TipQueue.MinFetchInterval != 0 {
		t.Fatalf("Default().TipQueue = %+v, want zero value (node.go's newTipQueueConfig treats zero as \"keep pubsub default\")", cfg.TipQueue)
	}
}
