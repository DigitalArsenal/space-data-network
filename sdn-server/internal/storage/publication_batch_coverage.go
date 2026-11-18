package storage

// publication_batch_coverage.go — which of a batch's records no completed
// publication of its lane carries.
//
// A batch whose publication failed is not published, but its records may be:
// a later pull of the same source that returns a record again tags that
// record to the later batch as well (a repeat CID takes the new batch's tag),
// and the later batch's publication carries it. A record the later pulls did
// not return (an element set its publisher has since replaced) stays tagged
// to the failed batch alone, and no publication carries it. The publication
// alert of the failed batch (internal/api dataset_publication_coverage.go)
// clears only when no record of the batch is left unpublished.
//
// A record counts as published when it is tagged to a batch of the lane that
// has a COMPLETED publication series of its whole batch: a series recorded
// by the publication's completion (never a "legacy:" series adopted from
// rows, which a publication that failed partway can leave), with the
// fingerprint of the batch's record set (a series limited to a head of the
// batch has none). Such a series exported every record tagged to its batch,
// and a batch's tags are written by that batch's ingest only, so a record
// tagged to it is in the series. Retention keeps the series of every batch
// the store still holds unless the lane declares its batches snapshots, so
// a series released by a snapshot lane's retention makes its records count
// as unpublished here (the count errs on the side of keeping an alert).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// ErrBatchCoverageUnsupported is the answer of a store format whose tags this
// file does not read (format 2).
var ErrBatchCoverageUnsupported = errors.New("this store format does not report which records of a batch are published")

// ErrTooManyUnpublished is the answer of UnpublishedBatchRecords when more
// records of the batch are unpublished than the caller follows.
var ErrTooManyUnpublished = errors.New("more records of the batch are unpublished than are followed")

// batchCoveragePage bounds the CIDs one step of the count reads.
const batchCoveragePage = 2048

// UnpublishedBatchRecords answers the CIDs of the records tagged to batchID
// in the lane (schema, provider, source) that carry no tag of a batch of the
// lane with a completed publication of its whole batch, and how many records
// the batch holds. More than max unpublished records is ErrTooManyUnpublished.
//
// Cost: one read of the batch's CIDs and of their tags, a page of 2,048 at a
// time, and one read of the lane's series.
func (s *FlatSQLStore) UnpublishedBatchRecords(ctx context.Context, schemaName, providerID, sourceName, batchID string, max int) (unpublished []string, total int64, err error) {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" || batchID == "" {
		return nil, 0, errors.New("schema, provider, source and batch are required")
	}
	published, err := s.publishedLaneBatches(ctx, schemaName, providerID, sourceName)
	if err != nil {
		return nil, 0, err
	}
	covered := func(provider, source, batch string) bool {
		return provider == providerID && source == sourceName && published[batch]
	}
	switch b := s.rb.(type) {
	case format4Backend:
		return b.unpublishedBatchRecords(ctx, schemaName, providerID, sourceName, batchID, covered, max)
	case nil:
		return s.unpublishedBatchRecordsFormat1(ctx, schemaName, providerID, sourceName, batchID, published, max)
	default:
		return nil, 0, ErrBatchCoverageUnsupported
	}
}

// EachLaneBatchCIDPage calls fn with the CIDs of the lane's records tagged to
// batchID, a page of at most 2,048 at a time, until fn errs or none are left.
func (s *FlatSQLStore) EachLaneBatchCIDPage(ctx context.Context, schemaName, providerID, sourceName, batchID string, fn func([]string) error) error {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" || batchID == "" {
		return errors.New("schema, provider, source and batch are required")
	}
	switch s.rb.(type) {
	case format4Backend:
		after := int64(0)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			cids, last, more, err := s.LaneBatchCIDPage(schemaName, providerID, sourceName, batchID, after, batchCoveragePage)
			if err != nil {
				return err
			}
			if len(cids) > 0 {
				if err := fn(cids); err != nil {
					return err
				}
			}
			if !more || last <= after {
				return nil
			}
			after = last
		}
	case nil:
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := s.format1BatchCIDPage(ctx, schemaName, providerID, sourceName, batchID, after)
			if err != nil {
				return fmt.Errorf("read batch %q records: %w", batchID, err)
			}
			if len(page) > 0 {
				if err := fn(page); err != nil {
					return err
				}
			}
			if len(page) < batchCoveragePage {
				return nil
			}
			after = page[len(page)-1]
		}
	default:
		return ErrBatchCoverageUnsupported
	}
}

// publishedLaneBatches is the lane's batches with a completed publication
// series of the whole batch that carries records: recorded at a
// publication's completion (not adopted as "legacy:"), with a fingerprint.
func (s *FlatSQLStore) publishedLaneBatches(ctx context.Context, schemaName, providerID, sourceName string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT batch_id FROM sdn_dataset_publication_series
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND record_count > 0
		  AND batch_id != '' AND series_id NOT LIKE 'legacy:%' AND fingerprint != ''`,
		schemaName, providerID, sourceName)
	if err != nil {
		return nil, fmt.Errorf("read the lane's published batches: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var batch string
		if err := rows.Scan(&batch); err != nil {
			return nil, err
		}
		out[batch] = true
	}
	return out, rows.Err()
}

func (b format4Backend) unpublishedBatchRecords(ctx context.Context, schemaName, providerID, sourceName, batchID string, covered func(provider, source, batch string) bool, max int) ([]string, int64, error) {
	var unpublished []string
	var total int64
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		cids, last, more, err := b.s.LaneBatchCIDPage(schemaName, providerID, sourceName, batchID, after, batchCoveragePage)
		if err != nil {
			return nil, 0, err
		}
		if len(cids) > 0 {
			tags, err := b.f4TagRows(schemaName, cids)
			if err != nil {
				return nil, 0, err
			}
			for _, c := range cids {
				total++
				if !anyTagCovered(tags[c], covered) {
					if len(unpublished) >= max {
						return nil, 0, ErrTooManyUnpublished
					}
					unpublished = append(unpublished, c)
				}
			}
		}
		if !more || last <= after {
			return unpublished, total, nil
		}
		after = last
	}
}

func anyTagCovered(rows []format4.TagRow, covered func(provider, source, batch string) bool) bool {
	for _, r := range rows {
		if covered(r.Provider, r.Source, r.Batch) {
			return true
		}
	}
	return false
}

// format1BatchCIDPage is one page of a batch's CIDs on a format-1 store,
// whose tags are rows of sdn_record_source_tags.
func (s *FlatSQLStore) format1BatchCIDPage(ctx context.Context, schemaName, providerID, sourceName, batchID, after string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT cid FROM sdn_record_source_tags
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ? AND cid > ?
		ORDER BY cid LIMIT ?`, schemaName, providerID, sourceName, batchID, after, batchCoveragePage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// unpublishedBatchRecordsFormat1 is the count on a format-1 store.
func (s *FlatSQLStore) unpublishedBatchRecordsFormat1(ctx context.Context, schemaName, providerID, sourceName, batchID string, published map[string]bool, max int) ([]string, int64, error) {
	var unpublished []string
	var total int64
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		page, err := s.format1BatchCIDPage(ctx, schemaName, providerID, sourceName, batchID, after)
		if err != nil {
			return nil, 0, fmt.Errorf("read batch %q records: %w", batchID, err)
		}
		if len(page) == 0 {
			return unpublished, total, nil
		}
		batches, err := func() (map[string][]string, error) {
			s.mu.RLock()
			defer s.mu.RUnlock()
			args := []any{schemaName, providerID, sourceName}
			for _, c := range page {
				args = append(args, c)
			}
			rows, err := s.db.QueryContext(ctx, `
				SELECT cid, batch_id FROM sdn_record_source_tags
				WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND cid IN (`+placeholderList(len(page))+`)`, args...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			out := map[string][]string{}
			for rows.Next() {
				var c, batch string
				if err := rows.Scan(&c, &batch); err != nil {
					return nil, err
				}
				out[c] = append(out[c], batch)
			}
			return out, rows.Err()
		}()
		if err != nil {
			return nil, 0, fmt.Errorf("read the tags of batch %q records: %w", batchID, err)
		}
		for _, c := range page {
			total++
			done := false
			for _, batch := range batches[c] {
				if published[batch] {
					done = true
					break
				}
			}
			if !done {
				if len(unpublished) >= max {
					return nil, 0, ErrTooManyUnpublished
				}
				unpublished = append(unpublished, c)
			}
		}
		after = page[len(page)-1]
		if len(page) < batchCoveragePage {
			return unpublished, total, nil
		}
	}
}
