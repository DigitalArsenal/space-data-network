package storage

// auxiliary_metadata_canary_test.go — the CANARY-SHAPED store-open measurement.
//
// This is the harness that reproduces, on a laptop, the shape Hephaestus
// measured on vm-orbit-det-01: an auxiliary journal in the tens of megabytes
// against a disk-backed control database. It is not a unit test and never runs
// in a normal `go test ./...`: it builds a multi-megabyte fixture, so it is
// gated behind SDN_FLATSQL_AUX_CANARY_MB.
//
//	SDN_FLATSQL_AUX_CANARY_MB=20 go test ./internal/storage/ \
//	  -run TestAuxiliaryReplayCanaryStoreOpen -v -timeout 3600s
//
// Run it on the SAME box against the pre-fix commit to get the A leg. Absolute
// numbers from a laptop are not the fleet's numbers; the RATIO and the
// cold-versus-warm difference are the evidence.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAuxiliaryReplayCanaryStoreOpen(t *testing.T) {
	raw := os.Getenv("SDN_FLATSQL_AUX_CANARY_MB")
	if raw == "" {
		t.Skip("set SDN_FLATSQL_AUX_CANARY_MB=<megabytes> to run the canary-shaped store-open measurement")
	}
	targetMB, err := strconv.Atoi(raw)
	if err != nil || targetMB <= 0 {
		t.Fatalf("SDN_FLATSQL_AUX_CANARY_MB=%q is not a positive integer", raw)
	}
	target := int64(targetMB) << 20

	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)

	build := time.Now()
	gen := 0
	for store.auxiliaryMetadata.validLength() < target {
		gen++
		writeAuxiliaryFixture(t, store, gen)
	}
	journalBytes := store.auxiliaryMetadata.validLength()
	t.Logf("fixture: %d generations, auxiliary journal %d bytes (%.1f MB), built in %s",
		gen, journalBytes, float64(journalBytes)/(1<<20), time.Since(build).Round(time.Millisecond))
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	// COLD: no control database at all, so the whole journal is replayed.
	if err := removeControlDatabaseFiles(filepath.Join(basePath, flatSQLControlDBName)); err != nil {
		t.Fatalf("discard control database: %v", err)
	}
	coldStart := time.Now()
	cold := newFixtureStore(t, basePath)
	coldOpen := time.Since(coldStart)
	coldFrames := cold.bootAuxFrames
	if err := cold.Close(); err != nil {
		t.Fatalf("Close() cold: %v", err)
	}

	// WARM: the mark the cold boot left behind.
	warmStart := time.Now()
	warm := newFixtureStore(t, basePath)
	warmOpen := time.Since(warmStart)
	warmFrames := warm.bootAuxFrames
	warmResume := warm.bootAuxFrom
	if err := warm.Close(); err != nil {
		t.Fatalf("Close() warm: %v", err)
	}

	fmt.Printf("CANARY auxiliary journal %.1f MB | COLD store-open %s (%d frames) | WARM store-open %s (%d frames, resumed at %d)\n",
		float64(journalBytes)/(1<<20), coldOpen.Round(time.Millisecond), coldFrames,
		warmOpen.Round(time.Millisecond), warmFrames, warmResume)

	if warmFrames != 0 {
		t.Fatalf("warm store-open applied %d auxiliary frames, want 0", warmFrames)
	}
	if warmOpen > coldOpen {
		t.Fatalf("warm store-open %s is SLOWER than cold %s; the mark is not doing its job", warmOpen, coldOpen)
	}
}
