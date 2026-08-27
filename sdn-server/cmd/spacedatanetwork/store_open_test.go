package main

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// seedReadVerbStore writes one record (control tables + derived summaries) and
// one directory row (auxiliary), then closes the store and returns its path.
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
	if err := seed.CheckpointRecordCatalog(); err != nil {
		seed.Close()
		t.Fatalf("seed checkpoint: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	return dir, validator
}

// readVerbAnswers is everything the four read verbs actually ask the store.
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

// A DEFERRED OPEN MAY NOT CHANGE THE ANSWER. THAT IS THE WHOLE CONTRACT.
//
// openStoreForReading now defers the record-catalog replay and every derived
// rebuild, because a read verb was paying for the entire store to print a
// status line (480 s and no answer on host-02). The danger that buys is a
// SILENT WRONG ANSWER: an un-hydrated control table reads as zero records, not
// as an error. So this pins the fixed point — a verb that declares what it
// reads gets byte-identical answers to the old fully-hydrated open.
func TestDeclaredNeedsAnswerTheSameAsAFullyHydratedOpen(t *testing.T) {
	dir, validator := seedReadVerbStore(t)

	// The legacy shape: no options at all, everything hydrated up front.
	legacyStore, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatalf("legacy open: %v", err)
	}
	want := collectReadVerbAnswers(t, legacyStore)
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("legacy close: %v", err)
	}
	if want.summaryRecords == 0 || want.recentRecords == 0 || want.rawRecords == 0 {
		t.Fatalf("fixture is not discriminating: legacy open answered %+v — every field must be non-zero or a deferral bug cannot be detected", want)
	}

	// dataset-pnm / EPM export declare the record catalog.
	catalogStore, err := openStoreForReading(dir, validator, storeReadNeeds{recordCatalog: true})
	if err != nil {
		t.Fatalf("open with recordCatalog: %v", err)
	}
	defer catalogStore.Close()
	if got, err := catalogStore.QueryRecentRecords("OMM.fbs", 10); err != nil {
		t.Fatalf("QueryRecentRecords: %v", err)
	} else if len(got) != want.recentRecords {
		t.Fatalf("QueryRecentRecords returned %d records with recordCatalog declared, fully-hydrated open returned %d — the deferral silently emptied a read verb",
			len(got), want.recentRecords)
	}
	if got, err := catalogStore.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10}); err != nil {
		t.Fatalf("QueryRawRecords: %v", err)
	} else if len(got) != want.rawRecords {
		t.Fatalf("QueryRawRecords returned %d records with recordCatalog declared, fully-hydrated open returned %d",
			len(got), want.rawRecords)
	}

	// search declares the derived source summaries.
	summaryStore, err := openStoreForReading(dir, validator, storeReadNeeds{sourceSummaries: true})
	if err != nil {
		t.Fatalf("open with sourceSummaries: %v", err)
	}
	defer summaryStore.Close()
	got, err := summaryStore.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if got == nil || got.TotalRecords != want.summaryRecords {
		t.Fatalf("DataSummary reported %v with sourceSummaries declared, fully-hydrated open reported %d — search would under-report the store",
			got, want.summaryRecords)
	}
}

// THE COST HAS TO ACTUALLY BE AVOIDED, or this change is only risk.
//
// sync status reads the pin ledger, dataset-shard publications and the
// directory — all auxiliary, and the auxiliary journal is never deferred. So
// its open must NOT hydrate the record catalog, and must still answer.
func TestSyncStatusOpenSkipsTheRecordCatalog(t *testing.T) {
	dir, validator := seedReadVerbStore(t)

	store, err := openStoreForReading(dir, validator, storeReadNeeds{})
	if err != nil {
		t.Fatalf("open with no declared needs: %v", err)
	}
	defer store.Close()

	if store.RecordCatalogHydrated() {
		t.Fatal("a verb that declared no needs still hydrated the record catalog — " +
			"on host-02 that replay is an 8.5 GB journal and 'sync status' answered nothing in 8 minutes")
	}

	// And the auxiliary state it DOES read is present without any hydration.
	records, err := store.QueryDirectory(storage.DirectoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("QueryDirectory on an un-hydrated open: %v", err)
	}
	_ = records
	if _, err := store.LocalReplicaStats(storage.LocalReplicaStatsQuery{}); err != nil {
		t.Fatalf("LocalReplicaStats on an un-hydrated open: %v", err)
	}
}

// The read-only fallback must carry the SAME deferrals. It is the path a live
// box actually takes (the daemon holds the writer lock), and it is the one
// that measured 480 s — a read-only open persists no control database, so it
// is cold every single time.
func TestReadOnlyOpenIsDeferredToo(t *testing.T) {
	dir, validator := seedReadVerbStore(t)

	holder, err := storage.NewFlatSQLStore(dir, validator,
		storage.WithDeferredRecordCatalogReplay(), storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("holder open: %v", err)
	}
	defer holder.Close()

	store, err := openStoreForReading(dir, validator, storeReadNeeds{})
	if err != nil {
		t.Fatalf("read-only open under writer-lock contention: %v", err)
	}
	defer store.Close()

	if !store.IsReadOnly() {
		t.Fatal("expected the read-only fallback while another process holds the writer lock")
	}
	if store.RecordCatalogHydrated() {
		t.Fatal("the READ-ONLY fallback hydrated the record catalog — this is the exact path that measured 480 s on host-02")
	}
}
