package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
//	SDN_REPRO_JOURNAL=/data/record-catalog.flatsqlmeta go test ./internal/storage/ \
//		  -run ReproHost01JournalReplay -v -timeout 180m
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

// TestReproHost01JournalReplayWithLiveWrites replays the real journal WHILE
// live traffic writes records — which is host-01's actual condition and the ONE
// axis the plain repro above does not cover.
//
// It is what separates the two defects in this failure:
//
//   - GUEST (flatsql, fixed): a SQL error inside a bound INSERT must come back
//     as an error, not as `unreachable` in flatsql_query_params. Before the
//     no-eh query-error latch this test could only ever end in a poisoned
//     engine, so the host-side cause underneath was invisible.
//   - HOST: replayRowIDState snapshots rowid ownership ONCE, at replay start,
//     but a chunked replay releases the store lock between windows, so live
//     inserts take rowids from the SAME space the replay is handing out.
//
// A failure here must therefore be a clean, named SQL error — never a trap and
// never a poisoned runtime.
//
//	SDN_REPRO_JOURNAL=/path/record-catalog.flatsqlmeta go test ./internal/storage/ \
//	  -run ReproHost01JournalReplayWithLiveWrites -v -timeout 180m
func TestReproHost01JournalReplayWithLiveWrites(t *testing.T) {
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
	store, err := NewFlatSQLStore(dir, validator, WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	live := 0
	liveErrs := 0
	replayStart := time.Now()
	n, err := store.ReplayRecordCatalogContext(context.Background(), false, func(done int) {
		// One live publish per replay window, on the live write path, in the
		// gap where the replay does not hold the store lock.
		if _, werr := store.Store("RFM.fbs", []byte(fmt.Sprintf("live record %d", done)), "TestPeer", nil); werr != nil {
			liveErrs++
			if liveErrs <= 3 {
				t.Logf("live write at %d frames failed: %v", done, werr)
			}
			return
		}
		live++
	})
	elapsed := time.Since(replayStart).Round(time.Millisecond)

	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "unreachable") || strings.Contains(low, "poison") {
			t.Fatalf("GUEST TRAPPED under live writes after %d frames / %s: %v", n, elapsed, err)
		}
		t.Fatalf("replay failed under live writes after %d frames / %s (clean error, host-side): %v",
			n, elapsed, err)
	}
	t.Logf("replay COMPLETED %d frames in %s with %d live writes (%d live errors)",
		n, elapsed, live, liveErrs)
}
