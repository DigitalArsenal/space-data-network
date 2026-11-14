package datasync

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"strings"
)

// RecordIndexQueryHash binds the existing index cursor to its schema and filters.
func RecordIndexQueryHash(q storage.RecordIndexPageQuery) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"index", q.SchemaName, q.ProviderID, q.SourceName, q.BatchID, q.NoradLike,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
