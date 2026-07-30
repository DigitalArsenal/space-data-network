package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func reproEngineMemMiB(s *FlatSQLStore) float64 {
	rt, _ := s.EngineRuntime()
	if rt == nil {
		return -1
	}
	st, err := rt.MemoryStats()
	if err != nil {
		return -1
	}
	return float64(st.Bytes) / (1 << 20)
}

// TestReproHost01JournalReplay replays a REAL record-catalog journal into a real
// FlatSQL engine, exactly as the daemon and the identity-wizard CLI do, so the
// host-01 `unreachable` trap in flatsql_query_params reproduces off the host.
//
// SDN_REPRO_JOURNAL=/data/record-catalog.flatsqlmeta go test ./internal/storage/ \
//	  -run ReproHost01JournalReplay -v -timeout 180m
func TestReproHost01JournalReplay(t *testing.T) {
	src := os.Getenv("SDN_REPRO_JOURNAL")
	if src == "" {
		t.Skip("set SDN_REPRO_JOURNAL")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "record-catalog.flatsqlmeta")
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create journal copy: %v", err)
	}
	if _, err := out.ReadFrom(in); err != nil {
		t.Fatalf("copy journal: %v", err)
	}
	out.Close()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}

	start := time.Now()
	// Open DEFERRED so the replay is driven explicitly with progress, the way
	// the daemon does it; the CLI's non-deferred open runs the same frames.
	store, err := NewFlatSQLStore(dir, validator, WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	t.Logf("store opened in %s", time.Since(start).Round(time.Millisecond))

	last := 0
	replayStart := time.Now()
	n, err := store.ReplayRecordCatalogContext(context.Background(), false, func(done int) {
		if done-last >= 25000 {
			last = done
			t.Logf("progress %d frames in %s (engineMem=%.1f MiB)", done,
				time.Since(replayStart).Round(time.Millisecond), reproEngineMemMiB(store))
		}
	})
	if err != nil {
		t.Fatalf("REPRODUCED: replay failed after %d frames / %s: %v (engineMem=%.1f MiB)",
			n, time.Since(replayStart).Round(time.Millisecond), err, reproEngineMemMiB(store))
	}
	t.Logf("replay COMPLETED %d frames in %s (engineMem=%.1f MiB)", n,
		time.Since(replayStart).Round(time.Millisecond), reproEngineMemMiB(store))
}
