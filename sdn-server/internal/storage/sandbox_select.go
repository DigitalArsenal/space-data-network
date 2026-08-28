package storage

// Sandboxed read-only SELECT — the store half of POST /api/v1/query
// (docs Phase G.5). One statement, SELECT only, caller-supplied caps; rows
// come back as strings for transport. Lives in its own file: flatsql.go is
// under an active lock-accounting claim and this adds a reader, not a change
// to one.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SandboxSelectCaps bounds one sandboxed SELECT.
type SandboxSelectCaps struct {
	// MaxRows caps returned rows (0 = the 1000 default).
	MaxRows int
	// MaxBytes caps the accumulated cell bytes (0 = the 4 MiB default).
	MaxBytes int
	// Timeout bounds the statement (0 = the 5s default).
	Timeout time.Duration
}

// SandboxSelectResult is one query's answer.
type SandboxSelectResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
}

// forbidden inside a sandboxed statement even when it starts with SELECT.
var sandboxForbidden = regexp.MustCompile(`(?i)\b(pragma\w*|attach|detach|insert|update|delete|create|drop|alter|replace|vacuum|reindex)\b`)

// SandboxedSelect runs ONE read-only SELECT through the engine under the
// store read lock, with a statement timeout, row cap and byte cap. Anything
// that is not a single SELECT statement is refused before the engine sees it.
func (s *FlatSQLStore) SandboxedSelect(ctx context.Context, sql string, caps SandboxSelectCaps) (*SandboxSelectResult, error) {
	stmt := strings.TrimSpace(sql)
	stmt = strings.TrimSuffix(stmt, ";")
	if stmt == "" {
		return nil, fmt.Errorf("sandboxed select: empty statement")
	}
	if strings.Contains(stmt, ";") {
		return nil, fmt.Errorf("sandboxed select: one statement only")
	}
	upper := strings.ToUpper(stmt)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return nil, fmt.Errorf("sandboxed select: SELECT statements only")
	}
	if m := sandboxForbidden.FindString(stmt); m != "" {
		return nil, fmt.Errorf("sandboxed select: %q is not allowed", strings.ToLower(m))
	}

	maxRows := caps.MaxRows
	if maxRows <= 0 || maxRows > 1000 {
		maxRows = 1000
	}
	maxBytes := caps.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}
	timeout := caps.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if s.db == nil {
		return nil, fmt.Errorf("sandboxed select: store is not open")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("sandboxed select: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("sandboxed select: columns: %w", err)
	}

	out := &SandboxSelectResult{Columns: cols}
	bytesUsed := 0
	cells := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range cells {
		ptrs[i] = &cells[i]
	}
	for rows.Next() {
		if len(out.Rows) >= maxRows {
			out.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("sandboxed select: scan: %w", err)
		}
		row := make([]string, len(cols))
		for i, v := range cells {
			var cell string
			switch t := v.(type) {
			case nil:
				cell = ""
			case []byte:
				// BLOB columns (e.g. _data) travel as their size, not their
				// bytes — the record endpoints serve content; this surface
				// answers structure.
				cell = fmt.Sprintf("<%d bytes>", len(t))
			default:
				cell = fmt.Sprint(t)
			}
			bytesUsed += len(cell)
			row[i] = cell
		}
		if bytesUsed > maxBytes {
			out.Truncated = true
			break
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sandboxed select: %w", err)
	}
	return out, nil
}
