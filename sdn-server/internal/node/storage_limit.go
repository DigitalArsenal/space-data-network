package node

import (
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// storageLimitMu guards storage.max_size, which the dashboard's storage
// editor changes while the node runs (owner 2026-10-07).
var storageLimitMu sync.RWMutex

// StorageMaxSize is the node's storage.max_size as configured ("" means the
// default share of the drive).
func (n *Node) StorageMaxSize() string {
	storageLimitMu.RLock()
	defer storageLimitMu.RUnlock()
	return n.config.Storage.MaxSize
}

// StorageLimitBytes resolves the current limit against the drive holding the
// store.
func (n *Node) StorageLimitBytes() (int64, error) {
	return config.StorageConfig{MaxSize: n.StorageMaxSize()}.ResolveMaxSizeBytes(n.config.Storage.Path)
}

// SetStorageMaxSize changes the limit now: the store's engine takes it, and a
// quota pass runs at once, evicting the oldest records if the store is over.
func (n *Node) SetStorageMaxSize(spec string) error {
	maxBytes, err := config.StorageConfig{MaxSize: spec}.ResolveMaxSizeBytes(n.config.Storage.Path)
	if err != nil {
		return err
	}
	if n.store != nil {
		if err := n.store.SetQuotaBytes(maxBytes); err != nil {
			return err
		}
	}
	storageLimitMu.Lock()
	n.config.Storage.MaxSize = spec
	storageLimitMu.Unlock()
	go n.enforceStorageQuota()
	return nil
}
