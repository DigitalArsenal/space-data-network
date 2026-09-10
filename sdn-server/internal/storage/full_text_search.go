package storage

// Full-text extraction/tokenization belongs to FlatSQL. This connector only
// maintains coverage of the durable record catalog, including cold records.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var ErrSearchIndexBuilding = errors.New("Search index is building. Retry shortly.")

// Persisted text depends on the extractor's actual implementation as well as
// the schema. An engine upgrade must not reuse text derived by older code.
var fullTextEngineHash = sha256.Sum256(flatsqlrt.EmbeddedWasm())

// Stored records may be slices of compiler-emitted size-prefixed buffers.
// Restore the engine's stream frame before validation: the removed prefix was
// part of the original alignment of 64-bit fields. FlatSQL also accepts a
// transport frame around a buffer originally built without a size prefix.
func fullTextRecordFrame(data []byte) []byte {
	payload := engineRecordPayload(data)
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	return frame
}

// Search-box terms are literal text, never SQL or FTS operators. SQLite owns
// Unicode tokenization; every whitespace-delimited term must match.
func fullTextMatchExpression(search string) (string, error) {
	terms := strings.Fields(search)
	if len(search) > 1024 || len(terms) > 64 {
		return "", errors.New("Search is limited to 1024 bytes and 64 terms")
	}
	for i, term := range terms {
		terms[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(terms, " AND "), nil
}

// The caller holds s.mu, preventing a failed live write from racing a query
// between its initial readiness check and the actual index read.
func (s *FlatSQLStore) checkFullTextReadyLocked(filter RawRecordQuery) error {
	if strings.TrimSpace(filter.Search) == "" {
		return nil
	}
	state := s.fullTextState(filter.SchemaName)
	if state == nil {
		return ErrSearchIndexBuilding
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failure != nil {
		return fmt.Errorf("Search index unavailable: %w", state.failure)
	}
	if !state.ready || !s.RecordCatalogHydrated() {
		return ErrSearchIndexBuilding
	}
	return nil
}

type fullTextIndexState struct {
	mu                          sync.Mutex
	schema, table, fingerprint  string
	binarySchema                []byte
	initialized, running, ready bool
	failure                     error
	failedAt                    time.Time
	cancel                      context.CancelFunc
	indexed                     int64
	rebuild                     bool
}

// CheckFullTextSearch starts/resumes one dataset's derived index. Call before
// taking s.mu: the background worker needs the write lock in bounded windows.
func (s *FlatSQLStore) CheckFullTextSearch(schema, search string) error {
	if strings.TrimSpace(search) == "" {
		return nil
	}
	if _, err := fullTextMatchExpression(search); err != nil {
		return err
	}
	if s == nil {
		return errors.New("Record store is unavailable")
	}
	if !s.RecordCatalogHydrated() {
		return ErrSearchIndexBuilding
	}
	schema = normalizeSchemaNameForEpoch(schema)
	_, table, _, ok := EngineRelationSchemaText(schema)
	if !ok {
		return errors.New("Full-text search is unavailable for this schema")
	}
	binarySchema, available := sds.SearchSchema(schema)
	if !available {
		return errors.New("Complete search schema is unavailable")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("record-text-bfbs-framed-v1\n%x\n%x", fullTextEngineHash, sha256.Sum256(binarySchema)))))
	s.fullTextMu.Lock()
	defer s.fullTextMu.Unlock()
	if s.fullTextClosing {
		return errors.New("Record store is closing")
	}
	if s.fullTextStates == nil {
		s.fullTextStates = make(map[string]*fullTextIndexState)
		s.fullTextSlots = make(chan struct{}, 1)
	}
	state := s.fullTextStates[schema]
	if state == nil {
		state = &fullTextIndexState{schema: schema, table: table, fingerprint: fingerprint, binarySchema: binarySchema}
		s.fullTextStates[schema] = state
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.ready {
		return nil
	}
	if state.failure != nil && time.Since(state.failedAt) < 5*time.Second {
		return fmt.Errorf("Search index unavailable: %w", state.failure)
	}
	if !state.running {
		ctx, cancel := context.WithCancel(context.Background())
		state.cancel = cancel
		state.running = true
		state.failure = nil
		s.fullTextWorkers.Add(1)
		go s.buildFullTextIndex(ctx, state, s.fullTextSlots)
	}
	return ErrSearchIndexBuilding
}

func (s *FlatSQLStore) fullTextState(schema string) *fullTextIndexState {
	s.fullTextMu.Lock()
	defer s.fullTextMu.Unlock()
	return s.fullTextStates[normalizeSchemaNameForEpoch(schema)]
}

func (s *FlatSQLStore) stopFullTextIndexes() {
	s.fullTextMu.Lock()
	s.fullTextClosing = true
	for _, state := range s.fullTextStates {
		state.mu.Lock()
		if state.cancel != nil {
			state.cancel()
		}
		state.mu.Unlock()
	}
	s.fullTextMu.Unlock()
	s.fullTextWorkers.Wait()
}

func (s *FlatSQLStore) initializeFullTextIndex(state *fullTextIndexState) (int64, error) {
	defer s.lockWrite("initialize full-text index")()
	statements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS sdn_record_fts USING fts5(text, tokenize='unicode61 remove_diacritics 2')`,
		`CREATE TABLE IF NOT EXISTS sdn_record_fts_progress (schema_name TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, last_rowid INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS idx_sdn_record_fts_scan ON sdn_record_index(schema_name)`,
		`CREATE TRIGGER IF NOT EXISTS sdn_record_fts_delete AFTER DELETE ON sdn_record_index BEGIN DELETE FROM sdn_record_fts WHERE rowid=old.rowid; END`,
	}
	for _, statement := range statements {
		// Each entry is one complete SQLite statement. Query sends it directly
		// to SQLite; the compatibility Exec splitter cannot parse trigger bodies.
		rows, err := s.db.Query(flatsqldrv.WithoutJournal(statement))
		if err != nil {
			return 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
	}
	var fingerprint string
	var last int64
	err := s.db.QueryRow(`SELECT fingerprint,last_rowid FROM sdn_record_fts_progress WHERE schema_name=?`, state.schema).Scan(&fingerprint, &last)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	state.mu.Lock()
	rebuild := state.rebuild
	state.rebuild = false
	state.mu.Unlock()
	if fingerprint != state.fingerprint || rebuild {
		last = 0
	}
	if _, err = s.db.Exec(flatsqldrv.WithoutJournal(`INSERT INTO sdn_record_fts_progress(schema_name,fingerprint,last_rowid) VALUES(?,?,?) ON CONFLICT(schema_name) DO UPDATE SET fingerprint=excluded.fingerprint,last_rowid=excluded.last_rowid`), state.schema, state.fingerprint, last); err != nil {
		return 0, err
	}
	state.mu.Lock()
	state.initialized = true
	state.mu.Unlock()
	return last, nil
}

type fullTextRecord struct {
	rowID int64
	cid   string
	data  []byte
}

func (s *FlatSQLStore) fullTextRecordExists(schema string, record fullTextRecord) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found int
	err := s.db.QueryRow(`SELECT 1 FROM sdn_record_index WHERE rowid=? AND schema_name=? AND cid=?`, record.rowID, schema, record.cid).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *FlatSQLStore) fullTextWindow(schema string, after int64) ([]fullTextRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT rowid,cid FROM sdn_record_index INDEXED BY idx_sdn_record_fts_scan WHERE schema_name=? AND rowid>? ORDER BY rowid LIMIT 32`, schema, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []fullTextRecord
	for rows.Next() {
		var record fullTextRecord
		if err := rows.Scan(&record.rowID, &record.cid); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *FlatSQLStore) buildFullTextIndex(ctx context.Context, state *fullTextIndexState, slots chan struct{}) {
	defer s.fullTextWorkers.Done()
	var failure error
	defer func() {
		state.mu.Lock()
		state.running = false
		if failure != nil {
			state.failure = failure
			state.failedAt = time.Now()
			state.ready = false
		}
		state.mu.Unlock()
	}()
	// Several tables may be searched together. Only one archive scan per store
	// holds payloads at a time, leaving live reads and ingestion room to run.
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return
	}
	last, err := s.initializeFullTextIndex(state)
	if err != nil {
		failure = err
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		if !s.RecordCatalogHydrated() {
			failure = ErrSearchIndexBuilding
			return
		}
		window, err := s.fullTextWindow(state.schema, last)
		if err != nil {
			failure = err
			return
		}
		if len(window) == 0 {
			state.mu.Lock()
			state.ready = state.failure == nil
			state.mu.Unlock()
			return
		}
		payloadBytes := 0
		for i := range window {
			if ctx.Err() != nil {
				return
			}
			record, err := s.GetRecord(state.schema, window[i].cid)
			if err != nil {
				// Withdrawal can occur after the scan but before the payload read.
				exists, lookupErr := s.fullTextRecordExists(state.schema, window[i])
				if lookupErr == nil && !exists {
					continue
				}
				failure = err
				return
			}
			window[i].data = record.Data
			payloadBytes += len(record.Data)
			if payloadBytes >= 8<<20 {
				window = window[:i+1]
				break
			}
		}
		unlock := s.lockWrite("index full-text window")
		tx, err := s.db.Begin()
		if err == nil {
			for _, record := range window {
				if len(record.data) == 0 {
					continue
				}
				_, err = tx.Exec(flatsqldrv.WithoutJournal(`INSERT OR REPLACE INTO sdn_record_fts(rowid,text) SELECT rowid,flatsql_record_text(?,?,?) FROM sdn_record_index WHERE rowid=? AND schema_name=? AND cid=?`), state.table, fullTextRecordFrame(record.data), state.binarySchema, record.rowID, state.schema, record.cid)
				if err != nil {
					err = fmt.Errorf("index %s record %s: %w", state.schema, record.cid, err)
					break
				}
			}
			if err == nil {
				_, err = tx.Exec(flatsqldrv.WithoutJournal(`UPDATE sdn_record_fts_progress SET last_rowid=? WHERE schema_name=? AND fingerprint=?`), window[len(window)-1].rowID, state.schema, state.fingerprint)
			}
			if err != nil {
				_ = tx.Rollback()
			} else {
				err = tx.Commit()
			}
		}
		unlock()
		if err != nil {
			failure = err
			return
		}
		last = window[len(window)-1].rowID
		state.mu.Lock()
		state.indexed += int64(len(window))
		state.mu.Unlock()
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func upsertFullTextExec(exec sqlExecer, state *fullTextIndexState, schema, cid string, data []byte) error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	initialized := state.initialized
	state.mu.Unlock()
	if !initialized {
		return nil
	}
	_, err := exec.Exec(flatsqldrv.WithoutJournal(`INSERT OR REPLACE INTO sdn_record_fts(rowid,text) SELECT rowid,flatsql_record_text(?,?,?) FROM sdn_record_index WHERE schema_name=? AND cid=?`), state.table, fullTextRecordFrame(data), state.binarySchema, schema, cid)
	if err != nil {
		state.mu.Lock()
		state.ready = false
		state.rebuild = true
		state.failure = err
		state.failedAt = time.Now()
		state.mu.Unlock()
	}
	return err
}

// FullTextIndexState reports the derived search index for schema: "ready",
// "building", "failed", or "cold" when nothing has requested it since boot.
func (s *FlatSQLStore) FullTextIndexState(schema string) string {
	if s == nil {
		return "unavailable"
	}
	state := s.fullTextState(schema)
	if state == nil {
		return "cold"
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	switch {
	case state.ready:
		return "ready"
	case state.running:
		return "building"
	case state.failure != nil:
		return "failed"
	default:
		return "building"
	}
}

// schemasWithRecords lists the schema names present in the record index.
func (s *FlatSQLStore) schemasWithRecords() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query(`SELECT DISTINCT schema_name FROM sdn_record_index ORDER BY schema_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schemas []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		schemas = append(schemas, name)
	}
	return schemas, rows.Err()
}

// WarmFullTextIndexes starts the derived search index for every schema that
// has records and a complete search schema, so a cold node does not answer
// its first user search with SEARCH_INDEX_BUILDING (node-transfer tests,
// 2026-09-10, required change 5). Builds share the single existing slot and
// run in the background; the return names what was scheduled versus skipped.
func (s *FlatSQLStore) WarmFullTextIndexes() (scheduled, skipped []string, err error) {
	schemas, err := s.schemasWithRecords()
	if err != nil {
		return nil, nil, err
	}
	for _, schema := range schemas {
		if _, ok := sds.SearchSchema(normalizeSchemaNameForEpoch(schema)); !ok {
			skipped = append(skipped, schema)
			continue
		}
		switch err := s.CheckFullTextSearch(schema, "warm"); {
		case err == nil, errors.Is(err, ErrSearchIndexBuilding):
			scheduled = append(scheduled, schema)
		default:
			skipped = append(skipped, schema+": "+err.Error())
		}
	}
	return scheduled, skipped, nil
}

// WarmFullTextIndexesWhenHydrated polls until the record catalog is hydrated,
// then warms every searchable schema. It returns early when the store starts
// closing or ctx ends.
func (s *FlatSQLStore) WarmFullTextIndexesWhenHydrated(ctx context.Context, poll time.Duration) (scheduled, skipped []string, err error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for !s.RecordCatalogHydrated() {
		s.fullTextMu.Lock()
		closing := s.fullTextClosing
		s.fullTextMu.Unlock()
		if closing {
			return nil, nil, errors.New("record store is closing")
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-ticker.C:
		}
	}
	return s.WarmFullTextIndexes()
}
