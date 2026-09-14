package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func bootTestValidator(t *testing.T) *sds.Validator {
	t.Helper()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// openBootStore opens with the background checkpointer OFF so every test
// controls exactly when the marks move. The boot checkpoint and the Close
// checkpoint still run — those are the two that matter for a restart.
func openBootStore(t *testing.T, basePath string, v *sds.Validator) *FlatSQLStore {
	t.Helper()
	t.Setenv(checkpointIntervalEnv, "0")
	store, err := NewFlatSQLStore(basePath, v)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s): %v", basePath, err)
	}
	return store
}

// reopenDeferred reopens a store the way the daemon does: the engine hot
// window is not rebuilt at open.
func reopenDeferred(t *testing.T, basePath string) *FlatSQLStore {
	t.Helper()
	t.Setenv(checkpointIntervalEnv, "0")
	store, err := NewFlatSQLStore(basePath, bootTestValidator(t), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s, deferred): %v", basePath, err)
	}
	return store
}

// simulateCrash closes the store the way a SIGKILL would leave it: WITHOUT the
// clean-shutdown checkpoint, so the engine's record arena and both marks stay
// wherever the last checkpoint put them.
func simulateCrash(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	s.mu.Lock()
	s.controlDBDurable = false // suppresses only the final checkpoint
	s.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

// discardControlDatabaseForTest deletes the control database and the engine's
// record arena, the way an operator wiping a node would. Only the auxiliary
// journal survives; the next open replays it from the beginning.
func discardControlDatabaseForTest(t *testing.T, basePath string) {
	t.Helper()
	dbPath := filepath.Join(basePath, flatSQLControlDBName)
	for _, p := range []string{dbPath, dbPath + "-journal", dbPath + ".fsdata"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", p, err)
		}
	}
}

// digestJournalPrefixForTest fingerprints an auxiliary journal file from
// scratch over [0, limit).
func digestJournalPrefixForTest(f *os.File, limit int64) (string, error) {
	h := newAuxiliaryMetadataDigest()
	off := int64(0)
	if err := extendJournalDigest(h, f, &off, limit); err != nil {
		return "", err
	}
	return sealJournalDigest(h, limit)
}

// digestForeignPrefixForTest fingerprints the same frame headers in a
// DIFFERENT domain, to prove the auxiliary digest's domain prefix binds a
// mark to its own journal kind.
func digestForeignPrefixForTest(f *os.File, limit int64) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "foreign-journal-v1\n")
	off := int64(0)
	if err := extendJournalDigest(h, f, &off, limit); err != nil {
		return "", err
	}
	return sealJournalDigest(h, limit)
}

// storedRecordBytesForTest reads a record's bytes exactly as the producer
// table holds them (sealed for a field-encrypted standard).
func storedRecordBytesForTest(t *testing.T, s *FlatSQLStore, schemaName, cid string) []byte {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := s.storedRecordBytesLocked(schemaName, cid)
	if err != nil {
		t.Fatalf("stored bytes of %s/%s: %v", schemaName, cid, err)
	}
	return data
}
