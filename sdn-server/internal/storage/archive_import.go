package storage

// Archive import (fbcs program): re-import a signed $DPM archive — one this
// node produced with ArchiveDatasetSelection, or one another node published —
// keeping the ORIGINAL producer as the records' provenance.
//
// Verification is fail-closed and happens BEFORE anything lands: the manifest
// signature against the provider's Ed25519 key, then every asset's CID and
// SHA-256 against the manifest. A shard whose bytes disagree with the manifest
// imports nothing.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
)

// ImportArchiveOptions controls one archive import.
type ImportArchiveOptions struct {
	// ManifestBytes is the signed $DPM, bare or size-prefixed.
	ManifestBytes []byte
	// ProviderPublicKey verifies the manifest's provider signature. Required.
	ProviderPublicKey ed25519.PublicKey
	// AssetDir is an additional directory holding shards/ and indexes/ (another
	// node's archive plane, or an operator-supplied copy). The store's own
	// archive plane is always searched first; verified assets found elsewhere
	// or fetched are held in the store's plane so the archive stays local.
	AssetDir string
	// Fetch resolves an asset by CID when it is not on disk (nil on a node
	// without IPFS: a missing asset is then an error).
	Fetch func(ctx context.Context, cid string) ([]byte, error)
	// Now stamps the pin-ledger rows (default time.Now).
	Now func() time.Time
}

// ImportArchiveFromManifest verifies and imports one archive.
func ImportArchiveFromManifest(ctx context.Context, store *FlatSQLStore, opts ImportArchiveOptions) (*DatasetPublicationReplayResult, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(opts.ProviderPublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("provider public key is required")
	}
	manifestBytes := BareDPMBytes(opts.ManifestBytes)
	if len(manifestBytes) == 0 {
		return nil, fmt.Errorf("manifest bytes are required")
	}
	manifest, _, err := parseAndVerifyDatasetManifest(manifestBytes, opts.ProviderPublicKey)
	if err != nil {
		return nil, err
	}
	manifestCID, err := cidV1RawSHA256(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("compute manifest CID: %w", err)
	}
	assetMap, err := manifestAssetMap(manifest)
	if err != nil {
		return nil, err
	}
	shardAsset, ok := assetMap["DATA_SHARD"]
	if !ok {
		return nil, fmt.Errorf("DPM missing DATA_SHARD asset")
	}
	indexAsset, ok := assetMap["QUERY_INDEX"]
	if !ok {
		return nil, fmt.Errorf("DPM missing QUERY_INDEX asset")
	}
	holdDir := store.ArchiveOutputDir()
	if holdDir == "" {
		return nil, fmt.Errorf("archive plane directory is unavailable")
	}
	assetDir := strings.TrimSpace(opts.AssetDir)

	shardPath, err := resolveArchiveAsset(ctx, holdDir, assetDir, "shards", "shard", shardAsset, opts.Fetch)
	if err != nil {
		return nil, err
	}
	indexPath, err := resolveArchiveAsset(ctx, holdDir, assetDir, "indexes", "index", indexAsset, opts.Fetch)
	if err != nil {
		return nil, err
	}
	// Hold the manifest under the same name the archive plane writes, so the
	// archive lists on this node after the import.
	archiveID := strings.TrimSpace(string(manifest.DATASET_ID()))
	if archiveID != "" && !strings.ContainsAny(archiveID, "/\\") {
		manifestSHA := sha256Hex(manifestBytes)
		manifestPath := filepath.Join(holdDir, "manifests", fmt.Sprintf("%s-%s.dpm", archiveID, manifestSHA[:16]))
		if err := writeImmutableExportFile(manifestPath, manifestBytes); err != nil {
			return nil, fmt.Errorf("hold archive manifest: %w", err)
		}
	}

	providerPeerID := strings.TrimSpace(string(manifest.PROVIDER_PEER_ID()))
	imported, index, err := store.ImportDatasetShardFromFiles(shardPath, indexPath, providerPeerID)
	if err != nil {
		return nil, err
	}
	if err := recordManifestSourceBatchLicenses(store, manifest, index.SchemaName); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	shardInfo, _ := os.Stat(shardPath)
	indexInfo, _ := os.Stat(indexPath)
	entries := []PinLedgerEntry{
		{CID: shardAsset.CID, ByteHash: shardAsset.SHA256, RowCount: int64(index.RecordCount), ByteCount: fileSize(shardInfo, int64(shardAsset.Bytes))},
		{CID: indexAsset.CID, ByteHash: indexAsset.SHA256, ByteCount: fileSize(indexInfo, int64(indexAsset.Bytes))},
		{CID: manifestCID, ByteHash: sha256Hex(manifestBytes), ByteCount: int64(len(manifestBytes))},
	}
	for _, entry := range entries {
		entry.SchemaName = index.SchemaName
		entry.ProviderPeerID = providerPeerID
		entry.ProviderPublicKey = hex.EncodeToString(opts.ProviderPublicKey)
		entry.ProviderID = strings.TrimSpace(index.ProviderID)
		entry.SourceName = strings.TrimSpace(index.SourceName)
		entry.BatchID = archiveID
		entry.QueryProfile = ArchiveQueryProfile
		entry.Role = PinLedgerRoleArchive
		entry.SnapshotID = manifestCID
		entry.Head = index.QuerySHA256
		entry.TTL = 0
		entry.VerificationState = "verified"
		entry.VerifiedAt = now
		entry.UpdatedAt = now
		if err := store.UpsertPinLedgerEntry(entry); err != nil {
			return nil, fmt.Errorf("record archive pin ledger %s: %w", entry.CID, err)
		}
	}

	return &DatasetPublicationReplayResult{
		ManifestCID:  manifestCID,
		ShardCID:     shardAsset.CID,
		IndexCID:     indexAsset.CID,
		SchemaName:   index.SchemaName,
		RecordCount:  index.RecordCount,
		Imported:     imported,
		QuerySHA256:  index.QuerySHA256,
		ResultSHA256: index.ResultSHA256,
	}, nil
}

// BareDPMBytes strips the size prefix from a $DPM frame when it carries one;
// a bare manifest is returned unchanged. Unknown bytes are returned as given
// so the signature check reports them.
func BareDPMBytes(data []byte) []byte {
	if len(data) >= 12 && dpm.SizePrefixedDPMBufferHasIdentifier(data) {
		return data[4:]
	}
	return data
}

// resolveArchiveAsset finds an asset's verified bytes and returns a path
// under the store's own archive plane (<holdDir>/<subdir>/<FILE_NAME>). The
// search order is the plane itself, then assetDir (another node's plane or an
// operator-supplied directory), then the fetcher. Every candidate is checked
// against the manifest's CID and SHA-256 before it is used or held; a file
// that disagrees is an error, never silently replaced.
func resolveArchiveAsset(ctx context.Context, holdDir, assetDir, subdir, label string, asset publicationAsset, fetch func(context.Context, string) ([]byte, error)) (string, error) {
	fileName := strings.TrimSpace(asset.File)
	if fileName == "" || fileName != filepath.Base(fileName) || fileName == "." || fileName == ".." {
		// No usable file name in the manifest: fall back to the CID.
		fileName = datasetPublicationPathComponent(asset.CID) + "." + label
	}
	held := filepath.Join(holdDir, subdir, fileName)
	if info, err := os.Stat(held); err == nil && !info.IsDir() {
		if _, _, err := verifyFileCIDAndHash(label, held, asset.CID, asset.SHA256); err != nil {
			return "", fmt.Errorf("archive %s on disk does not match its manifest: %w", label, err)
		}
		return held, nil
	}
	if assetDir != "" && filepath.Clean(assetDir) != filepath.Clean(holdDir) {
		alt := filepath.Join(assetDir, subdir, fileName)
		if info, err := os.Stat(alt); err == nil && !info.IsDir() {
			if _, _, err := verifyFileCIDAndHash(label, alt, asset.CID, asset.SHA256); err != nil {
				return "", fmt.Errorf("archive %s does not match its manifest: %w", label, err)
			}
			if err := holdArchiveFile(alt, held); err != nil {
				return "", fmt.Errorf("hold archive %s: %w", label, err)
			}
			return held, nil
		}
	}
	if fetch == nil {
		return "", fmt.Errorf("archive %s %s is not held on this node", label, asset.CID)
	}
	data, err := fetch(ctx, asset.CID)
	if err != nil {
		return "", fmt.Errorf("fetch archive %s %s: %w", label, asset.CID, err)
	}
	if err := verifyBytesCIDAndHash(label, data, asset.CID, asset.SHA256); err != nil {
		return "", err
	}
	if err := writeImmutableExportFile(held, data); err != nil {
		return "", fmt.Errorf("hold archive %s: %w", label, err)
	}
	return held, nil
}

// holdArchiveFile copies a verified asset into the archive plane through a
// temp file + rename, so a partial copy is never mistaken for the asset.
func holdArchiveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

func fileSize(info os.FileInfo, fallback int64) int64 {
	if info != nil {
		return info.Size()
	}
	return fallback
}
