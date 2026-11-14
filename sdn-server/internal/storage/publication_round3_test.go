package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationSourceSequenceGapConsumer(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	defer func() { s.Close() }()
	readIndex := func(p *PublicationSelection) DatasetExportIndex {
		t.Helper()
		x, err := s.ExportDatasetWindow(t.TempDir(), IndexedRecordQuery{SchemaName: p.Schema, ProviderID: p.Provider, SourceName: p.Source, BatchID: p.Batch, Selection: p})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(x.IndexPath)
		if err != nil {
			t.Fatal(err)
		}
		var index DatasetExportIndex
		if err = json.Unmarshal(raw, &index); err != nil {
			t.Fatal(err)
		}
		return index
	}
	consumer := map[string]string{}
	cursor := uint64(0)
	gaps := 0
	apply := func(p *PublicationSelection, snapshot bool) {
		index := readIndex(p)
		if snapshot {
			consumer = map[string]string{}
			cursor = index.PublicationSourceCutoff
		} else {
			for _, seq := range index.PublicationSourceSequences {
				if seq <= cursor {
					continue
				}
				if seq != cursor+1 {
					gaps++
					return
				}
				cursor = seq
			}
		}
		for _, r := range p.refs {
			consumer[r.Identity()] = r.CID()
		}
	}
	reclamationDay(t, s, 0, 3)
	snap, err := s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	apply(snap.Index, true)
	snap.Close()
	terminalPublish(t, s)
	reclamationDay(t, s, 1, 3)
	for i := 0; i < 3; i++ {
		if _, err = s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "day-01:complete", &PublicationContentError{Cause: errors.New("synthetic content defect")}); err != nil {
			t.Fatal(err)
		}
	}
	reclamationDay(t, s, 2, 3)
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "day-02:complete")
	if err != nil {
		t.Fatal(err)
	}
	apply(p, false)
	p.Close()
	if gaps != 1 {
		t.Fatal("consumer silently missed gap", gaps)
	}
	snap, err = s.CaptureCatalogSnapshot("synthetic", "index", 0)
	if err != nil {
		t.Fatal(err)
	}
	apply(snap.Index, true)
	if cursor != 3 {
		t.Fatal("cutoff is not source-local", cursor)
	}
	for _, r := range snap.Index.refs {
		if consumer[r.Identity()] != r.CID() {
			t.Fatal("snapshot did not converge")
		}
	}
	snap.Close()
	s.Close()
	s = journalTestStore(t, dir)
	reclamationDay(t, s, 3, 3)
	p, err = s.SelectPublication("CAT.fbs", "synthetic", "index", "day-03:complete")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	apply(p, false)
	apply(p, false)
	if cursor != 4 || gaps != 1 {
		t.Fatalf("restart or duplicate broke sequence: %d %d", cursor, gaps)
	}
}
func TestPublicationMetadataRepairWithoutRestart(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			dir := t.TempDir()
			s := reclamationStore(t, dir)
			reclamationDay(t, s, 0, 2)
			entry := filepath.Join(s.publicationEntryDir(1), "entry.json")
			scope := filepath.Join(s.publicationEntryDir(1), "source.json")
			saved, _ := os.ReadFile(entry)
			savedScope, _ := os.ReadFile(scope)
			s.Close()
			if err := os.WriteFile(entry, []byte("bad"), 0600); err != nil {
				t.Fatal(err)
			}
			if unknown {
				if err := os.WriteFile(scope, []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s = journalTestStore(t, dir)
			defer s.Close()
			if len(s.PublicationRecoveryStatus()) == 0 {
				t.Fatal("missing isolation")
			}
			if err := os.WriteFile(entry, saved, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(scope, savedScope, 0600); err != nil {
				t.Fatal(err)
			}
			s.RetryPublicationRecovery()
			if len(s.PublicationRecoveryStatus()) != 0 {
				t.Fatal("repair still paused", s.PublicationRecoveryStatus())
			}
			reclamationDay(t, s, 1, 2)
		})
	}
}
func TestPublicationProductTakedownAndTemporaryCleanup(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	reclamationDay(t, s, 0, 2)
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "day-00:complete")
	if err != nil {
		t.Fatal(err)
	}
	id := p.refs[0].CID()
	p.Close()
	if err = s.ExcludeCatalogRecords("synthetic", "index", []string{id}); err != nil {
		t.Fatal(err)
	}
	p, err = s.SelectPublication("OCM.fbs", "synthetic", "products", "day-00")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.refs) != 1 {
		t.Fatal("excluded product still published", len(p.refs))
	}
	p.Close()
	temp := filepath.Join(s.publicationEntryDir(1), ".write-abandoned")
	if err = os.WriteFile(temp, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = journalTestStore(t, dir)
	defer s.Close()
	if _, err = os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("temporary survived startup")
	}
}
func TestPublicationUnpairedProductRefused(t *testing.T) {
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "OCM.fbs", Provider: "synthetic", Source: "products"}}); err == nil {
		t.Fatal("unbounded product source allowed")
	}
}

func TestPublicationUnappliedRequeueAndCapacityReduction(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	s.publicationFault = func(stage string) error {
		if stage == "engine" {
			return errors.New("engine unavailable")
		}
		return nil
	}
	_, rows := reclamationRecords(0, 3)
	if _, err := s.StoreBatchWithSourceTags("CAT.fbs", rows, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "repair"}); err == nil {
		t.Fatal("fault absent")
	}
	for i := 0; i < 3; i++ {
		if _, err := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "repair", &PublicationContentError{Cause: errors.New("bad attempt")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "repair", "requeue"); err != nil {
		t.Fatal("unapplied requeue failed", err)
	}
	s.publicationFault = nil
	s.RetryPublicationRecovery()
	if len(s.PublicationRecoveryStatus()) != 0 || len(s.RetainedPublicationBatches()) != 1 {
		t.Fatal("runtime requeue did not recover")
	}
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", CatalogObjectLimit: 1}}); err == nil {
		t.Fatal("capacity reduced below retained use")
	}
}

func TestPublicationReceiptCompactionContinuedBatch(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	s := journalTestStore(t, t.TempDir())
	defer s.Close()
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	for day := 0; day < 5; day++ {
		_, rows := reclamationRecords(day, 1)
		if _, err := s.StoreBatchWithSourceTags("CAT.fbs", rows, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "continued"}); err != nil {
			t.Fatal(err)
		}
		terminalPublish(t, s)
	}
	stat, err := os.Stat(filepath.Join(s.publicationDir(), "receipts.checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != 48 {
		t.Fatal("continued receipt duplicated", stat.Size())
	}
	paths, _ := filepath.Glob(filepath.Join(s.publicationDir(), "receipt-*"))
	if len(paths) != 0 {
		t.Fatal("individual receipts remain")
	}
}

func TestPublicationQuarantineStatusContinuedEntries(t *testing.T) {
	s := &FlatSQLStore{}
	e := publicationEntry{Sequence: 1, Schema: "CAT.fbs", Tags: SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "continued"}, BatchFailure: publicationFailure{Attempts: 3, Reason: "bad content"}}
	s.updatePublicationQuarantine(e)
	e.Sequence = 2
	e.BatchFailure = publicationFailure{}
	s.updatePublicationQuarantine(e)
	if len(s.PublicationRecoveryStatus()) != 1 {
		t.Fatal("later entry cleared earlier quarantine")
	}
	e.Sequence = 1
	e.Published = true
	s.updatePublicationQuarantine(e)
	if len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("completed entry remained quarantined")
	}
}
