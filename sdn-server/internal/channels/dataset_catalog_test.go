package channels

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func catalogFixture(t *testing.T, key ed25519.PrivateKey, at time.Time, offset int) (*storage.DatasetPublicationManifest, []byte) {
	t.Helper()
	export, err := storage.ExportDatasetRecords(filepath.Join(t.TempDir(), "export"), storage.IndexedRecordQuery{
		SchemaName: "OMM.fbs", ProviderID: "provider", SourceName: "orbits", Limit: 10, Offset: offset, OrderByCID: true,
	}, []storage.DatasetExportRecord{{Data: sds.NewOMMBuilder().WithNoradCatID(25544).Build(), SourceTags: storage.SourceTags{ProviderID: "provider", SourceName: "orbits", BatchID: "current", ContentKeyID: "public"}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := storage.BuildSignedDatasetPublicationManifest(t.TempDir(), storage.DatasetPublicationManifestOptions{
		Export: export, DatasetID: "provider", UpdateID: "current", FileID: "provider:OMM.fbs:current", ProviderPeerID: "publisher", PublishedAt: at, SigningKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	pnm, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{PublishedAt: at, SigningKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, pnm
}

func TestDatasetCatalogReplacesPersistsAndDoesNotHoldRemoteRecords(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	now := time.Now().UTC().Truncate(time.Second)
	first, pnm1 := catalogFixture(t, key, now.Add(-time.Minute), 0)
	second, pnm2 := catalogFixture(t, key, now, 0)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	store, err := storage.NewFlatSQLStore(path, validator)
	if err != nil {
		t.Fatal(err)
	}
	for _, edition := range []struct{ manifest, pnm []byte }{{first.Bytes, pnm1}, {second.Bytes, pnm2}, {first.Bytes, pnm1}, {second.Bytes, pnm2}} {
		if err := RememberDatasetCatalog(store, "publisher", pub, edition.pnm, edition.manifest, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewFlatSQLStore(path, validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := ReadDatasetCatalog(store, now)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	entry := entries[0]
	if entry.ManifestCID != second.CID || entry.PNMCID != storage.ComputeCID(pnm2) || entry.Rows != 1 {
		t.Fatalf("wrong cached edition: %+v", entry)
	}
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Sources) != 0 {
		t.Fatalf("manifest cache polluted held records: %+v", summary.Sources)
	}
	files, err := os.ReadDir(datasetCatalogPath(store))
	if err != nil || len(files) != 1 {
		t.Fatalf("old cache editions accumulated on disk: %v %v", files, err)
	}
	info, err := files[0].Info()
	if err != nil || info.Size() > int64(len(second.Bytes)+len(pnm2)+1024) {
		t.Fatalf("cache footprint includes retired bytes: %v %v", info, err)
	}
	pins, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{})
	if err != nil || len(pins) != 0 {
		t.Fatalf("unexpected data pins: %+v %v", pins, err)
	}
	if err := RememberDatasetCatalog(store, "publisher", pub, pnm1, second.Bytes, now); err == nil {
		t.Fatal("accepted mismatched PNM and DPM")
	}
}

func TestDatasetCatalogRejectsTamperingWrongIdentityFutureAndPartialPages(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	now := time.Now().UTC()
	manifest, _ := catalogFixture(t, key, now, 0)
	if _, err := DecodeDatasetCatalog(manifest.Bytes, pub, "other", now); err == nil {
		t.Fatal("accepted wrong publisher")
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if _, err := DecodeDatasetCatalog(manifest.Bytes, other, "publisher", now); err == nil {
		t.Fatal("accepted wrong signing key")
	}
	corrupt := append([]byte(nil), manifest.Bytes...)
	corrupt[0] = 255
	if _, err := DecodeDatasetCatalog(corrupt, pub, "publisher", now); err == nil {
		t.Fatal("accepted corrupted buffer")
	}
	if _, err := DecodeDatasetCatalog(manifest.Bytes, pub, "publisher", now.Add(-time.Hour)); err == nil {
		t.Fatal("accepted future publication")
	}
	partial, _ := catalogFixture(t, key, now, 10)
	if _, err := DecodeDatasetCatalog(partial.Bytes, pub, "publisher", now); err == nil {
		t.Fatal("accepted paginated result as source catalog")
	}
	if _, err := DecodeDatasetCatalog(make([]byte, MaxDatasetCatalogManifestBytes+1), pub, "publisher", now); err == nil {
		t.Fatal("accepted oversized manifest")
	}
}
