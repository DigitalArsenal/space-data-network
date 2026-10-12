package storage

import (
	"errors"
	"fmt"
	"strings"
)

// RecordExpiryQuery names one lane of one standard and the instant before
// which its records have left a rolling window. The record's own indexed
// epoch decides (extractIndexedFields / format2 typeconfig: OMM and OEM epoch,
// MPE EPOCH, WXF VALID_TIME_MS, ...); records whose standard carries no epoch
// are never expired by this.
//
// Owner 2026-10-12, the weather node: "we only need to keep a rolling window
// of 3 days, we should NOT be archiving more than that" - the window itself is
// the caller's (data-source/record-retention module config, default 72 h).
type RecordExpiryQuery struct {
	SchemaName string
	ProviderID string
	SourceName string
	BeforeUnix int64 // records with an epoch before this (unix seconds) are deleted
	Batch      int   // index page size (default 500)
	MaxDeletes int   // stop after this many deletions (default 100000)
}

// RecordExpiryResult is what one expiry pass did.
type RecordExpiryResult struct {
	Deleted int  `json:"deleted"`
	Missing int  `json:"missing"` // listed by the index, no longer stored
	Capped  bool `json:"capped"`  // stopped at MaxDeletes with more to go
}

// ExpireRecords deletes the lane's records older than q.BeforeUnix, paging
// the record index (the store's own catalog: every stored record, not the
// engine's resident hot window) oldest first through the deletions.
func (s *FlatSQLStore) ExpireRecords(q RecordExpiryQuery) (RecordExpiryResult, error) {
	var res RecordExpiryResult
	schema := strings.TrimSpace(q.SchemaName)
	if schema == "" {
		return res, errors.New("record expiry: schema is required")
	}
	if strings.TrimSpace(q.SourceName) == "" && strings.TrimSpace(q.ProviderID) == "" {
		// A whole standard's records across every producer is not a rolling
		// window of one feed: refuse rather than sweep the node.
		return res, errors.New("record expiry: provider_id or source_name is required")
	}
	batch, limit := q.Batch, q.MaxDeletes
	if batch <= 0 {
		batch = 500
	}
	if limit <= 0 {
		limit = 100000
	}
	before := q.BeforeUnix
	seen := map[string]bool{}
	for res.Deleted < limit {
		rows, _, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: schema, ProviderID: q.ProviderID, SourceName: q.SourceName,
			EpochBefore: &before, Limit: batch})
		if err != nil {
			return res, fmt.Errorf("record expiry: %w", err)
		}
		progressed := false
		for _, row := range rows {
			if seen[row.CID] {
				continue
			}
			seen[row.CID] = true
			progressed = true
			if err := s.Delete(schema, row.CID); err != nil {
				if errors.Is(err, ErrRecordNotFound) {
					res.Missing++
					continue
				}
				return res, fmt.Errorf("record expiry: delete %s: %w", row.CID, err)
			}
			res.Deleted++
			if res.Deleted >= limit {
				res.Capped = len(rows) > 0
				break
			}
		}
		if len(rows) < batch || !progressed {
			break
		}
	}
	return res, nil
}
