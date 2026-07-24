package storage

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFlatSQLStoreIdentityNamespacesIsolateRecords(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	basePath := filepath.Join(t.TempDir(), "sdn")

	identity := DatastoreIdentity{
		SchemaName:      "OMM",
		SourcePeerID:    "16Uiu2HAmCatalogFixture",
		SourcePublicKey: "provider-public-key",
		ProviderID:      "space-data-network-02",
		SourceName:      "catalogfixture-gp-historical",
		BatchHead:       "feed-head-2",
		QueryProfile:    "epoch.day",
		CanonicalParams: map[string]string{"day": "2024-01-01", "fill": "as_of"},
		SnapshotID:      "snapshot-2",
		HighWaterMark:   "rowid:100",
		ArtifactHash:    "sha256:abc123",
	}
	sameIdentity := identity
	sameIdentity.SchemaName = "OMM.fbs"
	sameIdentity.CanonicalParams = map[string]string{"fill": "as_of", "day": "2024-01-01"}

	key, err := identity.Key()
	if err != nil {
		t.Fatalf("identity key failed: %v", err)
	}
	sameKey, err := sameIdentity.Key()
	if err != nil {
		t.Fatalf("same identity key failed: %v", err)
	}
	if key == "" {
		t.Fatal("identity key is empty")
	}
	if sameKey != key {
		t.Fatalf("equivalent identity key = %q, want %q", sameKey, key)
	}

	store, err := NewFlatSQLStoreForIdentity(basePath, validator, identity)
	if err != nil {
		t.Fatalf("NewFlatSQLStoreForIdentity failed: %v", err)
	}
	if store.basePath != filepath.Join(basePath, "datastores", key) {
		t.Fatalf("store basePath = %q, want identity namespace path", store.basePath)
	}
	payload := sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectID("1998-067A").
		WithObjectName("ISS").
		WithEpoch("2024-01-01T00:00:00Z").
		Build()
	cid, err := store.Store("OMM.fbs", payload, "16Uiu2HAmCatalogFixture", nil)
	if err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store failed: %v", err)
	}

	reopened, err := NewFlatSQLStoreForIdentity(basePath, validator, sameIdentity)
	if err != nil {
		t.Fatalf("reopen same identity failed: %v", err)
	}
	count, err := reopened.CountRawRecords(RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("count same identity failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("same identity count = %d, want 1", count)
	}
	if got, err := reopened.Get("OMM.fbs", cid); err != nil {
		t.Fatalf("same identity get failed: %v", err)
	} else if string(got) != string(payload) {
		t.Fatal("same identity payload mismatch")
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store failed: %v", err)
	}

	otherIdentity := identity
	otherIdentity.SourceName = "catalogfixture-gp"
	otherStore, err := NewFlatSQLStoreForIdentity(basePath, validator, otherIdentity)
	if err != nil {
		t.Fatalf("open other identity failed: %v", err)
	}
	otherCount, err := otherStore.CountRawRecords(RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("count other identity failed: %v", err)
	}
	if otherCount != 0 {
		t.Fatalf("other identity count = %d, want 0", otherCount)
	}
	if err := otherStore.Close(); err != nil {
		t.Fatalf("close other store failed: %v", err)
	}
}

func TestFlatSQLStoreIdentityNamespacesRegisterForDiscovery(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	basePath := filepath.Join(t.TempDir(), "sdn")
	first := DatastoreIdentity{
		SchemaName:    "OMM.fbs",
		SourcePeerID:  "source:legacy-sqlite",
		ProviderID:    "space-data-network-02",
		SourceName:    "catalogfixture-gp-historical",
		BatchHead:     "historical-head",
		QueryProfile:  DatasetPublicationQueryProfile,
		SnapshotID:    "historical-head",
		HighWaterMark: "historical-head",
		ArtifactHash:  "historical-head",
	}
	second := first
	second.SourceName = "catalogfixture-gp"
	second.BatchHead = "live-head"
	second.SnapshotID = "live-head"
	second.HighWaterMark = "live-head"
	second.ArtifactHash = "live-head"

	store, err := NewFlatSQLStoreForIdentity(basePath, validator, first)
	if err != nil {
		t.Fatalf("open first identity store failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first identity store failed: %v", err)
	}
	store, err = NewFlatSQLStoreForIdentity(basePath, validator, second)
	if err != nil {
		t.Fatalf("open second identity store failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close second identity store failed: %v", err)
	}
	store, err = NewFlatSQLStoreForIdentity(basePath, validator, first)
	if err != nil {
		t.Fatalf("reopen first identity store failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close reopened first identity store failed: %v", err)
	}

	entries, err := ListDatastoreIdentities(basePath)
	if err != nil {
		t.Fatalf("ListDatastoreIdentities failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("registry entries = %d, want 2: %#v", len(entries), entries)
	}
	if entries[0].Key == entries[1].Key {
		t.Fatalf("registry has duplicate keys: %#v", entries)
	}
	if entries[0].Identity.SourceName != "catalogfixture-gp" || entries[1].Identity.SourceName != "catalogfixture-gp-historical" {
		t.Fatalf("registry entries not sorted by provider/source identity: %#v", entries)
	}
}
