package storage

import (
	"errors"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"
	"os"
	"path/filepath"
	"testing"
)

func reclamationRecords(day, count int) ([][]byte, [][]byte) {
	var products, indexes [][]byte
	for i := 0; i < count; i++ {
		b := flatbuffers.NewBuilder(256)
		stamp := b.CreateString(fmt.Sprintf("synthetic-day-%02d", day))
		OCM.MetadataStart(b)
		OCM.MetadataAddNORAD_CAT_ID(b, uint32(900000+i))
		OCM.MetadataAddOBJECT_NAME(b, stamp)
		meta := OCM.MetadataEnd(b)
		OCM.OCMStart(b)
		OCM.OCMAddMETADATA(b, meta)
		root := OCM.OCMEnd(b)
		b.FinishWithFileIdentifier(root, []byte("$OCM"))
		product := b.FinishedBytes()
		products = append(products, product)
		b = flatbuffers.NewBuilder(256)
		id := b.CreateString(computeCID(product))
		CAT.CATProductStart(b)
		CAT.CATProductAddRECORD_CID(b, id)
		ref := CAT.CATProductEnd(b)
		CAT.CATStartPRODUCTSVector(b, 1)
		b.PrependUOffsetT(ref)
		refs := b.EndVector(1)
		CAT.CATStart(b)
		CAT.CATAddNORAD_CAT_ID(b, uint32(900000+i))
		CAT.CATAddPRODUCTS(b, refs)
		root = CAT.CATEnd(b)
		b.FinishWithFileIdentifier(root, []byte("$CAT"))
		indexes = append(indexes, b.FinishedBytes())
	}
	return products, indexes
}
func reclamationDay(t *testing.T, s *FlatSQLStore, day, count int) {
	t.Helper()
	products, indexes := reclamationRecords(day, count)
	for _, x := range []struct {
		schema, source, batch string
		rows                  [][]byte
	}{{"OCM.fbs", "products", fmt.Sprintf("day-%02d", day), products}, {"CAT.fbs", "index", fmt.Sprintf("day-%02d:complete", day), indexes}} {
		if _, err := s.StoreBatchWithSourceTags(x.schema, x.rows, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: x.source, BatchID: x.batch}); err != nil {
			t.Fatal(err)
		}
	}
}
func reclamationStore(t *testing.T, dir string) *FlatSQLStore {
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products"}, {Schema: "OCM.fbs", Provider: "synthetic", Source: "products"}}); err != nil {
		t.Fatal(err)
	}
	return s
}
func reclamationPublish(t *testing.T, s *FlatSQLStore, seen map[string]bool) {
	t.Helper()
	for _, b := range s.RetainedPublicationBatches() {
		key := b.Schema + "/" + b.Batch
		if seen != nil && seen[key] {
			t.Fatal("batch published twice")
		}
		p, err := s.SelectPublication(b.Schema, b.Provider, b.Source, b.Batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ExportDatasetWindow(t.TempDir(), IndexedRecordQuery{SchemaName: b.Schema, ProviderID: b.Provider, SourceName: b.Source, BatchID: b.Batch, Selection: p}); err != nil {
			t.Fatal(err)
		}
		if err = s.CompletePublication(p); err != nil {
			t.Fatal(err)
		}
		p.Close()
		if seen != nil {
			seen[key] = true
		}
	}
	if err := s.ReclaimPublication(); err != nil {
		t.Fatal(err)
	}
}
func TestPublicationReclamationThirtyDays(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	defer func() { s.Close() }()
	seen := map[string]bool{}
	var plateau PublicationJournalStats
	for day := 0; day < 30; day++ {
		reclamationDay(t, s, day, 2000)
		reclamationPublish(t, s, seen)
		stats := s.PublicationJournalStats()
		if day == 1 {
			plateau = stats
		}
		if day > 1 && (stats.Entries != plateau.Entries || stats.Records != plateau.Records || stats.ReferenceBytes != plateau.ReferenceBytes || stats.PayloadBytes != plateau.PayloadBytes) {
			t.Fatalf("journal did not plateau on day %d: %+v versus %+v", day, stats, plateau)
		}
		if day == 14 {
			s.Close()
			s = journalTestStore(t, dir)
		}
	}
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	fresh := reclamationStore(t, t.TempDir())
	defer fresh.Close()
	reclamationDay(t, fresh, 29, 2000)
	want, err := fresh.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer want.Close()
	a, n := snap.Index.Fingerprint()
	b, m := want.Index.Fingerprint()
	if a != b || n != 2000 || m != n {
		t.Fatal("last index differs from fresh snapshot")
	}
	a, n = snap.Products.Fingerprint()
	b, m = want.Products.Fingerprint()
	if a != b || n != 2000 || m != n {
		t.Fatal("last products differ from fresh snapshot")
	}
	if len(seen) != 60 {
		t.Fatalf("announced batches=%d", len(seen))
	}
	t.Logf("days=30 objects=2000 announced_batches=%d plateau=%+v final=%+v", len(seen), plateau, s.PublicationJournalStats())
}
func TestPublicationReclamationPinsAndCrash(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, stage := range []string{"reclaim-receipt", "reclaim-renamed"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			s := reclamationStore(t, dir)
			reclamationDay(t, s, 0, 2)
			reclamationPublish(t, s, nil)
			old, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
			if err != nil {
				t.Fatal(err)
			}
			reclamationDay(t, s, 1, 2)
			reclamationPublish(t, s, nil)
			if _, err = old.Index.Record(0); err != nil {
				t.Fatal("pin lost", err)
			}
			old.Close()
			s.publicationFault = func(at string) error {
				if at == stage {
					return errors.New("interrupted reclamation")
				}
				return nil
			}
			if err = s.ReclaimPublication(); err == nil {
				t.Fatal("fault not reached")
			}
			s.Close()
			s = journalTestStore(t, dir)
			defer s.Close()
			snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer snap.Close()
			if _, err = snap.Products.Record(0); err != nil {
				t.Fatal(err)
			}
			if len(s.RetainedPublicationBatches()) != 0 {
				t.Fatal("completed publication reappeared")
			}
			dirs, _ := filepath.Glob(filepath.Join(dir, "publication-journal", ".reclaimed-*"))
			if len(dirs) > 0 {
				t.Fatal("abandoned reclamation")
			}
			if _, err = os.Stat(s.publicationReceiptPath("CAT.fbs", "synthetic", "index", "day-00:complete")); err != nil {
				t.Fatal("missing receipt", err)
			}
		})
	}
}
