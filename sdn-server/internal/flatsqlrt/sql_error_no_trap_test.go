package flatsqlrt

import (
	"fmt"
	"strings"
	"testing"
)

// A SQL RUNTIME ERROR IS A RESULT, NEVER A TRAP.
//
// graph task mod-flatsql-query-params-unreachable-trap. host-01's
// record-catalog hydration aborted on every boot with
//
//	[error] execution failed: unreachable, Code: 0x40a
//	[error] calling stack:3351, 3351, 3351, 325, 192, 574, 3351
//	    When executing module name: "flatsql", function name: "flatsql_query_params"
//
// and had therefore NEVER finished hydrating its 1,344,427-frame catalog. The
// cause was not the journal, the row count, AOT, the guest bytecode or the
// WasmEdge pin (all measured clean): the engine artifact the servers execute,
// flatsql-wasi-noeh.wasm, is compiled `-fignore-exceptions`, so
// SQLiteEngine::execute's `throw std::runtime_error("SQL execution error: ...")`
// lowered to `unreachable`. The try/catch in flatsql_capi.cpp was dead code on
// that artifact. Any ordinary constraint violation reaching the guest — which
// live traffic writing to sdn_record_index during a replay can produce — aborted
// the guest and poisoned the whole engine.
//
// This fixture asserts the ABI contract directly, so the defect cannot come back
// through a different SQL error class or a different entry point: every
// host-reachable query entry must return an error and leave the engine USABLE.
func TestSQLErrorNeverTrapsTheGuest(t *testing.T) {
	rt, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	db, err := rt.CreateDatabase(ommTestSchema, "sql-error-no-trap")
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := db.Query(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Query(`INSERT INTO t (id, v) VALUES (?, ?)`, 1, "seed"); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	cases := []struct {
		name string
		want string
		run  func() error
	}{
		{
			// The host-01 class: the replay assigns explicit rowids into
			// sdn_record_index, so a rowid that collides with one live traffic
			// already wrote is a PRIMARY KEY violation on a bound INSERT.
			name: "flatsql_query_params: rowid/PRIMARY KEY collision",
			want: "UNIQUE constraint failed",
			run: func() error {
				_, err := db.Query(`INSERT INTO t (id, v) VALUES (?, ?)`, 1, "collide")
				return err
			},
		},
		{
			name: "flatsql_query_params: NOT NULL violation",
			want: "NOT NULL constraint failed",
			run: func() error {
				_, err := db.Query(`INSERT INTO t (id, v) VALUES (?, ?)`, 2, nil)
				return err
			},
		},
		{
			name: "flatsql_query_params: unknown table",
			want: "no such table",
			run: func() error {
				_, err := db.Query(`INSERT INTO missing_table (id) VALUES (?)`, 3)
				return err
			},
		},
		{
			name: "flatsql_query: unparameterized statement error",
			want: "no such table",
			run: func() error {
				_, err := db.Query(`INSERT INTO missing_table (id) VALUES (7)`)
				return err
			},
		},
		{
			name: "flatsql_query_many: error inside a batch",
			want: "UNIQUE constraint failed",
			run: func() error {
				_, err := db.QueryMany([]QueryRequest{
					{SQL: `INSERT INTO t (id, v) VALUES (?, ?)`, Params: []interface{}{10, "ok"}},
					{SQL: `INSERT INTO t (id, v) VALUES (?, ?)`, Params: []interface{}{10, "dup"}},
				})
				return err
			},
		},
		{
			name: "flatsql_query_template: error inside a registered template",
			want: "UNIQUE constraint failed",
			run: func() error {
				if err := db.RegisterQueryTemplate("dup-insert",
					`INSERT INTO t (id, v) VALUES (?, ?)`, false); err != nil {
					return fmt.Errorf("register: %w", err)
				}
				if _, err := db.QueryTemplate("dup-insert", 20, "first"); err != nil {
					return fmt.Errorf("first template run: %w", err)
				}
				_, err := db.QueryTemplate("dup-insert", 20, "second")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatalf("expected an error from %s", tc.name)
			}
			if strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), "poisoned") {
				t.Fatalf("GUEST TRAPPED on a plain SQL error (this is the defect): %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not carry the SQL diagnosis %q", err, tc.want)
			}
			if rt.Poisoned() {
				t.Fatalf("engine poisoned by a plain SQL error")
			}
			// The engine must still be USABLE afterwards — a trap would have
			// left it refused for every later caller.
			res, err := db.Query(`SELECT COUNT(*) FROM t`)
			if err != nil {
				t.Fatalf("engine unusable after a SQL error: %v", err)
			}
			if len(res.Rows) != 1 {
				t.Fatalf("SELECT COUNT(*) returned %d rows", len(res.Rows))
			}
		})
	}
}
