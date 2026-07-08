// Package flatsqldrv exposes a FlatSQL-WASM engine database
// (internal/flatsqlrt) through database/sql, so existing SQLite-dialect code
// swaps engines without rewriting its SQL. The engine is in-memory; callers
// that need durable state must persist domain records directly instead of
// replaying SQL statements.
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

// WithoutJournal is kept as a compatibility no-op for older storage call
// sites. The FlatSQL driver no longer records SQL statements.
func WithoutJournal(query string) string {
	return query
}

// Open wraps an engine database in a database/sql handle. The caller keeps
// ownership of db's lifetime.
//
// The pool allows multiple connections so callers may run a query while
// iterating another result set (a single-conn pool deadlocks there). All
// connections are stateless proxies onto the ONE engine SQLite context:
// every statement serializes on the engine lock, and a shared mutex keeps
// Exec + its last_insert_rowid()/changes() follow-ups atomic. Transactions
// are engine-global — callers must serialize writes themselves (the storage
// layer's RWMutex already does).
func Open(db *flatsqlrt.Database) *sql.DB {
	shared := &engineGate{}
	sqldb := sql.OpenDB(&connector{db: db, gate: shared})
	sqldb.SetMaxOpenConns(8)
	sqldb.SetMaxIdleConns(8)
	sqldb.SetConnMaxLifetime(0)
	return sqldb
}

// engineGate serializes exec+meta (and tx) sequences across connections.
type engineGate struct{ mu sync.Mutex }

type connector struct {
	db   *flatsqlrt.Database
	gate *engineGate
}

func (c *connector) Connect(context.Context) (driver.Conn, error) {
	return &conn{db: c.db, gate: c.gate}, nil
}

func (c *connector) Driver() driver.Driver { return dr{} }

type dr struct{}

func (dr) Open(string) (driver.Conn, error) {
	return nil, errors.New("flatsqldrv: use flatsqldrv.Open with an engine database")
}

type conn struct {
	db   *flatsqlrt.Database
	gate *engineGate
	inTx bool
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
	return &tx{c: c}, nil
}

type tx struct{ c *conn }

func (t *tx) Commit() error {
	if _, err := t.c.db.Query("COMMIT"); err != nil {
		return err
	}
	t.c.inTx = false
	return nil
}

func (t *tx) Rollback() error {
	_, err := t.c.db.Query("ROLLBACK")
	t.c.inTx = false
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

// splitStatements breaks a multi-statement SQL string into individual
// statements (quote-, comment-, and trigger-unaware-but-sufficient: the
// engine's flatsql_query executes ONE statement, while database/sql callers
// legitimately Exec multi-statement DDL blocks the way sqlite3_exec allows).
// Statements containing bind parameters must be single (params bind to one
// statement only) — callers already follow that convention.
func splitStatements(query string) []string {
	var stmts []string
	var b strings.Builder
	i := 0
	n := len(query)
	for i < n {
		ch := query[i]
		switch {
		case ch == '\'' || ch == '"' || ch == '`':
			quote := ch
			b.WriteByte(ch)
			i++
			for i < n {
				b.WriteByte(query[i])
				if query[i] == quote {
					if i+1 < n && query[i+1] == quote { // escaped quote
						i++
						b.WriteByte(query[i])
						i++
						continue
					}
					i++
					break
				}
				i++
			}
		case ch == '-' && i+1 < n && query[i+1] == '-':
			for i < n && query[i] != '\n' {
				b.WriteByte(query[i])
				i++
			}
		case ch == '/' && i+1 < n && query[i+1] == '*':
			for i < n {
				b.WriteByte(query[i])
				if query[i] == '*' && i+1 < n && query[i+1] == '/' {
					b.WriteByte(query[i+1])
					i += 2
					break
				}
				i++
			}
		case ch == ';':
			if s := strings.TrimSpace(b.String()); s != "" {
				stmts = append(stmts, s)
			}
			b.Reset()
			i++
		default:
			b.WriteByte(ch)
			i++
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
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

	stmts := splitStatements(query)
	if len(stmts) > 1 && len(params) > 0 {
		return nil, errors.New("flatsqldrv: bind parameters not supported in multi-statement Exec")
	}
	if len(stmts) <= 1 {
		if _, err := c.db.Query(query, params...); err != nil {
			return nil, err
		}
	} else {
		for _, stmt := range stmts {
			if _, err := c.db.Query(stmt); err != nil {
				return nil, fmt.Errorf("%w (in multi-statement exec: %.60q)", err, stmt)
			}
		}
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
