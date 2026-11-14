package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationExcludedTerminalRetainsGeneration(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := reclamationStore(t, t.TempDir())
	defer s.Close()
	products, index := reclamationRecords(0, 2)
	put := func(schema, source, batch string, rows [][]byte) {
		t.Helper()
		if _, err := s.StoreBatchWithSourceTags(schema, rows, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: source, BatchID: batch}); err != nil {
			t.Fatal(err)
		}
	}
	put("OCM.fbs", "products", "products", products)
	put("CAT.fbs", "index", "chunk", index[:1])
	put("CAT.fbs", "index", "last:complete", index[1:])
	if err := s.ExcludeCatalogRecords("synthetic", "index", []string{computeCID(index[1])}); err != nil {
		t.Fatal(err)
	}
	terminalPublish(t, s)
	if _, err := os.Stat(filepath.Join(s.publicationEntryDir(3), "generation")); err != nil {
		t.Fatal("terminal marker reclaimed", err)
	}
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if snap.Cutoff != 3 || len(snap.Index.refs) != 1 {
		t.Fatalf("cutoff=%d count=%d", snap.Cutoff, len(snap.Index.refs))
	}
}
func TestPublicationSequenceAfterNewestReclaimed(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	defer func() { s.Close() }()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	_, index := reclamationRecords(0, 2)
	put := func(batch string, row []byte) {
		t.Helper()
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", [][]byte{row}, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: batch}); err != nil {
			t.Fatal(err)
		}
	}
	put("keep", index[0])
	put("remove", index[1])
	if err := s.ExcludeCatalogRecords("synthetic", "index", []string{computeCID(index[1])}); err != nil {
		t.Fatal(err)
	}
	terminalPublish(t, s)
	if len(s.publicationEntries) != 1 || s.publicationHighWater != 2 {
		t.Fatalf("entries=%d high=%d", len(s.publicationEntries), s.publicationHighWater)
	}
	put("after", index[0])
	if s.publicationHighWater != 3 {
		t.Fatal("reused sequence", s.publicationHighWater)
	}
	terminalPublish(t, s)
	s.Close()
	s = journalTestStore(t, dir)
	put("restart", index[0])
	if s.publicationHighWater != 4 {
		t.Fatal("restart reused sequence", s.publicationHighWater)
	}
}

func terminalPublish(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	for _, b := range s.RetainedPublicationBatches() {
		p, err := s.SelectPublication(b.Schema, b.Provider, b.Source, b.Batch)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.CompletePublication(p); err != nil {
			t.Fatal(err)
		}
		p.Close()
	}
	if err := s.ReclaimPublication(); err != nil {
		t.Fatal(err)
	}
}
