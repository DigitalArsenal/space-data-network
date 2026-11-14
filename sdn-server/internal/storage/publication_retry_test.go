package storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestPublicationTransientFailuresAndAutomaticRetry(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "batch")
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, os.ErrPermission, errors.New("transport unavailable")} {
		for i := 0; i < 8; i++ {
			q, err := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "batch", cause)
			if err != nil || q {
				t.Fatalf("transient quarantined: %v %v", q, err)
			}
		}
	}
	if s.publicationEntries[0].BatchFailure.Attempts != 0 {
		t.Fatal("transient advanced counter")
	}
	for i := 0; i < 3; i++ {
		_, err := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "batch", &PublicationContentError{Cause: errors.New("bad content")})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.RetainedPublicationBatches()) != 0 {
		t.Fatal("content not quarantined")
	}
	s.retryQuarantinedPublication(time.Now().Add(time.Hour))
	if len(s.RetainedPublicationBatches()) != 0 {
		t.Fatal("early retry")
	}
	s.retryQuarantinedPublication(time.Now().Add(7 * time.Hour))
	if len(s.RetainedPublicationBatches()) != 1 {
		t.Fatal("automatic retry absent")
	}
}
func TestPublicationGenerationAttemptCountedOnce(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := reclamationStore(t, t.TempDir())
	defer s.Close()
	reclamationDay(t, s, 0, 1)
	e := s.publicationEntries[len(s.publicationEntries)-1]
	s.publicationEntries[len(s.publicationEntries)-1].Generation = false
	e.Generation = false
	if err := s.recordGenerationFailure(e, os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	if s.publicationEntries[len(s.publicationEntries)-1].GenerationFailure.Attempts != 0 {
		t.Fatal("read failure counted")
	}
	for i := 0; i < 2; i++ {
		if err := s.recordGenerationFailure(e, &PublicationContentError{Cause: errors.New("unresolved")}); err != nil {
			t.Fatal(err)
		}
	}
	if s.publicationEntries[len(s.publicationEntries)-1].GenerationFailure.Attempts != 1 {
		t.Fatal("same captured attempt counted twice")
	}
}
