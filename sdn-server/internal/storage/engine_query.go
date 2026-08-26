package storage

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// QueryRawStream executes SQL whose result cells are all BLOBs (e.g.
// `SELECT _data FROM "OMM@catalogfixture-gp" WHERE ...`) inside the engine and
// returns the aligned size-prefixed FlatBuffer stream — the wire format.
// This is the generic engine query surface for the module hostcall bridge
// and the retrieval module (loop C.1).
func (s *FlatSQLStore) QueryRawStream(sql string, params ...interface{}) (*flatsqlrt.RawStream, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, fmt.Errorf("engine database not available")
	}
	return s.engineDB.QueryRawFlatBufferStream(sql, params...)
}

// ResponseArtifactCacheKey returns the engine's deterministic cache key for
// a query response artifact (the ETag identity for conditional GET).
func (s *FlatSQLStore) ResponseArtifactCacheKey(schemaName, schemaVersion, sql string, opts flatsqlrt.ResponseArtifactKeyOptions) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engine == nil {
		return "", fmt.Errorf("engine not available")
	}
	return s.engine.BuildResponseArtifactCacheKey(schemaName, schemaVersion, sql, opts)
}

// ==================== Sandboxed public query (gateway loop G.5) ====================

// QuerySandboxedStream executes ONE untrusted read-only SELECT whose every
// result cell is a BLOB (the public /api/v1/query fb path) under the
// engine's in-wasm sandbox: authorizer-restricted to the record vtabs /
// shadow tables / unified views (control tables invisible), single
// statement, statement timeout, row/byte caps. This is the C.8b read-only
// discipline applied per-statement to the LIVE engine session — writes are
// structurally impossible (typed *flatsqlrt.SandboxError), no second store
// open, no cache interaction.
func (s *FlatSQLStore) QuerySandboxedStream(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (*flatsqlrt.RawStream, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, fmt.Errorf("engine database not available")
	}
	return s.engineDB.QuerySandboxedStream(sql, caps, params...)
}

// QuerySandboxedJSON is QuerySandboxedStream's tabular sibling: the engine
// assembles a bare JSON array of {"<column>": value} objects IN-WASM with
// column names verbatim from SQLite — schema-exact key capitalization
// (NORAD_CAT_ID, MEAN_MOTION, ...) is structural, never re-spelled by hand.
//
// THE PAYLOAD IS MADE VALID UTF-8 HERE, AT THE BOUNDARY. A JSON text that is
// not valid UTF-8 is not JSON (RFC 8259 §8.1) — every strict reader rejects
// the whole body, however tame the other rows are. The engine's in-wasm writer
// escapes only `"`, `\` and bytes < 0x20, so ANY `string` column hands the
// record's bytes to the response verbatim, and a record is peer-supplied data:
// nothing on the write path requires a FlatBuffers string to be valid UTF-8,
// and hundreds of `string` columns are projected across the routed standards.
// This is not a property of the fallback column that
// enginecatalog's >=1-column invariant emits (that one is a fixed-width
// number and cannot carry bytes at all) — it is a property of every projected
// string, and it predates the routing flip: the pinned $OMM table's
// CREATION_DATE behaves identically.
//
// THIS BOUNDARY IS NOT THE ONLY JSON PRODUCER ON /api/v1/query, AND IT NEVER
// CLAIMED TO BE THE LAST ONE. The public-query flow has two: the tabular
// projections THIS function assembles, and the full-record presentation, where
// the flow takes QuerySandboxedStream's raw FlatBuffer frames (deliberately
// unsanitized — that path is the wire format) and encodes them to JSON inside
// a wasm node. Those bytes never pass through here, so the host closes them
// where they reach the socket instead: flowrt/httpmount_json_wire.go holds
// every JSON-labelled response body of every mount to RFC 8259 (UTF-8, and
// never zero bytes). Sanitizing here as well is not redundant — the storage
// capability answers module callers that never touch HTTP.
//
// SANITIZING THE ASSEMBLED BYTES IS STRUCTURE-PRESERVING, not a re-encode.
// Every structural byte of the engine's JSON — `[`, `{`, `"`, `:`, `,`, `\`,
// the digits, the literals — is ASCII, i.e. valid UTF-8 by itself, so an
// invalid byte run can only occur INSIDE a string literal and replacing each
// run with U+FFFD cannot move a delimiter or change a key. The scan is one
// pass over a payload the caps already bound (SandboxCaps.MaxBytes), and it
// allocates only for a body that was going to be rejected anyway.
//
// The record itself is never rewritten: `_data` and the QuerySandboxedStream
// path carry the FlatBuffer byte for byte, so a consumer that wants the
// original bytes of a hostile string still gets them.
//
// THE BYTE CAP GOVERNS WHAT THE ENGINE ASSEMBLES, AND SANITIZING NEVER TURNS A
// WITHIN-CAP ANSWER INTO A FAILURE. Replacement can widen the body — U+FFFD is
// three bytes, and while a RUN of invalid bytes collapses to one replacement,
// isolated invalid bytes each cost two extra (measured at 1.81x on a hostile
// fixture, TestSanitizingNeverTurnsAWithinCapAnswerIntoAFailure) — so re-wearing
// SandboxCaps.MaxBytes here would hand a PEER the ability to make a query that
// fits the cap fail outright, on an anonymous public endpoint, by choosing the
// bytes of a record it publishes. That is a denial the boundary must not
// create: a caller asking for rows it is entitled to gets them. The volumetric
// contract still holds, because the engine REJECTS (never truncates) a result
// over MaxBytes before this function ever sees it, and the widening it applies
// afterwards is bounded by construction — every invalid byte becomes at most
// three, so the body is at most 3x the cap and only for input that was already
// hostile.
func (s *FlatSQLStore) QuerySandboxedJSON(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (payload []byte, rows, cols int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, 0, 0, fmt.Errorf("engine database not available")
	}
	payload, rows, cols, err = s.engineDB.QuerySandboxedJSON(sql, caps, params...)
	if err != nil {
		return nil, rows, cols, err
	}
	return validUTF8JSON(payload), rows, cols, nil
}

// validUTF8JSON returns payload unchanged when it is already valid UTF-8 (the
// overwhelming case, checked at ~GB/s), and otherwise replaces each invalid
// byte run with U+FFFD. See QuerySandboxedJSON for why that is safe on an
// assembled JSON body and why it belongs at this boundary rather than in the
// engine.
func validUTF8JSON(payload []byte) []byte {
	if utf8.Valid(payload) {
		return payload
	}
	return bytes.ToValidUTF8(payload, []byte(string(utf8.RuneError)))
}

// QuerySurfaceTable describes one queryable relation of the public query
// surface (the exact set the sandbox authorizer permits reading).
type QuerySurfaceTable struct {
	// Name as written in SQL (quote shadow names: "OMM@catalogfixture-gp").
	Name string `json:"name"`
	// Kind: "view" (unified UNION ALL view with _source) or "table"
	// (per-source shadow vtab / base vtab).
	Kind string `json:"kind"`
	// Source is the provider-source partition for shadow tables.
	Source string `json:"source,omitempty"`
	// Columns in engine order — schema-exact names plus the engine meta
	// columns (_data = the record FlatBuffer BLOB, _source on views).
	Columns []string `json:"columns"`
	// PlaceholderColumns names the subset of Columns that exist only to
	// satisfy the engine's >=1-column invariant (enginecatalog): the
	// standard's first field is a table/struct/union/vector the projection
	// cannot represent, so the column carries a FIXED-WIDTH one-byte read of
	// slot 0 — never that field's value. Advertising `SITE` on `LDM` with no
	// marker made a junk read look exactly like the field, which is a quiet
	// lie in a public listing; the honest answer for these standards is
	// `_data`. Synthesized by the API, so the key is lowercase (schema-exact
	// capitalization applies to SDS field names, not to fields the surface
	// invents).
	PlaceholderColumns []string `json:"placeholder_columns,omitempty"`
	// Records currently resident in the engine hot window for this relation.
	Records int64 `json:"records"`
}

// PublicQuerySurface enumerates the tables/views/columns the sandboxed
// public query may read, straight from the live engine (no hand-maintained
// list to drift). The surface is the engine HOT WINDOW: up to
// storage.engine_hot_window recent records per decorated schema and
// storage.engine_generic_hot_window for every other routed standard — full
// history lives in the append-only stream files, not in SQL.
//
// BOUNDED BY CONSTRUCTION, IN COST AND IN SIZE. Every embedded standard is
// routed now, so the naive surface is 226 base views plus 226 x sources shadow
// tables — 1,816 relations at host-01's seven sources.
//
//   - COST: the earlier shape ran `SELECT *` AND `SELECT count(*)` against
//     every one of them, which would make asking WHAT is queryable more
//     expensive than querying it. Probing columns ONCE PER SCHEMA was not
//     enough either: 226 prepared statements over 7-branch UNION ALL views
//     measured ~76-99 ms per listing while the `SELECT _data ... LIMIT 10` the
//     listing describes cost 0.4 ms — ~190x, on an UNAUTHENTICATED endpoint
//     whose every statement holds the single-threaded engine and s.mu, so a
//     handful of listings per second saturated the box every ingest and every
//     other read queues behind. Columns are therefore DERIVED from the schema
//     text the database was created from (engineRelationColumns) — zero
//     statements, and a shadow table, its view and the base vtab all declare
//     that same list by construction — and a relation is counted only when the
//     schema has records resident at all. What the listing costs is now one
//     count per POPULATED relation, independent of how many standards are
//     routed.
//   - SIZE: this is a PUBLIC response body. Listing every schema's shadow
//     partitions repeats the full column list 8x per standard on host-01 —
//     ~33,000 column entries, several hundred KB, for partitions that are
//     empty. So EVERY routed standard is listed (that is the point: the surface
//     must show what can be queried, and an empty answer is a valid answer),
//     but per-source partitions are listed only for the standards that
//     actually have records resident. The naming rule is uniform —
//     "<STANDARD>@<source>" — and the populated partitions are exactly the ones
//     a caller can get rows out of.
func (s *FlatSQLStore) PublicQuerySurface() ([]QuerySurfaceTable, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, fmt.Errorf("engine database not available")
	}

	srcNames := make([]string, 0, len(s.engineSources))
	for src := range s.engineSources {
		srcNames = append(srcNames, src)
	}
	sort.Strings(srcNames)

	routed := s.engineRoutedSchemaNames()
	surface := make([]QuerySurfaceTable, 0, len(routed)*(1+len(srcNames)))
	for _, schemaName := range routed {
		base := engineRoutedSchemas[schemaName].Table
		quotedBase := quoteEngineRelation(base)
		columns, ok := engineRelationColumns(base)
		if !ok {
			// A routed standard whose table the engine schema text does not
			// declare cannot exist (buildEngineRoutedSchemas reads the same
			// generated catalog the text is emitted from), and if it ever did
			// the honest answer is to say so rather than to list a standard
			// with no columns.
			return nil, fmt.Errorf("engine schema declares no table %q for routed standard %s", base, schemaName)
		}
		resident := s.engineResidentCount(schemaName) > 0
		var placeholders []string
		if field, junk := engineUnprojectableFirstFields[schemaName]; junk {
			placeholders = []string{field}
		}

		if len(srcNames) == 0 {
			surface = append(surface, QuerySurfaceTable{
				Name:               base,
				Kind:               "table",
				Columns:            columns,
				PlaceholderColumns: placeholders,
				Records:            s.countEngineRelation(quotedBase, resident),
			})
			continue
		}
		surface = append(surface, QuerySurfaceTable{
			Name:               base,
			Kind:               "view",
			Columns:            columns,
			PlaceholderColumns: placeholders,
			Records:            s.countEngineRelation(quotedBase, resident),
		})
		if !resident {
			// Nothing is in this standard's partitions, so listing one entry
			// per source would be 225 x sources empty relations carrying a
			// duplicate column list in a public body.
			continue
		}
		for _, src := range srcNames {
			name := base + "@" + src
			surface = append(surface, QuerySurfaceTable{
				Name:               name,
				Kind:               "table",
				Source:             src,
				Columns:            columns,
				PlaceholderColumns: placeholders,
				Records:            s.countEngineRelation(quoteEngineRelation(name), resident),
			})
		}
	}
	return surface, nil
}

// countEngineRelation returns the resident record count, and SKIPS the count
// entirely for a schema with nothing resident: COUNT(*) on a vtab scans every
// partition, and 226 routed standards make "nothing there" the overwhelmingly
// common case.
func (s *FlatSQLStore) countEngineRelation(quoted string, resident bool) int64 {
	if !resident {
		return 0
	}
	count, err := s.engineDB.Query("SELECT count(*) FROM " + quoted)
	if err != nil || len(count.Rows) != 1 || len(count.Rows[0]) != 1 {
		return 0
	}
	switch v := count.Rows[0][0].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func quoteEngineRelation(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
