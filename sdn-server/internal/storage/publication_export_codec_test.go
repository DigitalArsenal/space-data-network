package storage

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestOrdinaryPublicationFlatBufferIndex(t *testing.T) {
	root := t.TempDir()
	store := &FlatSQLStore{basePath: filepath.Join(root, "store")}
	dir := filepath.Join(store.DatasetPublicationOutputDir(), datasetPublicationPathComponent("OMM.fbs"))
	record := sds.NewOMMBuilder().WithObjectName("synthetic").Build()
	export, err := exportOrdinaryDatasetRecords(dir, IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: "synthetic", SourceName: "source"}, []DatasetExportRecord{{Data: record, SourceTags: SourceTags{ProviderID: "synthetic", SourceName: "source"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(export.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	index, err := parseDatasetExportIndexBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if index.RecordCount != 1 || index.Records[0].SourceTags.ProviderID != "synthetic" || !strings.HasSuffix(export.IndexPath, ".index.fb") {
		t.Fatal("binary export lost record metadata")
	}
	shard, err := os.ReadFile(export.ShardPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(shard) < 4 || int(binary.LittleEndian.Uint32(shard)) != len(record) || !bytes.Equal(shard[4:], record) {
		t.Fatal("export changed the SDS record stream")
	}
	row := DatasetShardPublication{SchemaName: export.SchemaName, QuerySHA256: export.QuerySHA256, IndexSHA256: export.IndexSHA256, ShardSHA256: export.ShardSHA256}
	path, err := store.DatasetPublicationIndexPath(row)
	if err != nil || path != export.IndexPath {
		t.Fatal("binary index cannot be served", err)
	}
	found := false
	for _, p := range datasetPublicationRowFiles(dir, row) {
		found = found || p == export.IndexPath
	}
	if !found {
		t.Fatal("binary index missing from retention candidates")
	}
	live := livePublicationFiles{indexes: map[string]bool{export.IndexSHA256[:16]: true}}
	if !live.referenced(export.IndexPath) {
		t.Fatal("live binary index unprotected")
	}
	delete(live.indexes, export.IndexSHA256[:16])
	if live.referenced(export.IndexPath) {
		t.Fatal("unreferenced binary index cannot be reclaimed")
	}

	// A fresh in-memory test key binds the actual binary asset's schema and hash.
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := crypto.UnmarshalEd25519PublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := BuildSignedDatasetPublicationManifest(t.TempDir(), DatasetPublicationManifestOptions{Export: export, DatasetID: "synthetic", UpdateID: "test", ProviderPeerID: id.String(), SigningKey: key, PublishedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := parseAndVerifyDatasetManifest(signed.Bytes, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for i := 0; i < manifest.ASSETSLength(); i++ {
		var asset dpm.DPMAsset
		if manifest.ASSETS(&asset, i) && asset.ASSET_KIND() == 1 {
			found = true
			if string(asset.SCHEMA_NAME()) != publicationstate.SchemaName || string(asset.SCHEMA_HASH()) != publicationstate.SchemaSHA256() || !strings.HasSuffix(string(asset.FILE_NAME()), ".index.fb") {
				t.Fatal("signed index descriptor does not identify its FlatBuffer schema")
			}
		}
	}
	if !found {
		t.Fatal("signed manifest has no index")
	}

	// Released JSON indexes remain readable through their existing reader.
	legacy := []byte(`{"version":1,"schemaName":"OMM.fbs","recordCount":0,"records":[]}`)
	if _, err := parseDatasetExportIndexBytes(legacy); err != nil {
		t.Fatal(err)
	}
	legacyRow := row
	legacyRow.IndexSHA256 = sha256Hex(legacy)
	legacyPath, err := store.DatasetPublicationIndexPath(legacyRow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if path, err := store.DatasetPublicationIndexPath(legacyRow); err != nil || path != legacyPath || !strings.HasSuffix(path, ".index.json") {
		t.Fatal("released index lookup lost", err)
	}
}
