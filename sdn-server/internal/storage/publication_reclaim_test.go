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
	"time"
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

func TestPublicationAppliedReadRetryNeverReplays(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	reclamationDay(t, s, 0, 2)
	reclamationDay(t, s, 1, 2)
	path := filepath.Join(s.publicationEntryDir(2), "records.sds")
	s.Close()
	if err := os.Rename(path, path+".unavailable"); err != nil {
		t.Fatal(err)
	}
	s = journalTestStore(t, dir)
	defer s.Close()
	if len(s.PublicationRecoveryStatus()) == 0 {
		t.Fatal("missing read failure")
	}
	// If recovery replays an applied entry this hook makes the control fail.
	before, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".unavailable", path); err != nil {
		t.Fatal(err)
	}
	s.RetryPublicationRecovery()
	if len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("read repair did not clear pause")
	}
	after, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index"})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("recovery changed live records")
	}
	for i := range before {
		if before[i].CID != after[i].CID {
			t.Fatal("applied old entry became live")
		}
	}
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "day-01:complete")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	current := p.refs[0].Path
	if err = os.Rename(current, current+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Record(0); err == nil {
		t.Fatal("export fault hidden")
	}
	if len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("export failure paused writes")
	}
	if err = os.Rename(current+".unavailable", current); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Record(0); err != nil {
		t.Fatal("export retry failed", err)
	}
}

func TestPublicationFailedGenerationContinues(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	reclamationDay(t, s, 0, 2)
	reclamationPublish(t, s, nil)
	_, bad := reclamationRecords(1, 2)
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", bad, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "bad:complete"}); err == nil {
		t.Fatal("unresolved generation accepted")
	}
	for i := 0; i < 5; i++ {
		s.RetryPublicationRecovery()
	}
	for _, e := range s.publicationEntries {
		if e.Tags.BatchID == "bad:complete" && e.GenerationFailure.Attempts != 3 {
			t.Fatalf("attempts=%d", e.GenerationFailure.Attempts)
		}
	}
	for _, b := range s.RetainedPublicationBatches() {
		if b.Batch == "bad:complete" {
			t.Fatal("quarantined generation still blocks dispatch")
		}
	}
	s.Close()
	s = journalTestStore(t, dir)
	defer s.Close()
	if len(s.PublicationRecoveryStatus()) == 0 {
		t.Fatal("quarantine status lost")
	}
	reclamationDay(t, s, 2, 2)
	reclamationPublish(t, s, nil)
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	snap.Close()
	if err = s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "bad:complete", "requeue"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range s.RetainedPublicationBatches() {
		found = found || b.Batch == "bad:complete"
	}
	if !found {
		t.Fatal("requeue did not restore batch")
	}
	for i := 0; i < 3; i++ {
		s.RetryPublicationRecovery()
	}
	if err = s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "bad:complete", "discard"); err != nil {
		t.Fatal(err)
	}
	if !s.PublicationBatchComplete("CAT.fbs", "synthetic", "index", "bad:complete") {
		t.Fatal("discard not durable completion")
	}
}
func TestPublicationGenerationReadDoesNotBlockWriter(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := reclamationStore(t, t.TempDir())
	defer s.Close()
	reclamationDay(t, s, 0, 2)
	captured, release := make(chan struct{}), make(chan struct{})
	s.publicationHook = func(stage string) {
		if stage == "generation-captured" {
			close(captured)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		products, index := reclamationRecords(1, 2)
		_, err := s.StoreBatchWithSourceTags("OCM.fbs", products, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "products", BatchID: "new"})
		if err == nil {
			_, err = s.StoreBatchWithSourceTags("CAT.fbs", index, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "new:complete"})
		}
		done <- err
	}()
	<-captured
	other := make(chan error, 1)
	go func() {
		products, _ := reclamationRecords(2, 2)
		_, err := s.StoreBatchWithSourceTags("OCM.fbs", products, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "products", BatchID: "other"})
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("generation capture held global write lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestPublicationPlainTerminalAndBatchExclusion(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "plain:complete")
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "plain:complete")
	if err != nil {
		t.Fatal(err)
	}
	id := p.refs[0].CID
	p.Close()
	if err = s.ExcludeCatalogRecords("synthetic", "index", []string{id}); err != nil {
		t.Fatal(err)
	}
	p, err = s.SelectPublication("CAT.fbs", "synthetic", "index", "plain:complete")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if len(p.refs) != 0 {
		t.Fatal("excluded record remained in pending batch")
	}
}
func TestPublicationShardCopyFallback(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	if err := os.WriteFile(from, []byte("synthetic bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	noLink := func(string, string) error { return errors.New("unsupported") }
	if err := installPublicationShardWith(from, to, noLink); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(to)
	if string(got) != "synthetic bytes" {
		t.Fatal("copy changed bytes")
	}
	if err := installPublicationShardWith(from, to, noLink); !os.IsExist(err) {
		t.Fatal("existing bytes overwritten")
	}
}

func TestPublicationBatchQuarantineAndRequeue(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "bad")
	journalTestPut(t, s, "later")
	for i := 1; i <= 3; i++ {
		quarantined, err := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "bad", errors.New("inert transport failure"))
		if err != nil || quarantined != (i == 3) {
			t.Fatalf("attempt %d: %v %v", i, quarantined, err)
		}
	}
	s.Close()
	s = journalTestStore(t, dir)
	defer s.Close()
	pending := s.RetainedPublicationBatches()
	if len(pending) != 1 || pending[0].Batch != "later" {
		t.Fatal("quarantined batch blocked source")
	}
	if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "bad", "requeue"); err != nil {
		t.Fatal(err)
	}
	if len(s.RetainedPublicationBatches()) != 2 {
		t.Fatal("requeue lost payload")
	}
}
func TestPublicationIndependentRecoveryCauses(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "one")
	journalTestPut(t, s, "two")
	paths := []string{filepath.Join(s.publicationEntryDir(1), "records.sds"), filepath.Join(s.publicationEntryDir(2), "records.sds")}
	s.Close()
	for _, path := range paths {
		if err := os.Rename(path, path+".unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	s = journalTestStore(t, dir)
	defer s.Close()
	if err := os.Rename(paths[0]+".unavailable", paths[0]); err != nil {
		t.Fatal(err)
	}
	s.RetryPublicationRecovery()
	if len(s.PublicationRecoveryStatus()) == 0 {
		t.Fatal("first repair cleared second cause")
	}
	if err := os.Rename(paths[1]+".unavailable", paths[1]); err != nil {
		t.Fatal(err)
	}
	s.RetryPublicationRecovery()
	if len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("complete repair did not clear pause")
	}
}
