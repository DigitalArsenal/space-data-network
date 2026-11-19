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
// has a COMPLETED publication series of its whole batch whose fingerprint is
// the fingerprint of the batch's record set NOW: a series recorded by the
// publication's completion (never a "legacy:" series adopted from rows,
// which a publication that failed partway can leave), with the fingerprint
// of the set it exported (a series limited to a head of the batch has none).
// A batch stored in parts gains records after a publication of its first
// part; its set then differs from the set that series exported, so none of
// its records counts until a publication of the whole batch as it is now
// completes. With the fingerprints equal the series exported exactly the
// batch's current records, so a record tagged to the batch is in it. A tag,
// once written, stays while the store holds its record (a repeat CID adds
// the new batch's tag to the record's tags), so a record seen tagged to a
// batch whose set equals the series' set, read after the tag, is in the
// series. Retention keeps the series of every batch the store still holds
// unless the lane declares its batches snapshots, so a series released by a
// snapshot lane's retention makes its records count as unpublished here
// (the count errs on the side of keeping an alert).
//
// Cost of a fingerprint: one read of the batch's CIDs, sorted (the read a
// publication of the batch makes to fingerprint it). A count reads it for
// each batch that tags a record of the counted batch and has a completed
// series, once per count; a batch with no completed series costs nothing.

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
// lane whose whole current set a completed publication exported, and how
// many records the batch holds. More than max unpublished records is
// ErrTooManyUnpublished.
//
// Cost: one read of the batch's CIDs and of their tags, a page of 2,048 at a
// time, one read of the lane's series, and one fingerprint of each batch
// that tags one of the records and has a completed series.
func (s *FlatSQLStore) UnpublishedBatchRecords(ctx context.Context, schemaName, providerID, sourceName, batchID string, max int) (unpublished []string, total int64, err error) {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" || batchID == "" {
		return nil, 0, errors.New("schema, provider, source and batch are required")
	}
	switch s.rb.(type) {
	case format4Backend, nil:
	default:
		return nil, 0, ErrBatchCoverageUnsupported
	}
	completed, err := s.completedLaneBatchFingerprints(ctx, schemaName, providerID, sourceName)
	if err != nil {
		return nil, 0, err
	}
	current := map[string]bool{}
	published := func(batch string) (bool, error) {
		if len(completed[batch]) == 0 {
			return false, nil
		}
		if known, ok := current[batch]; ok {
			return known, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		fingerprint, _, err := s.PagedDatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batch)
		if err != nil {
			return false, fmt.Errorf("fingerprint batch %q: %w", batch, err)
		}
		current[batch] = fingerprint != "" && completed[batch][fingerprint]
		return current[batch], nil
	}
	covered := func(provider, source, batch string) (bool, error) {
		if provider != providerID || source != sourceName {
			return false, nil
		}
		return published(batch)
	}
	switch b := s.rb.(type) {
	case format4Backend:
		return b.unpublishedBatchRecords(ctx, schemaName, providerID, sourceName, batchID, covered, max)
	default:
		return s.unpublishedBatchRecordsFormat1(ctx, schemaName, providerID, sourceName, batchID, published, max)
	}
}

// BatchPublishedAsHeld reports whether a completed publication series of the
// whole batch (recorded at completion, not "legacy:", with a fingerprint)
// exported exactly the records the store holds tagged to the batch now: the
// fingerprint of the batch's current set is the fingerprint of such a series.
// Cost: one read of the lane's series and, when the batch has a completed
// series, one fingerprint of the batch.
func (s *FlatSQLStore) BatchPublishedAsHeld(ctx context.Context, schemaName, providerID, sourceName, batchID string) (bool, error) {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" || batchID == "" {
		return false, errors.New("schema, provider, source and batch are required")
	}
	completed, err := s.completedLaneBatchFingerprints(ctx, schemaName, providerID, sourceName)
	if err != nil {
		return false, err
	}
	if len(completed[batchID]) == 0 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fingerprint, _, err := s.PagedDatasetPublicationSetFingerprint(schemaName, providerID, sourceName, batchID)
	if err != nil {
		return false, fmt.Errorf("fingerprint batch %q: %w", batchID, err)
	}
	return fingerprint != "" && completed[batchID][fingerprint], nil
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

// completedLaneBatchFingerprints is, per batch of the lane, the fingerprints
// of its completed publication series of the whole batch that carry
// records: recorded at a publication's completion (not adopted as
// "legacy:"), with a fingerprint.
func (s *FlatSQLStore) completedLaneBatchFingerprints(ctx context.Context, schemaName, providerID, sourceName string) (map[string]map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT batch_id, fingerprint FROM sdn_dataset_publication_series
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND record_count > 0
		  AND batch_id != '' AND series_id NOT LIKE 'legacy:%' AND fingerprint != ''`,
		schemaName, providerID, sourceName)
	if err != nil {
		return nil, fmt.Errorf("read the lane's published batches: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var batch, fingerprint string
		if err := rows.Scan(&batch, &fingerprint); err != nil {
			return nil, err
		}
		if out[batch] == nil {
			out[batch] = map[string]bool{}
		}
		out[batch][fingerprint] = true
	}
	return out, rows.Err()
}

func (b format4Backend) unpublishedBatchRecords(ctx context.Context, schemaName, providerID, sourceName, batchID string, covered func(provider, source, batch string) (bool, error), max int) ([]string, int64, error) {
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
				done, err := anyTagCovered(tags[c], covered)
				if err != nil {
					return nil, 0, err
				}
				if !done {
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

func anyTagCovered(rows []format4.TagRow, covered func(provider, source, batch string) (bool, error)) (bool, error) {
	for _, r := range rows {
		done, err := covered(r.Provider, r.Source, r.Batch)
		if err != nil || done {
			return done, err
		}
	}
	return false, nil
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
func (s *FlatSQLStore) unpublishedBatchRecordsFormat1(ctx context.Context, schemaName, providerID, sourceName, batchID string, published func(batch string) (bool, error), max int) ([]string, int64, error) {
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
				ok, err := published(batch)
				if err != nil {
					return nil, 0, err
				}
				if ok {
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
