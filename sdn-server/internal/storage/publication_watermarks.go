package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

type publicationWatermarks struct {
	Global  uint64
	Sources map[string]uint64
}

func publicationSourceID(schema, provider, source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(schema+"\x00"+provider+"\x00"+source)))
}
func (s *FlatSQLStore) nextPublicationSourceSequence(schema, provider, source string) uint64 {
	id := publicationSourceID(schema, provider, source)
	n := s.publicationSourceWater[id]
	return n + 1
}
func (s *FlatSQLStore) savePublicationWatermarks(global uint64, schema, provider, source string, seq uint64) error {
	m := publicationWatermarks{Global: max(global, s.publicationHighWater), Sources: map[string]uint64{}}
	for k, v := range s.publicationSourceWater {
		m.Sources[k] = v
	}
	id := publicationSourceID(schema, provider, source)
	m.Sources[id] = max(m.Sources[id], seq)
	raw, _ := marshalPublication(m)
	if err := publicationAtomicFile(filepath.Join(s.publicationDir(), "watermarks.fb"), raw); err != nil {
		return err
	}
	s.publicationHighWater = m.Global
	s.publicationSourceWater = m.Sources
	return nil
}
func (s *FlatSQLStore) loadPublicationWatermarks() error {
	raw, err := os.ReadFile(filepath.Join(s.publicationDir(), "watermarks.fb"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var m publicationWatermarks
	if err = unmarshalPublication(raw, &m); err != nil {
		return err
	}
	s.publicationHighWater = max(s.publicationHighWater, m.Global)
	s.publicationSourceWater = m.Sources
	return nil
}

// Existing retained journals acquire source-local sequence numbers before use.
func (s *FlatSQLStore) migratePublicationSourceSequences() error {
	high := map[string]uint64{}
	for i, e := range s.publicationEntries {
		if e.Schema == "" || !e.ReadReady {
			continue
		}
		id := publicationSourceID(e.Schema, e.Tags.ProviderID, e.Tags.SourceName)
		if e.SourceSequence == 0 {
			e.SourceSequence = high[id] + 1
			raw, _ := marshalPublication(e)
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "entry.fb"), raw); err != nil {
				return err
			}
			s.publicationEntries[i] = e
		}
		high[id] = max(high[id], e.SourceSequence)
	}
	if s.publicationSourceWater == nil {
		s.publicationSourceWater = map[string]uint64{}
	}
	for k, v := range high {
		s.publicationSourceWater[k] = max(s.publicationSourceWater[k], v)
	}
	return nil
}
