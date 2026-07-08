package main

// Loop C.8b: read-flavored CLI verbs (search, sync status, identity export,
// dataset-pnm list/export, ...) must work against a store a live daemon
// holds. The v2 store is single-writer, so those verbs open the store
// read-only when the exclusive writer lock is taken: compact metadata is
// replayed from its CRC-valid prefix, stream files are only ever read, and
// all writes are rejected with storage.ErrStoreReadOnly.

import (
	"errors"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// openStoreForReading opens the store at storagePath preferring a normal
// writable open, and falls back to a read-only open (with a stderr notice)
// when another process — typically the daemon — holds the writer lock.
func openStoreForReading(storagePath string, validator *sds.Validator) (*storage.FlatSQLStore, error) {
	store, err := storage.NewFlatSQLStore(storagePath, validator)
	if err == nil {
		return store, nil
	}
	if !errors.Is(err, storage.ErrStoreLocked) {
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	roStore, roErr := storage.NewFlatSQLStoreReadOnly(storagePath, validator)
	if roErr != nil {
		return nil, fmt.Errorf("failed to open storage read-only after writer-lock contention (%v): %w", err, roErr)
	}
	fmt.Fprintf(os.Stderr, "note: store at %s is held by another process (daemon); opened READ-ONLY point-in-time view\n", storagePath)
	return roStore, nil
}
