package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationReceiptTailAndSourceIsolation(t *testing.T) {
	for _, tail := range []bool{true, false} {
		t.Run(fmt.Sprint(tail), func(t *testing.T) {
			s := &FlatSQLStore{basePath: t.TempDir(), publicationSources: []PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "a"}, {Schema: "CAT.fbs", Provider: "synthetic", Source: "b"}}}
			if err := os.MkdirAll(s.publicationDir(), 0700); err != nil {
				t.Fatal(err)
			}
			for _, source := range []string{"a", "b"} {
				for i := 1; i <= 3; i++ {
					e := publicationEntry{Schema: "CAT.fbs", Sequence: uint64(i), Tags: SourceTags{ProviderID: "synthetic", SourceName: source, BatchID: fmt.Sprint(i)}}
					if err := s.savePublicationReceipt(e); err != nil {
						t.Fatal(err)
					}
				}
			}
			path := filepath.Join(s.publicationDir(), publicationReceiptFile("CAT.fbs", "synthetic", "a"))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tail {
				clear(raw[len(raw)-48:])
			} else {
				raw[48+40] ^= 1
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = s.recoverPublicationReceipts(); err != nil {
				t.Fatal(err)
			}
			if tail {
				stat, _ := os.Stat(path)
				if stat.Size() != 96 || len(s.publicationFailures) != 0 {
					t.Fatal("tail not recovered", stat.Size(), s.publicationFailures)
				}
			} else if len(s.publicationFailures) != 1 || s.publicationFailures[publicationSourceKey{"CAT.fbs", "synthetic", "a"}] == nil {
				t.Fatal("middle corruption not isolated", s.publicationFailures)
			}
			id := sha256.Sum256([]byte("CAT.fbs\x00synthetic\x00b\x003"))
			if s.publicationReceipts[id] != 3 {
				t.Fatal("unrelated receipts lost")
			}
		})
	}
}

func TestPublicationCorruptWatermarksRebuild(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := reclamationStore(t, dir)
	reclamationDay(t, s, 0, 2)
	terminalPublish(t, s)
	reclamationDay(t, s, 1, 2)
	terminalPublish(t, s)
	high := s.publicationHighWater
	path := filepath.Join(s.publicationDir(), "watermarks.json")
	s.Close()
	if err := os.WriteFile(path, []byte("{torn"), 0600); err != nil {
		t.Fatal(err)
	}
	s = journalTestStore(t, dir)
	defer s.Close()
	if len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("watermark corruption stopped sources", s.PublicationRecoveryStatus())
	}
	reclamationDay(t, s, 2, 2)
	for _, e := range s.publicationEntries {
		if e.Sequence > high && e.SourceSequence <= high {
			t.Fatal("rebuilt sequence reused an old cursor")
		}
	}
}
