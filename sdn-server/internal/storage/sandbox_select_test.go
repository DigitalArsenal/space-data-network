package storage

import (
	"context"
	"strings"
	"testing"
)

// The sandbox refuses everything that is not one SELECT, before the engine
// runs anything.
func TestSandboxedSelectRefusals(t *testing.T) {
	s := &FlatSQLStore{} // guards fire before any store state is touched
	for _, tc := range []struct{ name, sql, wantErr string }{
		{"empty", "  ", "empty statement"},
		{"two statements", "SELECT 1; SELECT 2", "one statement only"},
		{"insert", "INSERT INTO OMM VALUES (1)", "SELECT statements only"},
		{"pragma smuggled", "SELECT * FROM OMM WHERE pragma_table_info('x')", `"pragma_table_info" is not allowed`},
		{"drop smuggled", "WITH x AS (SELECT 1) SELECT * FROM x; DROP TABLE OMM", "one statement only"},
		{"attach", "SELECT load_extension('x') FROM sqlite_master WHERE ATTACH", `"attach" is not allowed`},
		{"nil store select", "SELECT 1", "store is not open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SandboxedSelect(context.Background(), tc.sql, SandboxSelectCaps{})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
