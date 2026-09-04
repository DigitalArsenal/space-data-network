package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// TestReadGateAnswersRebuildingWithoutWaitingOnTheWriteLock: while a poisoned
// engine is rebuilt under the store write lock, a reader gets
// ErrEngineRebuilding at once instead of queueing behind the rebuild.
func TestReadGateAnswersRebuildingWithoutWaitingOnTheWriteLock(t *testing.T) {
	s := &FlatSQLStore{}
	s.engineRebuilding.Store(true)

	// Hold the write lock the way RecoverPoisonedEngine does for the whole
	// rebuild; a gated reader must not need it.
	s.mu.Lock()
	defer s.mu.Unlock()

	started := time.Now()
	done := make(chan error, 3)
	go func() { _, err := s.QueryRawStream("SELECT 1"); done <- err }()
	go func() {
		_, err := s.QuerySandboxedStream("SELECT 1", flatsqlrt.SandboxCaps{MaxRows: 1, MaxBytes: 1 << 16, Timeout: time.Second})
		done <- err
	}()
	go func() { _, err := s.QueryRawRecords(RawRecordQuery{}); done <- err }()
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, ErrEngineRebuilding) {
				t.Fatalf("reader error = %v, want ErrEngineRebuilding", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("a reader waited on the write lock during the rebuild (%s)", time.Since(started))
		}
	}
}
