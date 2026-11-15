package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// TestOpensWithFormat5EngineCoversFormat4AndFormat5 pins which store formats
// open through the threaded flatsql-p4 engine: format 5, and format 4, which
// converts to format 5 at first start. Formats 1 and 2 have their own engines.
func TestOpensWithFormat5EngineCoversFormat4AndFormat5(t *testing.T) {
	for format, want := range map[int]bool{0: false, 1: false, 2: false, 4: true, 5: true} {
		if got := opensWithFormat5Engine(format); got != want {
			t.Errorf("opensWithFormat5Engine(%d) = %v, want %v", format, got, want)
		}
	}
}

// TestDaemonPrewarmCompilesFormat5EngineForFreshStore is the first start of a
// new node: an empty AOT cache and an empty storage path, under the default
// store format (5). The daemon's own prewarm must leave the format-5 engine's
// threaded artifact in the cache, because the store opens only from it
// ("threaded AOT artifact not found" otherwise). Before the fix the daemon
// compiled it only for a format-4 store, so a fresh node could not start.
func TestDaemonPrewarmCompilesFormat5EngineForFreshStore(t *testing.T) {
	if ok, why := format4.Runnable(); !ok {
		t.Skipf("format 5 cannot run in this process: %s", why)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	t.Setenv(format4.FormatEnv, "")

	cacheDir := storage.EngineAOTCacheDir()
	if !strings.HasPrefix(cacheDir, home) {
		t.Fatalf("EngineAOTCacheDir() = %q, want it under the test HOME %q", cacheDir, home)
	}
	cfg := &config.Config{Mode: "full"}
	cfg.Storage.Path = t.TempDir()
	if format, err := storage.StoreFormat(cfg.Storage.Path); err != nil || format != 5 {
		t.Fatalf("a new store's format = %d (%v), want 5", format, err)
	}

	var out bytes.Buffer
	if err := prewarmEngineAOTForDaemon(&out, cfg); err != nil {
		if strings.Contains(err.Error(), "AOT compiler unavailable") {
			t.Skipf("libwasmedge lacks the AOT compiler: %v", err)
		}
		t.Fatalf("prewarmEngineAOTForDaemon: %v", err)
	}
	p4 := flatsqlrt.P4ThreadsAOTPath(cacheDir)
	if info, err := os.Stat(p4); err != nil || info.Size() == 0 {
		t.Fatalf("the format-5 engine's threaded artifact is missing after the daemon's prewarm (%s): %v\noutput:\n%s", p4, err, out.String())
	}
	if !strings.Contains(out.String(), "compiled the format-4 engine AOT artifact") {
		t.Fatalf("the daemon's prewarm did not report compiling the format-5 engine:\n%s", out.String())
	}
}
