package storage

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestPublicationRowTypedRecoveryAndCutoffs(t *testing.T) {
	cutoff, source, zero := int64(33), int64(7), int64(0)
	base := DatasetShardPublication{
		SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BatchID: "snapshot",
		QueryProfile: DatasetPublicationQueryProfile, Offset: 8, Limit: 8, RecordCount: 8, ByteCount: 128,
		ShardCID: "synthetic-shard", IndexCID: "synthetic-index", ManifestCID: "synthetic-manifest", PNMCID: "synthetic-pnm",
		ShardSHA256: "shard-hash", IndexSHA256: "index-hash", QuerySHA256: "query-hash", ResultSHA256: "result-hash",
		FeedSequence: 9, PreviousHead: "previous", FeedHead: "head", PublishedAt: time.Unix(123, 456).UTC(),
		PublicationCutoff: &cutoff, PublicationSourceCutoff: &source,
	}
	for _, pair := range [][2]*int64{{&cutoff, &source}, {nil, nil}, {&zero, &zero}} {
		pub := base
		pub.PublicationCutoff, pub.PublicationSourceCutoff = pair[0], pair[1]
		raw, err := encodeAuxiliaryMetadataEvent(auxiliaryMetadataEvent{Kind: auxiliaryEventDatasetShardPublicationUpsert, DatasetShardPublication: &pub})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) < 12 || !bytes.Equal(raw[8:12], []byte("$PST")) {
			t.Fatal("publication recovery record is not a typed FlatBuffer")
		}
		decoded, err := decodeAuxiliaryMetadataEvent(raw)
		if err != nil || decoded.Kind != auxiliaryEventDatasetShardPublicationUpsert || !reflect.DeepEqual(decoded.DatasetShardPublication, &pub) {
			t.Fatalf("publication recovery round trip: %v", err)
		}
	}
	store, root := newLicenseTestStore(t)
	ordinary := base
	ordinary.BatchID = "ordinary"
	ordinary.PublicationCutoff, ordinary.PublicationSourceCutoff = nil, nil
	for _, pub := range []DatasetShardPublication{base, ordinary} {
		if err := store.UpsertDatasetShardPublication(pub); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFlatSQLStore(filepath.Join(root, "db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, pub := range []DatasetShardPublication{base, ordinary} {
		got, found, err := reopened.FindDatasetShardPublication(DatasetShardPublicationQuery{SchemaName: pub.SchemaName, ProviderID: pub.ProviderID, SourceName: pub.SourceName, BatchID: pub.BatchID, QueryProfile: pub.QueryProfile, Offset: pub.Offset, Limit: pub.Limit})
		if err != nil || !found {
			t.Fatalf("publication missing after reopen: %v", err)
		}
		if !reflect.DeepEqual(got.PublicationCutoff, pub.PublicationCutoff) || !reflect.DeepEqual(got.PublicationSourceCutoff, pub.PublicationSourceCutoff) {
			t.Fatal("nullable cutoffs changed after reopen")
		}
	}
}
