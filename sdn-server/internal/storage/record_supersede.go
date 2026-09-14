package storage

// record_supersede.go — CAT supersede on ingest, and the one record-removal
// helper every delete path shares.
//
// OWNER 2026-09-02: "CAT should overwrite, no historical CAT stored". Within
// one producer's table a $CAT record supersedes the previous record for the
// same object. The identity is the one CAT.fbs itself defines: the
// (CATALOG_URI, CATALOG_OBJECT_ID) pair when both are present, else
// NORAD_CAT_ID, else OBJECT_ID. A record with none of them has no identity
// and supersedes nothing. Superseding is what stops a re-ingested, unchanged
// catalog from growing the store by a full edition every time.

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

// recordSupersedeKey returns the within-producer identity a record supersedes,
// or "" for a record that supersedes nothing. Only $CAT has a supersede rule
// today; every other standard is historical.
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

// supersedeInProducerTableTx removes, from ONE producer's table, every record
// that carries the same supersede key as the record about to be inserted, and
// returns the CIDs whose LAST copy went with it (removed from the index, the
// tags, the summaries and the full-text index). Those CIDs must be tombstoned
// in the engine after the caller's transaction commits
// (tombstoneEngineRecordsLocked). A CID still held by another producer's table
// stays a record. Caller holds s.mu for writing.
func (s *FlatSQLStore) supersedeInProducerTableTx(exec sqlQueryExecer, schemaName, tableName, key, newCID string) ([]string, error) {
	if key == "" {
		return nil, nil
	}
	rows, err := exec.Query(fmt.Sprintf(`SELECT cid FROM %s WHERE supersede_key = ? AND cid <> ?`, tableName), key, newCID)
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
