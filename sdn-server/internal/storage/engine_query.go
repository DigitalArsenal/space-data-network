package storage

import (
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// QueryRawStream executes SQL whose result cells are all BLOBs (e.g.
// `SELECT _data FROM "OMM@celestrak-gp" WHERE ...`) inside the engine and
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
