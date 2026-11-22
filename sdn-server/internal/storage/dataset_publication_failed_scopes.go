package storage

// dataset_publication_failed_scopes.go — the node's own dataset publications
// that failed and whose scope is still unpublished, kept across restarts.
//
// The publication_failed alert (internal/api dataset_publication_alert.go) is
// raised from these rows: a row is written when a publication fails, before
// the alert is raised, and removed after the alert's scope is found
// published. At start the node raises the alert again for every row, so a
// restart never drops an outstanding failure.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DatasetPublicationFailedScope is one failed scope of a lane: a batch (or ""
// for the whole lane) and the operation that failed on it.
type DatasetPublicationFailedScope struct {
	// Lane is the alert's subject (`<schema> <provider>/<source>`, or
	// `<schema> datastore <key>`).
	Lane         string
	BatchID      string
	Operation    string
	SchemaName   string
	ProviderID   string
	SourceName   string
	DatastoreKey string
	FailedAt     time.Time
	// Reason is the failure's text as the alert stated it.
	Reason string
}

func (s *FlatSQLStore) initDatasetPublicationFailedScopesTable() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_dataset_publication_failed_scopes (
			lane TEXT NOT NULL,
			batch_id TEXT NOT NULL DEFAULT '',
			operation TEXT NOT NULL,
			schema_name TEXT NOT NULL DEFAULT '',
			provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '',
			datastore_key TEXT NOT NULL DEFAULT '',
			-- Unix nanoseconds.
			failed_at INTEGER NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (lane, batch_id, operation)
		)
	`); err != nil {
		return fmt.Errorf("failed to create dataset publication failed scopes table: %w", err)
	}
	return nil
}

// RecordDatasetPublicationFailedScope writes a failed scope, replacing the
// row of the same lane, batch and operation.
func (s *FlatSQLStore) RecordDatasetPublicationFailedScope(scope DatasetPublicationFailedScope) error {
	if err := s.requireWritable("record dataset publication failed scope"); err != nil {
		return err
	}
	if strings.TrimSpace(scope.Lane) == "" || strings.TrimSpace(scope.Operation) == "" {
		return errors.New("lane and operation are required")
	}
	if scope.FailedAt.IsZero() {
		scope.FailedAt = time.Now()
	}
	defer s.lockWrite("RecordDatasetPublicationFailedScope")()
	if _, err := s.db.Exec(`
		INSERT INTO sdn_dataset_publication_failed_scopes
			(lane, batch_id, operation, schema_name, provider_id, source_name, datastore_key, failed_at, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(lane, batch_id, operation) DO UPDATE SET
			failed_at = excluded.failed_at, reason = excluded.reason`,
		scope.Lane, scope.BatchID, scope.Operation, scope.SchemaName, scope.ProviderID, scope.SourceName,
		scope.DatastoreKey, scope.FailedAt.UnixNano(), scope.Reason); err != nil {
		return fmt.Errorf("record failed publication scope %s batch %q: %w", scope.Lane, scope.BatchID, err)
	}
	return nil
}

// DeleteDatasetPublicationFailedScope removes a failed scope.
func (s *FlatSQLStore) DeleteDatasetPublicationFailedScope(lane, batchID, operation string) error {
	if err := s.requireWritable("delete dataset publication failed scope"); err != nil {
		return err
	}
	defer s.lockWrite("DeleteDatasetPublicationFailedScope")()
	if _, err := s.db.Exec(`
		DELETE FROM sdn_dataset_publication_failed_scopes
		WHERE lane = ? AND batch_id = ? AND operation = ?`, lane, batchID, operation); err != nil {
		return fmt.Errorf("delete failed publication scope %s batch %q: %w", lane, batchID, err)
	}
	return nil
}

// DatasetPublicationFailedScopes is every failed scope, oldest first.
func (s *FlatSQLStore) DatasetPublicationFailedScopes() ([]DatasetPublicationFailedScope, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT lane, batch_id, operation, schema_name, provider_id, source_name, datastore_key, failed_at, reason
		FROM sdn_dataset_publication_failed_scopes ORDER BY failed_at, lane, batch_id, operation`)
	if err != nil {
		return nil, fmt.Errorf("list failed publication scopes: %w", err)
	}
	defer rows.Close()
	var out []DatasetPublicationFailedScope
	for rows.Next() {
		var scope DatasetPublicationFailedScope
		var at int64
		if err := rows.Scan(&scope.Lane, &scope.BatchID, &scope.Operation, &scope.SchemaName, &scope.ProviderID,
			&scope.SourceName, &scope.DatastoreKey, &at, &scope.Reason); err != nil {
			return nil, err
		}
		scope.FailedAt = time.Unix(0, at).UTC()
		out = append(out, scope)
	}
	return out, rows.Err()
}
