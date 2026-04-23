package directory

import "github.com/spacedatanetwork/sdn-server/internal/storage"

const (
	KindNode = "node"
	KindUser = "user"
)

// Store captures the FlatSQL-backed directory operations needed by the service.
type Store interface {
	UpsertDirectoryRecord(storage.DirectoryRecord) error
	QueryDirectory(storage.DirectoryQuery) ([]storage.DirectoryRecord, error)
}
