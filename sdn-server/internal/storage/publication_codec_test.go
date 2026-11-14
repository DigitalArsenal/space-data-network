package storage

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

func TestPublicationMetadataFileForms(t *testing.T) {
	tags := SourceTags{ProviderID: "synthetic", SourceName: "index", SourceURL: "https://example.invalid", BatchID: "batch", ContentKeyID: "public", ProducerPeerID: "test", ProducerPublicKey: "test-public", License: "test", LicenseURL: "https://example.invalid/license", Citation: "synthetic", ShareAlike: true}
	entry := publicationEntry{Sequence: 7, SourceSequence: 3, Schema: "CAT.fbs", Tags: tags, Peer: "synthetic", Signature: []byte{1, 2}, Digest: "digest", Identity: true, OwnTS: true, Refused: map[int]int32{2: 5}}
	optionalID, optionalEpoch := uint32(0), int64(-1)
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"entry.fb", entry}, {"source.fb", publicationEntry{Sequence: 7, SourceSequence: 3, Schema: "CAT.fbs", Tags: tags}},
		{"sources.fb", []PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", ProductSource: "products", CatalogObjectLimit: 33000}}},
		{"applied", map[int]int32{2: 5}}, {"outcome", map[int]int32{}},
		{"watermarks.fb", publicationWatermarks{Global: 7, Sources: map[string]uint64{"source-b": 4, "source-a": 3}}},
		{"generation-failure.fb", publicationFailure{Attempts: 3, Reason: "invalid", RetryAt: time.Date(2026, 1, 1, 1, 2, 3, 4, time.UTC)}},
		{"batch-failure.fb", publicationFailure{Attempts: 1, Reason: "invalid"}},
		{"generation-abort.fb", []uint64{2, 7}},
		{"exclude.fb", map[string]bool{"synthetic-b": true, "synthetic-a": false}},
		{"exclude.fb.products", map[string]bool{"synthetic-product": true}},
		{"discard-effects.fb", map[string]string{"source-b": "discarded", "source-a": "discarded"}},
		{"tag-identity", tags},
		{"reclamation.fb", publicationReclaimCheckpoint{HighWater: 7, Reclaimed: 3, Pending: []uint64{2, 4}}},
		{"ordinary.index.fb", DatasetExportIndex{Version: 1, SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BatchID: "batch", QuerySHA256: "query", ResultSHA256: "result", ShardSHA256: "shard", ShardCID: "cid", ShardFile: "shard.fb", RecordCount: 1, Records: []DatasetExportIndexRecord{{CID: "synthetic", Offset: 4, Length: 16, SourceTags: tags, NoradCatID: &optionalID, EpochUnix: &optionalEpoch, EntityID: "synthetic", ObjectType: "PAYLOAD", OpsStatusCode: "+", EpochDay: "synthetic-day"}}, PublicationCutoff: 7, PublicationSourceCutoff: 3, PublicationSourceSequences: []uint64{3}, Indexes: DatasetExportMaterializedMap{ByNORAD: map[string][]int{"0": {0}}, ByEntityID: map[string][]int{"synthetic": {0}}, ByObjectType: map[string][]int{"PAYLOAD": {0}}, ByOpsStatusCode: map[string][]int{"+": {0}}, CAReadyResident: []int{0}, ActivePayloads: []int{0}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := marshalPublication(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			target := reflect.New(reflect.TypeOf(tc.value)).Interface()
			if err := unmarshalPublication(raw, target); err != nil {
				t.Fatal(err)
			}
			again, err := marshalPublication(reflect.ValueOf(target).Elem().Interface())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, again) {
				t.Fatal("round trip changed metadata fields")
			}
			flipped := bytes.Clone(raw)
			flipped[len(flipped)-1] ^= 1
			for _, data := range [][]byte{raw[:len(raw)-1], flipped} {
				if err := unmarshalPublication(data, target); err == nil {
					t.Fatal("accepted damaged metadata")
				}
			}
		})
	}
}

func TestPublicationTagIdentityDeterministic(t *testing.T) {
	tags := SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "batch", License: "test"}
	first := publicationTagIdentity("CAT.fbs", "synthetic", tags)
	for i := 0; i < 8; i++ {
		if publicationTagIdentity("CAT.fbs", "synthetic", tags) != first {
			t.Fatal("unstable tag identity")
		}
	}
	tags.ShareAlike = true
	if publicationTagIdentity("CAT.fbs", "synthetic", tags) == first {
		t.Fatal("tag field absent from identity")
	}
}
