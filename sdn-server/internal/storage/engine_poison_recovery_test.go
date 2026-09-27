package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// The host-02 failure, end to end: the engine is poisoned the way wasmrt
// poisons it when a statement runs past the per-call budget, nothing calls
// RecoverPoisonedEngine, and the store must still come back and take ingest
// again on its own.
func TestPoisonedEngineIsReplacedWithoutARestart(t *testing.T) {
	prev := enginePoisonWatchInterval
	enginePoisonWatchInterval = 20 * time.Millisecond
	t.Cleanup(func() { enginePoisonWatchInterval = prev })

	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()

	tags := SourceTags{ProviderID: "prov", SourceName: "gp", BatchID: "b1"}
	before, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1_700_000_000), "peer", nil, tags)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store("RFM.fbs", poisonTestPayload(1, 120), "peer", nil); err != nil {
		t.Fatal(err)
	}
	liveBefore, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatal(err)
	}

	engine, _ := store.EngineRuntime()
	epoch := store.EngineEpoch()
	engine.WasmModule().MarkPoisoned(fmt.Errorf("dedicated execution thread abandoned mid-call in %q: %w",
		"(batch)", errors.New("wasm execution exceeded wall-clock timeout")))
	if !engine.Poisoned() {
		t.Fatal("the engine does not report the wasmrt poison")
	}

	deadline := time.Now().Add(30 * time.Second)
	for store.EngineEpoch() == epoch {
		if time.Now().After(deadline) {
			t.Fatal("the poisoned engine was not replaced")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := store.EngineRecoveries(); got != 1 {
		t.Fatalf("EngineRecoveries = %d, want 1", got)
	}
	if replacement, _ := store.EngineRuntime(); replacement == nil || replacement == engine || replacement.Poisoned() {
		t.Fatal("the store is not on a healthy replacement engine")
	}

	// Ingest, reads and the quota counter all resume on the replacement.
	after, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, 20580, "HST", 1_700_000_500), "peer", nil, tags)
	if err != nil {
		t.Fatalf("ingest after recovery: %v", err)
	}
	for _, cid := range []string{before, after} {
		if _, err := store.GetRecord("OMM.fbs", cid); err != nil {
			t.Fatalf("read %s after recovery: %v", cid, err)
		}
	}
	liveAfter, err := store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes after recovery: %v", err)
	}
	if liveAfter <= liveBefore {
		t.Fatalf("LiveRecordBytes after recovery = %d, want more than %d", liveAfter, liveBefore)
	}
	requireCounterMatchesScan(t, store, "after engine recovery")
	if _, err := store.DataSummary(); err != nil {
		t.Fatalf("DataSummary after recovery: %v", err)
	}
}

// Without the watch the same poison is permanent for every store path that
// never calls RecoverPoisonedEngine itself — which is what host-02 measured.
func TestPoisonedEngineRefusesStoreWritesUntilReplaced(t *testing.T) {
	prev := enginePoisonWatchInterval
	enginePoisonWatchInterval = time.Hour
	t.Cleanup(func() { enginePoisonWatchInterval = prev })

	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()
	engine, _ := store.EngineRuntime()
	engine.WasmModule().MarkPoisoned(errors.New("dedicated execution thread abandoned mid-call"))

	if _, err := store.Store("RFM.fbs", poisonTestPayload(2, 64), "peer", nil); !wasmrt.IsPoisoned(err) {
		t.Fatalf("write on a poisoned engine = %v, want a poisoned-module refusal", err)
	}
	// A poisoned engine is not a zero: the old scan logged and skipped every
	// schema and answered 0.
	if _, err := store.LiveRecordBytes(); !wasmrt.IsPoisoned(err) {
		t.Fatalf("LiveRecordBytes on a poisoned engine = %v, want a poisoned-module refusal", err)
	}
}

func poisonTestPayload(i, size int) []byte {
	p := make([]byte, size)
	for b := range p {
		p[b] = byte((i*31 + b) % 251)
	}
	return p
}
