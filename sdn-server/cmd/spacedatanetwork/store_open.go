package main

// Loop C.8b: read-flavored CLI verbs (search, sync status, identity export,
// dataset-pnm list/export, ...) must work against a store a live daemon
// holds. The v2 store is single-writer, so those verbs open the store
// read-only when the exclusive writer lock is taken: compact metadata is
// replayed from its CRC-valid prefix, stream files are only ever read, and
// all writes are rejected with storage.ErrStoreReadOnly.
//
// A READ VERB PAID FOR THE WHOLE STORE, AND MEASURED THAT IS AN OUTAGE.
//
// This file used to open with NO StoreOptions, on both its writable and its
// read-only path. With no options `deferRecordCatalogReplay` and
// `deferBootRebuilds` are both false, so a verb that wants to print a status
// line first replayed the ENTIRE record-catalog journal into the control
// tables and then rebuilt every derived cache — source summaries AND the
// engine hot window for all 226 routed standards.
//
// MEASURED on host-02 with the deployed binary, daemon running (so this took
// the read-only fallback), box load 1.36:
//
//	spacedatanetwork sync status --config /etc/spacedatanetwork/retriever/config.yaml
//	ELAPSED=480s RC=124   (killed by an 8-minute cap; it never answered)
//
// Eighteen log lines, the last of them `cold auxiliary metadata — replayed
// 5616 frames from the beginning in 3.614s`, and then seven and a half minutes
// inside the cold replay of an 8.5 GB journal. A read-only open persists no
// control database, so it is cold EVERY time — there is no warm mark to
// resume from and the cost grows with the store forever.
//
// THE FIX IS NOT "DEFER EVERYTHING", because two of these verbs really do read
// that state and a blanket deferral would turn a hang into a WRONG ANSWER —
// `search` would report zero records and `dataset-pnm list` would find none.
// So the open is deferred by default and every caller DECLARES what it reads.
// storeReadNeeds is that declaration, and it is checked by
// TestEveryReadVerbDeclaresTheDerivedStateItReads.

import (
	"errors"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// storeReadNeeds is what a read verb actually reads beyond the auxiliary
// tables. The auxiliary journal is NEVER deferred (it is the node's own state,
// not a cache — see newFlatSQLStore), so the directory, the pin ledger and
// dataset-shard publications are always populated and need no declaration.
type storeReadNeeds struct {
	// recordCatalog covers the control tables the compact record-catalog
	// journal replays into: sdn_record_index, the per-producer `sds_p_*`
	// record tables that recordReadSource unions, and sdn_record_source_tags.
	// Needed by anything that reads RECORDS: QueryRecentRecords,
	// QueryRawRecords, QueryRecords.
	recordCatalog bool

	// sourceSummaries covers sdn_record_source_summary, the derived per-lane
	// counts DataSummary reports. It IMPLIES recordCatalog: the summaries are
	// rebuilt FROM the control tables, so summarising an un-replayed store
	// answers zero rather than failing.
	sourceSummaries bool
}

// openStoreForReading opens the store at storagePath preferring a normal
// writable open, and falls back to a read-only open (with a stderr notice)
// when another process — typically the daemon — holds the writer lock.
//
// Both paths open DEFERRED and then hydrate exactly what `needs` declares, so
// a verb pays for the state it reads and nothing else. A hydration failure is
// returned, never swallowed: answering from an unpopulated table is the one
// outcome worse than answering slowly.
func openStoreForReading(storagePath string, validator *sds.Validator, needs storeReadNeeds) (*storage.FlatSQLStore, error) {
	deferred := []storage.StoreOption{
		storage.WithDeferredRecordCatalogReplay(),
		storage.WithDeferredBootRebuilds(),
	}

	store, err := storage.NewFlatSQLStore(storagePath, validator, deferred...)
	if err != nil {
		if !errors.Is(err, storage.ErrStoreLocked) {
			return nil, fmt.Errorf("failed to open storage: %w", err)
		}
		roStore, roErr := storage.NewFlatSQLStoreReadOnly(storagePath, validator, deferred...)
		if roErr != nil {
			return nil, fmt.Errorf("failed to open storage read-only after writer-lock contention (%v): %w", err, roErr)
		}
		fmt.Fprintf(os.Stderr, "note: store at %s is held by another process (daemon); opened READ-ONLY point-in-time view\n", storagePath)
		store = roStore
	}

	if err := hydrateForReading(store, needs); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// hydrateForReading populates exactly the declared state. Split out so the
// test can drive it directly against both open flavours.
func hydrateForReading(store *storage.FlatSQLStore, needs storeReadNeeds) error {
	// SUMMARIES ARE DERIVED FROM THE CONTROL TABLES, so they IMPLY the catalog.
	// RebuildSourceSummaries reads sdn_record_source_tags and the per-producer
	// record tables — exactly the state the catalog replay populates — so
	// rebuilding without replaying first summarises an EMPTY store and reports
	// zero records, cheerfully and wrongly. Found by
	// TestDeclaredNeedsAnswerTheSameAsAFullyHydratedOpen on its first run,
	// against the first cut of this file.
	if needs.recordCatalog || needs.sourceSummaries {
		if _, err := store.ReplayRecordCatalog(false, nil); err != nil {
			return fmt.Errorf("hydrate record catalog for reading: %w", err)
		}
	}
	if needs.sourceSummaries {
		if err := store.RebuildSourceSummaries(); err != nil {
			return fmt.Errorf("rebuild source summaries for reading: %w", err)
		}
	}
	return nil
}
