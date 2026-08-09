package storage

import (
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

	// GROUPING BY THE INDEX PREFIX IS NOT COSMETIC. schema_name is the leading
	// column of idx_sdn_record_source_tags_lookup (schema_name, provider_id,
	// source_name, batch_id); grouping by (provider_id, source_name) alone is
	// not an index-ordered prefix, so SQLite had to sort every row of a 1.6 M-row
	// table through a temp B-tree. Measured on host-01 2026-08-09: **24.9 s and
	// 26.0 s of engine hold** in the slow-statement log, inside the one engine
	// lock, on the retrieval-ledger reconciliation that runs after every boot.
	// Adding schema_name to the GROUP BY makes it an ordered covering-index scan
	// with no sort — and it cannot change the ANSWER, because the loop below
	// re-keys on sourceCountKey(provider, source) and ACCUMULATES (`+=`), so a
	// provider/source pair split across schemas is summed back together exactly
	// as the single grouped aggregate summed it.
	rows, err := s.db.Query(`
		SELECT provider_id, source_name, COUNT(*)
		FROM sdn_record_source_tags
		WHERE provider_id <> '' OR source_name <> ''
		GROUP BY schema_name, provider_id, source_name
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
