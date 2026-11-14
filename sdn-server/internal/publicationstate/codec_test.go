package publicationstate

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

func TestPublicationStateIntegrity(t *testing.T) {
	bodies := []*pst.BodyT{
		{Type: pst.BodyJournalEntry, Value: &pst.JournalEntryT{Sequence: 7, Schema: "CAT.fbs", Tags: &pst.SourceTagsT{ProviderId: "synthetic"}, Refused: []*pst.RefusalT{}}},
		{Type: pst.BodySourceList, Value: &pst.SourceListT{Sources: []*pst.RetainedSourceT{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}}},
		{Type: pst.BodyOutcome, Value: &pst.OutcomeT{Refused: []*pst.RefusalT{{Index: 2, Code: 3}}}},
		{Type: pst.BodyWatermarks, Value: &pst.WatermarksT{Global: 7, Sources: []*pst.SourceSequenceT{{Id: "source", Sequence: 3}}}},
		{Type: pst.BodyFailure, Value: &pst.FailureT{Attempts: 3, Reason: "invalid stream", RetryAt: "2026-01-01T00:00:00Z"}},
		{Type: pst.BodySequenceList, Value: &pst.SequenceListT{Sequences: []uint64{2, 7}}},
		{Type: pst.BodyExclusions, Value: &pst.ExclusionsT{Entries: []*pst.ExclusionT{{Cid: "synthetic", Excluded: true}}}},
		{Type: pst.BodySourceTags, Value: &pst.SourceTagsT{ProviderId: "synthetic", SourceName: "index"}},
		{Type: pst.BodyNoticeList, Value: &pst.NoticeListT{Entries: []*pst.NoticeT{{Key: "source", Reason: "discarded"}}}},
		{Type: pst.BodyReclamation, Value: &pst.ReclamationT{HighWater: 7, Reclaimed: 2, Pending: []uint64{3}}},
		{Type: pst.BodyExportIndex, Value: &pst.ExportIndexT{Version: 1, SchemaName: "CAT.fbs", RecordCount: 1, PublicationSourceSequences: []uint64{}, Records: []*pst.IndexRecordT{{Cid: "synthetic", Length: 16}}}},
		{Type: pst.BodyBatchAction, Value: &pst.BatchActionT{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", Batch: "batch", Action: "requeue"}},
	}
	for _, body := range bodies {
		t.Run(body.Type.String(), func(t *testing.T) {
			raw := Encode(body)
			decoded, err := Decode(raw, body.Type)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, Encode(decoded)) {
				t.Fatal("noncanonical round trip")
			}
			for end := 0; end < len(raw); end++ {
				if _, err := Decode(raw[:end], body.Type); err == nil {
					t.Fatalf("accepted torn write at %d", end)
				}
			}
			for i := range raw {
				corrupt := bytes.Clone(raw)
				corrupt[i] ^= 1
				if _, err := Decode(corrupt, body.Type); err == nil {
					t.Fatalf("accepted flipped bit at %d", i)
				}
			}
			if _, err := Decode(append(bytes.Clone(raw), 0), body.Type); err == nil {
				t.Fatal("accepted trailing byte")
			}
			if _, err := Decode(raw, pst.BodyNONE); err == nil {
				t.Fatal("accepted wrong record kind")
			}
		})
	}
}

func TestPublicationStateVerifierBeforeAccessors(t *testing.T) {
	raw := Encode(&pst.BodyT{Type: pst.BodySourceTags, Value: &pst.SourceTagsT{ProviderId: "synthetic"}})
	// Destroy the root offset independently of the checksum. The structural
	// verifier must return an error, without accessing or unpacking that table.
	binary.LittleEndian.PutUint32(raw[4:], ^uint32(0))
	if _, err := Decode(raw, pst.BodySourceTags); err == nil {
		t.Fatal("accepted invalid root offset")
	}
}
