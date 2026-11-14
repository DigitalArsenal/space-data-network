package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestPublicationReclamationConcurrentAppendAndPausedSource(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}, {Schema: "CAT.fbs", Provider: "synthetic", Source: "paused"}}); err != nil {
		t.Fatal(err)
	}
	_, rows := reclamationRecords(0, 1)
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", rows, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "paused", BatchID: "paused"}); err != nil {
		t.Fatal(err)
	}
	s.pausePublication(s.publicationEntries[0], errors.New("injected source pause"))
	for i := 0; i < 4; i++ {
		journalTestPut(t, s, fmt.Sprintf("initial-%d", i))
	}
	appends := 0
	s.publicationHook = func(stage string) {
		if stage == "reclamation-captured" {
			appends++
			done := make(chan struct{})
			go func() { journalTestPut(t, s, fmt.Sprintf("concurrent-%d", appends)); close(done) }()
			<-done
		}
	}
	for i := 0; i < 4; i++ {
		before := s.PublicationJournalStats().Reclaimed
		terminalPublish(t, s)
		after := s.PublicationJournalStats()
		if after.Reclaimed <= before {
			t.Fatalf("sweep starved by concurrent append: %+v", after)
		}
	}
	s.publicationHook = nil
	stats := s.PublicationJournalStats()
	if appends < 4 || stats.Skipped == 0 || stats.LastSkip == "" {
		t.Fatalf("missing sweep status: %+v appends=%d", stats, appends)
	}
	if s.publicationEntries[0].Tags.SourceName != "paused" {
		t.Fatal("paused source reclaimed")
	}
	t.Logf("concurrent_appends=%d reclaimed=%d skipped_sources=%d", appends, stats.Reclaimed, stats.Skipped)
}

func TestPublicationReclamationPassBound(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		journalTestPut(t, s, fmt.Sprintf("batch-%d", i))
	}
	for i, e := range s.publicationEntries {
		if err := publicationAtomicFile(filepath.Join(s.publicationEntryDir(e.Sequence), "published"), []byte("1\n")); err != nil {
			t.Fatal(err)
		}
		s.accountPublicationPending(e, -1)
		s.publicationEntries[i].Published = true
	}
	if err := s.ReclaimPublication(); err != nil {
		t.Fatal(err)
	}
	stats := s.PublicationJournalStats()
	if stats.Reclaimed != 64 || stats.Entries != 6 {
		t.Fatalf("unbounded retirement pass: %+v", stats)
	}
	s.RetryPublicationRecovery()
	stats = s.PublicationJournalStats()
	if stats.Entries != 1 || stats.Reclaimed != 69 {
		t.Fatalf("reconciliation did not drain remainder: %+v", stats)
	}
}
