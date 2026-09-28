package storage

// record_ingest_identity.go — "the same record, fetched again" at ingest.
//
// WHY THIS FILE EXISTS (graph: sdn-publication-hygiene-20260928). The record
// store dedupes by CID, and a CID is the hash of the record's bytes. That is
// exact for a parser whose output is a pure function of the upstream payload,
// and it is exactly wrong for one that stamps the fetch into the record. The
// $IQC parser (sigmf-captures) writes RETRIEVED_AT from the HTTP Date header
// and CREATED_AT/UPDATED_AT from the wall clock, so two pulls of the SAME
// IQEngine metadata index (same payload, same batch id 60cc9680…) produced
// 36,636 records whose bytes differed only in those three strings. Every
// daemon start on host-02 re-ran the pull, every pull landed 36,636 "new"
// records under the same batch id, and the within-batch duplicate reconcile
// could not see them because $IQC carries none of the satellite index columns
// it partitions by ("an empty index is not an identity"). The batch grew
// 36,636 -> 532,945 records and every auto-publish republished all of it.
//
// The ingest identity is the record's hash with those stamps blanked: the
// bytes of each declared string field are zeroed IN PLACE (its length and
// every offset are kept), so two records share an identity exactly when they
// are byte-identical apart from what one fetch wrote into them. Nothing else
// is ever masked, so a record whose upstream metadata changed — or that a
// fixed parser now normalizes differently — has a new identity and lands as a
// new record. A stamp of a different LENGTH shifts the layout and also yields
// a new identity: the failure direction is "store it", never "drop it".
//
// Scope is the ingest lane (schema, provider, source), the unit every other
// retention rule here is keyed by. An identity row points at the CID that
// holds it; the probe joins sdn_record_index, so a row whose record has since
// been deleted is never treated as held.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// ingestVolatileFields names, for one standard, its file identifier and the
// vtable slots (4 + 2*field id) of the string fields a parser fills with the
// time of one fetch.
type ingestVolatileFields struct {
	identifier string
	slots      []flatbuffers.VOffsetT
}

// ingestVolatileStringFields is the registry. A standard absent from it keeps
// plain CID dedupe, which is already exact for it.
//
// $IQC (IQC.fbs): RETRIEVED_AT is field 6 (slot 16), CREATED_AT field 44
// (slot 92), UPDATED_AT field 45 (slot 94). The IDL defines RETRIEVED_AT as
// "the time the source metadata was retrieved" — a property of the fetch, not
// of the capture — and the parser writes CREATED_AT/UPDATED_AT from the same
// clock (sigmf_captures_module.cpp, build_iqc_record).
var ingestVolatileStringFields = map[string]ingestVolatileFields{
	"IQC.fbs": {identifier: "$IQC", slots: []flatbuffers.VOffsetT{16, 92, 94}},
}

// ingestIdentityPrefix versions the identity. A change to the registry for a
// standard must change the prefix, or old rows would silently compare against
// a different mask.
const ingestIdentityPrefix = "ingest-v1:"

// recordIngestIdentity returns the record's ingest identity, or "" when the
// standard declares no volatile fields or the buffer cannot be walked (a
// record that cannot be walked keeps CID dedupe; it is never dropped).
func recordIngestIdentity(schemaName string, data []byte) string {
	fields, ok := ingestVolatileStringFields[schemaName]
	if !ok || len(data) == 0 {
		return ""
	}
	masked, ok := maskVolatileStrings(data, fields)
	if !ok {
		return ""
	}
	sum := sha256.Sum256(masked)
	return ingestIdentityPrefix + hex.EncodeToString(sum[:])
}

// ingestIdentityApplies reports whether a write is subject to identity dedupe:
// the standard declares volatile fields and the write carries a lane.
func ingestIdentityApplies(schemaName string, tags *SourceTags) bool {
	if tags == nil {
		return false
	}
	if _, ok := ingestVolatileStringFields[schemaName]; !ok {
		return false
	}
	return strings.TrimSpace(tags.ProviderID) != "" && strings.TrimSpace(tags.SourceName) != ""
}

// maskVolatileStrings copies data and zeroes the bytes of each declared string
// field. Plain and size-prefixed buffers are both accepted; the identifier
// must match. Any out-of-range offset refuses the mask.
func maskVolatileStrings(data []byte, fields ingestVolatileFields) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	var rootPos flatbuffers.UOffsetT
	switch {
	case len(data) >= 8 && flatbuffers.BufferHasIdentifier(data, fields.identifier):
		rootPos = flatbuffers.GetUOffsetT(data)
	case len(data) >= 12 && flatbuffers.SizePrefixedBufferHasIdentifier(data, fields.identifier):
		rootPos = flatbuffers.GetUOffsetT(data[flatbuffers.SizeUint32:]) + flatbuffers.SizeUint32
	default:
		return nil, false
	}
	if int(rootPos)+flatbuffers.SizeUOffsetT > len(data) {
		return nil, false
	}
	masked := append([]byte(nil), data...)
	tab := flatbuffers.Table{Bytes: masked, Pos: rootPos}
	for _, slot := range fields.slots {
		o := flatbuffers.UOffsetT(tab.Offset(slot))
		if o == 0 {
			continue
		}
		field := o + tab.Pos
		if int(field)+flatbuffers.SizeUOffsetT > len(masked) {
			return nil, false
		}
		start := field + flatbuffers.GetUOffsetT(masked[field:])
		if int(start)+flatbuffers.SizeUOffsetT > len(masked) {
			return nil, false
		}
		n := flatbuffers.GetUOffsetT(masked[start:])
		begin := int(start) + flatbuffers.SizeUOffsetT
		end := begin + int(n)
		if end > len(masked) || end < begin {
			return nil, false
		}
		clear(masked[begin:end])
	}
	return masked, true
}

func (s *FlatSQLStore) initRecordIngestIdentityTable() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_record_ingest_identity (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			identity TEXT NOT NULL,
			cid TEXT NOT NULL,
			PRIMARY KEY (schema_name, provider_id, source_name, identity)
		)
	`); err != nil {
		return fmt.Errorf("failed to create record ingest identity table: %w", err)
	}
	return nil
}

// ingestIdentityLane is the (provider, source) scope of one write.
type ingestIdentityLane struct {
	providerID string
	sourceName string
}

func ingestIdentityLaneOf(tags *SourceTags) ingestIdentityLane {
	if tags == nil {
		return ingestIdentityLane{}
	}
	return ingestIdentityLane{providerID: strings.TrimSpace(tags.ProviderID), sourceName: strings.TrimSpace(tags.SourceName)}
}

// heldIngestIdentities maps each identity the lane already holds to the CID
// holding it. Only identities whose CID is still in the record index count.
func heldIngestIdentities(exec sqlQueryExecer, schemaName string, lane ingestIdentityLane, identities []string) (map[string]string, error) {
	held := make(map[string]string, len(identities))
	unique := make([]string, 0, len(identities))
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity == "" {
			continue
		}
		if _, dup := seen[identity]; dup {
			continue
		}
		seen[identity] = struct{}{}
		unique = append(unique, identity)
	}
	for start := 0; start < len(unique); start += batchProbeCIDs {
		end := start + batchProbeCIDs
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]
		args := make([]any, 0, len(batch)+3)
		args = append(args, schemaName, lane.providerID, lane.sourceName)
		for _, identity := range batch {
			args = append(args, identity)
		}
		rows, err := exec.Query(`
			SELECT i.identity, i.cid
			FROM sdn_record_ingest_identity i
			INNER JOIN sdn_record_index x ON x.schema_name = i.schema_name AND x.cid = i.cid
			WHERE i.schema_name = ? AND i.provider_id = ? AND i.source_name = ?
			  AND i.identity IN (`+placeholderList(len(batch))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("probe %s ingest identities: %w", schemaName, err)
		}
		for rows.Next() {
			var identity, cid string
			if err := rows.Scan(&identity, &cid); err != nil {
				rows.Close()
				return nil, err
			}
			held[identity] = cid
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return held, nil
}

// ingestIdentityRow is one identity -> CID mapping to write.
type ingestIdentityRow struct {
	identity string
	cid      string
}

// upsertIngestIdentities writes identity rows. A conflicting row can only be a
// stale one (the probe found no live holder), so the new CID replaces it.
func upsertIngestIdentities(exec sqlExecer, schemaName string, lane ingestIdentityLane, rows []ingestIdentityRow) error {
	for start := 0; start < len(rows); start += batchStatementRows {
		end := start + batchStatementRows
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		args := make([]any, 0, len(batch)*5)
		for _, row := range batch {
			args = append(args, schemaName, lane.providerID, lane.sourceName, row.identity, row.cid)
		}
		if _, err := exec.Exec(`
			INSERT INTO sdn_record_ingest_identity (schema_name, provider_id, source_name, identity, cid)
			VALUES `+valueTupleList(len(batch), 5)+`
			ON CONFLICT(schema_name, provider_id, source_name, identity) DO UPDATE SET cid = excluded.cid`, args...); err != nil {
			return fmt.Errorf("record %s ingest identities: %w", schemaName, err)
		}
	}
	return nil
}

// cidsCarryingSourceTag returns which cids already carry exactly this source
// tag (the unique key of sdn_record_source_tags).
func cidsCarryingSourceTag(exec sqlQueryExecer, schemaName string, tags SourceTags, cids []string) (map[string]struct{}, error) {
	tags = normalizeSourceTags(tags)
	present := make(map[string]struct{}, len(cids))
	for start := 0; start < len(cids); start += batchProbeCIDs {
		end := start + batchProbeCIDs
		if end > len(cids) {
			end = len(cids)
		}
		batch := cids[start:end]
		args := make([]any, 0, len(batch)+7)
		args = append(args, schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey)
		for _, cid := range batch {
			args = append(args, cid)
		}
		rows, err := exec.Query(`
			SELECT cid FROM sdn_record_source_tags
			WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
			  AND content_key_id = ? AND producer_peer_id = ? AND producer_public_key = ?
			  AND cid IN (`+placeholderList(len(batch))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("probe %s source tags: %w", schemaName, err)
		}
		for rows.Next() {
			var cid string
			if err := rows.Scan(&cid); err != nil {
				rows.Close()
				return nil, err
			}
			present[cid] = struct{}{}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return present, nil
}

// tagIdentityRepeats makes every held CID an identity repeat pointed at carry
// the incoming source tag, so the batch that re-delivered the record still
// names it (a batch-scoped export of the new batch must include it). A tag the
// CID already carries is left alone — the replay of one batch writes nothing.
func tagIdentityRepeats(exec sqlQueryExecer, readSource, schemaName string, tags SourceTags, heldCIDs []string) error {
	if len(heldCIDs) == 0 {
		return nil
	}
	unique := make([]string, 0, len(heldCIDs))
	seen := make(map[string]struct{}, len(heldCIDs))
	for _, cid := range heldCIDs {
		if _, dup := seen[cid]; dup {
			continue
		}
		seen[cid] = struct{}{}
		unique = append(unique, cid)
	}
	present, err := cidsCarryingSourceTag(exec, schemaName, tags, unique)
	if err != nil {
		return err
	}
	for _, cid := range unique {
		if _, ok := present[cid]; ok {
			continue
		}
		if err := upsertSourceTagsTx(exec, readSource, schemaName, cid, tags, -1); err != nil {
			return err
		}
	}
	return nil
}

// IngestIdentityReconcileResult reports one lane's identity reconcile.
type IngestIdentityReconcileResult struct {
	SchemaName string `json:"schemaName"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	Apply      bool   `json:"apply"`
	// Scanned is how many of the lane's records were read.
	Scanned int64 `json:"scanned"`
	// Identities is how many distinct ingest identities the lane holds.
	Identities int64 `json:"identities"`
	// Duplicates is how many records repeat an identity an older record
	// already holds (deleted from the lane when Apply is set).
	Duplicates int64 `json:"duplicates"`
	// Unwalkable is how many records carry no identity (kept as they are).
	Unwalkable int64 `json:"unwalkable"`
	// RecordsDeleted counts records removed from the store entirely (no other
	// lane still tags them).
	RecordsDeleted int64 `json:"recordsDeleted"`
}

// reconcileIdentityChunk bounds the records one reconcile window reads and
// deletes under one store-lock hold.
const reconcileIdentityChunk = 512

// ReconcileLaneIngestIdentities repairs a lane written before ingest identity
// existed: it walks the lane's records oldest first (record-index rowid, the
// cursor peers hold), keeps the first record of every identity, backfills the
// identity rows, and — with apply — removes the later copies from the lane.
// A removed copy that no other lane tags leaves the store (index, producer
// tables, engine window). Dry run reports the same counts and writes nothing.
//
// Chunked like the batch supersede: each window reads and deletes at most
// reconcileIdentityChunk records under one lock hold, so a live node keeps
// serving between windows.
func (s *FlatSQLStore) ReconcileLaneIngestIdentities(schemaName, providerID, sourceName string, apply bool) (IngestIdentityReconcileResult, error) {
	result := IngestIdentityReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		Apply:      apply,
	}
	if s == nil {
		return result, errors.New("store is required")
	}
	if result.SchemaName == "" || result.ProviderID == "" || result.SourceName == "" {
		return result, errors.New("schema, provider and source are required")
	}
	if _, ok := ingestVolatileStringFields[result.SchemaName]; !ok {
		return result, fmt.Errorf("%s declares no ingest-time fields; CID dedupe is already exact for it", result.SchemaName)
	}
	if apply {
		if err := s.requireWritable("reconcile lane ingest identities"); err != nil {
			return result, err
		}
	}
	lane := ingestIdentityLane{providerID: result.ProviderID, sourceName: result.SourceName}

	cids, err := s.laneCIDsOldestFirst(result.SchemaName, lane)
	if err != nil {
		return result, err
	}
	kept := make(map[string]string, 1024)
	var removedAny bool
	for start := 0; start < len(cids); start += reconcileIdentityChunk {
		end := start + reconcileIdentityChunk
		if end > len(cids) {
			end = len(cids)
		}
		removed, err := s.reconcileIdentityWindow(result.SchemaName, lane, cids[start:end], kept, apply, &result)
		if err != nil {
			return result, err
		}
		if removed {
			removedAny = true
		}
		if (start/reconcileIdentityChunk)%64 == 63 {
			log.Infof("ingest identity reconcile %s %s/%s: %d of %d records read, %d duplicates", result.SchemaName, result.ProviderID, result.SourceName, result.Scanned, len(cids), result.Duplicates)
		}
	}
	result.Identities = int64(len(kept))
	if removedAny {
		tableName, err := sds.SchemaNameToTable(result.SchemaName)
		if err != nil {
			return result, err
		}
		release := s.lockWrite("ingest identity reconcile: source summary")
		err = s.rebuildSourceSummaryForSchema(result.SchemaName, tableName)
		release()
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// laneCIDsOldestFirst lists the lane's live records by record-index rowid.
func (s *FlatSQLStore) laneCIDsOldestFirst(schemaName string, lane ingestIdentityLane) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`
		SELECT x.cid
		FROM sdn_record_index x
		WHERE x.schema_name = ?
		  AND x.cid IN (
			SELECT tags.cid FROM sdn_record_source_tags tags
			WHERE tags.schema_name = ? AND tags.provider_id = ? AND tags.source_name = ?
		  )
		ORDER BY x.rowid ASC`, schemaName, schemaName, lane.providerID, lane.sourceName)
	if err != nil {
		return nil, fmt.Errorf("list %s lane records: %w", schemaName, err)
	}
	defer rows.Close()
	var cids []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		cids = append(cids, cid)
	}
	return cids, rows.Err()
}

// reconcileIdentityWindow handles one window of reconcileIdentityChunk CIDs.
func (s *FlatSQLStore) reconcileIdentityWindow(schemaName string, lane ingestIdentityLane, cids []string, kept map[string]string, apply bool, result *IngestIdentityReconcileResult) (bool, error) {
	defer s.lockWrite("ReconcileLaneIngestIdentities")()

	tables, err := s.recordTablesForSchema(schemaName)
	if err != nil {
		return false, fmt.Errorf("record tables: %w", err)
	}
	stored := make(map[string][]byte, len(cids))
	args := make([]any, 0, len(cids))
	for _, cid := range cids {
		args = append(args, cid)
	}
	for _, table := range tables {
		rows, err := s.db.Query(fmt.Sprintf(`SELECT cid, data FROM %s WHERE cid IN (%s)`, table, placeholderList(len(cids))), args...)
		if err != nil {
			return false, fmt.Errorf("read %s lane records: %w", schemaName, err)
		}
		for rows.Next() {
			var cid string
			var data []byte
			if err := rows.Scan(&cid, &data); err != nil {
				rows.Close()
				return false, err
			}
			if _, have := stored[cid]; !have {
				stored[cid] = data
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
	}

	var keepRows []ingestIdentityRow
	var duplicates []string
	duplicateOf := make(map[string]string)
	for _, cid := range cids {
		data, ok := stored[cid]
		if !ok {
			continue
		}
		result.Scanned++
		plaintext, err := s.openStoredRecordBytes(schemaName, data)
		if err != nil {
			result.Unwalkable++
			continue
		}
		identity := recordIngestIdentity(schemaName, plaintext)
		if identity == "" {
			result.Unwalkable++
			continue
		}
		if keeper, dup := kept[identity]; dup {
			result.Duplicates++
			duplicates = append(duplicates, cid)
			duplicateOf[cid] = keeper
			continue
		}
		kept[identity] = cid
		keepRows = append(keepRows, ingestIdentityRow{identity: identity, cid: cid})
	}
	if !apply {
		return false, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin identity reconcile window: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := upsertIngestIdentities(tx, schemaName, lane, keepRows); err != nil {
		return false, err
	}
	var deleted int64
	if len(duplicates) > 0 {
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			return false, err
		}
		// The kept record inherits every batch tag of this lane its copies
		// carried: a batch that delivered only the copy must still name it.
		if err := s.moveLaneTagsToKeepers(tx, schemaName, lane, duplicateOf); err != nil {
			return false, err
		}
		dupArgs := make([]any, 0, len(duplicates)+3)
		dupArgs = append(dupArgs, schemaName, lane.providerID, lane.sourceName)
		for _, cid := range duplicates {
			dupArgs = append(dupArgs, cid)
		}
		in := placeholderList(len(duplicates))
		if _, err := tx.Exec(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND cid IN (`+in+`)`, dupArgs...); err != nil {
			return false, fmt.Errorf("delete duplicate %s lane tags: %w", schemaName, err)
		}
		orphanArgs := make([]any, 0, len(duplicates)+2)
		for _, cid := range duplicates {
			orphanArgs = append(orphanArgs, cid)
		}
		orphanArgs = append(orphanArgs, schemaName)
		orphanWhere := `cid IN (` + in + `) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`
		countArgs := append([]any{schemaName}, orphanArgs...)
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = ? AND `+orphanWhere, countArgs...).Scan(&deleted); err != nil {
			return false, fmt.Errorf("count orphaned %s duplicates: %w", schemaName, err)
		}
		s.deleteRoutedMirrorsWhere(tx, tableName, orphanWhere, orphanArgs...)
		if _, err := tx.Exec(`DELETE FROM sdn_record_index WHERE schema_name = ? AND `+orphanWhere, countArgs...); err != nil {
			return false, fmt.Errorf("delete orphaned %s duplicates: %w", schemaName, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit identity reconcile window: %w", err)
	}
	committed = true
	result.RecordsDeleted += deleted
	if len(duplicates) > 0 {
		if _, err := s.tombstoneOrphanedEngineRowsLocked(schemaName); err != nil {
			return true, err
		}
	}
	return len(duplicates) > 0, nil
}

// moveLaneTagsToKeepers copies each duplicate's lane tags onto the record kept
// for its identity. Tags the keeper already carries are left alone.
func (s *FlatSQLStore) moveLaneTagsToKeepers(tx sqlQueryExecer, schemaName string, lane ingestIdentityLane, duplicateOf map[string]string) error {
	if len(duplicateOf) == 0 {
		return nil
	}
	dups := make([]string, 0, len(duplicateOf))
	for cid := range duplicateOf {
		dups = append(dups, cid)
	}
	byTag := make(map[SourceTags][]string)
	for start := 0; start < len(dups); start += batchProbeCIDs {
		end := start + batchProbeCIDs
		if end > len(dups) {
			end = len(dups)
		}
		batch := dups[start:end]
		args := make([]any, 0, len(batch)+3)
		args = append(args, schemaName, lane.providerID, lane.sourceName)
		for _, cid := range batch {
			args = append(args, cid)
		}
		rows, err := tx.Query(`
			SELECT cid, provider_id, source_name, COALESCE(source_url, ''), batch_id,
			       content_key_id, producer_peer_id, producer_public_key
			FROM sdn_record_source_tags
			WHERE schema_name = ? AND provider_id = ? AND source_name = ?
			  AND cid IN (`+placeholderList(len(batch))+`)`, args...)
		if err != nil {
			return fmt.Errorf("read duplicate %s lane tags: %w", schemaName, err)
		}
		for rows.Next() {
			var cid string
			var tag SourceTags
			if err := rows.Scan(&cid, &tag.ProviderID, &tag.SourceName, &tag.SourceURL, &tag.BatchID,
				&tag.ContentKeyID, &tag.ProducerPeerID, &tag.ProducerPublicKey); err != nil {
				rows.Close()
				return err
			}
			byTag[tag] = append(byTag[tag], duplicateOf[cid])
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if len(byTag) == 0 {
		return nil
	}
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return fmt.Errorf("record read source: %w", err)
	}
	for tag, keepers := range byTag {
		if err := tagIdentityRepeats(tx, readSource, schemaName, tag, keepers); err != nil {
			return err
		}
	}
	return nil
}
