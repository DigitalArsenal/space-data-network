package storage

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// RecordExpiryQuery names one lane of one standard and the instant before
// which its records have left a rolling window. The record's arrival on this
// node decides (the store's own time: source_timestamp, format 2 _arrival,
// format 4 TS), so every standard expires alike, records stored before this
// existed included, with no extraction rule added (format 4 refuses a changed
// epoch rule on a type holding data, C-5).
//
// Owner 2026-10-12, the weather node: "we only need to keep a rolling window
// of 3 days, we should NOT be archiving more than that" - the window itself is
// the caller's (data-source/record-retention module config, default 72 h).
type RecordExpiryQuery struct {
	SchemaName  string
	ProviderID  string
	SourceName  string
	BeforeUnix  int64         // records that arrived before this (unix seconds) are deleted
	Batch       int           // index page size (default 500)
	MaxDeletes  int           // maximum indexed records processed (default 100000)
	MaxDuration time.Duration // page-boundary time budget (default and maximum 30s)
}

// RecordExpiryResult is what one expiry pass did.
type RecordExpiryResult struct {
	Deleted int `json:"deleted"` // format 4 reports the engine's removed copies
	// Missing is the shortfall against indexed CIDs. On format 4 this is a
	// lower bound when concurrent removals coincide with multi-copy CIDs:
	// the engine returns a copy count, not individual CID outcomes.
	Missing int  `json:"missing"`
	Capped  bool `json:"capped"` // a work/time budget stopped a nonempty tail
}

// ExpireRecords deletes the lane's records that arrived before q.BeforeUnix,
// paging the record index (the store's own catalog: every stored record, not
// the engine's resident hot window).
func (s *FlatSQLStore) ExpireRecords(q RecordExpiryQuery) (res RecordExpiryResult, err error) {
	q.SchemaName = strings.TrimSpace(q.SchemaName)
	if q.SchemaName == "" {
		return res, errors.New("record expiry: schema is required")
	}
	if strings.TrimSpace(q.SourceName) == "" && strings.TrimSpace(q.ProviderID) == "" {
		return res, errors.New("record expiry: provider_id or source_name is required")
	}
	if q.Batch <= 0 {
		q.Batch = 500
	}
	q.Batch = min(q.Batch, 1000)
	if q.MaxDeletes <= 0 {
		q.MaxDeletes = 100000
	}
	if q.MaxDuration <= 0 || q.MaxDuration > 30*time.Second {
		q.MaxDuration = 30 * time.Second
	}
	started := time.Now()
	log.Infof("Record expiry start schema=%q lane=%q/%q before=%d batch=%d limit=%d budget=%s", q.SchemaName, q.ProviderID, q.SourceName, q.BeforeUnix, q.Batch, q.MaxDeletes, q.MaxDuration)
	defer func() {
		log.Infof("Record expiry end schema=%q lane=%q/%q deleted=%d missing=%d capped=%t duration=%s error=%v", q.SchemaName, q.ProviderID, q.SourceName, res.Deleted, res.Missing, res.Capped, time.Since(started), err)
	}()
	deadline := started.Add(q.MaxDuration)
	if b, ok := s.rb.(format4Backend); ok {
		return b.expireRecords(q, deadline)
	}
	return s.expireRecordsIndexed(q, deadline)
}

// expireRecords uses the engine's sequence index without hydrating payloads
// or recounting the lane per page. A fixed high-water mark excludes arrivals
// during this pass. Delete owns the partition transactions and index cleanup;
// retention sends one bounded CID vector, never one transaction per record.
func (b format4Backend) expireRecords(q RecordExpiryQuery, deadline time.Time) (res RecordExpiryResult, err error) {
	if err := b.s.requireWritable("expire records"); err != nil {
		return res, err
	}
	if err := b.closed(); err != nil {
		return res, err
	}
	typ, err := f4Type(q.SchemaName)
	if err != nil {
		return res, err
	}
	api, ctx := b.d.api(), b.d.ctx
	query := format4.Query{Type: typ, Lane: f4Lane(q.ProviderID, q.SourceName, "", "", ""),
		Order: format4.OrderSeqAsc, Bulk: true,
		Preds: []format4.Pred{f4Pred(format4.FieldTS, format4.OpLt, format2.Int(q.BeforeUnix))}}
	head, err := api.Head(ctx, format4.Query{Type: typ})
	if errors.Is(err, format4.ErrNoType) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("record expiry head: %w", err)
	}
	if head.Through == 0 {
		return res, nil
	}
	query.SeqThrough = head.Through
	for visited := 0; ; {
		// One extra row tells us whether the budget capped a nonempty tail.
		pageSize := min(q.Batch, q.MaxDeletes-visited)
		query.Limit = int64(pageSize + 1)
		rows, err := api.Scan(ctx, query)
		if errors.Is(err, format4.ErrNoType) {
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("record expiry page: %w", err)
		}
		if len(rows) == 0 {
			return res, nil
		}
		if pageSize == 0 || !time.Now().Before(deadline) {
			res.Capped = true
			return res, nil
		}
		page := rows[:min(len(rows), pageSize)]
		cids := make([]string, 0, len(page))
		seen := make(map[string]bool, len(page))
		for _, row := range page {
			query.SeqAfter = max(query.SeqAfter, row.Seq)
			if !seen[row.CID] {
				cids = append(cids, row.CID)
				seen[row.CID] = true
			}
		}
		n, err := api.Delete(ctx, typ, cids)
		res.Deleted += int(n)
		if err != nil {
			return res, fmt.Errorf("record expiry delete batch: %w", err)
		}
		// The engine counts removed copies, which can exceed the CID count.
		res.Missing += max(0, len(cids)-int(n))
		visited += len(page)
		if len(rows) <= pageSize {
			return res, nil
		}
	}
}

// expireRecordsIndexed keeps the legacy index and single-record backend path
// separate from retention's public validation and run reporting.
func (s *FlatSQLStore) expireRecordsIndexed(q RecordExpiryQuery, deadline time.Time) (RecordExpiryResult, error) {
	var res RecordExpiryResult
	schema := q.SchemaName
	batch, limit := q.Batch, q.MaxDeletes
	before := q.BeforeUnix
	offset := 0 // only missing index entries remain before the next live page
	for {
		pageSize := min(batch, limit-res.Deleted-res.Missing)
		rows, _, err := s.RecordIndexPage(RecordIndexPageQuery{SchemaName: schema, ProviderID: q.ProviderID, SourceName: q.SourceName,
			ArrivedBefore: &before, Limit: pageSize + 1, Offset: offset})
		if err != nil {
			return res, fmt.Errorf("record expiry: %w", err)
		}
		if len(rows) == 0 {
			return res, nil
		}
		if pageSize == 0 || !time.Now().Before(deadline) {
			res.Capped = true
			return res, nil
		}
		for _, row := range rows[:min(len(rows), pageSize)] {
			if !time.Now().Before(deadline) {
				res.Capped = true
				return res, nil
			}
			if err := s.Delete(schema, row.CID); err != nil {
				if strings.HasPrefix(err.Error(), "not found: ") { // every backend's Delete for a record it no longer holds
					res.Missing++
					offset++
					continue
				}
				return res, fmt.Errorf("record expiry: delete %s: %w", row.CID, err)
			}
			res.Deleted++
		}
		if len(rows) <= pageSize {
			break
		}
	}
	return res, nil
}
