package storage

// publication_batch_coverage.go — how many of a batch's records no completed
// publication of its lane exported.
//
// A batch whose publication failed is not published, but its records may be:
// a later pull of the same source that returns a record again tags that
// record to the later batch as well (a repeat CID takes the new batch's tag),
// and the later batch's publication carries it. A record the later pulls did
// not return (an element set its publisher has since replaced) stays tagged
// to the failed batch alone, and no publication carries it. The publication
// alert of the failed batch (internal/api dataset_publication_alert.go)
// clears only when this count finds no record of the batch unpublished.
//
// A record counts as published when it is tagged to a batch of the lane that
// has a COMPLETED publication series of its whole batch which exported it: a
// series recorded by the publication's completion (never a "legacy:" series
// adopted from rows, which a publication that failed partway can leave),
// with the fingerprint of the set it exported (a series limited to a head of
// the batch has none), equal to the fingerprint of the set a publication of
// the batch would export NOW. With the fingerprints equal the series
// exported exactly that set:
//
//   - on a lane whose publications export the store's records, the set is
//     every record tagged to the batch, so every record tagged to it is in
//     the series. A batch stored in parts gains records after a publication
//     of its first part; its set then differs from the series', and none of
//     its records counts until a publication of the whole batch as it is now
//     completes. A tag, once written, stays while the store holds its record
//     (a repeat CID adds the new batch's tag to the record's tags), so a
//     record read tagged to a batch whose set equals the series' set is in
//     the series.
//   - on a lane whose publications export the publication journal's
//     retained selection (SelectPublication), the set is the selection: the
//     batch's journal entries less discarded entries and catalog
//     exclusions. A record tagged to the batch in an entry the selection
//     leaves out was not exported, so a record counts only when its CID is
//     in the selection.
//
// Retention keeps the series of every batch the store still holds unless the
// lane declares its batches snapshots, so a series released by a snapshot
// lane's retention makes its records count as unpublished here (the count
// errs on the side of keeping an alert).
//
// Cost of one count: one read of the batch's CIDs and of their tags, a page
// of 2,048 at a time; one read of the lane's series; and, once per count,
// one fingerprint of each batch that tags one of the records and has a
// completed series (the read a publication of that batch makes to
// fingerprint it), plus, on a retained-selection lane, that batch's
// selection (at most 100,000 CIDs). Nothing is kept between counts.

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

// batchCoveragePage bounds the CIDs one step of the count reads.
const batchCoveragePage = 2048

// batchExport is what a completed series of a batch exported, as far as the
// count needs it: nothing (none matches the batch now), every record tagged
// to the batch, or the CIDs of a retained selection.
type batchExport struct {
	all  bool
	cids map[string]bool
}

func (e batchExport) has(cid string) bool { return e.all || e.cids[cid] }

// UnpublishedBatchRecords counts the records tagged to batchID in the lane
// (schema, provider, source) that no completed publication of a whole batch
// of the lane exported, and how many records the batch holds now. It reads
// the store as it is when called and keeps nothing; ctx ends it.
func (s *FlatSQLStore) UnpublishedBatchRecords(ctx context.Context, schemaName, providerID, sourceName, batchID string) (unpublished, total int64, err error) {
	schemaName, providerID = strings.TrimSpace(schemaName), strings.TrimSpace(providerID)
	sourceName, batchID = strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	if schemaName == "" || providerID == "" || sourceName == "" || batchID == "" {
		return 0, 0, errors.New("schema, provider, source and batch are required")
	}
	switch s.rb.(type) {
	case format4Backend, nil:
	default:
		return 0, 0, ErrBatchCoverageUnsupported
	}
	completed, err := s.completedLaneBatchFingerprints(ctx, schemaName, providerID, sourceName)
	if err != nil {
		return 0, 0, err
	}
	exports := map[string]batchExport{}
	exported := func(batch string) (batchExport, error) {
		if len(completed[batch]) == 0 {
			return batchExport{}, nil
		}
		if known, ok := exports[batch]; ok {
			return known, nil
		}
		export, err := s.currentBatchExport(ctx, schemaName, providerID, sourceName, batch, completed[batch])
		if err != nil {
			return batchExport{}, fmt.Errorf("fingerprint batch %q: %w", batch, err)
		}
		exports[batch] = export
		return export, nil
	}
	covered := func(cid, provider, source, batch string) (bool, error) {
		if provider != providerID || source != sourceName {
			return false, nil
		}
		export, err := exported(batch)
		return export.has(cid), err
	}
	switch b := s.rb.(type) {
	case format4Backend:
		return b.unpublishedBatchRecords(ctx, schemaName, providerID, sourceName, batchID, covered)
	default:
		return s.unpublishedBatchRecordsFormat1(ctx, schemaName, providerID, sourceName, batchID, covered)
	}
}

// currentBatchExport is what a completed series of batch with one of the
// fingerprints exported, when a publication of the batch now would export
// the same set; the zero batchExport when none would.
func (s *FlatSQLStore) currentBatchExport(ctx context.Context, schemaName, providerID, sourceName, batch string, fingerprints map[string]bool) (batchExport, error) {
	if err := ctx.Err(); err != nil {
		return batchExport{}, err
	}
	selection, err := s.SelectPublication(schemaName, providerID, sourceName, batch)
	if err != nil {
		return batchExport{}, err
	}
	if selection != nil {
		defer selection.Close()
		if fingerprint, _ := selection.Fingerprint(); fingerprint == "" || !fingerprints[fingerprint] {
			return batchExport{}, nil
		}
		cids := make(map[string]bool, 2*len(selection.refs))
		for _, r := range selection.refs {
			cids[r.CID()] = true
			cids[r.FramedCID()] = true
		}
		return batchExport{cids: cids}, nil
	}
	fingerprint, _, err := s.PagedDatasetPublicationSetFingerprintContext(ctx, schemaName, providerID, sourceName, batch)
	if err != nil {
		return batchExport{}, err
	}
	return batchExport{all: fingerprint != "" && fingerprints[fingerprint]}, nil
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

func (b format4Backend) unpublishedBatchRecords(ctx context.Context, schemaName, providerID, sourceName, batchID string, covered func(cid, provider, source, batch string) (bool, error)) (int64, int64, error) {
	var unpublished, total int64
	after := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		cids, last, more, err := b.s.LaneBatchCIDPage(schemaName, providerID, sourceName, batchID, after, batchCoveragePage)
		if err != nil {
			return 0, 0, err
		}
		if len(cids) > 0 {
			tags, err := b.f4TagRows(schemaName, cids)
			if err != nil {
				return 0, 0, err
			}
			for _, c := range cids {
				total++
				done, err := anyTagCovered(c, tags[c], covered)
				if err != nil {
					return 0, 0, err
				}
				if !done {
					unpublished++
				}
			}
		}
		if !more || last <= after {
			return unpublished, total, nil
		}
		after = last
	}
}

func anyTagCovered(cid string, rows []format4.TagRow, covered func(cid, provider, source, batch string) (bool, error)) (bool, error) {
	for _, r := range rows {
		done, err := covered(cid, r.Provider, r.Source, r.Batch)
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
func (s *FlatSQLStore) unpublishedBatchRecordsFormat1(ctx context.Context, schemaName, providerID, sourceName, batchID string, covered func(cid, provider, source, batch string) (bool, error)) (int64, int64, error) {
	var unpublished, total int64
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		page, err := s.format1BatchCIDPage(ctx, schemaName, providerID, sourceName, batchID, after)
		if err != nil {
			return 0, 0, fmt.Errorf("read batch %q records: %w", batchID, err)
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
			return 0, 0, fmt.Errorf("read the tags of batch %q records: %w", batchID, err)
		}
		for _, c := range page {
			total++
			done := false
			for _, batch := range batches[c] {
				ok, err := covered(c, providerID, sourceName, batch)
				if err != nil {
					return 0, 0, err
				}
				if ok {
					done = true
					break
				}
			}
			if !done {
				unpublished++
			}
		}
		after = page[len(page)-1]
		if len(page) < batchCoveragePage {
			return unpublished, total, nil
		}
	}
}
