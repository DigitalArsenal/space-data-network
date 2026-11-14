package storage

import (
	"encoding/binary"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationSelectionLimitIsContentError(t *testing.T) {
	f := &publicationRefFile{Tags: SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "oversized"}}
	refs := make([]publicationRecordRef, maxPublicationSelectionRecords+1)
	for i := range refs {
		refs[i].file = f
		binary.LittleEndian.PutUint32(refs[i].Digest[:4], uint32(i))
	}
	s := &FlatSQLStore{basePath: t.TempDir(), publicationEntries: []publicationEntry{{Applied: true, ReadReady: true, Sequence: 1, Schema: "CAT.fbs", Tags: f.Tags, Refs: refs}}}
	_, err := s.retainedSelectionLocked("CAT.fbs", "synthetic", "index", "oversized", 1)
	if !IsPublicationContentError(err) {
		t.Fatalf("oversized immutable selection remains retryable: %v", err)
	}
}

func TestPublicationNeverAppliedDamageDoesNotBlockSource(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, damage := range []string{"checksum", "truncated", "missing"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			s := journalTestStore(t, dir)
			if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
				t.Fatal(err)
			}
			s.publicationFault = func(step string) error {
				if step == "engine" {
					return errors.New("stopped before engine write")
				}
				return nil
			}
			_, rows := reclamationRecords(0, 1)
			if _, err := s.StoreBatchWithSourceTags("CAT.fbs", rows, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "bad"}); err == nil {
				t.Fatal("fault not reached")
			}
			s.publicationFault = nil
			entry := s.publicationEntries[0]
			if entry.Applied {
				t.Fatal("fixture was applied")
			}
			path := filepath.Join(s.publicationEntryDir(entry.Sequence), "records.sds")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "checksum":
				raw[len(raw)-1] ^= 1
				err = os.WriteFile(path, raw, 0600)
			case "truncated":
				err = os.Truncate(path, 5)
			case "missing":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.publicationReferences(entry)
			if !IsPublicationContentError(err) {
				t.Fatalf("unclassified immutable input: %v", err)
			}
			for i := 0; i < 3; i++ {
				if _, saveErr := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "bad", err); saveErr != nil {
					t.Fatal(saveErr)
				}
			}
			s.RetryPublicationRecovery()
			journalTestPut(t, s, "good")
			if batches := s.RetainedPublicationBatches(); len(batches) != 1 || batches[0].Batch != "good" {
				t.Fatalf("blocked source: %+v", batches)
			}
			s.Close()
			s = journalTestStore(t, dir)
			defer s.Close()
			if batches := s.RetainedPublicationBatches(); len(batches) != 1 || batches[0].Batch != "good" {
				t.Fatalf("restart blocked source: %+v", batches)
			}
			if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "bad", "discard"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationOperatorManagesAnyUnfinishedBatch(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, attempts := range []int{0, 1, 2, 3} {
		s := journalTestStore(t, t.TempDir())
		if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
			t.Fatal(err)
		}
		journalTestPut(t, s, "batch")
		for i := 0; i < attempts; i++ {
			if _, err := s.RecordPublicationFailure("CAT.fbs", "synthetic", "index", "batch", publicationContentError(errors.New("bad bytes"))); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "batch", "requeue"); err != nil {
			t.Fatal(err)
		}
		if s.publicationEntries[0].BatchFailure.Attempts != 0 {
			t.Fatal("requeue ignored")
		}
		if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "batch", "discard"); err != nil {
			t.Fatal(err)
		}
		if !s.PublicationBatchComplete("CAT.fbs", "synthetic", "index", "batch") {
			t.Fatal("discard ignored")
		}
		s.Close()
	}
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
		t.Fatal(err)
	}
	s.publicationFault = func(step string) error {
		if step == "engine" {
			return errors.New("stopped before engine write")
		}
		return nil
	}
	_, err := s.StoreBatchWithSourceTags("CAT.fbs", [][]byte{sds.NewCATBuilder().WithNoradCatID(900001).WithObjectName("synthetic-test").Build()}, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "unapplied"})
	if err == nil {
		t.Fatal("fault not reached")
	}
	s.publicationFault = nil
	if err := s.ManagePublicationBatch("CAT.fbs", "synthetic", "index", "unapplied", "discard"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.publicationEntryDir(1), "discarded")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = journalTestStore(t, dir)
	defer s.Close()
	s.RetryPublicationRecovery()
	if len(s.RetainedPublicationBatches()) != 0 || len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("discard replayed or paused source")
	}
}
