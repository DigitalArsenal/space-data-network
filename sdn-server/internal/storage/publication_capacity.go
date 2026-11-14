package storage

// Each retained source reserves two complete generations. A CAT/OCM pair at N
// objects reserves 4*N references and 256 bytes per reference, plus 1 MiB per
// source for shared metadata. Payload size has its separate per-record bound.
func (s *FlatSQLStore) publicationRecordLimit() int {
	n := 0
	for _, x := range s.publicationSources {
		limit := x.CatalogObjectLimit
		if limit == 0 {
			limit = maxPublicationSelectionRecords
		}
		n += 2 * limit
	}
	if n == 0 {
		n = 2 * maxPublicationSelectionRecords
	}
	return n
}
func (s *FlatSQLStore) publicationByteLimit() int64 {
	return int64(s.publicationRecordLimit())*256 + int64(max(1, len(s.publicationSources)))*(1<<20)
}
