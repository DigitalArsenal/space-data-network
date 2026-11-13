package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DatasetPublicationCompletion describes one finished producer revision of the
// requested publication scope, not the size of the underlying source dataset.
// An empty State means no authoritative completion record is known.
type DatasetPublicationCompletion struct {
	State        string
	Revision     string
	TotalRecords int64
}

// DatasetPublicationCompletions binds each row to the newest completed producer
// revision naming its exact assets. Completion does not prove holder availability.
// Existing series/member tables suffice; adopted legacy series remain unknown.
// Each lookup returns at most one row, never materializing a lane's history.
func (s *FlatSQLStore) DatasetPublicationCompletions(rows []DatasetShardPublication) ([]DatasetPublicationCompletion, error) {
	if len(rows) > 1000 {
		return nil, errors.New("publication completion page is too large")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DatasetPublicationCompletion, len(rows))
	for i, row := range rows {
		if row.QueryProfile != DatasetPublicationQueryProfile {
			continue
		}
		err := s.db.QueryRowContext(ctx, `
			SELECT series.series_id, series.record_count
			FROM sdn_dataset_publication_series AS series
			WHERE series.schema_name = ? AND series.provider_id = ? AND series.source_name = ?
			  AND series.batch_id = ? AND series.series_id != '' AND series.series_id NOT LIKE 'legacy:%'
			  AND series.record_count > 0 AND series.record_count >= ?
			  AND EXISTS (SELECT 1 FROM sdn_dataset_publication_series_cids AS member
			    WHERE member.schema_name = series.schema_name AND member.provider_id = series.provider_id
			      AND member.source_name = series.source_name AND member.series_id = series.series_id
			      AND member.cid = ? AND member.role = 'shard')
			  AND EXISTS (SELECT 1 FROM sdn_dataset_publication_series_cids AS member
			    WHERE member.schema_name = series.schema_name AND member.provider_id = series.provider_id
			      AND member.source_name = series.source_name AND member.series_id = series.series_id
			      AND member.cid = ? AND member.role = 'index')
			  AND (? = '' OR EXISTS (SELECT 1 FROM sdn_dataset_publication_series_cids AS member
			    WHERE member.schema_name = series.schema_name AND member.provider_id = series.provider_id
			      AND member.source_name = series.source_name AND member.series_id = series.series_id
			      AND member.cid = ? AND member.role = 'manifest'))
			ORDER BY series.published_at DESC, series.rowid DESC LIMIT 1`,
			row.SchemaName, row.ProviderID, row.SourceName, row.BatchID, int64(row.Offset)+int64(row.RecordCount),
			row.ShardCID, row.IndexCID, row.ManifestCID, row.ManifestCID,
		).Scan(&out[i].Revision, &out[i].TotalRecords)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read publication completion: %w", err)
		}
		out[i].State = "complete"
	}
	return out, nil
}
