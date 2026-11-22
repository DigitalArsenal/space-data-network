package storage

// publication_failed_batch_check.go — whether a batch whose publication
// failed is published now.
//
// The publication alert of a failed batch (internal/api
// dataset_publication_alert.go) stays until one of two things is true of the
// batch itself:
//
//   - a COMPLETED publication of the whole batch exported the set the batch
//     holds now: a series recorded by the publication's completion (never a
//     "legacy:" series adopted from rows, which a publication that failed
//     partway can leave), whose fingerprint equals the fingerprint of the set
//     a publication of the batch would export now (a series limited to a
//     head of the batch has none). On a lane whose publications export the
//     publication journal's retained selection (SelectPublication), that
//     set is the selection: the batch's journal entries less discarded
//     entries and catalog exclusions;
//   - the batch holds no records any more (a replace-current lane superseded
//     them, they expired, or the lane was retracted).
//
// A publication of any other batch never counts, whatever records it shares
// with the failed batch: the check reads the failed batch only. Its cost is
// one fingerprint of the batch (the read a publication of the batch makes to
// fingerprint it) and one read of the batch's series rows; nothing in it
// grows with the lane or with the number of its other batches, and nothing
// is kept between checks.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrBatchCoverageUnsupported is the answer of a store format whose batches
// this file does not check (format 2).
var ErrBatchCoverageUnsupported = errors.New("this store format does not report whether a batch is published")

// FailedBatchCheck is what CheckFailedBatchPublication found of a batch.
type FailedBatchCheck struct {
	// Records is how many records a publication of the batch would export
	// now.
	Records int64
	// Published reports that a completed publication of the whole batch
	// exported exactly that set.
	Published bool
	// Series is how many completed publications of the whole batch the lane
	// records; PublishedRecords is the record count of the newest of them
	// (0 when there is none).
	Series           int
	PublishedRecords int64
	// Took is how long the fingerprint of the batch took.
	Took time.Duration
}

// CheckFailedBatchPublication reports whether batchID of the lane (schema,
// provider, source) is published as it is now, reading that batch only. ctx
// ends it.
func (s *FlatSQLStore) CheckFailedBatchPublication(ctx context.Context, schemaName, providerID, sourceName, batchID string) (FailedBatchCheck, error) {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" || batchID == "" {
		return FailedBatchCheck{}, errors.New("schema, provider, source and batch are required")
	}
	switch s.rb.(type) {
	case format4Backend, nil:
	default:
		return FailedBatchCheck{}, ErrBatchCoverageUnsupported
	}
	fingerprints, newest, series, err := s.completedBatchSeries(ctx, schemaName, providerID, sourceName, batchID)
	if err != nil {
		return FailedBatchCheck{}, err
	}
	started := time.Now()
	fingerprint, records, err := s.PagedDatasetPublicationSetFingerprintContext(ctx, schemaName, providerID, sourceName, batchID)
	if err != nil {
		return FailedBatchCheck{}, fmt.Errorf("fingerprint batch %q: %w", batchID, err)
	}
	check := FailedBatchCheck{
		Records:          int64(records),
		Published:        records > 0 && fingerprint != "" && fingerprints[fingerprint],
		Series:           series,
		PublishedRecords: newest,
		Took:             time.Since(started),
	}
	log.Infof("Failed batch check %s %s/%s: fingerprinted batch %q (%d records, %d completed publication(s) of the whole batch) in %s",
		schemaName, providerID, sourceName, batchID, records, series, check.Took.Round(time.Millisecond))
	return check, nil
}

// completedBatchSeries is the fingerprints of the batch's completed
// publication series of the whole batch that carry records (recorded at a
// publication's completion, not adopted as "legacy:", with a fingerprint),
// the record count of the newest, and how many there are.
func (s *FlatSQLStore) completedBatchSeries(ctx context.Context, schemaName, providerID, sourceName, batchID string) (map[string]bool, int64, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT fingerprint, record_count FROM sdn_dataset_publication_series
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ? AND record_count > 0
		  AND series_id NOT LIKE 'legacy:%' AND fingerprint != ''
		ORDER BY published_at DESC`,
		schemaName, providerID, sourceName, batchID)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read the completed publications of batch %q: %w", batchID, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	var newest int64
	n := 0
	for rows.Next() {
		var fingerprint string
		var count int64
		if err := rows.Scan(&fingerprint, &count); err != nil {
			return nil, 0, 0, err
		}
		if n == 0 {
			newest = count
		}
		n++
		out[fingerprint] = true
	}
	return out, newest, n, rows.Err()
}
