package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A retracted lane is a lane of this node's own publications its operator
// withdrew (POST /api/v1/admin/dataset-updates/retract). The mark does two
// things: the node refuses to publish the lane again until the mark is
// cleared, and a peer following the lane is told on its next quiet page that
// the lane is retracted, so it drops what it holds of it.

const datasetPublicationRetractedTable = `
		CREATE TABLE IF NOT EXISTS sdn_dataset_publication_retracted (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			-- Unix NANOSECONDS, as the series table.
			retracted_at INTEGER NOT NULL,
			PRIMARY KEY (schema_name, provider_id, source_name)
		)`

// MarkDatasetPublicationLaneRetracted records the lane as retracted at at.
// Marking a lane that is already marked keeps the first time.
func (s *FlatSQLStore) MarkDatasetPublicationLaneRetracted(lane DatasetPublicationLane, at time.Time) error {
	lane = lane.normalized()
	if lane.SchemaName == "" || lane.ProviderID == "" || lane.SourceName == "" {
		return errors.New("schema, provider and source are required to retract a lane")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	defer s.lockWrite("MarkDatasetPublicationLaneRetracted")()
	if _, err := s.db.Exec(`
		INSERT INTO sdn_dataset_publication_retracted (schema_name, provider_id, source_name, retracted_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(schema_name, provider_id, source_name) DO NOTHING`,
		lane.SchemaName, lane.ProviderID, lane.SourceName, at.UnixNano()); err != nil {
		return fmt.Errorf("mark %s %s/%s retracted: %w", lane.SchemaName, lane.ProviderID, lane.SourceName, err)
	}
	return nil
}

// DatasetPublicationLaneRetractedAt is when the lane was retracted; ok is
// false for a lane that is not retracted.
func (s *FlatSQLStore) DatasetPublicationLaneRetractedAt(lane DatasetPublicationLane) (at time.Time, ok bool, err error) {
	lane = lane.normalized()
	var nanos int64
	err = s.db.QueryRow(`
		SELECT retracted_at FROM sdn_dataset_publication_retracted
		WHERE schema_name = ? AND provider_id = ? AND source_name = ?`,
		lane.SchemaName, lane.ProviderID, lane.SourceName).Scan(&nanos)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read %s %s/%s retraction: %w", lane.SchemaName, lane.ProviderID, lane.SourceName, err)
	}
	return time.Unix(0, nanos).UTC(), true, nil
}
