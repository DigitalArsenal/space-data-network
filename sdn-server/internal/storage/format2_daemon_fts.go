package storage

// format2_daemon_fts.go — the interim full-text index on store format 2
// (design A6, T6 scope 7), until T8 moves text search into FlatSQL.
//
// ITS OWN INSTANCE AND FILE (A6). sdn_record_fts (FTS5) and
// sdn_record_fts_progress live in fts.flatsqldb, on a legacy engine runtime
// of their own with their own lock: the control instance holds only control
// tables, so an identity, directory or EPM read never waits on an index
// window. store-migrate writes the file from the legacy index.
//
// KEYED BY GSEQ. The legacy FTS rowid was the sdn_record_index rowid;
// store-migrate keeps every legacy rowid as the record's gseq (flatsql 3.2.0
// migrated_gseq), and gseqs come from one store-global counter (A6), so a
// migrated index is valid as it stands and a new record's text lands under
// its own gseq without colliding with another type's.
//
// THE FEED follows each searched type's arrivals in gseq order past its
// progress mark (a datasync page on a reader lane), extracts the text with
// the FTS instance's flatsql_record_text, and writes it in bounded windows
// (256 records or 8 MiB of payload, one transaction each: the index is
// derived data on its own lock, so a window amortizes its commit's fsync
// instead of keeping a control read short). Once it has caught
// up the type is "ready"; it then polls the type head for new arrivals. A
// record that died keeps its text row: a search reads hits in gseq order and
// keeps only the gseqs the type still holds live (A6 search paging), so a
// dead hit is never served.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// format2FTSDBName is the full-text index's own database (A6).
const format2FTSDBName = "fts.flatsqldb"

// format2FTSWindow is the records one index window covers.
const format2FTSWindow = 256

// format2FTSPoll is how often a caught-up feed looks for new arrivals.
var format2FTSPoll = 2 * time.Second

type format2FTS struct {
	mu      sync.Mutex
	states  map[string]*f2FTSState
	wg      sync.WaitGroup
	closing bool
	slot    chan struct{} // one feed extracts at a time (the FTS instance is one engine)

	// The FTS instance: its runtime, database and lock (a window holds it
	// for writing, a search for reading). Never s.mu.
	dbMu   sync.RWMutex
	engine *flatsqlrt.Runtime
	edb    *flatsqlrt.Database
	db     *sql.DB
	path   string
	root   string
}

// openFTSInstance opens (creating) fts.flatsqldb on a runtime of its own,
// TRUNCATE. Caller holds f.dbMu for writing.
func (f *format2FTS) openFTSInstanceLocked() error {
	if err := checkDatabaseFile(f.path); err != nil {
		return err
	}
	engine, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()), flatsqlrt.WithFileIORoot(f.root))
	if err != nil {
		return fmt.Errorf("full-text instance: %w", err)
	}
	edb, err := engine.OpenDatabase(engineDatabaseSchema, "sdn-fts", f.path, flatsqlrt.JournalTruncate)
	if err != nil {
		engine.Close()
		return fmt.Errorf("full-text instance: open %s: %w", f.path, err)
	}
	if err := registerEngineFileIDs(edb, nil); err != nil {
		edb.Destroy()
		engine.Close()
		return fmt.Errorf("full-text instance: %w", err)
	}
	f.engine, f.edb, f.db = engine, edb, flatsqldrv.Open(edb)
	return nil
}

func (f *format2FTS) closeFTSInstanceLocked() {
	if f.db != nil {
		_ = f.db.Close()
		f.db = nil
	}
	if f.edb != nil {
		f.edb.Destroy()
		f.edb = nil
	}
	if f.engine != nil {
		f.engine.Close()
		f.engine = nil
	}
}

// ftsDB returns the FTS instance's database, reopening a poisoned runtime
// (its own poison domain: the control instance and the partition store are
// untouched). Caller holds f.dbMu for writing.
func (f *format2FTS) ftsDBLocked() (*sql.DB, error) {
	if f.engine != nil && f.engine.Poisoned() {
		log.Warnf("format 2: the full-text instance was poisoned; reopening %s", f.path)
		f.engine.FileIO().CloseAll()
		f.closeFTSInstanceLocked()
	}
	if f.db == nil {
		if err := f.openFTSInstanceLocked(); err != nil {
			return nil, err
		}
	}
	return f.db, nil
}

type f2FTSState struct {
	mu                         sync.Mutex
	schema, table, fingerprint string
	bfbs                       []byte
	running, ready             bool
	failure                    error
	failedAt                   time.Time
	indexed, last              int64
}

func (s *FlatSQLStore) startFormat2FTS() {
	s.f2.fts = &format2FTS{states: map[string]*f2FTSState{}, slot: make(chan struct{}, 1),
		path: filepath.Join(s.basePath, format2FTSDBName), root: s.basePath}
}

func (s *FlatSQLStore) stopFormat2FTS() {
	if s.f2 == nil || s.f2.fts == nil {
		return
	}
	f := s.f2.fts
	f.mu.Lock()
	f.closing = true
	f.mu.Unlock()
	if s.f2.cancel != nil {
		s.f2.cancel()
	}
	f.wg.Wait()
	f.dbMu.Lock()
	f.closeFTSInstanceLocked()
	f.dbMu.Unlock()
}

// f2CheckFullTextSearch is CheckFullTextSearch on format 2: it starts the
// type's feed and answers ErrSearchIndexBuilding until the feed has caught up.
func (s *FlatSQLStore) f2CheckFullTextSearch(schema, search string) error {
	if strings.TrimSpace(search) == "" {
		return nil
	}
	if _, err := fullTextMatchExpression(search); err != nil {
		return err
	}
	schema = normalizeSchemaNameForEpoch(schema)
	_, table, _, ok := EngineRelationSchemaText(schema)
	if !ok {
		return errors.New("Full-text search is unavailable for this schema")
	}
	bfbs, available := sds.SearchSchema(schema)
	if !available {
		return errors.New("Complete search schema is unavailable")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("record-text-bfbs-framed-v1\n%x\n%x", fullTextEngineHash, sha256.Sum256(bfbs)))))
	f := s.f2.fts
	if f == nil {
		return errors.New("Record store is unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		return errors.New("Record store is closing")
	}
	st := f.states[schema]
	if st == nil {
		st = &f2FTSState{schema: schema, table: table, fingerprint: fingerprint, bfbs: bfbs}
		f.states[schema] = st
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ready {
		return nil
	}
	if st.failure != nil && time.Since(st.failedAt) < 5*time.Second {
		return fmt.Errorf("Search index unavailable: %w", st.failure)
	}
	if !st.running {
		st.running = true
		st.failure = nil
		f.wg.Add(1)
		go s.runFormat2FTSFeed(st)
	}
	return ErrSearchIndexBuilding
}

func (s *FlatSQLStore) f2FullTextIndexState(schema string) string {
	f := s.f2.fts
	if f == nil {
		return "unavailable"
	}
	f.mu.Lock()
	st := f.states[normalizeSchemaNameForEpoch(schema)]
	f.mu.Unlock()
	if st == nil {
		return "cold"
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.ready:
		return "ready"
	case st.running:
		return "building"
	case st.failure != nil:
		return "failed"
	}
	return "building"
}

func (s *FlatSQLStore) f2WarmFullTextIndexes() (scheduled, skipped []string, err error) {
	types, err := s.ps.Types(s.f2ctx())
	if err != nil {
		return nil, nil, err
	}
	var schemas []string
	for _, t := range types {
		if t.FirstLive > 0 {
			schemas = append(schemas, t.Schema)
		}
	}
	sort.Strings(schemas)
	for _, schema := range schemas {
		if _, ok := sds.SearchSchema(normalizeSchemaNameForEpoch(schema)); !ok {
			skipped = append(skipped, schema)
			continue
		}
		switch err := s.f2CheckFullTextSearch(schema, "warm"); {
		case err == nil, errors.Is(err, ErrSearchIndexBuilding):
			scheduled = append(scheduled, schema)
		default:
			skipped = append(skipped, schema+": "+err.Error())
		}
	}
	return scheduled, skipped, nil
}

func (s *FlatSQLStore) f2InitFTS(st *f2FTSState) (int64, error) {
	f := s.f2.fts
	f.dbMu.Lock()
	defer f.dbMu.Unlock()
	db, err := f.ftsDBLocked()
	if err != nil {
		return 0, err
	}
	for _, statement := range []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS sdn_record_fts USING fts5(text, tokenize='unicode61 remove_diacritics 2')`,
		`CREATE TABLE IF NOT EXISTS sdn_record_fts_progress (schema_name TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, last_rowid INTEGER NOT NULL DEFAULT 0)`,
	} {
		rows, err := db.Query(flatsqldrv.WithoutJournal(statement))
		if err != nil {
			return 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
	}
	var fingerprint string
	var last int64
	err = db.QueryRow(`SELECT fingerprint, last_rowid FROM sdn_record_fts_progress WHERE schema_name = ?`, st.schema).Scan(&fingerprint, &last)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if fingerprint != st.fingerprint {
		last = 0
	}
	if _, err := db.Exec(flatsqldrv.WithoutJournal(`INSERT INTO sdn_record_fts_progress(schema_name, fingerprint, last_rowid) VALUES(?, ?, ?)
		ON CONFLICT(schema_name) DO UPDATE SET fingerprint = excluded.fingerprint, last_rowid = excluded.last_rowid`),
		st.schema, st.fingerprint, last); err != nil {
		return 0, err
	}
	return last, nil
}

// runFormat2FTSFeed indexes one type until Close.
func (s *FlatSQLStore) runFormat2FTSFeed(st *f2FTSState) {
	f := s.f2.fts
	defer f.wg.Done()
	ctx := s.f2ctx()
	var failure error
	defer func() {
		st.mu.Lock()
		st.running = false
		if failure != nil && ctx.Err() == nil {
			st.failure = failure
			st.failedAt = time.Now()
			st.ready = false
			log.Warnf("format 2: full-text feed for %s failed: %v", st.schema, failure)
		}
		st.mu.Unlock()
	}()
	last, err := s.f2InitFTS(st)
	if err != nil {
		failure = err
		return
	}
	for ctx.Err() == nil {
		n, next, err := s.f2FTSWindow(ctx, st, last)
		if err != nil {
			failure = err
			return
		}
		if n > 0 {
			last = next
			st.mu.Lock()
			st.indexed += int64(n)
			st.last = last
			st.mu.Unlock()
			continue
		}
		st.mu.Lock()
		st.ready = true
		st.last = last
		st.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(format2FTSPoll):
		}
	}
}

// f2FTSWindow indexes the next window of arrivals past last. It returns how
// many records it covered and the new mark.
func (s *FlatSQLStore) f2FTSWindow(ctx context.Context, st *f2FTSState, last int64) (int, int64, error) {
	f := s.f2.fts
	select {
	case f.slot <- struct{}{}:
		defer func() { <-f.slot }()
	case <-ctx.Done():
		return 0, last, nil
	}
	recs, _, err := s.ps.SyncPage(ctx, format2.SyncQuery{Schema: st.schema, AfterGseq: last, Limit: format2FTSWindow})
	if err != nil {
		return 0, last, err
	}
	if len(recs) == 0 {
		return 0, last, nil
	}
	payload := 0
	for i, r := range recs {
		payload += len(r.Data)
		if payload >= 8<<20 {
			recs = recs[:i+1]
			break
		}
	}
	f.dbMu.Lock()
	defer f.dbMu.Unlock()
	db, err := f.ftsDBLocked()
	if err != nil {
		return 0, last, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, last, err
	}
	for _, r := range recs {
		plain, err := s.openStoredRecordBytes(st.schema, r.Data)
		if err != nil || len(plain) == 0 {
			continue
		}
		if _, err := tx.Exec(flatsqldrv.WithoutJournal(`INSERT OR REPLACE INTO sdn_record_fts(rowid, text) VALUES(?, flatsql_record_text(?, ?, ?))`),
			r.Gseq, st.table, fullTextRecordFrame(plain), st.bfbs); err != nil {
			_ = tx.Rollback()
			return 0, last, fmt.Errorf("index %s record %s: %w", st.schema, r.CID, err)
		}
	}
	next := recs[len(recs)-1].Gseq
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`UPDATE sdn_record_fts_progress SET last_rowid = ? WHERE schema_name = ? AND fingerprint = ?`),
		next, st.schema, st.fingerprint); err != nil {
		_ = tx.Rollback()
		return 0, last, err
	}
	if err := tx.Commit(); err != nil {
		return 0, last, err
	}
	return len(recs), next, nil
}

// f2SearchReady answers the readiness of a search (legacy
// checkFullTextReadyLocked).
func (s *FlatSQLStore) f2SearchReady(schema string) error {
	if err := s.f2CheckFullTextSearch(schema, "ready"); err != nil {
		return err
	}
	return nil
}

// f2SearchHits reads FTS hits in gseq order past after (and at or below
// maxGseq when > 0), limit rowids per call.
func (s *FlatSQLStore) f2SearchHits(match string, after, maxGseq int64, limit int) ([]int64, error) {
	query := `SELECT rowid FROM sdn_record_fts WHERE text MATCH ? AND rowid > ?`
	args := []any{match, after}
	if maxGseq > 0 {
		query += ` AND rowid <= ?`
		args = append(args, maxGseq)
	}
	query += fmt.Sprintf(` ORDER BY rowid LIMIT %d`, limit)
	f := s.f2.fts
	f.dbMu.RLock()
	defer f.dbMu.RUnlock()
	if f.db == nil {
		return nil, ErrSearchIndexBuilding
	}
	rows, err := f.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var g int64
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// f2SearchRawRecords serves a search page: FTS hits in gseq order, kept only
// where the type holds them live and the page's other conditions hold.
func (s *FlatSQLStore) f2SearchRawRecords(ctx context.Context, filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	if err := s.f2SearchReady(filter.SchemaName); err != nil {
		return nil, err
	}
	match, err := fullTextMatchExpression(filter.Search)
	if err != nil {
		return nil, err
	}
	f, err := s.f2CompileRawFilter(ctx, filter, false)
	if err != nil {
		return nil, err
	}
	if f.nothing {
		return nil, nil
	}
	// Search reads at type level: tag conditions on the FIRST copy's tags.
	f = f.on(f2Target{tag: f2TagSpec{provider: f.tag.provider, source: f.tag.source, batch: f.tag.batch, peer: f.tag.peer}})
	after := int64(0)
	if filter.UseRowIDCursor {
		after = filter.AfterRowID
	}
	skip := 0
	if !filter.UseRowIDCursor {
		skip = filter.Offset
	}
	var out []*Record
	for len(out) < filter.Limit {
		hits, err := s.f2SearchHits(match, after, filter.MaxRowID, 512)
		if err != nil {
			return nil, err
		}
		if len(hits) == 0 {
			break
		}
		after = hits[len(hits)-1]
		recs, err := s.f2RecordsAtGseqs(filter.SchemaName, hits, f, hydrate)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if skip > 0 {
				skip--
				continue
			}
			out = append(out, r)
			if len(out) >= filter.Limit {
				break
			}
		}
	}
	return out, nil
}

// f2RecordsAtGseqs reads the live records of a type at the given gseqs that
// meet f, in gseq order.
func (s *FlatSQLStore) f2RecordsAtGseqs(schemaName string, gseqs []int64, f *f2RawFilter, hydrate bool) ([]*Record, error) {
	g := &f2RawFilter{conds: append([]string(nil), f.conds...), params: append([]format2.Cell(nil), f.params...)}
	var marks []string
	var params []format2.Cell
	for _, v := range gseqs {
		marks = append(marks, "?")
		params = append(params, format2.Int(v))
	}
	g.add("_gseq IN ("+strings.Join(marks, ",")+")", params...)
	return s.f2SelectPoint(schemaName, fmt.Sprintf("SELECT %s FROM %s%s ORDER BY _gseq", format2.RecColumns,
		format2.QuoteIdent(format2.TypeName(schemaName)), g.where()), g.params, hydrate)
}

// f2SearchCount counts the live hits (exact: every hit is checked against
// the type).
func (s *FlatSQLStore) f2SearchCount(ctx context.Context, filter RawRecordQuery) (int64, error) {
	if err := s.f2SearchReady(filter.SchemaName); err != nil {
		return 0, err
	}
	match, err := fullTextMatchExpression(filter.Search)
	if err != nil {
		return 0, err
	}
	f, err := s.f2CompileRawFilter(ctx, filter, false)
	if err != nil {
		return 0, err
	}
	if f.nothing {
		return 0, nil
	}
	f = f.on(f2Target{tag: f2TagSpec{provider: f.tag.provider, source: f.tag.source, batch: f.tag.batch, peer: f.tag.peer}})
	var total int64
	after := int64(0)
	for {
		hits, err := s.f2SearchHits(match, after, filter.MaxRowID, 512)
		if err != nil {
			return 0, err
		}
		if len(hits) == 0 {
			return total, nil
		}
		after = hits[len(hits)-1]
		g := &f2RawFilter{conds: append([]string(nil), f.conds...), params: append([]format2.Cell(nil), f.params...)}
		var marks []string
		var params []format2.Cell
		for _, v := range hits {
			marks = append(marks, "?")
			params = append(params, format2.Int(v))
		}
		g.add("_gseq IN ("+strings.Join(marks, ",")+")", params...)
		res, err := s.ps.Query(ctx, format2.Request{SQL: fmt.Sprintf("SELECT COUNT(*) FROM %s%s",
			format2.QuoteIdent(format2.TypeName(filter.SchemaName)), g.where()), Params: g.params})
		if format2.NoSuchType(err, filter.SchemaName) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		total += res.Rows[0][0].Int64()
	}
}
