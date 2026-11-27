package storage

import (
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// WithPrivateSchemas configures schemas that remain private to trusted
// capabilities and authenticated admins: excluded from PublicQuerySurface,
// refused by sandboxed public SQL queries (QuerySandboxedStream /
// QuerySandboxedJSON), and hidden from public catalog/source/index/summary/
// scan/query/record endpoints.
func WithPrivateSchemas(schemas []string) StoreOption {
	return func(c *storeConfig) {
		if len(schemas) == 0 {
			return
		}
		c.privateSchemas = append(c.privateSchemas, schemas...)
	}
}

func (s *FlatSQLStore) setPrivateSchemas(schemas []string) {
	if s == nil || len(schemas) == 0 {
		return
	}
	ps := make(map[string]bool, len(schemas))
	pt := make(map[string]bool, len(schemas))
	for _, raw := range schemas {
		norm := sds.NormalizeSchemaFileName(raw)
		if norm == "" {
			continue
		}
		ps[norm] = true
		code := strings.ToUpper(strings.TrimSuffix(norm, ".fbs"))
		if code != "" {
			pt[code] = true
		}
	}
	s.privateSchemas = ps
	s.privateTables = pt
}

// IsPrivateSchema reports whether schema (e.g. "AIS" or "AIS.fbs") is
// configured private on this store.
func (s *FlatSQLStore) IsPrivateSchema(schema string) bool {
	if s == nil || len(s.privateSchemas) == 0 {
		return false
	}
	norm := sds.NormalizeSchemaFileName(schema)
	if norm == "" {
		return false
	}
	return s.privateSchemas[norm]
}

// filterPublicQuerySurface removes any relation belonging to a private schema.
func (s *FlatSQLStore) filterPublicQuerySurface(surface []QuerySurfaceTable) []QuerySurfaceTable {
	if s == nil || len(s.privateTables) == 0 || len(surface) == 0 {
		return surface
	}
	out := make([]QuerySurfaceTable, 0, len(surface))
	for _, rel := range surface {
		base, _, _ := strings.Cut(rel.Name, "@")
		if s.privateTables[strings.ToUpper(strings.TrimSpace(base))] {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// checkSandboxedSQLPrivate rejects untrusted public SQL that references any
// private schema table or per-source shadow relation, returning the exact same
// SandboxCodeNotAuthorized error the in-WASM authorizer uses for hidden tables.
// It checks both token-level SQLite identifiers (including single-quoted table
// names such as FROM 'AIS' or main.'AIS') and SQLite's own resolved EXPLAIN
// QUERY PLAN table scan targets.
func (s *FlatSQLStore) checkSandboxedSQLPrivate(sqlText string, params ...interface{}) error {
	if s == nil || len(s.privateTables) == 0 {
		return nil
	}
	if sqlReferencesPrivateTable(sqlText, s.privateTables) {
		return &flatsqlrt.SandboxError{
			Code:    flatsqlrt.SandboxCodeNotAuthorized,
			Message: "sandbox: not-authorized: table is not on the public query surface",
		}
	}
	s.mu.RLock()
	db := s.engineDB
	var planDetails []string
	if db != nil {
		if res, err := db.Query("EXPLAIN QUERY PLAN "+sqlText, params...); err == nil && res != nil {
			for _, row := range res.Rows {
				for _, cell := range row {
					if str, ok := cell.(string); ok && str != "" {
						planDetails = append(planDetails, str)
					}
				}
			}
		}
	}
	s.mu.RUnlock()

	for _, detail := range planDetails {
		if explainDetailReferencesPrivateTable(detail, s.privateTables) {
			return &flatsqlrt.SandboxError{
				Code:    flatsqlrt.SandboxCodeNotAuthorized,
				Message: "sandbox: not-authorized: table is not on the public query surface",
			}
		}
	}
	return nil
}

func explainDetailReferencesPrivateTable(detail string, privateTables map[string]bool) bool {
	for _, rawTok := range strings.Fields(detail) {
		tok := strings.Trim(rawTok, `"'`+"`[](),;")
		if tok == "" {
			continue
		}
		if dot := strings.LastIndexByte(tok, '.'); dot >= 0 {
			tok = tok[dot+1:]
		}
		base, _, _ := strings.Cut(tok, "@")
		if privateTables[strings.ToUpper(base)] {
			return true
		}
	}
	return false
}

// sqlReferencesPrivateTable lexes SQLite tokens, skipping comments and blob
// literals, and checks whether any bare, double-quoted, backtick-quoted,
// bracket-quoted, or table-context single-quoted identifier names a private
// base table or "<TABLE>@<source>" shadow table.
func sqlReferencesPrivateTable(sqlText string, privateTables map[string]bool) bool {
	if len(privateTables) == 0 {
		return false
	}
	matchesPrivate := func(ident string) bool {
		ident = strings.TrimSpace(ident)
		if ident == "" {
			return false
		}
		if dot := strings.LastIndexByte(ident, '.'); dot >= 0 {
			ident = strings.TrimSpace(ident[dot+1:])
			ident = strings.Trim(ident, `"'`+"`[]")
		}
		base, _, _ := strings.Cut(ident, "@")
		return privateTables[strings.ToUpper(strings.TrimSpace(base))]
	}

	type parenFrame struct {
		inFromClause bool
		inOnOrUsing  bool
		atParenStart bool
	}
	var stack []parenFrame
	inFromClause := false
	inOnOrUsing := false
	atParenStart := false
	prevWasDotOrTableKw := false

	updateClauseState := func(upperWord string) {
		if atParenStart {
			atParenStart = false
			if upperWord == "SELECT" || upperWord == "VALUES" || upperWord == "WITH" {
				inFromClause = false
				inOnOrUsing = false
				prevWasDotOrTableKw = false
				return
			}
		}
		switch upperWord {
		case "FROM", "JOIN", "INTO", "UPDATE", "TABLE", "INDEXED", "PRAGMA":
			inFromClause = true
			inOnOrUsing = false
			prevWasDotOrTableKw = true
		case "ON", "USING":
			if inFromClause {
				inOnOrUsing = true
			}
			prevWasDotOrTableKw = false
		case "WHERE", "SELECT", "SET", "VALUES", "GROUP", "ORDER", "HAVING", "LIMIT", "WINDOW", "UNION", "EXCEPT", "INTERSECT":
			inFromClause = false
			inOnOrUsing = false
			prevWasDotOrTableKw = false
		case "AS", "NATURAL", "LEFT", "RIGHT", "INNER", "OUTER", "CROSS", "FULL":
			// Stay in current clause state.
		default:
			prevWasDotOrTableKw = false
		}
	}

	n := len(sqlText)
	i := 0
	for i < n {
		c := sqlText[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		// Line comment: -- ...\n
		if c == '-' && i+1 < n && sqlText[i+1] == '-' {
			i += 2
			for i < n && sqlText[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment: /* ... */
		if c == '/' && i+1 < n && sqlText[i+1] == '*' {
			i += 2
			for i+1 < n && !(sqlText[i] == '*' && sqlText[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}
			continue
		}
		// Blob literal X'...' / x'...'
		if (c == 'x' || c == 'X') && i+1 < n && sqlText[i+1] == '\'' {
			i += 2
			for i < n {
				if sqlText[i] == '\'' {
					i++
					if i < n && sqlText[i] == '\'' {
						i++
						continue
					}
					break
				}
				i++
			}
			prevWasDotOrTableKw = false
			continue
		}
		// Single-quoted literal '...' — SQLite also accepts single-quoted strings
		// as table names in FROM 'AIS', main.'AIS', JOIN 'AIS', etc.
		if c == '\'' {
			i++
			var buf strings.Builder
			for i < n {
				if sqlText[i] == '\'' {
					i++
					if i < n && sqlText[i] == '\'' {
						buf.WriteByte('\'')
						i++
						continue
					}
					break
				}
				buf.WriteByte(sqlText[i])
				i++
			}
			lit := buf.String()
			if ((inFromClause && !inOnOrUsing) || prevWasDotOrTableKw || strings.Contains(lit, "@")) && matchesPrivate(lit) {
				return true
			}
			atParenStart = false
			prevWasDotOrTableKw = false
			continue
		}
		// Double-quoted or backtick-quoted SQLite identifier ("..." or `...`)
		if c == '"' || c == '`' {
			quote := c
			i++
			var buf strings.Builder
			for i < n {
				if sqlText[i] == quote {
					i++
					if i < n && sqlText[i] == quote {
						buf.WriteByte(quote)
						i++
						continue
					}
					break
				}
				buf.WriteByte(sqlText[i])
				i++
			}
			if matchesPrivate(buf.String()) {
				return true
			}
			prevWasDotOrTableKw = false
			continue
		}
		// Bracket-quoted SQLite identifier ([...])
		if c == '[' {
			i++
			start := i
			for i < n && sqlText[i] != ']' {
				i++
			}
			ident := sqlText[start:i]
			if i < n {
				i++
			}
			if matchesPrivate(ident) {
				return true
			}
			prevWasDotOrTableKw = false
			continue
		}
		// Bare identifier or keyword
		if isSQLIdentStart(c) {
			start := i
			i++
			for i < n && isSQLIdentPart(sqlText[i]) {
				i++
			}
			word := sqlText[start:i]
			if matchesPrivate(word) {
				return true
			}
			updateClauseState(strings.ToUpper(word))
			continue
		}
		if c == '(' {
			stack = append(stack, parenFrame{
				inFromClause: inFromClause,
				inOnOrUsing:  inOnOrUsing,
				atParenStart: atParenStart,
			})
			if inFromClause && !inOnOrUsing {
				// Could be a parenthesized join-clause `FROM ('AIS')` or a subquery `FROM (SELECT 1)`.
				atParenStart = true
				prevWasDotOrTableKw = true
			} else {
				inFromClause = false
				inOnOrUsing = false
				atParenStart = true
				prevWasDotOrTableKw = false
			}
			i++
			continue
		}
		if c == ')' {
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				inFromClause = top.inFromClause
				inOnOrUsing = top.inOnOrUsing
				atParenStart = false
			} else {
				inFromClause = false
				inOnOrUsing = false
				atParenStart = false
			}
			prevWasDotOrTableKw = false
			i++
			continue
		}
		if c == '.' {
			prevWasDotOrTableKw = true
			i++
			continue
		}
		if c == ',' {
			if inFromClause {
				// A comma at the FROM-clause parenthesis depth ends any ON/USING clause
				// and introduces the next table in the FROM list.
				inOnOrUsing = false
				prevWasDotOrTableKw = true
			}
			i++
			continue
		}
		if c == '=' || c == '<' || c == '>' || c == '!' {
			prevWasDotOrTableKw = false
			i++
			continue
		}
		i++
	}
	return false
}

func isSQLIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 0x80
}

func isSQLIdentPart(c byte) bool {
	return isSQLIdentStart(c) || (c >= '0' && c <= '9') || c == '$' || c == '@'
}
