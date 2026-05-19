package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFlatSQLStoreRecordsDurablePinLedgerEntries(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	entry := PinLedgerEntry{
		CID:               "bafkshard",
		SchemaName:        "OMM.fbs",
		ProviderPeerID:    "16Uiu2HCelesTrak",
		ProviderPublicKey: "provider-public-key",
		ProviderID:        "space-data-network-02",
		SourceName:        "celestrak-gp",
		BatchID:           "batch-001",
		QueryProfile:      DatasetPublicationQueryProfile,
		SnapshotID:        "head-2",
		Head:              "head-2",
		HighWaterMark:     "published-feed-v1:1778436120:1:50000:8000000",
		ByteHash:          "sha256:shard",
		Role:              "shard",
		RowCount:          50000,
		ByteCount:         8_000_000,
		TTL:               24 * time.Hour,
		VerificationState: "verified",
		VerifiedAt:        time.Unix(1_778_436_120, 0).UTC(),
	}
	if err := store.UpsertPinLedgerEntry(entry); err != nil {
		t.Fatalf("UpsertPinLedgerEntry failed: %v", err)
	}

	entries, err := store.ListPinLedgerEntries(PinLedgerQuery{
		SchemaName:     "OMM.fbs",
		ProviderPeerID: "16Uiu2HCelesTrak",
		QueryProfile:   DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("ListPinLedgerEntries failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %#v", len(entries), entries)
	}
	got := entries[0]
	if got.CID != entry.CID || got.ByteHash != entry.ByteHash || got.Role != "shard" || got.VerificationState != "verified" {
		t.Fatalf("unexpected pin ledger entry: %#v", got)
	}
	if got.RowCount != entry.RowCount || got.HighWaterMark != entry.HighWaterMark {
		t.Fatalf("pin row/head metadata was not preserved: %#v", got)
	}
	if got.TTL != 24*time.Hour || got.VerifiedAt.Unix() != entry.VerifiedAt.Unix() {
		t.Fatalf("pin timing was not preserved: %#v", got)
	}
}

func TestFlatSQLStoreReportsLocalReplicaStatsFromRowsAndPinLedger(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	tags := SourceTags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: "batch-001", ContentKeyID: "public"}
	if _, err := store.StoreWithSourceTags("OMM.fbs", sds.NewOMMBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS").
		WithEpoch("2026-05-12T00:00:00Z").
		Build(), "source:celestrak", nil, tags); err != nil {
		t.Fatalf("store OMM failed: %v", err)
	}
	verifiedAt := time.Unix(1_778_436_120, 0).UTC()
	for _, entry := range []PinLedgerEntry{
		{
			CID:               "bafkshard-a",
			SchemaName:        "OMM.fbs",
			ProviderPeerID:    "16Uiu2HCelesTrak",
			ProviderPublicKey: "provider-public-key",
			ProviderID:        "space-data-network-02",
			SourceName:        "celestrak-gp",
			BatchID:           "batch-001",
			QueryProfile:      DatasetPublicationQueryProfile,
			SnapshotID:        "head-2",
			Head:              "head-2",
			HighWaterMark:     "published-feed-v1:1778436120:2:75000:12000000",
			ByteHash:          "sha256:shard-a",
			Role:              "shard",
			RowCount:          50000,
			ByteCount:         8_000_000,
			VerificationState: "verified",
			VerifiedAt:        verifiedAt,
		},
		{
			CID:               "bafkshard-b",
			SchemaName:        "OMM.fbs",
			ProviderPeerID:    "16Uiu2HCelesTrak",
			ProviderPublicKey: "provider-public-key",
			ProviderID:        "space-data-network-02",
			SourceName:        "celestrak-gp",
			BatchID:           "batch-001",
			QueryProfile:      DatasetPublicationQueryProfile,
			SnapshotID:        "head-3",
			Head:              "head-3",
			HighWaterMark:     "published-feed-v1:1778436120:2:75000:12000000",
			ByteHash:          "sha256:shard-b",
			Role:              "shard",
			RowCount:          25000,
			ByteCount:         4_000_000,
			VerificationState: "verified",
			VerifiedAt:        verifiedAt.Add(time.Minute),
		},
	} {
		if err := store.UpsertPinLedgerEntry(entry); err != nil {
			t.Fatalf("UpsertPinLedgerEntry failed: %v", err)
		}
	}

	stats, err := store.LocalReplicaStats(LocalReplicaStatsQuery{
		SchemaName:   "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		BatchID:      "batch-001",
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatalf("LocalReplicaStats failed: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats = %d, want 1: %#v", len(stats), stats)
	}
	got := stats[0]
	if got.LocalRows != 1 || got.PinnedRows != 75000 || got.PinnedBytes != 12_000_000 {
		t.Fatalf("unexpected row/byte stats: %#v", got)
	}
	if got.Head != "head-3" || got.SnapshotID != "head-3" || got.HighWaterMark != "published-feed-v1:1778436120:2:75000:12000000" {
		t.Fatalf("unexpected head stats: %#v", got)
	}
	if got.LastSyncedAt.Unix() != verifiedAt.Add(time.Minute).Unix() {
		t.Fatalf("last synced = %s, want %s", got.LastSyncedAt, verifiedAt.Add(time.Minute))
	}
}
