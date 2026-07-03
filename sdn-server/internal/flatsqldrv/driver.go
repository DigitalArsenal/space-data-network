// Package flatsqldrv exposes a FlatSQL-WASM engine database
// (internal/flatsqlrt) through database/sql, so existing SQLite-dialect code
// swaps engines without rewriting its SQL. The engine is in-memory; pair the
// driver with a StatementJournal for durable, replayable control-table state
// (docs/flatsql-store-v2.md).
//
// Concurrency: connections are stateless proxies onto one single-threaded
// engine; see Open for the pooling and serialization rules.
package flatsqldrv

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// Open wraps an engine database in a database/sql handle. If journal is
// non-nil, every committed mutating statement is appended to it (replayable
// at boot via Replay). The caller keeps ownership of db's lifetime.
//
// The pool allows multiple connections so callers may run a query while
// iterating another result set (a single-conn pool deadlocks there). All
// connections are stateless proxies onto the ONE engine SQLite context:
// every statement serializes on the engine lock, and a shared mutex keeps
// Exec + its last_insert_rowid()/changes() follow-ups atomic. Transactions
// are engine-global — callers must serialize writes themselves (the storage
// layer's RWMutex already does).
func Open(db *flatsqlrt.Database, journal *StatementJournal) *sql.DB {
	shared := &engineGate{}
	sqldb := sql.OpenDB(&connector{db: db, journal: journal, gate: shared})
	sqldb.SetMaxOpenConns(8)
	sqldb.SetMaxIdleConns(8)
	sqldb.SetConnMaxLifetime(0)
	return sqldb
}

// engineGate serializes exec+meta (and tx) sequences across connections.
type engineGate struct{ mu sync.Mutex }

type connector struct {
	db      *flatsqlrt.Database
	journal *StatementJournal
	gate    *engineGate
}

func (c *connector) Connect(context.Context) (driver.Conn, error) {
	return &conn{db: c.db, journal: c.journal, gate: c.gate}, nil
}

func (c *connector) Driver() driver.Driver { return dr{} }

type dr struct{}

func (dr) Open(string) (driver.Conn, error) {
	return nil, errors.New("flatsqldrv: use flatsqldrv.Open with an engine database")
}

type conn struct {
	db      *flatsqlrt.Database
	journal *StatementJournal
	gate    *engineGate
	inTx    bool
	// txBuf holds journal frames for the open transaction; they are flushed
	// on Commit and discarded on Rollback so replay never sees uncommitted
	// statements.
	txBuf []journalFrame
}

var (
	_ driver.Conn           = (*conn)(nil)
	_ driver.ExecerContext  = (*conn)(nil)
	_ driver.QueryerContext = (*conn)(nil)
	_ driver.ConnBeginTx    = (*conn)(nil)
)

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return &stmt{c: c, query: query}, nil
}

func (c *conn) Close() error { return nil }

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *conn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if c.inTx {
		return nil, errors.New("flatsqldrv: nested transactions not supported")
	}
	if _, err := c.db.Query("BEGIN"); err != nil {
		return nil, err
	}
	c.inTx = true
	c.txBuf = c.txBuf[:0]
	return &tx{c: c}, nil
}

type tx struct{ c *conn }

func (t *tx) Commit() error {
	if _, err := t.c.db.Query("COMMIT"); err != nil {
		return err
	}
	t.c.inTx = false
	if t.c.journal != nil && len(t.c.txBuf) > 0 {
		if err := t.c.journal.appendAll(t.c.txBuf); err != nil {
			return fmt.Errorf("flatsqldrv: journal append: %w", err)
		}
	}
	t.c.txBuf = nil
	return nil
}

func (t *tx) Rollback() error {
	_, err := t.c.db.Query("ROLLBACK")
	t.c.inTx = false
	t.c.txBuf = nil
	return err
}

// convertArgs maps database/sql values onto the engine's param types.
func convertArgs(args []driver.NamedValue) ([]interface{}, error) {
	out := make([]interface{}, len(args))
	for i, a := range args {
		if a.Name != "" {
			return nil, errors.New("flatsqldrv: named parameters not supported")
		}
		switch v := a.Value.(type) {
		case nil:
			out[i] = nil
		case bool, int64, float64, string, []byte:
			out[i] = v
		case time.Time:
			// Match mattn/go-sqlite3's primary timestamp format so stored
			// text stays comparable across the migration.
			out[i] = v.Format("2006-01-02 15:04:05.999999999-07:00")
		default:
			return nil, fmt.Errorf("flatsqldrv: unsupported arg type %T", a.Value)
		}
	}
	return out, nil
}

// isMutation decides whether a statement belongs in the journal.
func isMutation(query string) bool {
	q := strings.TrimSpace(query)
	for {
		if strings.HasPrefix(q, "--") {
			if idx := strings.IndexByte(q, '\n'); idx >= 0 {
				q = strings.TrimSpace(q[idx+1:])
				continue
			}
			return false
		}
		break
	}
	if len(q) < 3 {
		return false
	}
	head := strings.ToUpper(q[:min(12, len(q))])
	switch {
	case strings.HasPrefix(head, "SELECT"), strings.HasPrefix(head, "PRAGMA"),
		strings.HasPrefix(head, "EXPLAIN"), strings.HasPrefix(head, "BEGIN"),
		strings.HasPrefix(head, "COMMIT"), strings.HasPrefix(head, "ROLLBACK"):
		return false
	default:
		return true
	}
}

func (c *conn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	params, err := convertArgs(args)
	if err != nil {
		return nil, err
	}
	// Keep the statement and its metadata follow-ups atomic across pool
	// connections (last_insert_rowid()/changes() are engine-global state).
	c.gate.mu.Lock()
	defer c.gate.mu.Unlock()
	if _, err := c.db.Query(query, params...); err != nil {
		return nil, err
	}
	meta, err := c.db.Query("SELECT last_insert_rowid(), changes()")
	if err != nil {
		return nil, err
	}
	var lastID, affected int64
	if len(meta.Rows) == 1 && len(meta.Rows[0]) == 2 {
		lastID, _ = meta.Rows[0][0].(int64)
		affected, _ = meta.Rows[0][1].(int64)
	}

	if c.journal != nil && isMutation(query) {
		frame := journalFrame{SQL: query, Params: params}
		if c.inTx {
			c.txBuf = append(c.txBuf, frame)
		} else if err := c.journal.appendAll([]journalFrame{frame}); err != nil {
			return nil, fmt.Errorf("flatsqldrv: journal append: %w", err)
		}
	}
	return result{lastID: lastID, affected: affected}, nil
}

func (c *conn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	params, err := convertArgs(args)
	if err != nil {
		return nil, err
	}
	res, err := c.db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	return &rows{res: res}, nil
}

type stmt struct {
	c     *conn
	query string
}

func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 }

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.c.ExecContext(context.Background(), s.query, valuesToNamed(args))
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.c.QueryContext(context.Background(), s.query, valuesToNamed(args))
}

func valuesToNamed(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

type result struct{ lastID, affected int64 }

func (r result) LastInsertId() (int64, error) { return r.lastID, nil }
func (r result) RowsAffected() (int64, error) { return r.affected, nil }

type rows struct {
	res *flatsqlrt.Result
	pos int
}

func (r *rows) Columns() []string { return r.res.Columns }
func (r *rows) Close() error      { return nil }

func (r *rows) Next(dest []driver.Value) error {
	if r.pos >= len(r.res.Rows) {
		return io.EOF
	}
	row := r.res.Rows[r.pos]
	r.pos++
	for i := range dest {
		if i < len(row) {
			dest[i] = row[i]
		} else {
			dest[i] = nil
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
