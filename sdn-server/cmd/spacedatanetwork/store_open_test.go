package main

import (
	"errors"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// seedReadVerbStore writes one record (control tables + derived summaries) and
// closes the store, returning its path.
func seedReadVerbStore(t *testing.T) (string, *sds.Validator) {
	t.Helper()
	dir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	seed, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	payload := sds.NewOMMBuilder().
		WithNoradCatID(56775).
		WithObjectName("READ-VERB-FIXTURE").
		WithEpoch("2026-05-25T06:08:54Z").
		Build()
	if _, err := seed.StoreWithSourceTags("OMM.fbs", payload, "16Uiu2HReadVerbFixture", nil, storage.SourceTags{
		ProviderID:        "space-data-network-02",
		SourceName:        "readverb-gp",
		BatchID:           "read-verb-batch",
		ProducerPeerID:    "16Uiu2HReadVerbFixture",
		ProducerPublicKey: "provider-public-key",
	}); err != nil {
		seed.Close()
		t.Fatalf("seed record: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	return dir, validator
}

// readVerbAnswers is everything the read verbs actually ask the store.
type readVerbAnswers struct {
	summaryRecords int64
	recentRecords  int
	rawRecords     int
}

func collectReadVerbAnswers(t *testing.T, store *storage.FlatSQLStore) readVerbAnswers {
	t.Helper()
	got := readVerbAnswers{}
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if summary != nil {
		got.summaryRecords = summary.TotalRecords
	}
	recent, err := store.QueryRecentRecords("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryRecentRecords: %v", err)
	}
	got.recentRecords = len(recent)
	raw, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("QueryRawRecords: %v", err)
	}
	got.rawRecords = len(raw)
	return got
}

// A DEFERRED OPEN MAY NOT CHANGE THE ANSWER. The read verbs open the store
// without rebuilding the engine hot window; every record answer must be
// byte-identical to a full open, because the records live in the control
// tables and not in the engine.
func TestReadVerbOpenAnswersTheSameAsAFullOpen(t *testing.T) {
	dir, validator := seedReadVerbStore(t)

	full, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("full open: %v", err)
	}
	want := collectReadVerbAnswers(t, full)
	if err := full.Close(); err != nil {
		t.Fatalf("full close: %v", err)
	}
	if want.summaryRecords == 0 || want.recentRecords == 0 || want.rawRecords == 0 {
		t.Fatalf("fixture is not discriminating: full open answered %+v — every field must be non-zero", want)
	}

	store, err := openStoreForReading(dir, validator)
	if err != nil {
		t.Fatalf("openStoreForReading: %v", err)
	}
	defer store.Close()
	if store.EngineHotWindowHydrated() {
		t.Fatal("a read verb rebuilt the engine hot window it never queries")
	}
	if got := collectReadVerbAnswers(t, store); got != want {
		t.Fatalf("read verb open answered %+v, full open answered %+v", got, want)
	}
}

// While the daemon holds the store there is no second opener: the verb must
// say so instead of reading a stale or torn copy.
func TestReadVerbOpenRefusesAHeldStore(t *testing.T) {
	dir, validator := seedReadVerbStore(t)
	holder, err := storage.NewFlatSQLStore(dir, validator, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("holder open: %v", err)
	}
	defer holder.Close()

	if _, err := openStoreForReading(dir, validator); !errors.Is(err, storage.ErrStoreLocked) {
		t.Fatalf("open under writer-lock contention = %v, want ErrStoreLocked", err)
	}
}
