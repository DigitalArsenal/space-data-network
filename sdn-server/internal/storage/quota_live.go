package storage

import "fmt"

// SetQuotaBytes changes the store's disk quota while it runs (owner
// 2026-10-07: the dashboard's storage editor sets the limit). A format-4
// store hands it to its engine, which applies it to writes from then on; the
// node's periodic quota pass reads the node's limit on its own.
func (s *FlatSQLStore) SetQuotaBytes(bytes int64) error {
	if s == nil {
		return nil
	}
	if bytes < 0 {
		return fmt.Errorf("storage quota must not be negative")
	}
	if s.f4 != nil {
		return s.f4.api().SetQuota(bytes)
	}
	return nil
}
