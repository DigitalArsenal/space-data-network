package storage

// Full-text extraction/tokenization belongs to FlatSQL. This connector only
// maintains coverage of the durable record catalog, including cold records.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

var ErrSearchIndexBuilding = errors.New("Search index is building. Retry shortly.")

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
	definition, table, _, ok := EngineRelationSchemaText(schema)
	if !ok {
		return errors.New("Full-text search is unavailable for this schema")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte("record-text-v1\n"+definition)))
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
		state = &fullTextIndexState{schema: schema, table: table, fingerprint: fingerprint}
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
				_, err = tx.Exec(flatsqldrv.WithoutJournal(`INSERT OR REPLACE INTO sdn_record_fts(rowid,text) SELECT rowid,flatsql_record_text(?,?) FROM sdn_record_index WHERE rowid=? AND schema_name=? AND cid=?`), state.table, record.data, record.rowID, state.schema, record.cid)
				if err != nil {
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
	_, err := exec.Exec(flatsqldrv.WithoutJournal(`INSERT OR REPLACE INTO sdn_record_fts(rowid,text) SELECT rowid,flatsql_record_text(?,?) FROM sdn_record_index WHERE schema_name=? AND cid=?`), state.table, data, schema, cid)
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
