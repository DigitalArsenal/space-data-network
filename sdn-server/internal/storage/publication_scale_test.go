package storage

import (
	"fmt"
	"runtime"
	"testing"
	"unsafe"
)

func TestPublicationCatalogScaleDaily(t *testing.T) {
	if testing.Short() {
		t.Skip("catalog scale and daily reclamation exercise requires a full test run")
	}
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, n := range []int{33000, 100000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := reclamationStore(t, t.TempDir())
			defer s.Close()
			if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products", CatalogObjectLimit: n}, {Schema: "OCM.fbs", Provider: "synthetic", Source: "products", CatalogObjectLimit: n}}); err != nil {
				t.Fatal(err)
			}
			for day := 0; day < 3; day++ {
				reclamationDay(t, s, day, n)
				peak := s.PublicationJournalStats()
				want := 2 * n
				if day > 0 {
					want = 4 * n
				}
				if peak.Records != want || peak.ReferenceBytes > s.publicationByteLimit() {
					t.Fatalf("peak=%+v want=%d cap=%d", peak, want, s.publicationByteLimit())
				}
				scalePublish(t, s)
				plateau := s.PublicationJournalStats()
				if plateau.Records != 2*n || plateau.Entries != 2 {
					t.Fatalf("plateau=%+v", plateau)
				}
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				t.Logf("objects=%d day=%d reference_struct_bytes=%d peak_records=%d peak_reference_bytes=%d plateau_records=%d plateau_reference_bytes=%d total_heap_bytes=%d", n, day, unsafe.Sizeof(publicationRecordRef{}), peak.Records, peak.ReferenceBytes, plateau.Records, plateau.ReferenceBytes, m.HeapAlloc)
			}
		})
	}
}

func scalePublish(t *testing.T, s *FlatSQLStore) {
	t.Helper()
	for _, b := range s.RetainedPublicationBatches() {
		p, err := s.SelectPublication(b.Schema, b.Provider, b.Source, b.Batch)
		if err != nil {
			t.Fatal(err)
		}
		for offset := 0; offset < len(p.refs); offset += 10000 {
			if _, err = s.ExportDatasetWindow(t.TempDir(), IndexedRecordQuery{SchemaName: b.Schema, ProviderID: b.Provider, SourceName: b.Source, BatchID: b.Batch, Selection: p, Limit: 10000, Offset: offset}); err != nil {
				t.Fatal(err)
			}
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
