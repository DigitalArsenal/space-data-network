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

// engineSchemaBaseTables are the SDS record tables routed into the engine
// (only OMM so far — loop B.3 slice; further standards join here).
var engineSchemaBaseTables = []string{"OMM"}

// PublicQuerySurface enumerates the tables/views/columns the sandboxed
// public query may read, straight from the live engine (no hand-maintained
// list to drift). The surface is the engine HOT WINDOW: up to
// storage.engine_hot_window recent records per schema — full history lives
// in the append-only stream files, not in SQL.
func (s *FlatSQLStore) PublicQuerySurface() ([]QuerySurfaceTable, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.engineDB == nil {
		return nil, fmt.Errorf("engine database not available")
	}

	names := make([]string, 0, 1+len(s.engineSources))
	kinds := make([]string, 0, cap(names))
	sources := make([]string, 0, cap(names))
	for _, base := range engineSchemaBaseTables {
		if len(s.engineSources) > 0 {
			names = append(names, base)
			kinds = append(kinds, "view")
			sources = append(sources, "")
			srcNames := make([]string, 0, len(s.engineSources))
			for src := range s.engineSources {
				srcNames = append(srcNames, src)
			}
			sort.Strings(srcNames)
			for _, src := range srcNames {
				names = append(names, base+"@"+src)
				kinds = append(kinds, "table")
				sources = append(sources, src)
			}
		} else {
			names = append(names, base)
			kinds = append(kinds, "table")
			sources = append(sources, "")
		}
	}

	surface := make([]QuerySurfaceTable, 0, len(names))
	for i, name := range names {
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		cols, err := s.engineDB.Query("SELECT * FROM " + quoted + " LIMIT 0")
		if err != nil {
			return nil, fmt.Errorf("enumerate columns of %s: %w", name, err)
		}
		count, err := s.engineDB.Query("SELECT count(*) FROM " + quoted)
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", name, err)
		}
		var records int64
		if len(count.Rows) == 1 && len(count.Rows[0]) == 1 {
			switch v := count.Rows[0][0].(type) {
			case int64:
				records = v
			case float64:
				records = int64(v)
			}
		}
		surface = append(surface, QuerySurfaceTable{
			Name:    name,
			Kind:    kinds[i],
			Source:  sources[i],
			Columns: cols.Columns,
			Records: records,
		})
	}
	return surface, nil
}
