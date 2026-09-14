package main

// Read-flavored CLI verbs (search, sync status, identity export, dataset-pnm
// list/export, ...) open the record store DEFERRED: every record read from the
// control tables is available the instant the open returns, and the engine
// hot window — which only the sandboxed SQL surface reads — is not rebuilt for
// a verb that never queries it.
//
// The store is single-writer and there is no read-only open: a second process
// cannot share the engine's database file safely, and it has nothing to
// re-derive a private copy from. While the daemon holds the store, a read verb
// reads through the daemon's API instead; this helper says so.

import (
	"errors"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// openStoreForReading opens the store at storagePath for a read verb.
func openStoreForReading(storagePath string, validator *sds.Validator) (*storage.FlatSQLStore, error) {
	store, err := storage.NewFlatSQLStore(storagePath, validator, storage.WithDeferredBootRebuilds())
	if err != nil {
		if errors.Is(err, storage.ErrStoreLocked) {
			return nil, fmt.Errorf("the store at %s is held by another process (the daemon): stop it, or read through its API: %w", storagePath, err)
		}
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	return store, nil
}
