package storage

// Incremental resident counters keep admission and status independent of history.
func (s *FlatSQLStore) accountPublicationEntry(e publicationEntry, delta int) {
	if !e.Applied {
		return
	}
	s.publicationRecordCount += delta * len(e.Refs)
	s.publicationReferenceCountBytes += int64(delta) * e.ReferenceBytes
	for _, r := range e.Refs {
		s.publicationPayloadCountBytes += int64(delta) * (int64(r.Size) + 4)
	}
	s.accountPublicationPending(e, delta)
}
func (s *FlatSQLStore) accountPublicationPending(e publicationEntry, delta int) {
	if !e.Applied || e.Published || e.BatchFailure.Attempts >= 3 || e.GenerationFailure.Attempts >= 3 {
		return
	}
	if s.publicationPendingCounts == nil {
		s.publicationPendingCounts = map[publicationSourceKey]map[string]int{}
	}
	key := publicationSourceKey{e.Schema, e.Tags.ProviderID, e.Tags.SourceName}
	batches := s.publicationPendingCounts[key]
	if batches == nil {
		batches = map[string]int{}
		s.publicationPendingCounts[key] = batches
	}
	batches[e.Tags.BatchID] += delta
	if batches[e.Tags.BatchID] <= 0 {
		delete(batches, e.Tags.BatchID)
	}
}
