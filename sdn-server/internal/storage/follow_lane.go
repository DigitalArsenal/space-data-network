package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// A followed lane (internal/node/follow.go) on both of its ends. The producer
// states the lane's current batch and serves its list of CIDs and its
// records by CID; the follower tags what it holds to the batch and removes,
// per producer, the records the producer's statement no longer covers.

// ErrFollowLaneUnsupported is a store format without per-lane tag rows, on
// which a followed lane's batches are not stated or kept.
var ErrFollowLaneUnsupported = errors.New("following a lane's batches needs a format 5 store")

// LaneCurrentBatch is the producer's statement of a lane: its current batch,
// the newest batch it holds (the lane counters' instance first stored
// latest; the higher sequence, then the name, breaks a tie), and the records
// of that batch. "" when the lane holds none. The engine's read ends when
// ctx ends.
func (s *FlatSQLStore) LaneCurrentBatch(ctx context.Context, schemaName, providerID, sourceName string) (string, int64, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return "", 0, ErrFollowLaneUnsupported
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return "", 0, err
	}
	lanes, err := b.f4LanesContext(ctx, typ)
	if err != nil {
		return "", 0, err
	}
	type batch struct{ first, maxSeq, records int64 }
	batches := map[string]*batch{}
	want := f4Lane(providerID, sourceName, "", "", "")
	for _, l := range lanes {
		if l.Records <= 0 || l.Batch == "" || !f4LaneMeets(l, want) {
			continue
		}
		bt := batches[l.Batch]
		if bt == nil {
			bt = &batch{first: l.First}
			batches[l.Batch] = bt
		}
		if l.First < bt.first {
			bt.first = l.First
		}
		bt.maxSeq = max(bt.maxSeq, l.MaxSeq)
		bt.records += l.Records
	}
	current := ""
	var cur *batch
	for id, bt := range batches {
		if cur == nil || bt.first > cur.first || (bt.first == cur.first && (bt.maxSeq > cur.maxSeq || (bt.maxSeq == cur.maxSeq && id > current))) {
			current, cur = id, bt
		}
	}
	if cur == nil {
		return "", 0, nil
	}
	return current, cur.records, nil
}

// LaneTotals is a lane's records and bytes (batchID "": every batch; else
// that batch), from the lane counters alone: the sum over the type's live
// tag instances meeting the lane. It reads no record, so it costs the lane's
// instances, not its records. A record tagged to two of the lane's batches
// is counted in each: the lane's sum is its records exactly when no record
// is in two of its batches, and more than them otherwise (a HEAD that counts
// each record once reads every record of the lane). The engine's read ends
// when ctx ends.
func (s *FlatSQLStore) LaneTotals(ctx context.Context, schemaName, providerID, sourceName, batchID string) (records, bytes int64, err error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return 0, 0, ErrFollowLaneUnsupported
	}
	if err := b.closed(); err != nil {
		return 0, 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return 0, 0, err
	}
	lanes, err := b.f4LanesContext(ctx, typ)
	if err != nil {
		return 0, 0, err
	}
	want := f4Lane(providerID, sourceName, batchID, "", "")
	if want.Provider == "" || want.Source == "" {
		return 0, 0, errors.New("a lane's totals name its provider and source")
	}
	for _, l := range lanes {
		if l.Records > 0 && f4LaneMeets(l, want) {
			records += l.Records
			bytes += l.Bytes
		}
	}
	return records, bytes, nil
}

// followReadFault is nil in every release build. A build with the tag
// sdn_e2e_follow_faults sets it (follow_faults_e2e.go): a test lane's batch
// list or fetch by CID is a slow store read, for the publication real-daemon
// tests. read is "list" or "fetch"; it runs under the read's context.
var followReadFault func(ctx context.Context, read, sourceName string) error

// LaneBatchCIDPage is one page of a batch's list: the CIDs of the lane's
// records tagged to batchID, in sequence order after afterSeq, at most limit.
// It returns the last sequence read and whether more may follow. The
// engine's read ends when ctx ends (a follower that left).
func (s *FlatSQLStore) LaneBatchCIDPage(ctx context.Context, schemaName, providerID, sourceName, batchID string, afterSeq int64, limit int) ([]string, int64, bool, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return nil, 0, false, ErrFollowLaneUnsupported
	}
	if err := b.closed(); err != nil {
		return nil, 0, false, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, 0, false, err
	}
	if strings.TrimSpace(batchID) == "" {
		return nil, 0, false, errors.New("a batch's list names the batch")
	}
	read, done := b.readContext(ctx)
	defer done()
	if followReadFault != nil {
		if err := followReadFault(read, "list", sourceName); err != nil {
			return nil, 0, false, err
		}
	}
	// A list is a follower's read: the interactive class, under the
	// producer's limit on followers' reads (protocol), so the one bulk
	// lane stays the node's own (its publications' fingerprint scans).
	q := format4.Query{Type: typ, Lane: f4Lane(providerID, sourceName, batchID, "", ""), Order: format4.OrderSeqAsc,
		SeqAfter: afterSeq, Limit: int64(limit)}
	recs, err := b.d.api().Scan(read, q)
	if errors.Is(err, format4.ErrNoType) {
		return nil, afterSeq, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	cids := make([]string, 0, len(recs))
	last := afterSeq
	for _, r := range recs {
		cids = append(cids, r.CID)
		last = r.Seq
	}
	return dedupeStrings(cids), last, int64(len(recs)) >= q.Limit, nil
}

// LaneRecordsByCID is the records of cids the lane holds tagged to batchID,
// each carrying that tag; a CID it does not hold so is left out. The
// engine's reads end when ctx ends (a follower that left).
func (s *FlatSQLStore) LaneRecordsByCID(ctx context.Context, schemaName, providerID, sourceName, batchID string, cids []string) ([]*Record, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return nil, ErrFollowLaneUnsupported
	}
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	providerID, sourceName, batchID = strings.TrimSpace(providerID), strings.TrimSpace(sourceName), strings.TrimSpace(batchID)
	read, done := b.readContext(ctx)
	defer done()
	if followReadFault != nil {
		if err := followReadFault(read, "fetch", sourceName); err != nil {
			return nil, err
		}
	}
	tags, err := b.f4TagRowsContext(read, schemaName, cids)
	if err != nil {
		return nil, err
	}
	tagged := map[string]format4.Tag{}
	var held []string
	for _, c := range dedupeStrings(cids) {
		for _, r := range tags[c] {
			if r.Provider == providerID && r.Source == sourceName && r.Batch == batchID {
				tagged[c] = r.Tag
				held = append(held, c)
				break
			}
		}
	}
	if len(held) == 0 {
		return nil, nil
	}
	recs, err := b.d.api().Get(read, typ, held, false, true)
	if errors.Is(err, format4.ErrNoType) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := b.f4Records(schemaName, recs, true)
	if err != nil {
		return nil, err
	}
	for _, r := range out {
		r.SourceTags = f4SourceTags(tagged[r.CID])
	}
	return out, nil
}

// HeldRecordBytes is the bytes of the records of cids this node holds, by
// CID (opened, as a scan returns them); a CID it does not hold is left out.
func (s *FlatSQLStore) HeldRecordBytes(schemaName string, cids []string) (map[string][]byte, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return nil, ErrFollowLaneUnsupported
	}
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	var stored []string
	for _, c := range dedupeStrings(cids) {
		if f4CIDStored(c) {
			stored = append(stored, c)
		}
	}
	out := make(map[string][]byte, len(stored))
	if len(stored) == 0 {
		return out, nil
	}
	recs, err := b.d.api().Get(b.d.ctx, typ, stored, false, true)
	if errors.Is(err, format4.ErrNoType) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		data, err := s.openStoredRecordBytes(schemaName, r.Data)
		if err != nil {
			return nil, err
		}
		out[r.CID] = data
	}
	return out, nil
}

// FollowLaneBatchCounts is the records of each batch of a lane that this
// node stored from one producer peer (the lane counters of the instances
// whose producer tag is producer).
func (s *FlatSQLStore) FollowLaneBatchCounts(schemaName, providerID, sourceName, producer string) (map[string]int64, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return nil, ErrFollowLaneUnsupported
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	lanes, err := b.f4Lanes(typ)
	if err != nil {
		return nil, err
	}
	want := f4Lane(providerID, sourceName, "", producer, "")
	out := map[string]int64{}
	for _, l := range lanes {
		if l.Records > 0 && f4LaneMeets(l, want) {
			out[l.Batch] += l.Records
		}
	}
	return out, nil
}

// FollowLaneBatchCIDs is the CIDs of one batch of a lane stored from one
// producer peer, in sequence order.
func (s *FlatSQLStore) FollowLaneBatchCIDs(schemaName, providerID, sourceName, batchID, producer string) ([]string, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return nil, ErrFollowLaneUnsupported
	}
	if err := b.closed(); err != nil {
		return nil, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return nil, err
	}
	q := format4.Query{Type: typ, Lane: f4Lane(providerID, sourceName, batchID, producer, ""), Order: format4.OrderSeqAsc, Bulk: true,
		Limit: publicationSetFingerprintPage}
	var cids []string
	for {
		recs, err := b.d.api().Scan(b.d.ctx, q)
		if errors.Is(err, format4.ErrNoType) {
			return cids, nil
		}
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			cids = append(cids, r.CID)
		}
		if int64(len(recs)) < q.Limit {
			return dedupeStrings(cids), nil
		}
		q.SeqAfter = recs[len(recs)-1].Seq
	}
}

// RemoveFollowLaneRecords removes records of a lane this node stored from
// one producer peer: a record goes only when every tag it holds is of that
// lane and producer and of a batch drop accepts; a record also held under
// any other tag stays. The tag check and the delete of each part run
// with every engine write held off (tagMu), so no record gains a tag between
// them. It returns the records removed.
func (s *FlatSQLStore) RemoveFollowLaneRecords(schemaName, providerID, sourceName, producer string, drop func(batch string) bool, cids []string) (int64, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return 0, ErrFollowLaneUnsupported
	}
	if err := s.requireWritable("remove followed records"); err != nil {
		return 0, err
	}
	if err := b.closed(); err != nil {
		return 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return 0, err
	}
	providerID, sourceName = strings.TrimSpace(providerID), strings.TrimSpace(sourceName)
	producer = strings.TrimSpace(producer)
	var removed int64
	for start := 0; start < len(cids); start += 1024 {
		n, err := b.removeFollowLanePart(schemaName, typ, providerID, sourceName, producer, drop, cids[start:min(start+1024, len(cids))])
		removed += n
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (b format4Backend) removeFollowLanePart(schemaName, typ, providerID, sourceName, producer string, drop func(string) bool, part []string) (int64, error) {
	b.s.tagMu.Lock()
	defer b.s.tagMu.Unlock()
	tags, err := b.f4TagRows(schemaName, part)
	if err != nil {
		return 0, err
	}
	var gone []string
	for _, c := range part {
		rows := tags[c]
		only := len(rows) > 0
		for _, r := range rows {
			if r.Provider != providerID || r.Source != sourceName || r.ProducerPeer != producer || !drop(r.Batch) {
				only = false
				break
			}
		}
		if only {
			gone = append(gone, c)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	n, err := b.d.api().Delete(b.d.ctx, typ, gone)
	if errors.Is(err, format4.ErrNoType) {
		return 0, nil
	}
	if err != nil {
		return n, fmt.Errorf("remove followed records of %s: %w", schemaName, err)
	}
	return n, nil
}

// SupersedeFollowLane keeps one batch of a lane this node follows from
// producer and removes what that producer's other batches alone hold. When
// every other batch's tags in the lane are that producer's, the engine's
// supersede does it in one transaction (it also drops those batches' tags
// from records the kept batch holds); otherwise each record of the
// producer's other batches goes only when every tag it holds is that
// producer's and none is the kept batch, so another producer's records of
// the lane stay. Engine writes are held off throughout (tagMu). It returns
// the records removed.
func (s *FlatSQLStore) SupersedeFollowLane(schemaName, providerID, sourceName, producer, keepBatch string) (int64, error) {
	b, ok := s.rb.(format4Backend)
	if !ok {
		return 0, ErrFollowLaneUnsupported
	}
	if err := s.requireWritable("supersede a followed lane"); err != nil {
		return 0, err
	}
	if err := b.closed(); err != nil {
		return 0, err
	}
	typ, err := f4Type(schemaName)
	if err != nil {
		return 0, err
	}
	providerID, sourceName = strings.TrimSpace(providerID), strings.TrimSpace(sourceName)
	producer, keepBatch = strings.TrimSpace(producer), strings.TrimSpace(keepBatch)
	if providerID == "" || sourceName == "" || producer == "" || keepBatch == "" {
		return 0, errors.New("a followed lane's supersede names the lane, the producer and the batch to keep")
	}
	s.tagMu.Lock()
	lanes, err := b.f4Lanes(typ)
	if err != nil {
		s.tagMu.Unlock()
		return 0, err
	}
	want := f4Lane(providerID, sourceName, "", "", "")
	others := map[string]bool{}
	sole := true
	for _, l := range lanes {
		if l.Records <= 0 || l.Batch == keepBatch || !f4LaneMeets(l, want) {
			continue
		}
		if l.ProducerPeer != producer {
			sole = false
		}
		others[l.Batch] = true
	}
	if len(others) == 0 {
		s.tagMu.Unlock()
		return 0, nil
	}
	if sole {
		defer s.tagMu.Unlock()
		r, err := b.d.api().Supersede(b.d.ctx, typ, providerID, sourceName, keepBatch, true)
		if errors.Is(err, format4.ErrNoType) {
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf("supersede followed lane %s %s/%s: %w", schemaName, providerID, sourceName, err)
		}
		return r.RecordsDeleted, nil
	}
	s.tagMu.Unlock()
	batches := make([]string, 0, len(others))
	for id := range others {
		batches = append(batches, id)
	}
	sort.Strings(batches)
	var removed int64
	for _, id := range batches {
		cids, err := s.FollowLaneBatchCIDs(schemaName, providerID, sourceName, id, producer)
		if err != nil {
			return removed, err
		}
		n, err := s.RemoveFollowLaneRecords(schemaName, providerID, sourceName, producer, func(b string) bool { return b != keepBatch }, cids)
		removed += n
		if err != nil {
			return removed, err
		}
	}
	return removed, nil
}
