package storage

import (
	"strings"
	"sync"
)

// writeSignals wakes readers waiting for new records of a type: each type
// has one channel, closed (and replaced) when a write stores records of the
// type. A reader takes the channel before it reads, so a write that lands
// after its read closes the channel it waits on.
type writeSignals struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

func writeSignalKey(schemaName string) string {
	name := strings.TrimPrefix(strings.TrimSpace(schemaName), "$")
	return strings.ToUpper(strings.TrimSuffix(strings.TrimSuffix(name, ".fbs"), ".FBS"))
}

// WriteSignal is a channel closed by the next write that stores records of
// schemaName. Stores of format 5 signal; other formats never close it, so a
// reader also waits with a deadline.
func (s *FlatSQLStore) WriteSignal(schemaName string) <-chan struct{} {
	key := writeSignalKey(schemaName)
	s.writes.mu.Lock()
	defer s.writes.mu.Unlock()
	if s.writes.ch == nil {
		s.writes.ch = map[string]chan struct{}{}
	}
	c := s.writes.ch[key]
	if c == nil {
		c = make(chan struct{})
		s.writes.ch[key] = c
	}
	return c
}

// signalWrite wakes the readers of schemaName.
func (s *FlatSQLStore) signalWrite(schemaName string) {
	key := writeSignalKey(schemaName)
	s.writes.mu.Lock()
	defer s.writes.mu.Unlock()
	if c := s.writes.ch[key]; c != nil {
		close(c)
		delete(s.writes.ch, key)
	}
}
