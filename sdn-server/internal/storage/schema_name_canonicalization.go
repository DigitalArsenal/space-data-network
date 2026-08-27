package storage

// One standard, one stored schema name — for the rows that were already
// written under the other one.
//
// The WRITE path was canonicalized at the connector boundary (sdn 39662af9,
// caps/storage.go canonicalStoredSchemaName) and the journal is canonicalized
// on decode (record_catalog_journal.go), so nothing new can land under a bare
// code again. Neither of those touches rows already in the control database:
// on host-02, 452,882 $TBS rows are still filed under "TBS" while every reader
// normalizes to "TBS.fbs", which is precisely the split that made them
// unexportable. This is the boot fix-up for those rows.
//
// It is application-blind. It never names a standard: it asks the SDS
// validator to normalize whatever spellings the store actually contains, and
// corrects the ones that answer with a different canonical name. A spelling
// the validator does not recognize is left exactly as it is.
//
// It is idempotent. It works off the store's own DISTINCT schema names, it
// merges rather than collides (INSERT OR IGNORE then DELETE, so a CID that
// already exists under BOTH spellings keeps the canonical row), and once no
// non-canonical spelling remains it is a single cheap DISTINCT scan that
// changes nothing. It ledgers each correction in sdn_metadata with the row
// counts and the time it took.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// schemaNameCanonicalizationLedgerKey is the sdn_metadata key the ledger line
// lands under. One key, rewritten on each correcting run.
const schemaNameCanonicalizationLedgerKey = "storage.schema_name_canonicalization"

// schemaNameCanonicalizationBatch bounds one UPDATE. A production ingest box
// carries hundreds of thousands of rows under the wrong spelling and every
// updated row also moves in the primary key's b-tree.
const schemaNameCanonicalizationBatch = 20000

// schemaNameCanonicalizationBatchBudget mirrors the source-tag migration's:
// the engine kills any single call at five minutes and poisons itself, so a
// batch that gets anywhere near that must say so while the store still works.
const schemaNameCanonicalizationBatchBudget = 60 * time.Second

// canonicalizeStoredSchemaNames rewrites non-canonical schema_name values in
// the record index, the source-tag rows and the source summary.
func (s *FlatSQLStore) canonicalizeStoredSchemaNames() error {
	stored, err := s.distinctStoredSchemaNames()
	if err != nil {
		return err
	}
	corrections := map[string]string{}
	for _, name := range stored {
		canonical := sds.NormalizeSchemaFileName(name)
		if canonical == "" || canonical == name {
			continue
		}
		corrections[name] = canonical
	}
	if len(corrections) == 0 {
		return nil
	}

	started := time.Now()
	var ledger []string
	for legacy, canonical := range corrections {
		moved, err := s.canonicalizeOneSchemaName(legacy, canonical)
		if err != nil {
			return err
		}
		ledger = append(ledger, fmt.Sprintf("%s->%s:%d", legacy, canonical, moved))
		log.Infof("Schema-name canonicalization: moved %d row(s) from %q to %q", moved, legacy, canonical)
	}
	line := fmt.Sprintf("%s %s in %s",
		time.Now().UTC().Format(time.RFC3339), strings.Join(ledger, " "), time.Since(started).Round(time.Millisecond))
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_metadata (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`), schemaNameCanonicalizationLedgerKey, line, time.Now().Unix()); err != nil {
		return fmt.Errorf("ledger schema-name canonicalization: %w", err)
	}
	return nil
}

// distinctStoredSchemaNames walks the DISTINCT schema names by SEEKING past
// each one, not by scanning. `SELECT DISTINCT schema_name` over a multi-million
// row table is one unbounded engine call, and one unbounded engine call is how
// this work poisoned an engine in rehearsal; schema_name leads
// sdn_record_index's primary key, so each step here is one index descent and
// there are as many steps as there are standards in the store.
func (s *FlatSQLStore) distinctStoredSchemaNames() ([]string, error) {
	var names []string
	cursor := ""
	for i := 0; i < schemaNameScanLimit; i++ {
		var name string
		err := s.db.QueryRow(`
			SELECT schema_name FROM sdn_record_index
			WHERE schema_name > ?
			ORDER BY schema_name
			LIMIT 1
		`, cursor).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list stored schema names: %w", err)
		}
		names = append(names, name)
		cursor = name
	}
	return nil, fmt.Errorf("list stored schema names: seek did not terminate after %d names", schemaNameScanLimit)
}

// schemaNameScanLimit bounds the seek above. The SDS catalog is a couple of
// hundred standards and each can appear under at most a canonical and a bare
// spelling, so anything past this is a loop, not a catalog.
const schemaNameScanLimit = 4096

// canonicalizeOneSchemaName moves every row filed under legacy to canonical
// and reports how many record-index rows moved.
func (s *FlatSQLStore) canonicalizeOneSchemaName(legacy, canonical string) (int64, error) {
	var moved int64
	for {
		batchStarted := time.Now()
		// MERGE, NOT COLLIDE. The same CID can already be present under the
		// canonical name (a record re-ingested after the write path was
		// fixed); the canonical row wins and the legacy one is dropped.
		result, err := s.db.Exec(flatsqldrv.WithoutJournal(`
			INSERT OR IGNORE INTO sdn_record_index (
				schema_name, cid, norad_cat_id, entity_id, object_type,
				ops_status_code, epoch_unix, epoch_day, source_timestamp, created_at
			)
			SELECT ?, cid, norad_cat_id, entity_id, object_type,
			       ops_status_code, epoch_unix, epoch_day, source_timestamp, created_at
			FROM sdn_record_index
			WHERE schema_name = ?
			LIMIT ?
		`), canonical, legacy, schemaNameCanonicalizationBatch)
		if err != nil {
			return moved, fmt.Errorf("canonicalize record index %q: %w", legacy, err)
		}
		inserted, _ := result.RowsAffected()

		deleted, err := s.db.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM sdn_record_index
			WHERE rowid IN (
				SELECT rowid FROM sdn_record_index WHERE schema_name = ? LIMIT ?
			)
		`), legacy, schemaNameCanonicalizationBatch)
		if err != nil {
			return moved, fmt.Errorf("retire legacy record index rows %q: %w", legacy, err)
		}
		removed, _ := deleted.RowsAffected()
		moved += inserted
		if removed == 0 {
			break
		}
		if elapsed := time.Since(batchStarted); elapsed > schemaNameCanonicalizationBatchBudget {
			log.Warnf("Schema-name canonicalization: a %d-row batch took %s, which is within reach of the engine's 5m per-call limit",
				schemaNameCanonicalizationBatch, elapsed.Round(time.Millisecond))
		}
	}

	// Bounded, for the same reason the record-index loop above is.
	for {
		if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
			INSERT OR IGNORE INTO `+sourceTagRowsTable+` (schema_name, cid, provenance_id, created_at)
			SELECT ?, cid, provenance_id, created_at
			FROM (
				SELECT cid, provenance_id, created_at FROM `+sourceTagRowsTable+`
				WHERE schema_name = ? ORDER BY cid LIMIT ?
			)
		`), canonical, legacy, schemaNameCanonicalizationBatch); err != nil {
			return moved, fmt.Errorf("canonicalize source tag rows %q: %w", legacy, err)
		}
		deleted, err := s.db.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM `+sourceTagRowsTable+`
			WHERE schema_name = ?
			  AND cid IN (SELECT cid FROM `+sourceTagRowsTable+` WHERE schema_name = ? ORDER BY cid LIMIT ?)
		`), legacy, legacy, schemaNameCanonicalizationBatch)
		if err != nil {
			return moved, fmt.Errorf("retire legacy source tag rows %q: %w", legacy, err)
		}
		removed, _ := deleted.RowsAffected()
		if removed == 0 {
			break
		}
	}

	summaryExists, err := s.tableExists("sdn_record_source_summary")
	if err != nil {
		return moved, err
	}
	if summaryExists {
		if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
			UPDATE OR REPLACE sdn_record_source_summary SET schema_name = ? WHERE schema_name = ?
		`), canonical, legacy); err != nil {
			return moved, fmt.Errorf("canonicalize source summary %q: %w", legacy, err)
		}
	}
	return moved, nil
}
