package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationGenerationAdmissionAndHeadroom(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	configure := func(limit int) error {
		return s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products", CatalogObjectLimit: limit}, {Schema: "OCM.fbs", Provider: "synthetic", Source: "products", CatalogObjectLimit: limit}})
	}
	if err := configure(4); err != nil {
		t.Fatal(err)
	}
	// Overshoot by one object fits the explicit 25% headroom on both sides.
	reclamationDay(t, s, 0, 5)
	terminalPublish(t, s)
	before := s.PublicationJournalStats().Records
	products, indexes := reclamationRecords(1, 6)
	put := func(schema, source, batch string, records [][]byte) error {
		_, err := s.StoreBatchWithSourceTags(schema, records, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: source, BatchID: batch})
		return err
	}
	if err := put("OCM.fbs", "products", "oversized", products); err == nil || !strings.Contains(err.Error(), "catalog_object_limit=4") {
		t.Fatalf("missing capacity refusal: %v", err)
	}
	if s.PublicationJournalStats().Records != before {
		t.Fatal("oversized products partially admitted")
	}
	if err := put("OCM.fbs", "products", "staged", products[:5]); err != nil {
		t.Fatal(err)
	}
	if p, err := s.SelectPublication("OCM.fbs", "synthetic", "products", "staged"); err == nil {
		p.Close()
		t.Fatal("uncommitted generation announced")
	}
	// The index refuses the generation as a unit, releasing its staged products.
	if err := put("CAT.fbs", "index", "oversized:complete", indexes); err == nil {
		t.Fatal("oversized index admitted")
	}
	if s.PublicationJournalStats().Records != before {
		t.Fatalf("aborted products retained: %+v", s.PublicationJournalStats())
	}
	s.Close()
	s = journalTestStore(t, dir)
	defer s.Close()
	reclamationDay(t, s, 2, 5)
	if err := configure(1); err != nil {
		t.Fatal("decrease blocked opening", err)
	}
	if s.publicationSources[0].CatalogObjectLimit != 4 {
		t.Fatal("capacity was lowered")
	}
	if err := configure(0); err != nil {
		t.Fatal("removed setting blocked opening", err)
	}
	if s.publicationSources[0].CatalogObjectLimit != 4 {
		t.Fatal("removed setting lost durable capacity")
	}
	if err := s.ReclaimPublication(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationReservationAndInterruptedAbort(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products", CatalogObjectLimit: 2}, {Schema: "OCM.fbs", Provider: "synthetic", Source: "products", CatalogObjectLimit: 2}, {Schema: "CAT.fbs", Provider: "synthetic", Source: "other", CatalogObjectLimit: 1}}); err != nil {
		t.Fatal(err)
	}
	products, indexes := reclamationRecords(0, 2)
	if _, err := s.StoreBatchWithSourceTags("OCM.fbs", products, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "products", BatchID: "reserved"}); err != nil {
		t.Fatal(err)
	}
	_, filler := reclamationRecords(1, s.publicationRecordLimit()-2)
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", filler, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "other", BatchID: "fill"}); err == nil {
		t.Fatal("unrelated source consumed reserved index capacity")
	}
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", indexes, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "reserved:complete"}); err != nil {
		t.Fatal(err)
	}
	products, _ = reclamationRecords(1, 2)
	if _, err := s.StoreBatchWithSourceTags("OCM.fbs", products, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "products", BatchID: "interrupted"}); err != nil {
		t.Fatal(err)
	}
	seq := s.publicationHighWater
	plan := filepath.Join(s.publicationDir(), "generation-abort.fb")
	s.Close()
	raw, _ := marshalPublication([]uint64{seq})
	if err := os.WriteFile(plan, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s = journalTestStore(t, dir)
	defer s.Close()
	if got := s.PublicationJournalStats().Records; got != 4 {
		t.Fatalf("interrupted abort retained staged products: %d", got)
	}
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	snap.Close()
	reclamationDay(t, s, 2, 2)
}

func TestPublicationThreeGenerationLagCoalesces(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := reclamationStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products", CatalogObjectLimit: 8}, {Schema: "OCM.fbs", Provider: "synthetic", Source: "products", CatalogObjectLimit: 8}}); err != nil {
		t.Fatal(err)
	}
	for day := 0; day < 3; day++ {
		reclamationDay(t, s, day, 8)
	}
	if got := s.PublicationJournalStats().Records; got != 48 {
		t.Fatalf("pending bytes lost: %d", got)
	}
	terminalPublish(t, s)
	if err := s.ReclaimPublication(); err != nil {
		t.Fatal(err)
	}
	if got := s.PublicationJournalStats().Records; got != 16 {
		t.Fatalf("completed generations did not coalesce: %d", got)
	}
	reclamationDay(t, s, 3, 8)
}
