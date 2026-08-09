package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SourceRecordCounts reports how many records this store ACTUALLY holds for
// each provenance pair, keyed "<provider_id>/<source_name>" — the same key the
// operational retrieval ledger (internal/sourcemetrics) uses for its rows.
//
// WHY THIS READS THE TAGS TABLE, NOT THE SUMMARY. sdn_record_source_summary is
// a DERIVED cache: rebuildSourceSummaryForSchema deletes a schema's rows and
// reinserts them, so a caller that happens to read it mid-rebuild sees an empty
// answer for a store that is full. sdn_record_source_tags is the durable
// per-record provenance row itself — written on store, removed only when the
// record is superseded or deleted — so "zero here" means "no such record",
// which is the only claim a reconciliation is allowed to act on.
//
// The caller MUST still confirm RecordCatalogHydrated() first: on a node that
// deferred its compact-catalog replay, the tags table is legitimately empty
// until the replay lands, and mistaking that for data loss would send the node
// back to a publisher it does not owe a pull.
func (s *FlatSQLStore) SourceRecordCounts() (map[string]int64, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	// IT READS THE SUMMARY NOW, AND THE RACE THE COMMENT ABOVE GUARDS AGAINST
	// CANNOT HAPPEN. Every writer of sdn_record_source_summary — RebuildDerivedState,
	// RebuildSourceSummaries, RecoverPoisonedEngine, SupersedeSourceBatches,
	// ReconcileSourceBatch and garbageCollectBeforeLocked — holds s.mu in WRITE
	// mode for the whole rebuild, and this read holds it in READ mode. A Go
	// RWMutex admits no reader while a writer holds it, so "a caller that happens
	// to read it mid-rebuild sees an empty answer" is not a state this store can
	// be in. What the comment was really describing is a cost, and that cost was
	// measured: on host-01, 2026-08-09, aggregating the tag table cost **24.9 s,
	// 26.0 s and 29.1 s of engine hold**, inside the single engine lock, on the
	// retrieval-ledger reconciliation that runs after every boot.
	//
	// Grouping by the index prefix was tried first and REFUTED on the live box:
	// EXPLAIN confirmed the temp B-tree was gone, and the statement still cost
	// 29.1 s, because the cost is the COVERING INDEX SCAN of 1.8 M entries, not
	// the sort. Nothing that reads a row per record can be cheap in this engine.
	// The summary is the same fact at tens of rows.
	//
	// The durability guarantee the comment asks for is kept by the fallback
	// below, not by scanning: an EMPTY summary is not accepted as "no such
	// record" while the durable tag table still holds rows.
	rows, err := s.db.Query(`
		SELECT provider_id, source_name, SUM(record_count)
		FROM sdn_record_source_summary
		WHERE (provider_id <> '' OR source_name <> '') AND record_count > 0
		GROUP BY provider_id, source_name
	`)
	if err != nil {
		return nil, fmt.Errorf("count records per source: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int64, 8)
	for rows.Next() {
		var (
			providerID string
			sourceName string
			count      int64
		)
		if err := rows.Scan(&providerID, &sourceName, &count); err != nil {
			return nil, fmt.Errorf("scan record count per source: %w", err)
		}
		key := sourceCountKey(providerID, sourceName)
		if key == "" {
			continue
		}
		counts[key] += count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate record counts per source: %w", err)
	}
	if len(counts) == 0 {
		// An empty summary is only trustworthy if the DURABLE tag table is empty
		// too. One indexed descent (LIMIT 1), not a scan — the whole point.
		var probe int
		err := s.db.QueryRow(`SELECT 1 FROM sdn_record_source_tags LIMIT 1`).Scan(&probe)
		if err == nil {
			return nil, fmt.Errorf(
				"source record counts unavailable: sdn_record_source_summary is empty but " +
					"sdn_record_source_tags is not — the derived summary has not been rebuilt yet, and " +
					"reporting zero here would tell a reconciliation this store lost data it still holds")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("probe durable source tags: %w", err)
		}
	}
	return counts, nil
}

// sourceCountKey mirrors sourcemetrics.SourceID so the two ledgers are joinable
// without either package importing the other.
func sourceCountKey(providerID, sourceName string) string {
	providerID = strings.TrimSpace(providerID)
	sourceName = strings.TrimSpace(sourceName)
	switch {
	case providerID == "" && sourceName == "":
		return ""
	case providerID == "":
		return sourceName
	case sourceName == "":
		return providerID
	}
	return providerID + "/" + sourceName
}
