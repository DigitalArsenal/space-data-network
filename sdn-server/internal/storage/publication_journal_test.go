package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func journalTestStore(t *testing.T, dir string) *FlatSQLStore {
	t.Helper()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewFlatSQLStore(dir, v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func journalTestPut(t *testing.T, s *FlatSQLStore, batch string) {
	t.Helper()
	data := sds.NewCATBuilder().WithNoradCatID(900001).WithObjectName("synthetic-" + batch).Build()
	_, err := s.StoreWithSourceTags("CAT.fbs", data, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: batch})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPublicationJournalCrashChild(t *testing.T) {
	dir := os.Getenv("SDN_PUBLICATION_CRASH_DIR")
	if dir == "" {
		t.Skip("subprocess only")
	}
	s := journalTestStore(t, dir)
	s.publicationHook = func(boundary string) {
		if boundary == os.Getenv("SDN_PUBLICATION_CRASH_BOUNDARY") {
			os.Exit(73)
		}
	}
	journalTestPut(t, s, "changed")
	t.Fatal("crash boundary not reached")
}
func TestPublicationJournalCrashRecovery(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, boundary := range []string{"prepared", "stored"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			s := journalTestStore(t, dir)
			if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
				t.Fatal(err)
			}
			journalTestPut(t, s, "initial")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestPublicationJournalCrashChild$", "-test.timeout=2m")
			cmd.Env = append(os.Environ(), "SDN_PUBLICATION_CRASH_DIR="+dir, "SDN_PUBLICATION_CRASH_BOUNDARY="+boundary)
			out, err := cmd.CombinedOutput()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 73 {
				t.Fatalf("child error %v: %s", err, out)
			}
			s = journalTestStore(t, dir)
			defer s.Close()
			if len(s.RetainedPublicationBatches()) != 2 {
				t.Fatal("recovery lost a batch")
			}
			for _, batch := range []string{"initial", "changed"} {
				ex, err := s.ExportDatasetWindow(t.TempDir(), IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BatchID: batch})
				if err != nil || ex.RecordCount != 1 {
					t.Fatalf("export after recovery: %v", err)
				}
			}
			live, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index"})
			if err != nil || len(live) != 1 {
				t.Fatal("live CAT overwrite changed", err)
			}
			if _, err = s.ExpireRecords(RecordExpiryQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BeforeUnix: time.Now().Add(time.Hour).Unix()}); err != nil {
				t.Fatal(err)
			}
			live, err = s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index"})
			if err != nil || len(live) != 0 {
				t.Fatal("expiry did not run", err)
			}
			for _, batch := range []string{"initial", "changed"} {
				if _, err = s.ExportDatasetWindow(t.TempDir(), IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BatchID: batch}); err != nil {
					t.Fatal("expiry lost pending bytes", err)
				}
			}
			if _, err = os.Stat(filepath.Join(dir, "publication-journal", "00000000000000000002", "applied")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
