package storage

// Dataset-publication licence carriage (graph task
// sdn-dataset-publication-license-carriage): a CC-BY-SA source such as SatNOGS
// $RFB may only be republished on SDN if its licence travels with it. These
// tests pin the whole chain — ingest tags -> batch licence row -> export source
// batch -> signed DPM SOURCES -> DPM re-read — and pin the byte-stability of
// the unlicensed path, which must publish exactly the bytes it published
// before licence carriage existed.

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

const (
	licenceTestSPDX     = "CC-BY-SA-4.0"
	licenceTestURL      = "https://creativecommons.org/licenses/by-sa/4.0/"
	licenceTestCitation = "SatNOGS DB contributors, CC BY-SA 4.0"
)

func newLicenceTestStore(t *testing.T) (*FlatSQLStore, string) {
	t.Helper()
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, tmpDir
}

func licenceTestCATRecord(norad uint32, name string) []byte {
	return sds.NewCATBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID("1998-067A").
		WithObjectType("PAYLOAD").
		WithOpsStatus("OPERATIONAL").
		Build()
}

// TestSourceBatchLicenseCarriesIntoSignedDPM is the end-to-end assertion: a
// batch ingested with licence metadata publishes a DPM whose SOURCES entry
// carries LICENSE / LICENSE_URL / CITATION, and that manifest still verifies
// against its own provider signature (i.e. the licence is inside the signed
// bytes and the unsigned-manifest rebuild restores it).
func TestSourceBatchLicenseCarriesIntoSignedDPM(t *testing.T) {
	store, tmpDir := newLicenceTestStore(t)

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "satnogs-rfb",
		SourceURL:    "https://db.satnogs.org/api/transmitters/",
		BatchID:      "source-sha-satnogs-001",
		ContentKeyID: "public",
		License:      licenceTestSPDX,
		LicenseURL:   licenceTestURL,
		Citation:     licenceTestCitation,
		ShareAlike:   true,
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", licenceTestCATRecord(25544, "ISS (ZARYA)"), "source:satnogs", nil, tags); err != nil {
		t.Fatalf("store licensed record: %v", err)
	}

	// The licence landed as batch-keyed state, not as per-record columns.
	license, found, err := store.SourceBatchLicenseFor("CAT.fbs", tags.ProviderID, tags.SourceName, tags.BatchID)
	if err != nil {
		t.Fatalf("SourceBatchLicenseFor: %v", err)
	}
	if !found {
		t.Fatalf("ingest did not record a batch licence for %s/%s", tags.SourceName, tags.BatchID)
	}
	if license.License != licenceTestSPDX || license.LicenseURL != licenceTestURL || license.Citation != licenceTestCitation || !license.ShareAlike {
		t.Fatalf("recorded batch licence = %+v", license)
	}

	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow: %v", err)
	}
	if len(export.SourceBatches) != 1 {
		t.Fatalf("SourceBatches = %d, want 1", len(export.SourceBatches))
	}
	batch := export.SourceBatches[0]
	if batch.License != licenceTestSPDX || batch.LicenseURL != licenceTestURL || batch.Citation != licenceTestCitation {
		t.Fatalf("export source batch licence = %+v", batch)
	}
	if !batch.ShareAlike {
		t.Fatalf("export source batch lost the share-alike flag: %+v", batch)
	}

	// The export index bytes must NOT have grown licence fields: licence is
	// batch state, and the per-record index is the hashed, signed surface.
	indexBytes, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatalf("read export index: %v", err)
	}
	for _, needle := range []string{licenceTestSPDX, licenceTestURL, licenceTestCitation, "License", "Citation"} {
		if containsString(indexBytes, needle) {
			t.Fatalf("export index leaked licence field %q into per-record bytes", needle)
		}
	}

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 3)
	}
	signingKey := ed25519.NewKeyFromSeed(seed)
	export.ShardCID = "bafkreiglicensedshardcidplaceholder000000000000000000000000"
	export.IndexCID = "bafkreiglicensedindexcidplaceholder000000000000000000000000"

	manifest, err := BuildSignedDatasetPublicationManifest(tmpDir, DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      "satnogs-rfb",
		UpdateID:       tags.BatchID,
		ProviderPeerID: tags.ProviderID,
		PublishedAt:    time.Unix(1770000000, 0).UTC(),
		SigningKey:     signingKey,
		SchemaHash:     "cat-schema-hash",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest: %v", err)
	}

	root := dpm.GetRootAsDPM(manifest.Bytes, 0)
	if root.SOURCESLength() != 1 {
		t.Fatalf("DPM SOURCESLength = %d, want 1", root.SOURCESLength())
	}
	var source dpm.DPMSourceBatch
	if !root.SOURCES(&source, 0) {
		t.Fatalf("DPM SOURCES[0] missing")
	}
	if got := string(source.LICENSE()); got != licenceTestSPDX {
		t.Fatalf("DPM LICENSE = %q, want %q", got, licenceTestSPDX)
	}
	if got := string(source.LICENSE_URL()); got != licenceTestURL {
		t.Fatalf("DPM LICENSE_URL = %q, want %q", got, licenceTestURL)
	}
	if got := string(source.CITATION()); got != licenceTestCitation {
		t.Fatalf("DPM CITATION = %q, want %q", got, licenceTestCitation)
	}

	// Signature verification rebuilds the unsigned manifest from the parsed
	// DPM. If the rebuild dropped the licence the payload hash would change
	// and this would fail — this is the round-trip assertion.
	if _, err := VerifySignedDatasetPublicationManifest(manifest.Bytes, signingKey.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("licensed DPM does not verify against its own signature: %v", err)
	}
}

// TestUnlicensedDatasetManifestBytesUnchanged pins the no-licence path: an
// export whose batch declared no licence must produce the exact manifest bytes
// this node produced before licence carriage existed. The golden hash below was
// captured from the pre-change tree (main @ c0a9bc22) with the same inputs.
func TestUnlicensedDatasetManifestBytesUnchanged(t *testing.T) {
	const goldenManifestSHA256 = "c49d7d5f1433865b77c4fb4b2ba902ab40b3c44258ea51c00f6721291e338341"

	manifest := buildUnlicensedGoldenManifest(t)
	if manifest.SHA256 != goldenManifestSHA256 {
		t.Fatalf("unlicensed DPM bytes changed: sha256 = %s, want %s\n"+
			"licence carriage must leave batches without licence metadata byte-identical",
			manifest.SHA256, goldenManifestSHA256)
	}

	root := dpm.GetRootAsDPM(manifest.Bytes, 0)
	var source dpm.DPMSourceBatch
	if !root.SOURCES(&source, 0) {
		t.Fatalf("DPM SOURCES[0] missing")
	}
	// Absent, not empty: an unset FlatBuffers slot is what keeps the vtable —
	// and therefore the manifest CID and provider signature — unchanged.
	if source.LICENSE() != nil || source.LICENSE_URL() != nil || source.CITATION() != nil {
		t.Fatalf("unlicensed DPM wrote licence slots: LICENSE=%q LICENSE_URL=%q CITATION=%q",
			source.LICENSE(), source.LICENSE_URL(), source.CITATION())
	}
}

// buildUnlicensedGoldenManifest builds a fully deterministic DPM from a fixed
// export, signing key and timestamp so its bytes can be compared across trees.
func buildUnlicensedGoldenManifest(t *testing.T) *DatasetPublicationManifest {
	t.Helper()
	tmpDir := t.TempDir()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 11)
	}
	signingKey := ed25519.NewKeyFromSeed(seed)

	export := &DatasetExport{
		SchemaName:     "CAT.fbs",
		RecordCount:    2,
		CanonicalQuery: `{"schemaName":"CAT.fbs","limit":2}`,
		QuerySHA256:    "1111111111111111111111111111111111111111111111111111111111111111",
		ResultSHA256:   "2222222222222222222222222222222222222222222222222222222222222222",
		ShardPath:      filepath.Join(tmpDir, "shards", "golden.fbshard"),
		ShardSHA256:    "3333333333333333333333333333333333333333333333333333333333333333",
		ShardCID:       "bafkreigoldenshardcid000000000000000000000000000000000000000",
		ShardBytes:     4096,
		IndexPath:      filepath.Join(tmpDir, "indexes", "golden.index.json"),
		IndexSHA256:    "4444444444444444444444444444444444444444444444444444444444444444",
		IndexCID:       "bafkreigoldenindexcid000000000000000000000000000000000000000",
		IndexBytes:     512,
		SourceBatches: []DatasetExportSourceBatch{{
			ProviderID:    "space-data-network-02",
			SourceName:    "catalogfixture-satcat-csv",
			SourceURL:     "https://fixture.test/satcat/records.php",
			SourceSHA256:  "golden-batch-sha",
			ContentKeyID:  "public",
			ParserVersion: "satcat-csv-v1",
			RecordCount:   2,
		}},
	}

	manifest, err := BuildSignedDatasetPublicationManifest(tmpDir, DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       "cat-active",
		UpdateID:        "golden-batch-sha",
		ProviderPeerID:  "space-data-network-02",
		ProviderEPMCID:  "bafy-provider-epm",
		PublishedAt:     time.Unix(1760000000, 0).UTC(),
		SigningKey:      signingKey,
		SchemaHash:      "cat-schema-hash",
		QueryEngine:     "FlatSQL",
		QueryEngineVers: "sdn-index-v1",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest: %v", err)
	}
	return manifest
}

// TestSourceBatchLicenseSurvivesStoreReopen proves the licence is durable
// through the auxiliary metadata journal, like every other non-record table.
func TestSourceBatchLicenseSurvivesStoreReopen(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "db")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	store, err := NewFlatSQLStore(dbPath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	if err := store.UpsertSourceBatchLicense(SourceBatchLicense{
		SchemaName: "RFB.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "satnogs-rfb",
		BatchID:    "batch-reopen",
		License:    licenceTestSPDX,
		LicenseURL: licenceTestURL,
		Citation:   licenceTestCitation,
		ShareAlike: true,
	}); err != nil {
		t.Fatalf("UpsertSourceBatchLicense: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewFlatSQLStore(dbPath, validator)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	license, found, err := reopened.SourceBatchLicenseFor("RFB.fbs", "space-data-network-02", "satnogs-rfb", "batch-reopen")
	if err != nil {
		t.Fatalf("SourceBatchLicenseFor after reopen: %v", err)
	}
	if !found {
		t.Fatalf("batch licence did not survive a store reopen")
	}
	if license.License != licenceTestSPDX || license.LicenseURL != licenceTestURL ||
		license.Citation != licenceTestCitation || !license.ShareAlike {
		t.Fatalf("reopened batch licence = %+v", license)
	}
}

// TestUnlicensedIngestRecordsNoLicenseRow keeps the untagged path inert: no
// licence row, no licence in the export, no behaviour change.
func TestUnlicensedIngestRecordsNoLicenseRow(t *testing.T) {
	store, tmpDir := newLicenceTestStore(t)

	tags := SourceTags{
		ProviderID:   "space-data-network-02",
		SourceName:   "catalogfixture-satcat-csv",
		SourceURL:    "https://fixture.test/satcat.csv",
		BatchID:      "source-sha-plain",
		ContentKeyID: "public",
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", licenceTestCATRecord(40909, "SATELLITE-1001"), "source:fixture", nil, tags); err != nil {
		t.Fatalf("store unlicensed record: %v", err)
	}
	if _, found, err := store.SourceBatchLicenseFor("CAT.fbs", tags.ProviderID, tags.SourceName, tags.BatchID); err != nil {
		t.Fatalf("SourceBatchLicenseFor: %v", err)
	} else if found {
		t.Fatalf("unlicensed ingest wrote a batch licence row")
	}

	export, err := store.ExportDatasetWindow(filepath.Join(tmpDir, "export"), IndexedRecordQuery{
		SchemaName:          "CAT.fbs",
		ProviderID:          tags.ProviderID,
		SourceName:          tags.SourceName,
		BatchID:             tags.BatchID,
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	})
	if err != nil {
		t.Fatalf("ExportDatasetWindow: %v", err)
	}
	if len(export.SourceBatches) != 1 {
		t.Fatalf("SourceBatches = %d, want 1", len(export.SourceBatches))
	}
	if batch := export.SourceBatches[0]; batch.License != "" || batch.LicenseURL != "" || batch.Citation != "" || batch.ShareAlike {
		t.Fatalf("unlicensed export invented licence terms: %+v", batch)
	}

	// The shard and index hashes an unlicensed publication advertises are the
	// bytes the pre-change tree produced (main @ c0a9bc22) for the same store
	// contents and the same query: SourceTags grew fields, so this pins that
	// they stay absent from the serialized per-record provenance.
	const (
		goldenShardSHA256 = "ea928be73710cd3691364ac89b9dbc95fac95c9fb5e7f02884c977e7085f73ea"
		goldenIndexSHA256 = "2afcec793ae8fd1fda0d2b39ff1553aaf0169df2a13dfa5cdd095fbfd7308ea4"
	)
	if export.ShardSHA256 != goldenShardSHA256 {
		t.Fatalf("unlicensed shard bytes changed: %s, want %s", export.ShardSHA256, goldenShardSHA256)
	}
	if export.IndexSHA256 != goldenIndexSHA256 {
		t.Fatalf("unlicensed export index bytes changed: %s, want %s\n"+
			"SourceTags licence fields must stay out of the per-record index when empty",
			export.IndexSHA256, goldenIndexSHA256)
	}
}

func containsString(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}
