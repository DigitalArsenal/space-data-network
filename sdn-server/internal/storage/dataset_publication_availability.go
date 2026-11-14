package storage

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// DatasetPublicationUnavailableRetention gives an interrupted transfer or local
// repair a day to finish. PublishedAt and updated_at cannot establish absence:
// they describe publication writes, not the last time this node held the files.
const DatasetPublicationUnavailableRetention = 24 * time.Hour

// DatasetPublicationRetirementKey preserves catch-up deduplication separately
// from the serving index. A changed window or asset pair is a new publication.
func DatasetPublicationRetirementKey(pub DatasetShardPublication) string {
	identity := fmt.Sprintf("%q/%q/%q/%q/%q/%d/%d/%q/%q", pub.SchemaName, pub.ProviderID,
		pub.SourceName, pub.BatchID, pub.QueryProfile, pub.Offset, pub.Limit, pub.ShardCID, pub.IndexCID)
	return fmt.Sprintf("retired-publication:%x", sha256.Sum256([]byte(identity)))
}

// DatasetPublicationAvailability checks the exact local assets read-shard and
// read-index serve. It performs no network fetch and exposes no private paths.
func (s *FlatSQLStore) DatasetPublicationAvailability(pub DatasetShardPublication) (string, string) {
	shard, err := s.DatasetPublicationShardPath(pub)
	if err != nil {
		return "unavailable", "invalid_shard_metadata"
	}
	index, err := s.DatasetPublicationIndexPath(pub)
	if err != nil {
		return "unavailable", "invalid_index_metadata"
	}
	for _, asset := range []struct {
		role, path string
		size       int64
	}{{"shard", shard, pub.ByteCount}, {"index", index, 0}} {
		info, err := os.Stat(asset.path)
		if os.IsNotExist(err) {
			return "unavailable", asset.role + "_missing"
		}
		if err != nil {
			return "unavailable", asset.role + "_stat_failed"
		}
		if !info.Mode().IsRegular() {
			return "unavailable", asset.role + "_not_regular"
		}
		if asset.size > 0 && info.Size() != asset.size {
			return "unavailable", asset.role + "_size_mismatch"
		}
	}
	return "available", ""
}

// RetireUnavailableDatasetPublications reconciles only the listing index. It
// neither removes files/records nor releases pins. Observations and deletions
// use existing journal events, so they survive a checkpoint or journal replay.
func (s *FlatSQLStore) RetireUnavailableDatasetPublications(now time.Time) (int, error) {
	if err := s.requireWritable("retire unavailable dataset publications"); err != nil {
		return 0, err
	}
	if now.Unix() <= 0 {
		return 0, fmt.Errorf("publication observation time must be positive")
	}
	publications, err := s.ListDatasetShardPublicationsForProfile(DatasetPublicationQueryProfile, "")
	if err != nil {
		return 0, err
	}
	retired := 0
	for _, pub := range publications {
		didRetire, err := s.reconcileDatasetPublicationAvailability(pub, now)
		if err != nil {
			return retired, err
		}
		if didRetire {
			retired++
		}
	}
	return retired, nil
}

// Bound the write lock to one publication, and re-read it under that lock so
// a concurrent completed publication cannot be overwritten by the sweep.
func (s *FlatSQLStore) reconcileDatasetPublicationAvailability(candidate DatasetShardPublication, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := DatasetShardPublicationQuery{SchemaName: candidate.SchemaName, ProviderID: candidate.ProviderID,
		SourceName: candidate.SourceName, BatchID: candidate.BatchID, QueryProfile: candidate.QueryProfile,
		Offset: candidate.Offset, Limit: candidate.Limit}
	pub := candidate
	var head string
	err := s.db.QueryRow(`SELECT unavailable_since, feed_head FROM sdn_dataset_shard_publications
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
		AND query_profile = ? AND window_offset = ? AND window_limit = ?`, query.SchemaName,
		query.ProviderID, query.SourceName, query.BatchID, query.QueryProfile, query.Offset, query.Limit).
		Scan(&pub.UnavailableSince, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if head != candidate.FeedHead {
		return false, nil
	}
	availability, reason := s.DatasetPublicationAvailability(pub)
	// Permission/I/O errors do not prove that a node stopped holding a file.
	if reason == "shard_stat_failed" || reason == "index_stat_failed" {
		return false, fmt.Errorf("check publication %s: %s", pub.ShardCID, reason)
	}
	previous := pub.UnavailableSince
	if availability == "available" {
		pub.UnavailableSince = 0
	} else if previous == 0 || previous > now.Unix() {
		pub.UnavailableSince = now.Unix()
	} else if now.Sub(time.Unix(previous, 0)) >= DatasetPublicationUnavailableRetention {
		// Supersede deliberately used these rows as catch-up dedup keys.
		// Keep that fact in the replay ledger before removing the listing,
		// or a peer would import the same retired batch on its next poll.
		state := DatasetPublicationReplayState{PNMKey: DatasetPublicationRetirementKey(pub),
			SchemaName: pub.SchemaName, State: "retired", UpdatedAt: now}
		if err := s.applyDatasetPublicationReplayStateUpsert(state); err != nil {
			return false, err
		}
		if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{Kind: auxiliaryEventDatasetReplayStateUpsert,
			DatasetReplayState: &state}); err != nil {
			return false, err
		}
		if _, err := s.applyDatasetShardPublicationDeleteExact(query, pub.Offset); err != nil {
			return false, err
		}
		if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{Kind: auxiliaryEventDatasetShardPublicationDelete,
			DatasetShardPublicationDelete: &auxiliaryDatasetShardPublicationDelete{Query: query, Offset: pub.Offset, Exact: true}}); err != nil {
			return false, err
		}
		return true, nil
	}
	if pub.UnavailableSince == previous {
		return false, nil
	}
	if err := s.applyDatasetShardPublicationUpsert(pub); err != nil {
		return false, err
	}
	if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{Kind: auxiliaryEventDatasetShardPublicationUpsert,
		DatasetShardPublication: &pub}); err != nil {
		return false, err
	}
	return false, nil
}
