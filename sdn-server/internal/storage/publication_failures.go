package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type publicationFailure struct {
	Attempts int
	Reason   string
}

func (s *FlatSQLStore) loadPublicationFailures(e *publicationEntry) {
	for name, target := range map[string]*publicationFailure{"generation-failure.json": &e.GenerationFailure, "batch-failure.json": &e.BatchFailure} {
		raw, err := os.ReadFile(filepath.Join(s.publicationEntryDir(e.Sequence), name))
		if os.IsNotExist(err) {
			continue
		}
		if err == nil {
			err = json.Unmarshal(raw, target)
		}
		if err != nil {
			*target = publicationFailure{3, "failure receipt unreadable: " + err.Error()}
		}
	}
}
func (s *FlatSQLStore) recordGenerationFailure(e publicationEntry, cause error) error {
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for i, current := range s.publicationEntries {
		if current.Sequence == e.Sequence {
			if current.Generation {
				return nil
			}
			f := current.GenerationFailure
			f.Attempts++
			f.Reason = cause.Error()
			raw, _ := json.Marshal(f)
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "generation-failure.json"), raw); err != nil {
				return err
			}
			s.publicationEntries[i].GenerationFailure = f
			if f.Attempts >= 3 {
				log.Warnf("catalog generation quarantined after three attempts: %v", cause)
			}
		}
	}
	return nil
}

// RecordPublicationFailure durably sets aside a retained batch after three failed
// attempts. Its payload remains available for explicit requeue or discard.
func (s *FlatSQLStore) RecordPublicationFailure(schema, provider, source, batch string, cause error) (bool, error) {
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	attempts := 0
	for _, e := range s.publicationEntries {
		if e.Schema == schema && e.Tags.ProviderID == provider && e.Tags.SourceName == source && e.Tags.BatchID == batch && e.BatchFailure.Attempts > attempts {
			attempts = e.BatchFailure.Attempts
		}
	}
	attempts++
	found := false
	for i, e := range s.publicationEntries {
		if e.Schema == schema && e.Tags.ProviderID == provider && e.Tags.SourceName == source && e.Tags.BatchID == batch && !e.Published {
			f := publicationFailure{attempts, cause.Error()}
			raw, _ := json.Marshal(f)
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "batch-failure.json"), raw); err != nil {
				return false, err
			}
			s.publicationEntries[i].BatchFailure = f
			found = true
		}
	}
	if found && attempts >= 3 {
		log.Warnf("publication batch quarantined after three attempts: %v", cause)
	}
	return found && attempts >= 3, nil
}

// ManagePublicationBatch applies an explicit operator requeue or discard to a
// quarantined batch. Discard acknowledges lost publication without deleting live data.
func (s *FlatSQLStore) ManagePublicationBatch(schema, provider, source, batch, action string) error {
	if action != "requeue" && action != "discard" {
		return errors.New("publication action must be requeue or discard")
	}
	s.publicationWriteMu.Lock()
	defer s.publicationWriteMu.Unlock()
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for i, e := range s.publicationEntries {
		if e.Schema != schema || e.Tags.ProviderID != provider || e.Tags.SourceName != source || e.Tags.BatchID != batch {
			continue
		}
		if e.BatchFailure.Attempts < 3 && e.GenerationFailure.Attempts < 3 {
			continue
		}
		if !e.Applied {
			return errors.New("unapplied batch cannot be discarded or requeued")
		}
		if action == "discard" {
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "published"), []byte("1\n")); err != nil {
				return err
			}
			s.publicationEntries[i].Published = true
		}
		for _, name := range []string{"generation-failure.json", "batch-failure.json"} {
			if err := os.Remove(filepath.Join(s.publicationEntryDir(e.Sequence), name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := publicationSyncDir(s.publicationEntryDir(e.Sequence)); err != nil {
			return err
		}
		if action == "requeue" {
			s.publicationEntries[i].GenerationFailure = publicationFailure{}
		}
		s.publicationEntries[i].BatchFailure = publicationFailure{}
	}
	return nil
}
