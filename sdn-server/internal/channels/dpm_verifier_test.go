package channels

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestVerifySignedDPMManifestWithProviderKeyAcceptsMatchingSignature(t *testing.T) {
	t.Parallel()

	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	manifest := buildChannelVerifierDPM(t, privateKey, "DPM")

	evidence, err := VerifySignedDPMManifestWithProviderKey(manifest.Bytes, "DPM", publicKey)
	if err != nil {
		t.Fatalf("VerifySignedDPMManifestWithProviderKey failed: %v", err)
	}
	if evidence.FileID != "DPM" || evidence.SignatureType != "Ed25519" || evidence.ProviderPeer != "spaceaware" {
		t.Fatalf("unexpected DPM trust evidence: %#v", evidence)
	}
	if evidence.ManifestCID != manifest.CID {
		t.Fatalf("manifest CID = %q, want %q", evidence.ManifestCID, manifest.CID)
	}
}

func TestVerifySignedDPMManifestWithProviderKeyRejectsMismatchedSignature(t *testing.T) {
	t.Parallel()

	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	otherPublicKey, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	manifest := buildChannelVerifierDPM(t, privateKey, "DPM")

	if _, err := VerifySignedDPMManifestWithProviderKey(manifest.Bytes, "DPM", otherPublicKey); err == nil {
		t.Fatal("expected mismatched DPM provider key to be rejected")
	}
}

func buildChannelVerifierDPM(t *testing.T, signingKey ed25519.PrivateKey, fileID string) *storage.DatasetPublicationManifest {
	t.Helper()

	schemaName, err := SchemaNameFromStandardCode("OMM")
	if err != nil {
		t.Fatalf("SchemaNameFromStandardCode failed: %v", err)
	}
	export, err := storage.ExportDatasetRecords(filepath.Join(t.TempDir(), "export"), storage.IndexedRecordQuery{
		SchemaName:          schemaName,
		ProviderID:          "spaceaware",
		SourceName:          "channel:spaceaware-OMM",
		BatchID:             "batch-1",
		Limit:               10,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	}, []storage.DatasetExportRecord{{
		Data: sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build(),
		SourceTags: storage.SourceTags{
			ProviderID:        "spaceaware",
			SourceName:        "channel:spaceaware-OMM",
			BatchID:           "batch-1",
			ContentKeyID:      "public",
			ProducerPeerID:    "spaceaware",
			ProducerPublicKey: "spaceaware",
		},
	}})
	if err != nil {
		t.Fatalf("ExportDatasetRecords failed: %v", err)
	}
	manifest, err := storage.BuildSignedDatasetPublicationManifest(filepath.Join(t.TempDir(), "publish"), storage.DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       "spaceaware",
		UpdateID:        "batch-1",
		FileID:          fileID,
		ProviderPeerID:  "spaceaware",
		ProviderEPMCID:  "bafy-provider-epm",
		PublishedAt:     time.Now().UTC(),
		SigningKey:      signingKey,
		SchemaHash:      "channel-verifier-schema",
		QueryEngine:     "FlatSQL",
		QueryEngineVers: "sdn-channel-verifier-test",
	})
	if err != nil {
		t.Fatalf("BuildSignedDatasetPublicationManifest failed: %v", err)
	}
	return manifest
}
