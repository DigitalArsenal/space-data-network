package storage

// record_supersede.go — CAT supersede on ingest, and the one record-removal
// helper every delete path shares.
//
// OWNER 2026-09-02: "CAT should overwrite, no historical CAT stored". A $CAT
// record supersedes the previous record for the same object. The identity is
// the one CAT.fbs itself defines: the (CATALOG_URI, CATALOG_OBJECT_ID) pair
// when both are present, else NORAD_CAT_ID, else OBJECT_ID. A record with none
// of them has no identity and supersedes nothing. Superseding is what stops a
// re-ingested, unchanged catalog from growing the store by a full edition
// every time.
//
// THE SUPERSEDE LANE IS (PRODUCER, SOURCE), NOT THE PRODUCER ALONE. Scoping it
// to the producer alone was a defect: one provider publishes the SAME catalog
// in two encodings as two distinct SOURCES (CelesTrak's satcat.txt and
// satcat.csv), both land under one producer peer, and neither carries a
// catalog URI — so both reduced to `norad:<id>` and whichever source ran
// second deleted the other's entire edition. The store then never converged
// (each source reported a full insert every tick, forever, re-creating exactly
// the re-ingest redundancy this rule exists to remove), and every unchanged
// record kept moving in sdn_record_index — which flatsql-store-v2.md §3
// promises never happens, and which makes every subscriber re-download the
// whole catalog each cycle.
//
// Every other retention rule in this store is already keyed (provider,
// source): SupersedeSourceBatches, ReconcileSourceBatch, the
// sdn_record_source_tags uniqueness index, engineSourceName on the engine
// mirror. This one now is too. Two encodings of one object from two sources
// are two assertions with different provenance — the SATCAT forms disagree on
// MASS, OWNER and LAUNCH_SITE for the same object — so collapsing them by
// fetch order was silently picking a winner. One row per object PER SOURCE is
// the rule; the lever for storing fewer is the FLOW, not a source-blind
// supersede.

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

const (
	// catCatalogURISlot / catCatalogObjectIDSlot are the CAT.fbs vtable slots
	// of CATALOG_URI (field 24) and CATALOG_OBJECT_ID (field 25): 4 + 2*index.
	// Read through the table directly because the vendored Go accessors lag
	// the IDL; FlatBuffers field ids are append-only, so the slots are stable.
	catCatalogURISlot      = 52
	catCatalogObjectIDSlot = 54
)

// recordSupersedeKey returns a record's OBJECT IDENTITY — what CAT.fbs itself
// says makes two records describe the same physical object — or "" for a
// record that supersedes nothing. Only $CAT has a supersede rule today; every
// other standard is historical.
//
// The identity is not the key a row is stored under: supersedeKeysForIdentity
// scopes it to the source that wrote it.
func recordSupersedeKey(schemaName string, data []byte) string {
	if schemaName != "CAT.fbs" {
		return ""
	}
	cat, err := parseCAT(data)
	if err != nil {
		return ""
	}
	tab := cat.Table()
	catalogURI := strings.TrimSpace(string(tableStringField(tab, catCatalogURISlot)))
	catalogObjectID := strings.TrimSpace(string(tableStringField(tab, catCatalogObjectIDSlot)))
	if catalogURI != "" && catalogObjectID != "" {
		return "uri:" + catalogURI + "\x00" + catalogObjectID
	}
	if id := cat.NORAD_CAT_ID(); id > 0 {
		return "norad:" + strconv.FormatUint(uint64(id), 10)
	}
	if objectID := strings.TrimSpace(string(cat.OBJECT_ID())); objectID != "" {
		return "object:" + objectID
	}
	return ""
}

// tableStringField reads a string field by vtable slot; nil when absent.
func tableStringField(tab flatbuffers.Table, slot flatbuffers.VOffsetT) []byte {
	o := flatbuffers.UOffsetT(tab.Offset(slot))
	if o == 0 {
		return nil
	}
	return tab.ByteVector(o + tab.Pos)
}

// supersedeSourcePrefix scopes a stored supersede key to the source that wrote
// the row: "src:<source>\x00<object identity>". An unattributed write — a
// relayed record, a direct Store, StoreRoutedByProducer — has no source and
// stores the bare identity, which is its own lane.
const supersedeSourcePrefix = "src:"

// supersedeKeys is what one record contributes to the supersede lane: the key
// its own row is STORED under, and the keys whose rows it retires.
type supersedeKeys struct {
	stored string
	match  []string
}

func (k supersedeKeys) empty() bool { return len(k.match) == 0 }

// sourceNameOf is the ingest source a write is attributed to, or "" when the
// write carries no provenance.
func sourceNameOf(tags *SourceTags) string {
	if tags == nil {
		return ""
	}
	return strings.TrimSpace(tags.SourceName)
}

// recordSupersedeKeys scopes a record's object identity to the source writing
// it.
func recordSupersedeKeys(schemaName string, data []byte, sourceName string) supersedeKeys {
	return supersedeKeysForIdentity(recordSupersedeKey(schemaName, data), sourceName)
}

// supersedeKeysForStoredKey re-scopes a key read off an existing row to the
// source performing THIS write. The repeat-CID mirror copies a row out of
// whichever producer table already holds the content: the object identity in
// that key is the shared fact, but the source scope on it belongs to the other
// producer's lane and must not travel into this one.
func supersedeKeysForStoredKey(storedKey, sourceName string) supersedeKeys {
	return supersedeKeysForIdentity(supersedeIdentity(storedKey), sourceName)
}

// supersedeKeysForIdentity builds the stored key and the match set.
//
// A source-scoped write ALSO matches the bare identity, and that is the
// migration for every store written under the producer-only rule: those rows
// carry bare keys, and the first re-ingest of each object collapses them
// instead of leaving behind a generation no source can ever reach again (no
// store-wipe needed). It costs one thing, deliberately: while a bare row
// exists, a source-scoped write also retires an UNATTRIBUTED row for the same
// object under the same producer — which is precisely what the producer-only
// rule already did to it, so nothing that survives today is lost. The reverse
// does not hold: an unattributed write never touches a source's rows, because
// a record carrying no provenance cannot speak for a source that has some.
func supersedeKeysForIdentity(identity, sourceName string) supersedeKeys {
	if identity == "" {
		return supersedeKeys{}
	}
	// The NUL separator is ours, not the source's: one inside a source name
	// would make the scope ambiguous to supersedeIdentity.
	sourceName = strings.ReplaceAll(strings.TrimSpace(sourceName), "\x00", "")
	if sourceName == "" {
		return supersedeKeys{stored: identity, match: []string{identity}}
	}
	stored := supersedeSourcePrefix + sourceName + "\x00" + identity
	return supersedeKeys{stored: stored, match: []string{stored, identity}}
}

// supersedeIdentity strips the source scope from a stored key. Cutting at the
// FIRST NUL after the prefix is what keeps a "uri:<catalog>\x00<object>"
// identity — which carries a NUL of its own — intact.
func supersedeIdentity(storedKey string) string {
	rest, scoped := strings.CutPrefix(storedKey, supersedeSourcePrefix)
	if !scoped {
		return storedKey
	}
	if _, identity, found := strings.Cut(rest, "\x00"); found {
		return identity
	}
	return storedKey
}

// queuedKeysCollide reports whether a row still sitting in a batch write
// buffer carries a key this record's supersede lane would retire. The buffer
// is invisible to the supersede SELECT, so the caller flushes first when it
// does.
func queuedKeysCollide(queued map[string]struct{}, keys supersedeKeys) bool {
	for _, key := range keys.match {
		if _, collides := queued[key]; collides {
			return true
		}
	}
	return false
}

// supersedeInProducerTableTx removes, from ONE producer's table, every record
// in the incoming record's supersede lane — its own (source, object identity),
// plus the bare identity a pre-scoping store wrote — and returns the CIDs
// whose LAST copy went with it (removed from the index, the tags, the
// summaries and the full-text index). Those CIDs must be tombstoned in the
// engine after the caller's transaction commits
// (tombstoneEngineRecordsLocked). A CID still held by another producer's table
// stays a record. Caller holds s.mu for writing.
func (s *FlatSQLStore) supersedeInProducerTableTx(exec sqlQueryExecer, schemaName, tableName string, keys supersedeKeys, newCID string) ([]string, error) {
	if keys.empty() {
		return nil, nil
	}
	args := make([]any, 0, len(keys.match)+1)
	for _, key := range keys.match {
		args = append(args, key)
	}
	args = append(args, newCID)
	rows, err := exec.Query(fmt.Sprintf(
		`SELECT cid FROM %s WHERE supersede_key IN (%s) AND cid <> ?`,
		tableName, placeholderList(len(keys.match)),
	), args...)
	if err != nil {
		return nil, fmt.Errorf("find superseded %s records: %w", schemaName, err)
	}
	var old []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return nil, err
		}
		old = append(old, cid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var orphaned []string
	for _, cid := range old {
		if _, err := exec.Exec(fmt.Sprintf(`DELETE FROM %s WHERE cid = ?`, tableName), cid); err != nil {
			return orphaned, fmt.Errorf("delete superseded %s record %s: %w", schemaName, cid, err)
		}
		gone, err := s.removeRecordIfOrphanedTx(exec, schemaName, cid)
		if err != nil {
			return orphaned, err
		}
		if gone {
			orphaned = append(orphaned, cid)
		}
	}
	return orphaned, nil
}

// removeRecordIfOrphanedTx finishes removing a record whose producer-table
// row has just been deleted: when no other producer table still holds the
// CID, its index row, source tags (with their summary decrements) and
// full-text row are removed too. Returns true when the record is gone.
func (s *FlatSQLStore) removeRecordIfOrphanedTx(exec sqlQueryExecer, schemaName, cid string) (bool, error) {
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return false, err
	}
	var recordRowID, recordBytes sql.NullInt64
	err = exec.QueryRow(fmt.Sprintf(`SELECT rowid, record_length FROM %s WHERE cid = ?1`, readSource), cid).Scan(&recordRowID, &recordBytes)
	switch {
	case err == nil:
		return false, nil // another producer still holds it
	case errors.Is(err, sql.ErrNoRows):
	default:
		return false, fmt.Errorf("check remaining holders of %s: %w", cid, err)
	}
	return true, s.removeRecordCatalogRowsTx(exec, schemaName, cid, recordBytes.Int64, recordRowID.Int64)
}

// removeRecordCatalogRowsTx deletes a record's index row and its source tags
// (decrementing the derived summaries); the full-text row follows the index
// row by trigger. recordBytes and
// recordRowID feed the summary decrement; zero when unknown.
func (s *FlatSQLStore) removeRecordCatalogRowsTx(exec sqlQueryExecer, schemaName, cid string, recordBytes, recordRowID int64) error {
	tagRows, err := exec.Query(`
		SELECT provider_id, source_name, source_url, batch_id, content_key_id,
		       producer_peer_id, producer_public_key
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ?
	`, schemaName, cid)
	if err != nil {
		return fmt.Errorf("lookup deleted source tags: %w", err)
	}
	var deletedTags []SourceTags
	for tagRows.Next() {
		var tags SourceTags
		if err := tagRows.Scan(
			&tags.ProviderID, &tags.SourceName, &tags.SourceURL, &tags.BatchID,
			&tags.ContentKeyID, &tags.ProducerPeerID, &tags.ProducerPublicKey,
		); err != nil {
			tagRows.Close()
			return fmt.Errorf("scan deleted source tags: %w", err)
		}
		deletedTags = append(deletedTags, tags)
	}
	if err := tagRows.Close(); err != nil {
		return fmt.Errorf("close deleted source tags: %w", err)
	}
	if _, err := exec.Exec(flatsqldrv.WithoutJournal(`DELETE FROM sdn_record_index WHERE schema_name = ? AND cid = ?`), schemaName, cid); err != nil {
		return fmt.Errorf("delete index row for %s/%s: %w", schemaName, cid, err)
	}
	if _, err := exec.Exec(flatsqldrv.WithoutJournal(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND cid = ?`), schemaName, cid); err != nil {
		return fmt.Errorf("delete source tags for %s/%s: %w", schemaName, cid, err)
	}
	for _, tags := range deletedTags {
		if err := decrementSourceSummary(exec, schemaName, tags, recordBytes, recordRowID); err != nil {
			return err
		}
	}
	// The full-text row goes with the index row (sdn_record_fts_delete trigger).
	return nil
}
