package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DatasetPublicationReplayStateMaterialized    = "materialized"
	DatasetPublicationReplayStatePermanentError  = "permanent_error"
	datasetPublicationReplayStateTableName       = "sdn_dataset_publication_replay_state"
	datasetPublicationReplayStateUpdatedIndex    = "idx_sdn_dataset_publication_replay_state_updated"
	datasetPublicationReplayStateStateUpdatedIdx = "idx_sdn_dataset_publication_replay_state_state_updated"
)

type DatasetPublicationReplayState struct {
	PNMKey     string
	SchemaName string
	PNMCID     string
	FileID     string
	State      string
	Error      string
	UpdatedAt  time.Time
}

func (s *FlatSQLStore) initDatasetPublicationReplayStateTable() error {
	existed, err := s.tableExists(datasetPublicationReplayStateTableName)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_dataset_publication_replay_state (
			pnm_key TEXT PRIMARY KEY,
			schema_name TEXT NOT NULL DEFAULT '',
			pnm_cid TEXT NOT NULL DEFAULT '',
			file_id TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
		)
	`); err != nil {
		return fmt.Errorf("failed to create dataset publication replay state table: %w", err)
	}
	if err := s.createStartupIndex(datasetPublicationReplayStateTableName, datasetPublicationReplayStateUpdatedIndex, existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_dataset_publication_replay_state_updated
		ON sdn_dataset_publication_replay_state (updated_at DESC)
	`); err != nil {
		return err
	}
	return s.createStartupIndex(datasetPublicationReplayStateTableName, datasetPublicationReplayStateStateUpdatedIdx, existed, `
		CREATE INDEX IF NOT EXISTS idx_sdn_dataset_publication_replay_state_state_updated
		ON sdn_dataset_publication_replay_state (state, updated_at DESC)
	`)
}

func (s *FlatSQLStore) UpsertDatasetPublicationReplayState(state DatasetPublicationReplayState) error {
	if err := s.requireWritable("upsert dataset publication replay state"); err != nil {
		return err
	}
	state = normalizeDatasetPublicationReplayState(state)
	if state.PNMKey == "" {
		return errors.New("dataset publication replay state key is required")
	}
	if state.State == "" {
		return errors.New("dataset publication replay state is required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.applyDatasetPublicationReplayStateUpsert(state); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{
		Kind:               auxiliaryEventDatasetReplayStateUpsert,
		DatasetReplayState: &state,
	}); err != nil {
		return fmt.Errorf("append dataset replay state metadata: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) applyDatasetPublicationReplayStateUpsert(state DatasetPublicationReplayState) error {
	_, err := s.auxWrite().Exec(`
		INSERT INTO sdn_dataset_publication_replay_state (
			pnm_key, schema_name, pnm_cid, file_id, state, error, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pnm_key) DO UPDATE SET
			schema_name = excluded.schema_name,
			pnm_cid = excluded.pnm_cid,
			file_id = excluded.file_id,
			state = excluded.state,
			error = excluded.error,
			updated_at = excluded.updated_at
	`, state.PNMKey, state.SchemaName, state.PNMCID, state.FileID, state.State, state.Error, state.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert dataset publication replay state: %w", err)
	}
	return nil
}

func (s *FlatSQLStore) DatasetPublicationReplayState(pnmKey string) (DatasetPublicationReplayState, bool, error) {
	pnmKey = strings.TrimSpace(pnmKey)
	if pnmKey == "" {
		return DatasetPublicationReplayState{}, false, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	row := s.db.QueryRow(`
		SELECT pnm_key, schema_name, pnm_cid, file_id, state, error, updated_at
		FROM sdn_dataset_publication_replay_state
		WHERE pnm_key = ?
	`, pnmKey)
	state, err := scanDatasetPublicationReplayState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DatasetPublicationReplayState{}, false, nil
	}
	if err != nil {
		return DatasetPublicationReplayState{}, false, err
	}
	return state, true, nil
}

type datasetPublicationReplayStateScanner interface {
	Scan(dest ...any) error
}

func scanDatasetPublicationReplayState(scanner datasetPublicationReplayStateScanner) (DatasetPublicationReplayState, error) {
	var state DatasetPublicationReplayState
	var updatedAt int64
	if err := scanner.Scan(&state.PNMKey, &state.SchemaName, &state.PNMCID, &state.FileID, &state.State, &state.Error, &updatedAt); err != nil {
		return DatasetPublicationReplayState{}, err
	}
	state.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return state, nil
}

func normalizeDatasetPublicationReplayState(state DatasetPublicationReplayState) DatasetPublicationReplayState {
	state.PNMKey = strings.TrimSpace(state.PNMKey)
	state.SchemaName = strings.TrimSpace(state.SchemaName)
	state.PNMCID = strings.TrimSpace(state.PNMCID)
	state.FileID = strings.TrimSpace(state.FileID)
	state.State = strings.TrimSpace(state.State)
	state.Error = strings.TrimSpace(state.Error)
	return state
}
