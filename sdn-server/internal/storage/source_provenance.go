package storage

// Interned record provenance.
//
// THE MEASUREMENT THAT FORCED THIS (20,000 real $TBS records, `dbstat` over
// the resulting control.flatsqldb, bytes PER RECORD):
//
//	sdn_record_source_tags (table) .................. 373.6
//	sqlite_autoindex_sdn_record_source_tags_1 ....... 314.2   <- the 8-column PK
//	idx_sdn_record_source_tags_unique ............... 314.2   <- THE SAME 8 COLUMNS
//	idx_sdn_record_source_tags_recent ............... 163.0
//	idx_sdn_record_source_tags_batch_cid ............ 151.1
//	idx_sdn_record_source_tags_source_cid ........... 131.9
//	idx_sdn_record_source_tags_lookup ................ 77.4
//	                                          TOTAL 1,525.4 B/record
//
// That is 42% of everything the node writes per record, and 68% of the control
// database — to store, over and over, the SAME seven strings. A cellular batch
// of 368,178 records carries ONE provenance tuple ("mls-archive",
// "mls-final-full-cell-export", one URL, one batch id, one producer peer, one
// producer key) and paid for 368,178 copies of it across a table and six
// indexes.
//
// So the seven strings move into a dictionary keyed by an integer, and the
// per-record row becomes (schema_name, cid, provenance_id). This is a STORAGE
// layout change and nothing else:
//
//   - `sdn_record_source_tags` KEEPS ITS NAME, ITS COLUMNS AND ITS COLUMN
//     ORDER — it becomes a VIEW over the slim rows joined to the dictionary,
//     so every reader in the tree (118 references across 11 files, the gateway
//     API, the compaction snapshot, the engine rebuild) is untouched and every
//     SELECT keeps answering exactly what it answered before.
//   - only the WRITERS move to the base table, and they are listed in one
//     place: sourceTagWriteSQL below.
//   - the dictionary's identity is all SEVEN columns, so re-pointing a record
//     whose source_url changed stays a per-record operation with per-record
//     semantics (upsertSourceTagsTx), exactly as it was when the URL lived on
//     the record's own row.
//
// It knows no standard and reads no payload: $TBS pays the same rules as $OMM.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// sourceProvenanceTable interns the (provider, source, url, batch,
	// content key, producer peer, producer key) tuple.
	sourceProvenanceTable = "sdn_source_provenance"
	// sourceTagRowsTable is the per-record row: three small values.
	sourceTagRowsTable = "sdn_record_source_tag_rows"
	// sourceTagsViewName is the name every reader still uses.
	sourceTagsViewName = "sdn_record_source_tags"
	// sourceTagMigrationBatch bounds one migration transaction. The legacy
	// table on a production ingest box holds millions of rows; copying it in
	// one transaction would hold the engine for the whole copy and need a
	// rollback journal the size of the table.
	sourceTagMigrationBatch = 20000
	// sourceTagMigrationBatchBudget is the per-batch wall clock past which the
	// migration says it is approaching the engine's uninterruptible 5-minute
	// per-call limit. Crossing it is not fatal; being SILENT about it was.
	sourceTagMigrationBatchBudget = 60 * time.Second
)

// provenanceCache memoizes the tuple -> id lookup for the life of a store.
// A node ingests from a handful of sources, so this is a few dozen entries
// that remove one SELECT from every single record write.
type provenanceCache struct {
	mu sync.Mutex
	id map[string]int64
}

func (c *provenanceCache) get(key string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == nil {
		return 0, false
	}
	v, ok := c.id[key]
	return v, ok
}

func (c *provenanceCache) put(key string, id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == nil {
		c.id = map[string]int64{}
	}
	c.id[key] = id
}

func (c *provenanceCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id = nil
}

// provenanceKey is the in-memory cache key. It is NOT stored: the database
// identity is the unique index on the seven columns.
func provenanceKey(tags SourceTags) string {
	return strings.Join([]string{
		tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID,
		tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey,
	}, "\x00")
}

func sourceProvenanceTableSQL() string {
	return `
		CREATE TABLE IF NOT EXISTS ` + sourceProvenanceTable + ` (
			id INTEGER PRIMARY KEY,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			source_url TEXT NOT NULL DEFAULT '',
			batch_id TEXT NOT NULL DEFAULT '',
			content_key_id TEXT NOT NULL DEFAULT '',
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT ''
		)
	`
}

func sourceTagRowsTableSQL() string {
	// WITHOUT ROWID: the primary key IS the table, so the row is stored once
	// instead of once in a heap and again in an autoindex. On the measured
	// fixture that alone is ~90 B/record.
	return `
		CREATE TABLE IF NOT EXISTS ` + sourceTagRowsTable + ` (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			provenance_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (schema_name, cid, provenance_id)
		) WITHOUT ROWID
	`
}

// sourceTagsViewSQL reproduces the legacy table's column set and ORDER, so
// `SELECT *` and positional scans keep working.
func sourceTagsViewSQL() string {
	return `
		CREATE VIEW ` + sourceTagsViewName + ` AS
		SELECT r.schema_name         AS schema_name,
		       r.cid                 AS cid,
		       p.provider_id         AS provider_id,
		       p.source_name         AS source_name,
		       p.source_url          AS source_url,
		       p.batch_id            AS batch_id,
		       p.content_key_id      AS content_key_id,
		       p.producer_peer_id    AS producer_peer_id,
		       p.producer_public_key AS producer_public_key,
		       r.created_at          AS created_at
		FROM ` + sourceTagRowsTable + ` r
		JOIN ` + sourceProvenanceTable + ` p ON p.id = r.provenance_id
	`
}

// initSourceProvenance creates the dictionary, the slim row table, their
// indexes and the compatibility view, and migrates a legacy physical
// sdn_record_source_tags table if one is still there.
func (s *FlatSQLStore) initSourceProvenance() error {
	if _, err := s.db.Exec(sourceProvenanceTableSQL()); err != nil {
		return fmt.Errorf("create %s: %w", sourceProvenanceTable, err)
	}
	if _, err := s.db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sdn_source_provenance_identity
		ON ` + sourceProvenanceTable + ` (
			provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
	`); err != nil {
		return fmt.Errorf("create %s identity index: %w", sourceProvenanceTable, err)
	}
	// The URL-blind lookup: upsertSourceTagsTx has to find the row whose
	// identity matches EXCEPT the URL, because that is the row the legacy
	// 8-column primary key considered the same row.
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_source_provenance_identity_no_url
		ON ` + sourceProvenanceTable + ` (
			provider_id, source_name, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
	`); err != nil {
		return fmt.Errorf("create %s url-blind index: %w", sourceProvenanceTable, err)
	}
	if _, err := s.db.Exec(sourceTagRowsTableSQL()); err != nil {
		return fmt.Errorf("create %s: %w", sourceTagRowsTable, err)
	}
	// (schema, provenance, cid) answers every batch/source scan the four
	// legacy provider/source/batch indexes answered, because the whole
	// provider/source/url/batch/producer tuple IS the provenance id.
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tag_rows_provenance
		ON ` + sourceTagRowsTable + ` (schema_name, provenance_id, cid)
	`); err != nil {
		return fmt.Errorf("create %s provenance index: %w", sourceTagRowsTable, err)
	}
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tag_rows_recent
		ON ` + sourceTagRowsTable + ` (schema_name, created_at DESC, cid)
	`); err != nil {
		return fmt.Errorf("create %s recent index: %w", sourceTagRowsTable, err)
	}
	return s.ensureSourceTagsView()
}

// ensureSourceTagsView installs the compatibility view once the legacy
// physical table is out of the way.
func (s *FlatSQLStore) ensureSourceTagsView() error {
	legacyTable, err := s.tableExists(sourceTagsViewName)
	if err != nil {
		return err
	}
	if legacyTable {
		// A physical table still owns the name: migrate it first. That path
		// drops it and calls back here.
		return s.migrateSourceTagsToProvenance()
	}
	exists, err := s.viewExists(sourceTagsViewName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := s.db.Exec(sourceTagsViewSQL()); err != nil {
		return fmt.Errorf("create %s view: %w", sourceTagsViewName, err)
	}
	return s.ensureSourceTagsViewTriggers()
}

// ensureSourceTagsViewTriggers makes the view WRITABLE with the legacy table's
// semantics.
//
// The hot writers were moved to the base tables deliberately — a trigger fires
// once per affected row and the ingest path must not pay that. These triggers
// exist for everything else: the cold paths, anything a future change adds
// without knowing the layout changed, and the store's own tests.
//
// EVERY WRITE IN THESE BODIES IS GUARDED BY `NOT EXISTS`, NOT BY `OR IGNORE`.
// SQLite propagates the OUTER statement's conflict clause into a trigger body,
// so `INSERT OR REPLACE INTO sdn_record_source_tags ...` — which is exactly
// what the legacy-control migration issues — turned an `INSERT OR IGNORE` on
// the dictionary into an `INSERT OR REPLACE`, deleting the dictionary row and
// reinserting it under a NEW id. Every tag row already pointing at the old id
// was orphaned, and a 12-row legacy migration landed 1 row. A guarded plain
// INSERT has no conflict for the outer clause to resolve. Without them
// `INSERT INTO sdn_record_source_tags ...` answers "cannot modify ... because
// it is a view", which would make the rename a breaking change instead of the
// transparent one it is meant to be.
func (s *FlatSQLStore) ensureSourceTagsViewTriggers() error {
	statements := []string{
		`CREATE TRIGGER sdn_record_source_tags_insert
		 INSTEAD OF INSERT ON ` + sourceTagsViewName + `
		 BEGIN
		   INSERT INTO ` + sourceProvenanceTable + ` (
		     provider_id, source_name, source_url, batch_id,
		     content_key_id, producer_peer_id, producer_public_key
		   )
		   SELECT NEW.provider_id, NEW.source_name, COALESCE(NEW.source_url, ''), COALESCE(NEW.batch_id, ''),
		          COALESCE(NEW.content_key_id, ''), COALESCE(NEW.producer_peer_id, ''), COALESCE(NEW.producer_public_key, '')
		   WHERE NOT EXISTS (
		     SELECT 1 FROM ` + sourceProvenanceTable + ` p
		     WHERE p.provider_id = NEW.provider_id
		       AND p.source_name = NEW.source_name
		       AND p.source_url = COALESCE(NEW.source_url, '')
		       AND p.batch_id = COALESCE(NEW.batch_id, '')
		       AND p.content_key_id = COALESCE(NEW.content_key_id, '')
		       AND p.producer_peer_id = COALESCE(NEW.producer_peer_id, '')
		       AND p.producer_public_key = COALESCE(NEW.producer_public_key, '')
		   );
		   INSERT INTO ` + sourceTagRowsTable + ` (schema_name, cid, provenance_id, created_at)
		   SELECT NEW.schema_name, NEW.cid, p.id, COALESCE(NEW.created_at, strftime('%s', 'now'))
		   FROM ` + sourceProvenanceTable + ` p
		   WHERE p.provider_id = NEW.provider_id
		     AND p.source_name = NEW.source_name
		     AND p.source_url = COALESCE(NEW.source_url, '')
		     AND p.batch_id = COALESCE(NEW.batch_id, '')
		     AND p.content_key_id = COALESCE(NEW.content_key_id, '')
		     AND p.producer_peer_id = COALESCE(NEW.producer_peer_id, '')
		     AND p.producer_public_key = COALESCE(NEW.producer_public_key, '')
		     AND NOT EXISTS (
		       SELECT 1 FROM ` + sourceTagRowsTable + ` r
		       WHERE r.schema_name = NEW.schema_name AND r.cid = NEW.cid AND r.provenance_id = p.id
		     );
		 END`,
		`CREATE TRIGGER sdn_record_source_tags_delete
		 INSTEAD OF DELETE ON ` + sourceTagsViewName + `
		 BEGIN
		   DELETE FROM ` + sourceTagRowsTable + `
		   WHERE schema_name = OLD.schema_name AND cid = OLD.cid
		     AND provenance_id IN (
		       SELECT id FROM ` + sourceProvenanceTable + `
		       WHERE provider_id = OLD.provider_id AND source_name = OLD.source_name
		         AND source_url = COALESCE(OLD.source_url, '') AND batch_id = OLD.batch_id
		         AND content_key_id = OLD.content_key_id
		         AND producer_peer_id = OLD.producer_peer_id
		         AND producer_public_key = OLD.producer_public_key
		     );
		 END`,
		// UPDATE re-points the record at the dictionary entry carrying the new
		// values, which is what "updating a tag column" means once the columns
		// are interned. It never edits the dictionary row in place: that row is
		// shared, and editing it would rewrite the provenance of every other
		// record in the batch.
		`CREATE TRIGGER sdn_record_source_tags_update
		 INSTEAD OF UPDATE ON ` + sourceTagsViewName + `
		 BEGIN
		   INSERT INTO ` + sourceProvenanceTable + ` (
		     provider_id, source_name, source_url, batch_id,
		     content_key_id, producer_peer_id, producer_public_key
		   )
		   SELECT NEW.provider_id, NEW.source_name, COALESCE(NEW.source_url, ''), COALESCE(NEW.batch_id, ''),
		          COALESCE(NEW.content_key_id, ''), COALESCE(NEW.producer_peer_id, ''), COALESCE(NEW.producer_public_key, '')
		   WHERE NOT EXISTS (
		     SELECT 1 FROM ` + sourceProvenanceTable + ` p
		     WHERE p.provider_id = NEW.provider_id
		       AND p.source_name = NEW.source_name
		       AND p.source_url = COALESCE(NEW.source_url, '')
		       AND p.batch_id = COALESCE(NEW.batch_id, '')
		       AND p.content_key_id = COALESCE(NEW.content_key_id, '')
		       AND p.producer_peer_id = COALESCE(NEW.producer_peer_id, '')
		       AND p.producer_public_key = COALESCE(NEW.producer_public_key, '')
		   );
		   INSERT INTO ` + sourceTagRowsTable + ` (schema_name, cid, provenance_id, created_at)
		   SELECT NEW.schema_name, NEW.cid, p.id, COALESCE(NEW.created_at, OLD.created_at, strftime('%s', 'now'))
		   FROM ` + sourceProvenanceTable + ` p
		   WHERE p.provider_id = NEW.provider_id
		     AND p.source_name = NEW.source_name
		     AND p.source_url = COALESCE(NEW.source_url, '')
		     AND p.batch_id = COALESCE(NEW.batch_id, '')
		     AND p.content_key_id = COALESCE(NEW.content_key_id, '')
		     AND p.producer_peer_id = COALESCE(NEW.producer_peer_id, '')
		     AND p.producer_public_key = COALESCE(NEW.producer_public_key, '')
		     AND NOT EXISTS (
		       SELECT 1 FROM ` + sourceTagRowsTable + ` r
		       WHERE r.schema_name = NEW.schema_name AND r.cid = NEW.cid AND r.provenance_id = p.id
		     );
		   DELETE FROM ` + sourceTagRowsTable + `
		   WHERE schema_name = OLD.schema_name AND cid = OLD.cid
		     AND provenance_id IN (
		       SELECT id FROM ` + sourceProvenanceTable + `
		       WHERE provider_id = OLD.provider_id AND source_name = OLD.source_name
		         AND source_url = COALESCE(OLD.source_url, '') AND batch_id = OLD.batch_id
		         AND content_key_id = OLD.content_key_id
		         AND producer_peer_id = OLD.producer_peer_id
		         AND producer_public_key = OLD.producer_public_key
		     )
		     AND NOT (
		       OLD.provider_id = NEW.provider_id AND OLD.source_name = NEW.source_name
		       AND COALESCE(OLD.source_url, '') = COALESCE(NEW.source_url, '')
		       AND OLD.batch_id = COALESCE(NEW.batch_id, '')
		       AND OLD.content_key_id = COALESCE(NEW.content_key_id, '')
		       AND OLD.producer_peer_id = COALESCE(NEW.producer_peer_id, '')
		       AND OLD.producer_public_key = COALESCE(NEW.producer_public_key, '')
		     );
		   UPDATE ` + sourceTagRowsTable + `
		   SET created_at = COALESCE(NEW.created_at, OLD.created_at, created_at)
		   WHERE schema_name = NEW.schema_name AND cid = NEW.cid
		     AND provenance_id IN (
		       SELECT id FROM ` + sourceProvenanceTable + `
		       WHERE provider_id = NEW.provider_id
		         AND source_name = NEW.source_name
		         AND source_url = COALESCE(NEW.source_url, '')
		         AND batch_id = COALESCE(NEW.batch_id, '')
		         AND content_key_id = COALESCE(NEW.content_key_id, '')
		         AND producer_peer_id = COALESCE(NEW.producer_peer_id, '')
		         AND producer_public_key = COALESCE(NEW.producer_public_key, '')
		     );
		 END`,
	}
	// DROP then CREATE, never CREATE IF NOT EXISTS: a store upgraded while an
	// earlier body was shipped must get the current one, and three DDL
	// statements at open cost nothing.
	for _, name := range []string{"insert", "delete", "update"} {
		if _, err := s.db.Exec(`DROP TRIGGER IF EXISTS ` + sourceTagsViewName + `_` + name); err != nil {
			return fmt.Errorf("replace %s write trigger: %w", sourceTagsViewName, err)
		}
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("create %s write trigger: %w", sourceTagsViewName, err)
		}
	}
	return nil
}

func (s *FlatSQLStore) viewExists(name string) (bool, error) {
	var found string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'view' AND name = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// provenanceIDTx interns tags and returns its dictionary id, creating the
// dictionary row on first sight. Cached per store.
func (s *FlatSQLStore) provenanceIDTx(exec sqlExecer, tags SourceTags) (int64, error) {
	key := provenanceKey(tags)
	if id, ok := s.provenance.get(key); ok {
		return id, nil
	}
	id, err := lookupProvenanceID(exec, tags)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		if _, err := exec.Exec(`
			INSERT OR IGNORE INTO `+sourceProvenanceTable+` (
				provider_id, source_name, source_url, batch_id,
				content_key_id, producer_peer_id, producer_public_key
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID,
			tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey); err != nil {
			return 0, fmt.Errorf("intern source provenance: %w", err)
		}
		if id, err = lookupProvenanceID(exec, tags); err != nil {
			return 0, err
		}
		if id == 0 {
			return 0, fmt.Errorf("interned source provenance for %q/%q did not come back", tags.ProviderID, tags.SourceName)
		}
	}
	s.provenance.put(key, id)
	return id, nil
}

type sqlQueryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func lookupProvenanceID(exec sqlExecer, tags SourceTags) (int64, error) {
	rower, ok := exec.(sqlQueryRower)
	if !ok {
		return 0, fmt.Errorf("source provenance lookup needs a QueryRow-capable executor (%T)", exec)
	}
	var id int64
	err := rower.QueryRow(`
		SELECT id FROM `+sourceProvenanceTable+`
		WHERE provider_id = ? AND source_name = ? AND source_url = ?
		  AND batch_id = ? AND content_key_id = ?
		  AND producer_peer_id = ? AND producer_public_key = ?
	`, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID,
		tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lookup source provenance: %w", err)
	}
	return id, nil
}

// provenanceIDsMatchingSQL is the sub-select every provider/source/batch
// predicate now goes through. It takes the same argument order the legacy
// column predicates took.
func provenanceIDsMatchingSQL(predicate string) string {
	return `SELECT id FROM ` + sourceProvenanceTable + ` WHERE ` + predicate
}

// migrateSourceTagsToProvenance rewrites a legacy physical
// sdn_record_source_tags table into the dictionary + slim rows + view, in
// bounded batches, and is a no-op once the view is in place.
//
// It is idempotent: it works off the legacy table's own rows, writes with
// INSERT OR IGNORE, and only drops the legacy table after the copy completes.
// An interrupted run resumes by re-copying rows it already copied, which the
// OR IGNORE makes free.
func (s *FlatSQLStore) migrateSourceTagsToProvenance() error {
	isTable, err := s.tableExists(sourceTagsViewName)
	if err != nil {
		return err
	}
	if !isTable {
		return nil
	}

	// NO UNBOUNDED STATEMENT RUNS HERE, and the rehearsal is why. The engine
	// gives ONE call five minutes on an uninterruptible dedicated thread and
	// POISONS itself past that — a poisoned engine is a node that answers
	// nothing until a human intervenes, on a fleet that installs the newest
	// signed sequence by itself. So there is no COUNT(*) for a progress line,
	// no DISTINCT over the whole table, and no unbounded INSERT ... SELECT:
	// every statement below carries a LIMIT, and every batch is timed so a box
	// that is slower than the rehearsal says so in its log instead of dying.
	log.Infof("Source-tag provenance migration: interning the legacy source-tag table — the seven provenance strings move to a dictionary and the per-record row becomes (schema, cid, provenance id)")
	started := time.Now()

	var copied int64
	var batches int
	lastSchema, lastCID := "", ""
	for {
		batchStarted := time.Now()

		// The dictionary is seeded from THIS BATCH's rows only, so the seed is
		// bounded by the same LIMIT the copy is.
		if _, err := s.db.Exec(`
			INSERT INTO `+sourceProvenanceTable+` (
				provider_id, source_name, source_url, batch_id,
				content_key_id, producer_peer_id, producer_public_key
			)
			SELECT DISTINCT t.provider_id, t.source_name, COALESCE(t.source_url, ''), t.batch_id,
			       t.content_key_id, t.producer_peer_id, t.producer_public_key
			FROM (
				SELECT provider_id, source_name, source_url, batch_id,
				       content_key_id, producer_peer_id, producer_public_key
				FROM `+sourceTagsViewName+`
				WHERE schema_name > ? OR (schema_name = ? AND cid > ?)
				ORDER BY schema_name, cid
				LIMIT ?
			) t
			WHERE NOT EXISTS (
				SELECT 1 FROM `+sourceProvenanceTable+` p
				WHERE p.provider_id = t.provider_id
				  AND p.source_name = t.source_name
				  AND p.source_url = COALESCE(t.source_url, '')
				  AND p.batch_id = t.batch_id
				  AND p.content_key_id = t.content_key_id
				  AND p.producer_peer_id = t.producer_peer_id
				  AND p.producer_public_key = t.producer_public_key
			)
		`, lastSchema, lastSchema, lastCID, sourceTagMigrationBatch); err != nil {
			return fmt.Errorf("intern legacy source provenance: %w", err)
		}

		result, err := s.db.Exec(`
			INSERT INTO `+sourceTagRowsTable+` (schema_name, cid, provenance_id, created_at)
			SELECT t.schema_name, t.cid, p.id, COALESCE(t.created_at, 0)
			FROM (
				SELECT schema_name, cid, provider_id, source_name, source_url, batch_id,
				       content_key_id, producer_peer_id, producer_public_key, created_at
				FROM `+sourceTagsViewName+`
				WHERE schema_name > ? OR (schema_name = ? AND cid > ?)
				ORDER BY schema_name, cid
				LIMIT ?
			) t
			JOIN `+sourceProvenanceTable+` p
			  ON p.provider_id = t.provider_id
			 AND p.source_name = t.source_name
			 AND p.source_url = COALESCE(t.source_url, '')
			 AND p.batch_id = t.batch_id
			 AND p.content_key_id = t.content_key_id
			 AND p.producer_peer_id = t.producer_peer_id
			 AND p.producer_public_key = t.producer_public_key
			WHERE NOT EXISTS (
				SELECT 1 FROM `+sourceTagRowsTable+` r
				WHERE r.schema_name = t.schema_name AND r.cid = t.cid AND r.provenance_id = p.id
			)
		`, lastSchema, lastSchema, lastCID, sourceTagMigrationBatch)
		if err != nil {
			return fmt.Errorf("copy legacy source tags: %w", err)
		}
		affected, _ := result.RowsAffected()
		copied += affected
		batches++

		// Advance the cursor to the LAST row this batch covered. Bounded by
		// the same LIMIT, so it is an index walk of one batch, not a scan.
		var nextSchema, nextCID string
		err = s.db.QueryRow(`
			SELECT schema_name, cid FROM (
				SELECT schema_name, cid FROM `+sourceTagsViewName+`
				WHERE schema_name > ? OR (schema_name = ? AND cid > ?)
				ORDER BY schema_name, cid
				LIMIT ?
			) ORDER BY schema_name DESC, cid DESC LIMIT 1
		`, lastSchema, lastSchema, lastCID, sourceTagMigrationBatch).Scan(&nextSchema, &nextCID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("advance legacy source tag cursor: %w", err)
		}
		if nextSchema == lastSchema && nextCID == lastCID {
			break
		}
		lastSchema, lastCID = nextSchema, nextCID

		elapsed := time.Since(batchStarted)
		if batches%10 == 0 || elapsed > 30*time.Second {
			log.Infof("Source-tag provenance migration: %d row(s) interned in %d batch(es), last batch %s",
				copied, batches, elapsed.Round(time.Millisecond))
		}
		if elapsed > sourceTagMigrationBatchBudget {
			// The engine kills a call at five minutes. A batch that is even
			// close says so, loudly, while the store is still usable.
			log.Warnf("Source-tag provenance migration: a %d-row batch took %s, which is within reach of the engine's 5m per-call limit — the next release should lower sourceTagMigrationBatch",
				sourceTagMigrationBatch, elapsed.Round(time.Millisecond))
		}
	}

	if _, err := s.db.Exec(`DROP TABLE ` + sourceTagsViewName); err != nil {
		return fmt.Errorf("drop legacy source tags table: %w", err)
	}
	if _, err := s.db.Exec(sourceTagsViewSQL()); err != nil {
		return fmt.Errorf("create %s view after migration: %w", sourceTagsViewName, err)
	}
	if err := s.ensureSourceTagsViewTriggers(); err != nil {
		return err
	}
	s.provenance.reset()
	log.Infof("Source-tag provenance migration complete: %d row(s) in %d batch(es), %s; the legacy table and its six indexes are gone",
		copied, batches, time.Since(started).Round(time.Millisecond))
	return nil
}

// replaceWithPartialIndex installs createSQL on a store that does not have the
// index yet, and REPORTS — never performs — the rebuild on a store that does.
//
// THE REBUILD IS NOT STARTUP WORK, AND THE REHEARSAL IS WHY. A first version
// of this dropped the full index and rebuilt it partial at open. Run against a
// COPY of host-02's real 10.2 GB control database, that boot spent 46 minutes
// inside the engine and then died:
//
//	module poisoned ("flatsql"): "(batch)" exceeded 5m0s
//	(dedicated thread, uninterruptible) — instance will be refused until replaced
//
// CREATE INDEX over millions of rows is ONE engine call and cannot be split,
// so it will always be able to blow the engine's wall-clock budget — and a
// poisoned engine is a node that answers nothing, on every boot, until a human
// intervenes. The fleet installs the newest signed sequence automatically, so
// shipping that would have taken every box that carries a large store.
//
// This follows the rule the full indexes already followed (createStartupIndex):
// an index that is not there yet on an existing table is maintenance work. A
// store created after this change gets partial indexes from birth and pays
// nothing; a store that already has the full ones keeps them, correct and
// slower to store, until maintenance rebuilds them.
func (s *FlatSQLStore) replaceWithPartialIndex(indexName, createSQL string, tableExisted bool) error {
	var existingSQL string
	err := s.db.QueryRow(`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&existingSQL)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if tableExisted {
			log.Warnf("Skipping synchronous startup creation of missing index %s; rebuild indexes during maintenance", indexName)
			return nil
		}
		_, err := s.db.Exec(createSQL)
		return err
	case err != nil:
		return err
	}
	if strings.Contains(strings.ToUpper(existingSQL), " WHERE ") {
		return nil
	}
	log.Infof("Index %s is still the FULL index: rebuilding it as partial would save 154.1 B per non-satellite record, but CREATE INDEX over this table is one uninterruptible engine call — it is maintenance work, not startup work", indexName)
	return nil
}

// RebuildPartialRecordIndexes is the MAINTENANCE half of replaceWithPartialIndex:
// it performs the drop-and-recreate that open deliberately refuses to do.
//
// It is exported so the maintenance lane — which already owns the operations
// that cost O(store) and are allowed to take as long as they take — can run it
// deliberately, on a box the operator has chosen, instead of on every boot of
// every box at once. Callers take the store write lock.
func (s *FlatSQLStore) RebuildPartialRecordIndexes() error {
	if err := s.requireWritable("rebuild partial record indexes"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, idx := range partialRecordIndexDefinitions() {
		var existingSQL string
		err := s.db.QueryRow(`SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name = ?`, idx.name).Scan(&existingSQL)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := s.db.Exec(idx.createSQL); err != nil {
				return fmt.Errorf("create partial index %s: %w", idx.name, err)
			}
			continue
		}
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToUpper(existingSQL), " WHERE ") {
			continue
		}
		started := time.Now()
		log.Infof("Maintenance: rebuilding %s as a partial index", idx.name)
		if _, err := s.db.Exec(`DROP INDEX IF EXISTS ` + idx.name); err != nil {
			return fmt.Errorf("drop full index %s: %w", idx.name, err)
		}
		if _, err := s.db.Exec(idx.createSQL); err != nil {
			return fmt.Errorf("create partial index %s: %w", idx.name, err)
		}
		log.Infof("Maintenance: rebuilt %s as a partial index in %s", idx.name, time.Since(started).Round(time.Millisecond))
	}
	return nil
}

// partialRecordIndexDefinition is one sdn_record_index secondary index and the
// statement that creates it.
type partialRecordIndexDefinition struct {
	name      string
	createSQL string
}

// partialRecordIndexDefinitions is the ONE place these five indexes are
// defined, so open and maintenance can never disagree about their shape.
//
// PARTIAL, AND FOR A MEASURED REASON. Five of these six indexes key on a
// column that only a SATELLITE record populates — norad_cat_id, entity_id,
// object_type, epoch_day, epoch_unix. A $TBS cell site, an $IRM mark, and every
// other non-satellite record leaves all of them NULL, and then paid 154.1 B PER
// RECORD (measured with `dbstat` over 20,000 real $TBS records) to be filed
// under an all-NULL key in five b-trees that can never be searched for it.
//
// `WHERE <column> IS NOT NULL` is the smallest possible statement of that: it
// is application-blind — no standard is named, nothing reads a payload — and it
// costs the satellite standards NOTHING, because a query that says
// `norad_cat_id = ?` implies `norad_cat_id IS NOT NULL` and SQLite's planner
// uses a partial index whose WHERE clause the query implies.
func partialRecordIndexDefinitions() []partialRecordIndexDefinition {
	return []partialRecordIndexDefinition{
		{"idx_sdn_record_index_lookup", `
			CREATE INDEX IF NOT EXISTS idx_sdn_record_index_lookup
			ON sdn_record_index (schema_name, epoch_day, norad_cat_id, entity_id, source_timestamp DESC)
			WHERE epoch_day IS NOT NULL
		`},
		{"idx_sdn_record_index_norad", `
			CREATE INDEX IF NOT EXISTS idx_sdn_record_index_norad
			ON sdn_record_index (schema_name, norad_cat_id, source_timestamp DESC)
			WHERE norad_cat_id IS NOT NULL
		`},
		{"idx_sdn_record_index_entity", `
			CREATE INDEX IF NOT EXISTS idx_sdn_record_index_entity
			ON sdn_record_index (schema_name, entity_id, source_timestamp DESC)
			WHERE entity_id IS NOT NULL
		`},
		{"idx_sdn_record_index_catalog_filters", `
			CREATE INDEX IF NOT EXISTS idx_sdn_record_index_catalog_filters
			ON sdn_record_index (schema_name, object_type, ops_status_code, norad_cat_id)
			WHERE object_type IS NOT NULL OR ops_status_code IS NOT NULL
		`},
		{"idx_sdn_record_index_time_window", `
			CREATE INDEX IF NOT EXISTS idx_sdn_record_index_time_window
			ON sdn_record_index (schema_name, epoch_unix, source_timestamp DESC)
			WHERE epoch_unix IS NOT NULL
		`},
	}
}
