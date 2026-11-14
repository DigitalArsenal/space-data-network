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

// Publication upserts are journaled as typed PublicationRow frames, and the
// availability sweep records its first local observation of missing assets
// through exactly that frame. A cold boot replays the whole journal: the
// observation, and with it the retention clock, must survive.
func TestPublicationRowJournalReplayKeepsUnavailableSince(t *testing.T) {
	root := t.TempDir()
	basePath := filepath.Join(root, "store")
	store := newFixtureStore(t, basePath)
	cutoff, source := int64(41), int64(5)
	pub := DatasetShardPublication{
		SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index", BatchID: "snapshot",
		QueryProfile: DatasetPublicationQueryProfile, Offset: 0, Limit: 4, RecordCount: 4, ByteCount: 64,
		ShardCID: "synthetic-shard", IndexCID: "synthetic-index", ManifestCID: "synthetic-manifest", PNMCID: "synthetic-pnm",
		ShardSHA256:  "1111111111111111111111111111111111111111111111111111111111111111",
		IndexSHA256:  "2222222222222222222222222222222222222222222222222222222222222222",
		QuerySHA256:  "3333333333333333333333333333333333333333333333333333333333333333",
		ResultSHA256: "4444444444444444444444444444444444444444444444444444444444444444",
		FeedSequence: 1, FeedHead: "head", PublishedAt: time.Unix(1700000000, 0).UTC(),
		PublicationCutoff: &cutoff, PublicationSourceCutoff: &source,
	}
	if err := store.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatal(err)
	}
	query := DatasetShardPublicationQuery{SchemaName: pub.SchemaName, ProviderID: pub.ProviderID, SourceName: pub.SourceName,
		BatchID: pub.BatchID, QueryProfile: pub.QueryProfile, Offset: pub.Offset, Limit: pub.Limit}
	// No shard or index file was ever written: the sweep observes absence.
	observed := time.Unix(1800000000, 0)
	if retired, err := store.RetireUnavailableDatasetPublications(observed); err != nil || retired != 0 {
		t.Fatalf("first observation retired %d: %v", retired, err)
	}
	got, found, err := store.FindDatasetShardPublication(query)
	if err != nil || !found || got.UnavailableSince != observed.Unix() {
		t.Fatalf("first observation not recorded: found=%v since=%d: %v", found, got.UnavailableSince, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	coldPath := filepath.Join(root, "cold")
	copyDir(t, basePath, coldPath)
	discardControlDatabaseForTest(t, coldPath)
	cold := newFixtureStore(t, coldPath)
	defer cold.Close()
	if cold.bootAuxWarm || cold.bootAuxFrames == 0 {
		t.Fatalf("want a cold boot that replays the journal, got warm=%v frames=%d", cold.bootAuxWarm, cold.bootAuxFrames)
	}
	got, found, err = cold.FindDatasetShardPublication(query)
	if err != nil || !found {
		t.Fatalf("publication missing after journal replay: %v", err)
	}
	if got.UnavailableSince != observed.Unix() {
		t.Fatalf("journal replay changed unavailable_since %d -> %d", observed.Unix(), got.UnavailableSince)
	}
	if !reflect.DeepEqual(got.PublicationCutoff, pub.PublicationCutoff) || !reflect.DeepEqual(got.PublicationSourceCutoff, pub.PublicationSourceCutoff) {
		t.Fatal("journal replay changed the paired cutoffs")
	}
	// The clock runs from the first observation, not from the replay.
	if retired, err := cold.RetireUnavailableDatasetPublications(observed.Add(DatasetPublicationUnavailableRetention)); err != nil || retired != 1 {
		t.Fatalf("retention after journal replay retired %d, want 1: %v", retired, err)
	}
}
