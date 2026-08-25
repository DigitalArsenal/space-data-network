package storage

import (
	"fmt"
	"sort"
	"strings"

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
func (s *FlatSQLStore) QuerySandboxedJSON(sql string, caps flatsqlrt.SandboxCaps, params ...interface{}) (payload []byte, rows, cols int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, 0, 0, fmt.Errorf("engine database not available")
	}
	return s.engineDB.QuerySandboxedJSON(sql, caps, params...)
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
// routed now, so the naive surface is 227 base views plus 227 x sources shadow
// tables — 1,816 relations at host-01's seven sources.
//
//   - COST: the earlier shape ran `SELECT *` AND `SELECT count(*)` against
//     every one of them, which would make asking WHAT is queryable more
//     expensive than querying it. Columns are probed ONCE PER SCHEMA (a shadow
//     table and its view declare the same columns, by construction — the view
//     is a UNION ALL over the shadows), and a relation is counted only when the
//     schema has records resident at all.
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
		cols, err := s.engineDB.Query("SELECT * FROM " + quotedBase + " LIMIT 0")
		if err != nil {
			return nil, fmt.Errorf("enumerate columns of %s: %w", base, err)
		}
		resident := s.engineResident[schemaName] > 0

		if len(srcNames) == 0 {
			surface = append(surface, QuerySurfaceTable{
				Name:    base,
				Kind:    "table",
				Columns: cols.Columns,
				Records: s.countEngineRelation(quotedBase, resident),
			})
			continue
		}
		surface = append(surface, QuerySurfaceTable{
			Name:    base,
			Kind:    "view",
			Columns: cols.Columns,
			Records: s.countEngineRelation(quotedBase, resident),
		})
		if !resident {
			// Nothing is in this standard's partitions, so listing one entry
			// per source would be 226 x sources empty relations carrying a
			// duplicate column list in a public body.
			continue
		}
		for _, src := range srcNames {
			name := base + "@" + src
			surface = append(surface, QuerySurfaceTable{
				Name:    name,
				Kind:    "table",
				Source:  src,
				Columns: cols.Columns,
				Records: s.countEngineRelation(quoteEngineRelation(name), resident),
			})
		}
	}
	return surface, nil
}

// countEngineRelation returns the resident record count, and SKIPS the count
// entirely for a schema with nothing resident: COUNT(*) on a vtab scans every
// partition, and 227 routed standards make "nothing there" the overwhelmingly
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
