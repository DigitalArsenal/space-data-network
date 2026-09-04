package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func newImportArchiveTargetStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "flatsql-archive-import-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestImportArchiveFromManifestKeepsTheOriginalProducer(t *testing.T) {
	source, _, tags := newArchiveTestStore(t)
	providerPublicKey, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x61}, 128)))
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const producer = "12D3KooWArchiveImportProducer"
	archive, err := source.ArchiveDatasetSelection(context.Background(), archiveTestFilter(tags), ArchiveDatasetOptions{
		ArchiveID:       "archive-cat-import-1",
		ProviderPeerID:  producer,
		SigningKey:      signingKey,
		PublishedAt:     time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
		SourceFeedHeads: []ArchiveSourceFeedHead{},
	})
	if err != nil {
		t.Fatalf("ArchiveDatasetSelection: %v", err)
	}

	target := newImportArchiveTargetStore(t)
	result, err := ImportArchiveFromManifest(context.Background(), target, ImportArchiveOptions{
		ManifestBytes:     archive.Manifest.Bytes,
		ProviderPublicKey: providerPublicKey,
		AssetDir:          source.ArchiveOutputDir(),
	})
	if err != nil {
		t.Fatalf("ImportArchiveFromManifest: %v", err)
	}
	if result.Imported != 2 || result.RecordCount != 2 || result.SchemaName != "CAT.fbs" {
		t.Fatalf("result = %+v, want 2 CAT records imported", result)
	}
	if result.ManifestCID != archive.Manifest.CID || result.ShardCID != archive.Export.ShardCID {
		t.Fatalf("result CIDs = %s / %s, want %s / %s", result.ManifestCID, result.ShardCID, archive.Manifest.CID, archive.Export.ShardCID)
	}

	summary, err := target.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	var lane *DataSourceSummary
	for i := range summary.Sources {
		src := &summary.Sources[i]
		if src.SchemaName == "CAT.fbs" && src.ProviderID == tags.ProviderID && src.SourceName == tags.SourceName {
			lane = src
		}
	}
	if lane == nil {
		t.Fatalf("imported lane missing from summary: %+v", summary.Sources)
	}
	if lane.Count != 2 {
		t.Fatalf("imported lane count = %d, want 2", lane.Count)
	}
	if lane.ProducerPeerID != producer {
		t.Fatalf("imported producer = %q, want the archive's provider %q", lane.ProducerPeerID, producer)
	}

	// The import is idempotent and every asset is ledgered as a permanent
	// archive pin on the importing node, with the manifest held locally.
	entries, err := target.ListPinLedgerEntries(PinLedgerQuery{Role: PinLedgerRoleArchive, BatchID: "archive-cat-import-1"})
	if err != nil {
		t.Fatalf("ListPinLedgerEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("archive pin ledger rows = %d, want 3 (shard, index, manifest)", len(entries))
	}
	for _, entry := range entries {
		if entry.ProviderPeerID != producer || entry.SnapshotID != archive.Manifest.CID || entry.TTL != 0 {
			t.Fatalf("ledger row %+v does not carry the archive identity", entry)
		}
	}
	held := filepath.Join(target.ArchiveOutputDir(), "manifests", filepath.Base(archive.Manifest.Path))
	if _, err := os.Stat(held); err != nil {
		t.Fatalf("manifest not held on the importing node at %s: %v", held, err)
	}
	if _, err := ImportArchiveFromManifest(context.Background(), target, ImportArchiveOptions{
		ManifestBytes:     archive.Manifest.Bytes,
		ProviderPublicKey: providerPublicKey,
		AssetDir:          source.ArchiveOutputDir(),
	}); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again, _ := target.DataSummary(); again.TotalRecords != summary.TotalRecords {
		t.Fatalf("second import changed the record count: %d -> %d", summary.TotalRecords, again.TotalRecords)
	}
}

func TestImportArchiveRejectsTamperedAssetsAndWrongKeys(t *testing.T) {
	source, _, tags := newArchiveTestStore(t)
	providerPublicKey, signingKey, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x62}, 128)))
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	archive, err := source.ArchiveDatasetSelection(context.Background(), archiveTestFilter(tags), ArchiveDatasetOptions{
		ArchiveID:       "archive-cat-tamper-1",
		ProviderPeerID:  "12D3KooWArchiveTamperProducer",
		SigningKey:      signingKey,
		SourceFeedHeads: []ArchiveSourceFeedHead{},
	})
	if err != nil {
		t.Fatalf("ArchiveDatasetSelection: %v", err)
	}

	// Wrong key: the signature does not verify and nothing is read.
	otherKey, _, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{0x63}, 128)))
	target := newImportArchiveTargetStore(t)
	if _, err := ImportArchiveFromManifest(context.Background(), target, ImportArchiveOptions{
		ManifestBytes:     archive.Manifest.Bytes,
		ProviderPublicKey: otherKey,
		AssetDir:          source.ArchiveOutputDir(),
	}); err == nil {
		t.Fatalf("import verified with a foreign key")
	}

	// Tampered shard: copy the plane, flip a byte in the shard, import fails
	// and the target stays empty.
	planeCopy := filepath.Join(t.TempDir(), "dataset-archives")
	if err := os.CopyFS(planeCopy, os.DirFS(source.ArchiveOutputDir())); err != nil {
		t.Fatalf("copy archive plane: %v", err)
	}
	shardCopy := filepath.Join(planeCopy, "shards", filepath.Base(archive.Export.ShardPath))
	shardBytes, err := os.ReadFile(shardCopy)
	if err != nil {
		t.Fatalf("read shard copy: %v", err)
	}
	shardBytes[len(shardBytes)-1] ^= 0xff
	if err := os.WriteFile(shardCopy, shardBytes, 0o600); err != nil {
		t.Fatalf("tamper shard copy: %v", err)
	}
	if _, err := ImportArchiveFromManifest(context.Background(), target, ImportArchiveOptions{
		ManifestBytes:     archive.Manifest.Bytes,
		ProviderPublicKey: providerPublicKey,
		AssetDir:          planeCopy,
	}); err == nil {
		t.Fatalf("import accepted a tampered shard")
	}
	summary, err := target.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if summary.TotalRecords != 0 {
		t.Fatalf("tampered import stored %d records, want 0", summary.TotalRecords)
	}
	if entries, _ := target.ListPinLedgerEntries(PinLedgerQuery{Role: PinLedgerRoleArchive}); len(entries) != 0 {
		t.Fatalf("tampered import ledgered %d rows, want 0", len(entries))
	}

	// A missing asset without a fetcher is refused in plain terms.
	os.Remove(shardCopy)
	if _, err := ImportArchiveFromManifest(context.Background(), target, ImportArchiveOptions{
		ManifestBytes:     archive.Manifest.Bytes,
		ProviderPublicKey: providerPublicKey,
		AssetDir:          planeCopy,
	}); err == nil {
		t.Fatalf("import succeeded without the shard on disk")
	}
}
