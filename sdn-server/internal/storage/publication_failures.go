package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type publicationFailure struct {
	Attempts int
	Reason   string
	RetryAt  time.Time
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
			*target = publicationFailure{Attempts: 3, Reason: "failure receipt unreadable: " + err.Error(), RetryAt: time.Now().Add(6 * time.Hour)}
		}
	}
}
func (s *FlatSQLStore) recordGenerationFailure(e publicationEntry, cause error) error {
	if !IsPublicationContentError(cause) {
		return nil
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for i, current := range s.publicationEntries {
		if current.Sequence == e.Sequence {
			if current.Generation || current.GenerationFailure.Attempts != e.GenerationFailure.Attempts {
				return nil
			}
			f := current.GenerationFailure
			f.Attempts++
			f.Reason = cause.Error()
			f.RetryAt = time.Now().Add(6 * time.Hour)
			raw, _ := json.Marshal(f)
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "generation-failure.json"), raw); err != nil {
				return err
			}
			s.accountPublicationPending(s.publicationEntries[i], -1)
			s.publicationEntries[i].GenerationFailure = f
			s.accountPublicationPending(s.publicationEntries[i], 1)
			s.updatePublicationQuarantine(s.publicationEntries[i])
			if f.Attempts >= 3 {
				log.Warnf("catalog generation quarantined after three attempts: %v", cause)
			}
		}
	}
	return nil
}

// RecordPublicationFailure durably sets aside a retained batch after three failed
// content attempts. Transient failures do not advance this count. Retained
// payloads are retried after six hours or an explicit requeue.
func (s *FlatSQLStore) RecordPublicationFailure(schema, provider, source, batch string, cause error) (bool, error) {
	if !IsPublicationContentError(cause) {
		return false, nil
	}
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
		if e.Schema == schema && e.Tags.ProviderID == provider && e.Tags.SourceName == source && e.Tags.BatchID == batch && e.Applied && !e.Discarded {
			if _, readErr := s.publicationReferences(e); readErr != nil {
				s.pausePublicationLocked(e, readErr)
				continue
			}
		}
		if e.Schema == schema && e.Tags.ProviderID == provider && e.Tags.SourceName == source && e.Tags.BatchID == batch && !e.Published {
			f := publicationFailure{Attempts: attempts, Reason: cause.Error(), RetryAt: time.Now().Add(6 * time.Hour)}
			raw, _ := json.Marshal(f)
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "batch-failure.json"), raw); err != nil {
				return false, err
			}
			s.accountPublicationPending(s.publicationEntries[i], -1)
			s.publicationEntries[i].BatchFailure = f
			s.accountPublicationPending(s.publicationEntries[i], 1)
			s.updatePublicationQuarantine(s.publicationEntries[i])
			found = true
		}
	}
	if found && attempts >= 3 {
		log.Warnf("publication batch quarantined after three attempts: %v", cause)
	}
	return found && attempts >= 3, nil
}

// ManagePublicationBatch applies an explicit operator requeue or discard to a
// unfinished batch or an unreadable applied batch. Discard acknowledges lost
// publication without deleting live data.
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
		if e.Discarded || (e.Published && (!e.Applied || e.ReadReady)) {
			continue
		}
		if action == "discard" {
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "discarded"), []byte("1\n")); err != nil {
				return err
			}
			if e.Applied {
				if err := s.recordPublicationDiscardEffect(e); err != nil {
					return err
				}
			}
			if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "published"), []byte("1\n")); err != nil {
				return err
			}
			s.publicationEntries[i].Published = true
			s.publicationEntries[i].Discarded = true
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
		s.accountPublicationPending(e, -1)
		s.accountPublicationPending(s.publicationEntries[i], 1)
		s.updatePublicationQuarantine(s.publicationEntries[i])
	}
	return nil
}

// One durable notice per source preserves the explicit loss acknowledgement
// after entry reclamation. Replacing records does not erase that audit notice.
func (s *FlatSQLStore) recordPublicationDiscardEffect(e publicationEntry) error {
	path := filepath.Join(s.publicationDir(), "discard-effects.json")
	effects := map[string]string{}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &effects)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	key := e.Schema + "/" + e.Tags.ProviderID + "/" + e.Tags.SourceName
	effects[key] = fmt.Sprintf("applied entry %d batch %q explicitly discarded; objects whose winning record it held are missing until the producer writes them again; earlier discard acknowledgements also remain in effect", e.Sequence, e.Tags.BatchID)
	raw, err = json.Marshal(effects)
	if err != nil {
		return err
	}
	return publicationAtomicFile(path, raw)
}

// PublicationContentError identifies a deterministic defect in one retained batch.
// Service, transport, cancellation, and filesystem errors remain retryable.
type PublicationContentError struct{ Cause error }

func (e *PublicationContentError) Error() string { return e.Cause.Error() }
func (e *PublicationContentError) Unwrap() error { return e.Cause }
func IsPublicationContentError(err error) bool {
	var e *PublicationContentError
	return errors.As(err, &e)
}

// Only immutable input defects receive this type. Resource, configuration,
// output filesystem, transport, and cancellation errors remain service errors.
func publicationContentError(err error) error {
	if err == nil || IsPublicationContentError(err) {
		return err
	}
	return &PublicationContentError{Cause: err}
}

func publicationReadError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, os.ErrNotExist) {
		return publicationContentError(err)
	}
	return err
}

// An integrity failure during recovery cannot provide a selection for a normal
// publication attempt. Only never-applied entries can be set aside without a
// source pause; applied entries belong to the complete view.
func (s *FlatSQLStore) quarantineUnreadablePublication(e publicationEntry, cause error) error {
	f := publicationFailure{Attempts: 3, Reason: cause.Error(), RetryAt: time.Now().Add(6 * time.Hour)}
	raw, _ := json.Marshal(f)
	if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "batch-failure.json"), raw); err != nil {
		return err
	}
	s.publicationMu.Lock()
	defer s.publicationMu.Unlock()
	for i, old := range s.publicationEntries {
		if old.Sequence == e.Sequence {
			s.accountPublicationPending(old, -1)
			s.publicationEntries[i].BatchFailure = f
			s.accountPublicationPending(s.publicationEntries[i], 1)
			s.updatePublicationQuarantine(s.publicationEntries[i])
		}
	}
	return nil
}
func (s *FlatSQLStore) retryQuarantinedPublication(now time.Time) {
	s.publicationMu.Lock()
	var retry []publicationEntry
	for _, e := range s.publicationEntries {
		for _, f := range []publicationFailure{e.BatchFailure, e.GenerationFailure} {
			if !e.Published && f.Attempts >= 3 && (f.RetryAt.IsZero() || !now.Before(f.RetryAt)) {
				retry = append(retry, e)
				break
			}
		}
	}
	s.publicationMu.Unlock()
	for _, e := range retry {
		if err := s.ManagePublicationBatch(e.Schema, e.Tags.ProviderID, e.Tags.SourceName, e.Tags.BatchID, "requeue"); err != nil {
			log.Warnf("publication automatic requeue: %v", err)
		}
	}
}

func (s *FlatSQLStore) loadPublicationOutcome(e *publicationEntry) error {
	raw, err := os.ReadFile(filepath.Join(s.publicationEntryDir(e.Sequence), "outcome"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &e.Refused); err != nil {
		return err
	}
	e.OutcomeKnown = true
	return nil
}
