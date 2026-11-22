package storage

// lane_records_removed.go — who is told that records of a lane left the
// store.
//
// A supersede of a lane's batches (SupersedeSourceBatches, which retention of
// a replace-current lane runs) and an expiry pass (ExpireRecords) call the
// function set here after they removed records, with the lane they removed
// from. An expiry may name a provider or a source only; the other is then
// "", which stands for any. The publication service sets it to check the
// lane's failed batches (internal/api dataset_publication_alert.go). It runs
// on the caller's goroutine after the removal and must not block.

// SetLaneRecordsRemoved sets the function told that records of a lane were
// removed; nil unsets it.
func (s *FlatSQLStore) SetLaneRecordsRemoved(notify func(schemaName, providerID, sourceName string)) {
	if s == nil {
		return
	}
	if notify == nil {
		s.laneRecordsRemoved.Store(nil)
		return
	}
	s.laneRecordsRemoved.Store(&notify)
}

func (s *FlatSQLStore) noteLaneRecordsRemoved(schemaName, providerID, sourceName string) {
	if s == nil {
		return
	}
	if notify := s.laneRecordsRemoved.Load(); notify != nil {
		(*notify)(schemaName, providerID, sourceName)
	}
}
