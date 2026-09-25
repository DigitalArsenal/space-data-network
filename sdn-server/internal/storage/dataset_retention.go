package storage

// dataset_retention.go — the newest-batch rule behind the ReplaceCurrent
// subscription retention (owner ruling 2026-09-04: a subscription replaces
// the current set with each update; archiving every version is the option).
//
// A lane (schema, provider, source) accumulates one publication batch per
// upstream fetch. Under ReplaceCurrent the node keeps exactly ONE batch — the
// newest one it can serve in full — and evicts the rest through the same
// chunked path the pinned-dataset supersede uses (SupersedeSourceBatches).
// Two rules keep that honest:
//
//   - a batch still mid-import is never the answer AND blocks eviction
//     (pending): its records are landing window by window, and evicting the
//     previous batch before the new one is complete would leave the lane
//     with less than one current set;
//   - a batch the node imported from a verified manifest without recording
//     publication rows (the PNM replay path) becomes the set only when the
//     ledger has nothing servable, or when an EARLIER such import never got
//     its rows either. Otherwise it waits: its row normally lands minutes
//     later through the feed-head path, and until then evicting the servable
//     batch leaves the lane with nothing to serve (the 2026-09-25 host-01
//     outage: an ~18 min blank catalog on spaceaware.io every six hours).

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RetainCandidate names the batch an import just landed (empty when the
// caller only re-evaluates the lane).
type RetainCandidate struct {
	BatchID     string
	PublishedAt time.Time
}

// laneBatchState is one publication batch of a lane, newest first, with its
// servability probed.
type laneBatchState struct {
	id          string
	sequence    int64
	publishedAt time.Time
	servable    bool
}

// laneBatchStates lists the lane's ledger batches ordered by (max
// FeedSequence desc, max PublishedAt desc) with MaterializedDatasetBatch
// servability probed for each.
func (s *FlatSQLStore) laneBatchStates(schemaName, providerID, sourceName string) ([]laneBatchState, error) {
	if s == nil {
		return nil, errors.New("store is required")
	}
	schemaName = strings.TrimSpace(schemaName)
	providerID = strings.TrimSpace(providerID)
	sourceName = strings.TrimSpace(sourceName)
	if schemaName == "" {
		return nil, errors.New("schema name is required")
	}
	if providerID == "" {
		return nil, errors.New("provider id is required")
	}
	if sourceName == "" {
		return nil, errors.New("source name is required")
	}
	publications, err := s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   schemaName,
		ProviderID:   providerID,
		SourceName:   sourceName,
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]*laneBatchState{}
	batches := make([]*laneBatchState, 0)
	for _, pub := range publications {
		id := strings.TrimSpace(pub.BatchID)
		if id == "" {
			continue
		}
		entry := byID[id]
		if entry == nil {
			entry = &laneBatchState{id: id, sequence: pub.FeedSequence, publishedAt: pub.PublishedAt}
			byID[id] = entry
			batches = append(batches, entry)
			continue
		}
		if pub.FeedSequence > entry.sequence {
			entry.sequence = pub.FeedSequence
		}
		if pub.PublishedAt.After(entry.publishedAt) {
			entry.publishedAt = pub.PublishedAt
		}
	}
	sort.SliceStable(batches, func(i, j int) bool {
		if batches[i].sequence != batches[j].sequence {
			return batches[i].sequence > batches[j].sequence
		}
		if !batches[i].publishedAt.Equal(batches[j].publishedAt) {
			return batches[i].publishedAt.After(batches[j].publishedAt)
		}
		return batches[i].id > batches[j].id
	})
	out := make([]laneBatchState, 0, len(batches))
	for _, batch := range batches {
		_, servable, err := s.MaterializedDatasetBatch(schemaName, batch.id, DatasetBatchOptions{})
		if err != nil {
			return nil, err
		}
		batch.servable = servable
		out = append(out, *batch)
	}
	return out, nil
}

// NewestServableSourceBatch finds the newest publication batch of a lane
// that is fully materialized here (MaterializedDatasetBatch servability).
// Batches order by (max FeedSequence desc, max PublishedAt desc). ok=false
// when no ledger batch is servable. pending=true when a batch NEWER than the
// answer (or, without an answer, any batch) exists but is not servable —
// an import in flight; callers must not evict while pending.
func (s *FlatSQLStore) NewestServableSourceBatch(schemaName, providerID, sourceName string) (batchID string, publishedAt time.Time, ok bool, pending bool, err error) {
	states, err := s.laneBatchStates(schemaName, providerID, sourceName)
	if err != nil {
		return "", time.Time{}, false, false, err
	}
	for _, state := range states {
		if state.servable {
			return state.id, state.publishedAt, true, pending, nil
		}
		pending = true
	}
	return "", time.Time{}, false, pending, nil
}

// laneHasOtherUnledgeredBatch reports whether the lane holds records under a
// batch id that is neither in the publication ledger nor the current import:
// an earlier import whose publication row never arrived.
func (s *FlatSQLStore) laneHasOtherUnledgeredBatch(schemaName, providerID, sourceName, importedID string, ledger []laneBatchState) (bool, error) {
	known := map[string]bool{importedID: true, "": true}
	for _, state := range ledger {
		known[state.id] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT DISTINCT batch_id FROM sdn_record_source_tags WHERE schema_name = ? AND provider_id = ? AND source_name = ?`,
		strings.TrimSpace(schemaName), strings.TrimSpace(providerID), strings.TrimSpace(sourceName))
	if err != nil {
		return false, fmt.Errorf("list lane batches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return false, fmt.Errorf("scan lane batch: %w", err)
		}
		if !known[strings.TrimSpace(id.String)] {
			return true, nil
		}
	}
	return false, rows.Err()
}

// RetainNewestSourceBatch applies the ReplaceCurrent rule to one lane: keep
// the newest servable batch (or the just-imported batch when the ledger knows
// no servable batch) and evict every other batch.
//
// A just-imported batch with no ledger rows never displaces a servable one.
// The verified-manifest (PNM) replay path lands a batch's records minutes
// before its publication row and files arrive through the feed-head path, and
// until then nothing can serve it. Switching to it early evicted the lane's
// only servable publication: on host-01 every six-hourly OMM refresh left
// spaceaware.io with zero objects for ~18 minutes (2026-09-25). The feed-head
// path re-runs retention once the row lands, and the switch happens then. Returns the supersede result and the kept batch
// id; an empty id with a zero result means nothing was done — an import
// newer than the batch to keep is still in flight (pending) or there is no
// batch to keep.
//
// Pending is judged against the batch that would be kept: a ledger batch
// that is not servable AND newer than it (by ledger order for a ledger keep,
// by publication time for a just-imported keep without ledger rows) is a
// series still landing. Older non-servable batches are what an earlier
// eviction left behind and never block.
func (s *FlatSQLStore) RetainNewestSourceBatch(schemaName, providerID, sourceName string, imported RetainCandidate) (DatasetSupersedeResult, string, error) {
	states, err := s.laneBatchStates(schemaName, providerID, sourceName)
	if err != nil {
		return DatasetSupersedeResult{}, "", err
	}
	answer := -1
	for i, state := range states {
		if state.servable {
			answer = i
			break
		}
	}
	importedID := strings.TrimSpace(imported.BatchID)
	importedInLedger := false
	for _, state := range states {
		if state.id == importedID {
			importedInLedger = true
			break
		}
	}
	keep := ""
	keepIsImported := false
	if answer >= 0 {
		keep = states[answer].id
	}
	if importedID != "" {
		switch {
		case answer < 0:
			if !importedInLedger {
				keep, keepIsImported = importedID, true
			}
		case importedID == keep:
		case !importedInLedger && imported.PublishedAt.After(states[answer].publishedAt):
			// Newer, but not servable yet: keep what the lane can serve. Only a
			// lane whose rows evidently never come (an EARLIER import is still
			// unledgered) switches to the import, so such a lane holds at most
			// one extra batch instead of accumulating every import.
			stuck, err := s.laneHasOtherUnledgeredBatch(schemaName, providerID, sourceName, importedID, states)
			if err != nil {
				return DatasetSupersedeResult{}, "", err
			}
			if !stuck {
				return DatasetSupersedeResult{}, "", nil
			}
			keep, keepIsImported = importedID, true
		}
	}
	if keep == "" {
		return DatasetSupersedeResult{}, "", nil
	}
	pending := false
	for i, state := range states {
		if state.servable {
			continue
		}
		if keepIsImported {
			if state.publishedAt.After(imported.PublishedAt) {
				pending = true
				break
			}
			continue
		}
		if i < answer {
			pending = true
			break
		}
	}
	if pending {
		return DatasetSupersedeResult{}, "", nil
	}
	result, err := s.SupersedeSourceBatches(schemaName, providerID, sourceName, keep)
	if err != nil {
		return result, keep, err
	}
	return result, keep, nil
}
