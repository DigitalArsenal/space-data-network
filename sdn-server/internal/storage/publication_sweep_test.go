package storage

import (
	"errors"
	"fmt"
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
