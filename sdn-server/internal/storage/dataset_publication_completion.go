package storage

import "strings"

// DatasetPublicationCompletion is derived from the existing durable series
// ledger. An empty State means no authoritative completion record is known;
// adopted legacy series are never promoted to completed publications.
type DatasetPublicationCompletion struct {
	State        string
	Revision     string
	TotalRecords int64
}

// DatasetPublicationCompletions binds each row to the newest completed producer
// revision that names its exact assets. Completion describes the producer's
// publication, not whether this holder still has every asset of that revision.
// No storage schema or historical record is changed.
func (s *FlatSQLStore) DatasetPublicationCompletions(rows []DatasetShardPublication) ([]DatasetPublicationCompletion, error) {
	type completed struct {
		series  DatasetPublicationSeries
		members map[DatasetPublicationSeriesMember]bool
	}
	byLane := make(map[DatasetPublicationLane][]completed)
	out := make([]DatasetPublicationCompletion, len(rows))
	for i, row := range rows {
		if row.QueryProfile != DatasetPublicationQueryProfile {
			continue
		}
		lane := DatasetPublicationLane{SchemaName: row.SchemaName, ProviderID: row.ProviderID, SourceName: row.SourceName}
		all, ok := byLane[lane]
		if !ok {
			series, err := s.ListDatasetPublicationSeries(lane)
			if err != nil {
				return nil, err
			}
			for _, item := range series {
				if item.SeriesID == "" || strings.HasPrefix(item.SeriesID, "legacy:") || item.RecordCount <= 0 {
					continue
				}
				members := make(map[DatasetPublicationSeriesMember]bool, len(item.Members))
				for _, member := range item.Members {
					members[member] = true
				}
				all = append(all, completed{item, members})
			}
			byLane[lane] = all
		}
		for _, item := range all {
			if item.series.BatchID != row.BatchID || item.series.RecordCount < row.RecordCount {
				continue
			}
			if !item.members[DatasetPublicationSeriesMember{CID: row.ShardCID, Role: PinLedgerRoleShard}] || !item.members[DatasetPublicationSeriesMember{CID: row.IndexCID, Role: PinLedgerRoleIndex}] {
				continue
			}
			if row.ManifestCID != "" && !item.members[DatasetPublicationSeriesMember{CID: row.ManifestCID, Role: PinLedgerRoleManifest}] {
				continue
			}
			out[i] = DatasetPublicationCompletion{State: "complete", Revision: item.series.SeriesID, TotalRecords: int64(item.series.RecordCount)}
			break
		}
	}
	return out, nil
}
