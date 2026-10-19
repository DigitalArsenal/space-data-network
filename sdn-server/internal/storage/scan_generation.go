package storage

// scan_generation.go — the store's scan generation: 32 random bytes the
// node MACs its $QRP resume cursors with (datasync/resume_cursor.go).
//
// A resume cursor names a point in the store's record sequence (format 1's
// sdn_record_index rowid, format 4's seq). That point means the same thing
// only while the sequence does, so the generation turns whenever it might
// not:
//   - RebuildIndex rotates it;
//   - it lives in sdn_metadata, which a recovery that re-derives the control
//     database from the journals discards (field_encryption.go), so a
//     re-derived store starts a new generation;
//   - a new or wiped store starts with none.
// A cursor MAC'd under an earlier generation no longer verifies, and the
// client rescans from the first page (410 resume_cursor_expired).

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

const (
	scanGenerationMetadataKey = "sdn.scan.generation"
	scanGenerationBytes       = 32
)

// ScanGeneration returns the store's scan generation, creating it on first
// use. A store that cannot persist one (read-only, closing) gets a
// process-lifetime generation: its cursors expire at restart.
func (s *FlatSQLStore) ScanGeneration() []byte {
	s.scanGenerationMu.Lock()
	defer s.scanGenerationMu.Unlock()
	if s.scanGeneration != nil {
		return s.scanGeneration
	}
	if gen, err := s.readScanGeneration(); err == nil && gen != nil {
		s.scanGeneration = gen
		return gen
	} else if err != nil {
		log.Warnf("scan generation: read failed, starting a new one: %v", err)
	}
	s.scanGeneration = s.newScanGeneration()
	return s.scanGeneration
}

// rotateScanGeneration starts a new generation (RebuildIndex): every resume
// cursor issued before it expires.
func (s *FlatSQLStore) rotateScanGeneration() {
	s.scanGenerationMu.Lock()
	defer s.scanGenerationMu.Unlock()
	s.scanGeneration = s.newScanGeneration()
}

func (s *FlatSQLStore) readScanGeneration() ([]byte, error) {
	if s.closedErr() != nil {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var encoded string
	err := s.db.QueryRow(`SELECT value FROM sdn_metadata WHERE key = ?`, scanGenerationMetadataKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	gen, err := hex.DecodeString(encoded)
	if err != nil || len(gen) != scanGenerationBytes {
		return nil, errors.New("scan generation metadata is malformed")
	}
	return gen, nil
}

// newScanGeneration draws a generation and persists it. Caller holds
// scanGenerationMu.
func (s *FlatSQLStore) newScanGeneration() []byte {
	gen := make([]byte, scanGenerationBytes)
	if _, err := rand.Read(gen); err != nil {
		panic("scan generation: crypto/rand failed: " + err.Error())
	}
	if s.closedErr() != nil {
		return gen
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`
		INSERT INTO sdn_metadata (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, scanGenerationMetadataKey, hex.EncodeToString(gen), time.Now().Unix()); err != nil {
		log.Warnf("scan generation: not persisted (resume cursors expire at restart): %v", err)
	}
	return gen
}
