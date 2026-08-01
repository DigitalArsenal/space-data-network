package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func newArchiveTestStore(t *testing.T) (*FlatSQLStore, string, SourceTags) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "flatsql-archive-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-satcat-csv",
		SourceURL:    "https://fixture.test/pub/satcat.csv",
		BatchID:      "source-sha-001",
		ContentKeyID: "public",
	}
	recordA := sds.NewCATBuilder().
		WithNoradCatID(25544).
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	recordB := sds.NewCATBuilder().
		WithNoradCatID(40909).
		WithObjectName("SATELLITE-1001").
		WithObjectID("2015-049A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordA, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record A failed: %v", err)
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", recordB, "source:catalogfixture", nil, tags); err != nil {
		t.Fatalf("store record B failed: %v", err)
	}
	return store, tmpDir, tags
}

func archiveTestFilter(tags SourceTags) IndexedRecordQuery {
	return IndexedRecordQuery{
		SchemaName: "CAT.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		BatchID:    tags.BatchID,
	}
}

func TestArchiveDatasetSelectionPinsPermanentLedgerEntries(t *testing.T) {
	store, tmpDir, tags := newArchiveTestStore(t)
	providerPublicKey, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x51}, 128)))
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	heads := []ArchiveSourceFeedHead{{
		SchemaName:   "CAT.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: DatasetPublicationQueryProfile,
		FeedHead:     "published-feed-v1:1234:2:2:512",
		ManifestCID:  "bafysourcefeedheadmanifest",
	}}
	archive, err := store.ArchiveDatasetSelection(context.Background(), archiveTestFilter(tags), ArchiveDatasetOptions{
		ArchiveID:       "archive-cat-payloads-1",
		ProviderPeerID:  "12D3KooWArchiveTest",
		SigningKey:      signingKey,
		PublishedAt:     time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		SourceFeedHeads: heads,
	})
	if err != nil {
		t.Fatalf("ArchiveDatasetSelection failed: %v", err)
	}
	if archive.Export == nil || archive.Manifest == nil {
		t.Fatal("archive is missing export or manifest")
	}

	// The archive plane is its own directory, a sibling of dataset-publications.
	archiveDir := filepath.Join(filepath.Dir(filepath.Join(tmpDir, "db")), "dataset-archives")
	if !strings.HasPrefix(archive.Export.ShardPath, archiveDir) {
		t.Fatalf("archive shard %s not under archive plane dir %s", archive.Export.ShardPath, archiveDir)
	}
	if !strings.HasPrefix(archive.Manifest.Path, archiveDir) {
		t.Fatalf("archive manifest %s not under archive plane dir %s", archive.Manifest.Path, archiveDir)
	}
	for _, path := range []string{archive.Export.ShardPath, archive.Export.IndexPath, archive.Manifest.Path} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			t.Fatalf("archive artifact %s missing: %v", path, err)
		}
	}

	// The provider signature must verify with the feed-head auxiliary assets
	// embedded (rebuildUnsignedDatasetManifest round-trip).
	evidence, err := VerifySignedDatasetPublicationManifest(archive.Manifest.Bytes, providerPublicKey)
	if err != nil {
		t.Fatalf("archive manifest signature did not verify: %v", err)
	}
	if evidence.ManifestCID != archive.Manifest.CID {
		t.Fatalf("manifest CID = %s, want %s", evidence.ManifestCID, archive.Manifest.CID)
	}

	manifest := dpm.GetRootAsDPM(archive.Manifest.Bytes, 0)
	query := manifest.QUERY(nil)
	if query == nil {
		t.Fatal("archive DPM missing query binding")
	}
	if string(query.CANONICAL_QUERY()) != archive.Export.CanonicalQuery {
		t.Fatalf("canonical query mismatch: %s", string(query.CANONICAL_QUERY()))
	}
	foundFeedHead := false
	for i := 0; i < manifest.ASSETSLength(); i++ {
		var asset dpm.DPMAsset
		if !manifest.ASSETS(&asset, i) {
			continue
		}
		if asset.ASSET_KIND().String() != "OTHER" {
			continue
		}
		if string(asset.CID()) != "bafysourcefeedheadmanifest" {
			t.Fatalf("feed-head asset CID = %s", string(asset.CID()))
		}
		if string(asset.FILE_NAME()) != "SOURCE_FEED_HEAD" {
			t.Fatalf("feed-head asset FILE_NAME = %s", string(asset.FILE_NAME()))
		}
		if string(asset.DATA_ROOT()) != "published-feed-v1:1234:2:2:512" {
			t.Fatalf("feed-head asset DATA_ROOT = %s", string(asset.DATA_ROOT()))
		}
		foundFeedHead = true
	}
	if !foundFeedHead {
		t.Fatal("archive DPM has no OTHER-kind source feed-head asset")
	}

	// Pin ledger: shard + index + manifest, role='archive', TTL=0 (permanent).
	entries, err := store.ListPinLedgerEntries(PinLedgerQuery{Role: PinLedgerRoleArchive})
	if err != nil {
		t.Fatalf("list archive pin ledger entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("archive pin ledger entries = %d, want 3", len(entries))
	}
	wantCIDs := map[string]bool{
		archive.Export.ShardCID: false,
		archive.Export.IndexCID: false,
		archive.Manifest.CID:    false,
	}
	for _, entry := range entries {
		if entry.TTL != 0 {
			t.Fatalf("archive entry %s TTL = %v, want 0 (permanent)", entry.CID, entry.TTL)
		}
		if entry.QueryProfile != ArchiveQueryProfile {
			t.Fatalf("archive entry %s query profile = %q", entry.CID, entry.QueryProfile)
		}
		if entry.VerificationState != "verified" {
			t.Fatalf("archive entry %s verification state = %q", entry.CID, entry.VerificationState)
		}
		if entry.SnapshotID != archive.Manifest.CID {
			t.Fatalf("archive entry %s snapshot = %q, want manifest CID", entry.CID, entry.SnapshotID)
		}
		if _, ok := wantCIDs[entry.CID]; !ok {
			t.Fatalf("unexpected archive pin ledger CID %s", entry.CID)
		}
		wantCIDs[entry.CID] = true
	}
	for cid, seen := range wantCIDs {
		if !seen {
			t.Fatalf("archive pin ledger missing CID %s", cid)
		}
	}

	if !store.IsArchivePinnedCID(archive.Export.ShardCID) {
		t.Fatal("IsArchivePinnedCID should protect the archive shard CID")
	}
	if store.IsArchivePinnedCID("bafyunrelatedcid") {
		t.Fatal("IsArchivePinnedCID should not protect unrelated CIDs")
	}

	// The archive must never appear in the replication plane.
	if pubs, err := store.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   "CAT.fbs",
		QueryProfile: ArchiveQueryProfile,
	}); err != nil {
		t.Fatalf("list publications: %v", err)
	} else if len(pubs) != 0 {
		t.Fatalf("archive leaked into sdn_dataset_shard_publications: %d rows", len(pubs))
	}
}

func TestUpsertDatasetShardPublicationRefusesArchiveQueryProfile(t *testing.T) {
	store, _, tags := newArchiveTestStore(t)
	err := store.UpsertDatasetShardPublication(DatasetShardPublication{
		SchemaName:   "CAT.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: ArchiveQueryProfile,
		Limit:        10,
		RecordCount:  2,
		ShardCID:     "bafyarchive-shard",
		IndexCID:     "bafyarchive-index",
	})
	if err == nil {
		t.Fatal("archive query profile must be refused by UpsertDatasetShardPublication")
	}
	if !strings.Contains(err.Error(), "pin-ledger-only") {
		t.Fatalf("unexpected refusal error: %v", err)
	}
}

func TestArchiveSourceFeedHeadsDerivedFromPublications(t *testing.T) {
	store, _, tags := newArchiveTestStore(t)

	publishedAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	if err := store.UpsertDatasetShardPublication(DatasetShardPublication{
		SchemaName:   "CAT.fbs",
		ProviderID:   tags.ProviderID,
		SourceName:   tags.SourceName,
		BatchID:      tags.BatchID,
		QueryProfile: DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        100,
		RecordCount:  2,
		ByteCount:    512,
		ShardCID:     "bafyheadshard",
		IndexCID:     "bafyheadindex",
		ManifestCID:  "bafyheadmanifest",
		ShardSHA256:  strings.Repeat("ab", 32),
		IndexSHA256:  strings.Repeat("cd", 32),
		QuerySHA256:  strings.Repeat("ef", 32),
		ResultSHA256: strings.Repeat("ab", 32),
		PublishedAt:  publishedAt,
	}); err != nil {
		t.Fatalf("seed publication failed: %v", err)
	}

	heads, err := store.ArchiveSourceFeedHeads(archiveTestFilter(tags))
	if err != nil {
		t.Fatalf("ArchiveSourceFeedHeads failed: %v", err)
	}
	if len(heads) != 1 {
		t.Fatalf("derived heads = %d, want 1", len(heads))
	}
	head := heads[0]
	if head.ManifestCID != "bafyheadmanifest" {
		t.Fatalf("derived head manifest CID = %q", head.ManifestCID)
	}
	if head.FeedHead == "" {
		t.Fatal("derived head has no feed-head chain value")
	}
	if head.ProviderID != tags.ProviderID || head.SourceName != tags.SourceName || head.BatchID != tags.BatchID {
		t.Fatalf("derived head keys mismatch: %+v", head)
	}
}

func TestArchiveDatasetSelectionRequiresFeedHeadManifestCID(t *testing.T) {
	store, _, tags := newArchiveTestStore(t)
	_, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x52}, 128)))
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	_, err = store.ArchiveDatasetSelection(context.Background(), archiveTestFilter(tags), ArchiveDatasetOptions{
		ArchiveID:      "archive-missing-head",
		ProviderPeerID: "12D3KooWArchiveTest",
		SigningKey:     signingKey,
		SourceFeedHeads: []ArchiveSourceFeedHead{{
			SchemaName: "CAT.fbs",
			ProviderID: tags.ProviderID,
			SourceName: tags.SourceName,
			FeedHead:   "published-feed-v1:1:1:1:1",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "no manifest CID") {
		t.Fatalf("expected missing-manifest-CID refusal, got: %v", err)
	}
}
