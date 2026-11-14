package storage

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StopPublicationRetention is a startup operation. It preserves immutable files
// for existing selections; after every source is stopped, the journal can be
// removed with the node stopped. Pending and failed writes prevent retirement.
func (s *FlatSQLStore) StopPublicationRetention(schema, provider, source string) error {
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for _, e := range s.publicationEntries {
		if e.Schema == schema && e.Tags.ProviderID == provider && e.Tags.SourceName == source && (!e.Applied || !e.Published) {
			return errors.New("stop_publication_retention requires every retained batch to complete; drain publication first")
		}
	}
	var sources []PublicationSource
	for _, x := range s.publicationSources {
		if x.Schema == schema && x.Provider == provider && x.Source == source {
			continue
		}
		sources = append(sources, x)
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	if err = publicationAtomicFile(filepath.Join(s.publicationDir(), "sources.json"), raw); err != nil {
		return err
	}
	s.publicationSources = sources
	s.publishSourceKeys()
	s.publicationEnabled.Store(len(sources) > 0)
	return nil
}
func (s *FlatSQLStore) exclusionPath(provider, source string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + source))
	return filepath.Join(s.publicationDir(), fmt.Sprintf("exclude-%x.json", sum))
}

// ExcludeCatalogRecords is an additive operator takedown for future captures.
// Historical batch exports and already captured immutable selections are retained.
func (s *FlatSQLStore) ExcludeCatalogRecords(provider, source string, cids []string) error {
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	if !s.publicationSourceEnabled("CAT.fbs", provider, source) {
		return errors.New("catalog exclusions require retained CAT source")
	}
	excluded, err := s.catalogExclusions("CAT.fbs", provider, source)
	if err != nil {
		return err
	}
	for _, id := range cids {
		if id == "" {
			return errors.New("catalog exclusion CID is empty")
		}
		excluded[id] = true
	}
	raw, err := json.Marshal(excluded)
	if err != nil {
		return err
	}
	return publicationAtomicFile(s.exclusionPath(provider, source), raw)
}
func (s *FlatSQLStore) catalogExclusions(schema, provider, source string) (map[string]bool, error) {
	out := map[string]bool{}
	if schema != "CAT.fbs" {
		return out, nil
	}
	raw, err := os.ReadFile(s.exclusionPath(provider, source))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (s *FlatSQLStore) PublicationRetentionEnabled() bool { return s.publicationEnabled.Load() }

func (s *FlatSQLStore) PublicationRetainedSources() []PublicationSource {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	return append([]PublicationSource(nil), s.publicationSources...)
}
