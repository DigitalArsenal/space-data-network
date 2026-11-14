package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPublicationReviewIOBoundaries(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	for _, stage := range []string{"prepare", "write", "data-sync", "metadata", "rename", "directory-sync", "engine", "applied"} {
		t.Run(stage, func(t *testing.T) {
			if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: stage}}); err != nil {
				t.Fatal(err)
			}
			s.publicationFault = func(at string) error {
				if at == stage {
					return errors.New("synthetic I/O failure")
				}
				return nil
			}
			raw := sds.NewCATBuilder().WithNoradCatID(900001).Build()
			_, err := s.StoreWithSourceTags("CAT.fbs", raw, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: stage, BatchID: "batch"})
			if err == nil {
				t.Fatal("I/O fault hidden")
			}
			s.publicationFault = nil
			journalTestPut(t, s, "ordinary-"+stage)
			s.RetryPublicationRecovery()
			if len(s.PublicationRecoveryStatus()) != 0 {
				t.Fatal("transient I/O did not recover")
			}
		})
	}
}
func TestPublicationReviewCompletionAndRetirement(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "one")
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "one")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StopPublicationRetention("CAT.fbs", "synthetic", "index"); err == nil {
		t.Fatal("pending batch discarded")
	}
	if err = s.CompletePublication(p); err != nil {
		t.Fatal(err)
	}
	if len(s.RetainedPublicationBatches()) != 0 {
		t.Fatal("completed batch reconciled again")
	}
	s.Close()
	s = journalTestStore(t, dir)
	defer s.Close()
	if !s.PublicationBatchComplete("CAT.fbs", "synthetic", "index", "one") {
		t.Fatal("completion depended on series ledger")
	}
	journalTestPut(t, s, "one")
	if s.PublicationBatchComplete("CAT.fbs", "synthetic", "index", "one") {
		t.Fatal("continued batch incorrectly completed")
	}
	p, err = s.SelectPublication("CAT.fbs", "synthetic", "index", "one")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompletePublication(p); err != nil {
		t.Fatal(err)
	}
	if err = s.StopPublicationRetention("CAT.fbs", "synthetic", "index"); err != nil {
		t.Fatal(err)
	}
	if s.RetainsPublicationSource("CAT.fbs", "synthetic", "index") {
		t.Fatal("retirement failed")
	}
}
func TestPublicationReviewReadDoesNotWaitForWriter(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "initial")
	blocked, release := make(chan struct{}), make(chan struct{})
	s.publicationHook = func(stage string) {
		if stage == "prepared" {
			close(blocked)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		raw := sds.NewCATBuilder().WithNoradCatID(900001).WithObjectName("updated").Build()
		_, err := s.StoreWithSourceTags("CAT.fbs", raw, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "changed"})
		done <- err
	}()
	<-blocked
	read := make(chan error, 1)
	go func() {
		_, err := s.ExportDatasetWindow(filepath.Join(s.basePath, "export"), IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BatchID: "initial"})
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		t.Error("reader waited for writer")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestPublicationReviewHistoryMemory(t *testing.T) {
	dir := t.TempDir()
	s := &FlatSQLStore{basePath: dir}
	// 100000 synthetic history records for one catalog identity; no engine or signing.
	e := publicationEntry{Sequence: 1, Schema: "CAT.fbs", Tags: SourceTags{ProviderID: "synthetic", SourceName: "index"}, Applied: true, Generation: true}
	if err := os.MkdirAll(s.publicationEntryDir(1), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(s.publicationEntryDir(1), "records.sds"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for i := 0; i < 100000; i++ {
		data := sds.NewCATBuilder().WithNoradCatID(900001).WithObjectName(fmt.Sprintf("synthetic-%d", i)).Build()
		frame := make([]byte, 4+len(data))
		binary.LittleEndian.PutUint32(frame, uint32(len(data)))
		copy(frame[4:], data)
		if _, err = f.Write(frame); err != nil {
			t.Fatal(err)
		}
		h.Write(frame)
	}
	f.Close()
	e.Digest = hex.EncodeToString(h.Sum(nil))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	e.Refs, err = s.publicationReferences(e)
	if err != nil {
		t.Fatal(err)
	}
	s.publicationEntries = []publicationEntry{e}
	s.publicationSources = []PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products"}}
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(s)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("history_records=100000 selected_index=%d selected_products=%d retained_heap_growth_bytes=%d", len(snap.Index.refs), len(snap.Products.refs), growth)
	if len(snap.Index.refs) != 1 || len(snap.Products.refs) != 0 || growth > 128<<20 {
		t.Fatal("history payloads retained in snapshot")
	}
}
func TestPublicationReviewCatalogStandardsUnsealed(t *testing.T) {
	for _, schema := range []string{"CAT", "OCM"} {
		if encfield.HasEncryptedFields(schema) {
			t.Fatalf("retained standard %s has sealed fields", schema)
		}
	}
}
