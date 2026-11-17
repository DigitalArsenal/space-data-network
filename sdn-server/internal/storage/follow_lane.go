package storage

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// ErrFollowLaneUnsupported is a store format without per-lane tag rows, on
// which a followed lane's removals are not reconciled.
var ErrFollowLaneUnsupported = errors.New("reconciling a followed lane needs a format 5 store")

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

// RemoveFollowLaneRecords removes records of one batch of a lane that this
// node stored from one producer peer: a record goes only when every tag it
// holds is that (provider, source, batch, producer); a record also held
// under any other tag stays. It returns the records removed.
func (s *FlatSQLStore) RemoveFollowLaneRecords(schemaName, providerID, sourceName, batchID, producer string, cids []string) (int64, error) {
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
	batchID, producer = strings.TrimSpace(batchID), strings.TrimSpace(producer)
	var removed int64
	for start := 0; start < len(cids); start += 1024 {
		part := cids[start:min(start+1024, len(cids))]
		tags, err := b.f4TagRows(schemaName, part)
		if err != nil {
			return removed, err
		}
		var drop []string
		for _, c := range part {
			rows := tags[c]
			only := len(rows) > 0
			for _, r := range rows {
				if r.Provider != providerID || r.Source != sourceName || r.Batch != batchID || r.ProducerPeer != producer {
					only = false
					break
				}
			}
			if only {
				drop = append(drop, c)
			}
		}
		if len(drop) == 0 {
			continue
		}
		n, err := b.d.api().Delete(b.d.ctx, typ, drop)
		if errors.Is(err, format4.ErrNoType) {
			return removed, nil
		}
		if err != nil {
			return removed, fmt.Errorf("remove followed records of %s: %w", schemaName, err)
		}
		removed += n
	}
	return removed, nil
}
