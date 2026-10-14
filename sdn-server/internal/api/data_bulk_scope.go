package api

import (
	"sort"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// THE UNFILTERED BULK LIST IS SCOPED (multi-body round 2, coordinator D2,
// 2026-10-13). /api/v1/data/omm/bulk with no provider_id, source or batch
// used to return every $OMM lane the node holds; once a node holds the
// beyond-Earth lanes it would hand 895,910 asteroid rows to a caller that
// asked for "the catalog" (the dashboard data explorer, scripts). Lanes in
// config lanes.named_only (default: config.DefaultNamedOnlyLanes) are left
// out of an unfiltered list; a request that names the provider, source or
// batch reads them as before. When the node holds no named-only lane of the
// schema the query runs exactly as it always did.

// SetNamedOnlyLanes sets the lanes left out of the unfiltered bulk lists.
func (h *DataQueryHandler) SetNamedOnlyLanes(lanes []config.LaneSelector) {
	h.namedOnly = append([]config.LaneSelector(nil), lanes...)
}

type bulkLane struct{ providerID, sourceName string }

// unfilteredBulkLanes lists the schema's lanes an unfiltered bulk list
// reads, and whether any lane was left out.
func (h *DataQueryHandler) unfilteredBulkLanes(schemaName string) ([]bulkLane, bool, error) {
	if len(h.namedOnly) == 0 {
		return nil, false, nil
	}
	summary, err := h.store.DataSummary()
	if err != nil || summary == nil {
		return nil, false, err
	}
	seen := map[bulkLane]bool{}
	var visible []bulkLane
	hidden := false
	for _, src := range summary.Sources {
		if src.SchemaName != schemaName || src.Count <= 0 {
			continue
		}
		lane := bulkLane{providerID: src.ProviderID, sourceName: src.SourceName}
		if seen[lane] {
			continue
		}
		seen[lane] = true
		if config.AnyLaneMatches(h.namedOnly, "", schemaName, src.ProviderID, src.SourceName) {
			hidden = true
			continue
		}
		visible = append(visible, lane)
	}
	return visible, hidden, nil
}

// queryEpochLanes answers an epoch query over the given lanes only: one
// query per lane, then the best match per entity (the smallest delta from the
// requested epoch: the latest at or before it for as_of, the earliest at or
// after it for forward, the nearest for nearest), in entity order, limited.
// Day and window profiles keep every match.
func (h *DataQueryHandler) queryEpochLanes(query storage.EpochRecordQuery, lanes []bulkLane) ([]storage.EpochRecordMatch, error) {
	point := query.Profile != storage.EpochProfileDay && query.Profile != storage.EpochProfileWindow
	best := map[string]storage.EpochRecordMatch{}
	var all []storage.EpochRecordMatch
	for _, lane := range lanes {
		q := query
		q.ProviderID, q.SourceName = lane.providerID, lane.sourceName
		matches, err := h.store.QueryEpochRecords(q)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if !point {
				all = append(all, m)
				continue
			}
			if prev, ok := best[m.EntityKey]; !ok || m.DeltaSeconds < prev.DeltaSeconds {
				best[m.EntityKey] = m
			}
		}
	}
	if point {
		all = make([]storage.EpochRecordMatch, 0, len(best))
		for _, m := range best {
			all = append(all, m)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].EntityKey < all[j].EntityKey })
	if query.Limit > 0 && len(all) > query.Limit {
		all = all[:query.Limit]
	}
	return all, nil
}
