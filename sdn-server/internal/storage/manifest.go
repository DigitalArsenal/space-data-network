package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// DatasetPublicationManifestOptions controls signed SDS DPM generation.
type DatasetPublicationManifestOptions struct {
	Export          *DatasetExport
	DatasetID       string
	UpdateID        string
	FileID          string
	ProviderPeerID  string
	ProviderEPMCID  string
	PublishedAt     time.Time
	SigningKey      ed25519.PrivateKey
	SchemaHash      string
	QueryEngine     string
	QueryEngineVers string
	// AuxiliaryAssets are extra OTHER-kind content-addressed DPM assets bound
	// under the provider signature alongside the shard and index (e.g. the
	// archive plane's source feed-head provenance references). They use only
	// existing DPM v1.0.6 DPMAsset fields — no schema extension.
	AuxiliaryAssets []DPMAuxiliaryAsset
}

// DPMAuxiliaryAsset is one extra OTHER-kind, content-addressed asset embedded
// in a signed DPM. All fields map 1:1 onto existing DPMAsset schema fields;
// MULTIFORMAT_ADDRESS is always derived as "/ipfs/"+CID so the unsigned
// manifest can be rebuilt byte-for-byte during signature verification.
type DPMAuxiliaryAsset struct {
	CID        string
	FileName   string
	FileID     string
	DataRoot   string
	SchemaName string
	ByteSHA256 string
	ByteLength int64
}

// DatasetPublicationManifest is the signed DPM artifact for a dataset update.
type DatasetPublicationManifest struct {
	Path                   string
	CID                    string
	FileID                 string
	SHA256                 string
	Bytes                  []byte
	ByteLength             int64
	Signature              []byte
	SignaturePayloadSHA256 [32]byte
}

// DatasetPublicationPNMOptions controls the PNM announcement for a DPM CID.
type DatasetPublicationPNMOptions struct {
	FileName    string
	PublishedAt time.Time
	SigningKey  ed25519.PrivateKey
}

// DatasetPublicationReplayOptions controls replay verification for a PNM/DPM publication.
type DatasetPublicationReplayOptions struct {
	PNM               []byte
	ProviderPublicKey ed25519.PublicKey
	FetchByCID        func(context.Context, string) ([]byte, error)
	FetchByCIDToFile  func(context.Context, string, string) error
	FetchRetryDelays  []time.Duration
	WorkDir           string
}

// DatasetPublicationReplayResult summarizes a verified publication replay.
type DatasetPublicationReplayResult struct {
	ManifestCID  string
	ShardCID     string
	IndexCID     string
	SchemaName   string
	RecordCount  int
	Imported     int
	QuerySHA256  string
	ResultSHA256 string
}

type DatasetPublicationManifestTrustEvidence struct {
	ManifestCID    string
	FileID         string
	SignatureType  string
	ProviderPeer   string
	ProviderEPMCID string
	Encrypted      bool
	ContentKeyID   string
	PolicyID       string
}

func VerifySignedDatasetPublicationManifest(manifestBytes []byte, providerPublicKey ed25519.PublicKey) (DatasetPublicationManifestTrustEvidence, error) {
	manifest, _, err := parseAndVerifyDatasetManifest(manifestBytes, providerPublicKey)
	if err != nil {
		return DatasetPublicationManifestTrustEvidence{}, err
	}
	manifestCID, err := cidV1RawSHA256(manifestBytes)
	if err != nil {
		return DatasetPublicationManifestTrustEvidence{}, fmt.Errorf("compute manifest CID: %w", err)
	}
	evidence := DatasetPublicationManifestTrustEvidence{
		ManifestCID:    manifestCID,
		FileID:         strings.TrimSpace(string(manifest.FILE_ID())),
		SignatureType:  strings.TrimSpace(string(manifest.SIGNATURE_TYPE())),
		ProviderPeer:   strings.TrimSpace(string(manifest.PROVIDER_PEER_ID())),
		ProviderEPMCID: strings.TrimSpace(string(manifest.PROVIDER_EPM_CID())),
	}
	if enc := manifest.ENCRYPTION(nil); enc != nil {
		evidence.Encrypted = enc.ENCRYPTED()
		evidence.ContentKeyID = strings.TrimSpace(string(enc.CONTENT_KEY_ID()))
		evidence.PolicyID = strings.TrimSpace(string(enc.POLICY_ID()))
	}
	return evidence, nil
}

// BuildDatasetPublicationPNM creates one PNM announcing a signed DPM manifest CID.
func BuildDatasetPublicationPNM(manifest *DatasetPublicationManifest, opts DatasetPublicationPNMOptions) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("dataset publication manifest is required")
	}
	if strings.TrimSpace(manifest.CID) == "" {
		return nil, fmt.Errorf("manifest CID is required")
	}
	if opts.PublishedAt.IsZero() {
		opts.PublishedAt = time.Now().UTC()
	}
	if strings.TrimSpace(opts.FileName) == "" {
		opts.FileName = filepath.Base(manifest.Path)
	}
	fileIDValue := strings.TrimSpace(manifest.FileID)
	if fileIDValue == "" {
		return nil, fmt.Errorf("manifest file id is required")
	}
	builder := flatbuffers.NewBuilder(256)
	addr := builder.CreateString("/ipfs/" + manifest.CID)
	publishedAt := builder.CreateString(opts.PublishedAt.UTC().Format(time.RFC3339))
	cidOffset := builder.CreateString(manifest.CID)
	fileName := builder.CreateString(opts.FileName)
	fileID := builder.CreateString(fileIDValue)
	signatureBytes := manifest.Signature
	if len(opts.SigningKey) == ed25519.PrivateKeySize {
		signatureBytes = ed25519.Sign(opts.SigningKey, datasetPublicationPNMSignaturePayload(manifest.CID, fileIDValue))
	}
	signature := builder.CreateString(hex.EncodeToString(signatureBytes))
	signatureType := builder.CreateString("Ed25519")

	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, addr)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, publishedAt)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_NAME(builder, fileName)
	PNM.PNMAddFILE_ID(builder, fileID)
	PNM.PNMAddSIGNATURE(builder, signature)
	PNM.PNMAddSIGNATURE_TYPE(builder, signatureType)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func datasetPublicationPNMSignaturePayload(manifestCID, fileID string) []byte {
	payload := make([]byte, 0, len(manifestCID)+len(fileID)+18)
	payload = append(payload, []byte("SDN-DPM-PNM\x00")...)
	payload = append(payload, fileID...)
	payload = append(payload, 0)
	payload = append(payload, manifestCID...)
	return payload
}

// VerifyDatasetPublicationReplay verifies a signed PNM/DPM publication, resolves
// the referenced immutable bytes, replays the canonical FlatSQL query, and
// byte-compares the advertised result shard.
func VerifyDatasetPublicationReplay(ctx context.Context, store *FlatSQLStore, opts DatasetPublicationReplayOptions) (*DatasetPublicationReplayResult, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if len(opts.PNM) == 0 {
		return nil, fmt.Errorf("PNM bytes are required")
	}
	if len(opts.ProviderPublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 provider public key is required")
	}
	if opts.FetchByCID == nil {
		return nil, fmt.Errorf("CID fetcher is required")
	}
	if strings.TrimSpace(opts.WorkDir) == "" {
		return nil, fmt.Errorf("work dir is required")
	}
	if !PNM.SizePrefixedPNMBufferHasIdentifier(opts.PNM) {
		return nil, fmt.Errorf("PNM buffer missing identifier")
	}

	pnm := PNM.GetSizePrefixedRootAsPNM(opts.PNM, 0)
	manifestCID := strings.TrimSpace(string(pnm.CID()))
	fileID := strings.TrimSpace(string(pnm.FILE_ID()))
	if manifestCID == "" {
		return nil, fmt.Errorf("PNM missing manifest CID")
	}
	if fileID == "" {
		return nil, fmt.Errorf("PNM missing FILE_ID")
	}
	if sigType := strings.TrimSpace(string(pnm.SIGNATURE_TYPE())); sigType != "Ed25519" {
		return nil, fmt.Errorf("PNM SIGNATURE_TYPE = %q, want Ed25519", sigType)
	}
	pnmSignature, err := hex.DecodeString(strings.TrimSpace(string(pnm.SIGNATURE())))
	if err != nil {
		return nil, fmt.Errorf("decode PNM signature: %w", err)
	}
	if !ed25519.Verify(opts.ProviderPublicKey, datasetPublicationPNMSignaturePayload(manifestCID, fileID), pnmSignature) {
		return nil, fmt.Errorf("invalid PNM signature")
	}

	manifestBytes, err := fetchDatasetPublicationCID(ctx, opts.FetchByCID, opts.FetchRetryDelays, manifestCID)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest CID %s: %w", manifestCID, err)
	}
	if err := verifyBytesCIDAndHash("manifest", manifestBytes, manifestCID, ""); err != nil {
		return nil, err
	}
	manifest, unsignedManifest, err := parseAndVerifyDatasetManifest(manifestBytes, opts.ProviderPublicKey)
	if err != nil {
		return nil, err
	}
	_ = unsignedManifest
	if dpmFileID := strings.TrimSpace(string(manifest.FILE_ID())); dpmFileID != fileID {
		return nil, fmt.Errorf("PNM FILE_ID %q does not match DPM FILE_ID %q", fileID, dpmFileID)
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
	shardBytes, err := fetchDatasetPublicationCID(ctx, opts.FetchByCID, opts.FetchRetryDelays, shardAsset.CID)
	if err != nil {
		return nil, fmt.Errorf("fetch shard CID %s: %w", shardAsset.CID, err)
	}
	if err := verifyBytesCIDAndHash("shard", shardBytes, shardAsset.CID, shardAsset.SHA256); err != nil {
		return nil, err
	}
	indexBytes, err := fetchDatasetPublicationCID(ctx, opts.FetchByCID, opts.FetchRetryDelays, indexAsset.CID)
	if err != nil {
		return nil, fmt.Errorf("fetch index CID %s: %w", indexAsset.CID, err)
	}
	if err := verifyBytesCIDAndHash("index", indexBytes, indexAsset.CID, indexAsset.SHA256); err != nil {
		return nil, err
	}

	query := manifest.QUERY(nil)
	if query == nil {
		return nil, fmt.Errorf("DPM missing query binding")
	}
	filter, err := indexedRecordQueryFromCanonicalJSON(query.CANONICAL_QUERY())
	if err != nil {
		return nil, fmt.Errorf("parse canonical query: %w", err)
	}
	replayed, err := store.ExportDatasetWindow(opts.WorkDir, filter)
	if err != nil {
		return nil, fmt.Errorf("replay export: %w", err)
	}
	replayedBytes, err := os.ReadFile(replayed.ShardPath)
	if err != nil {
		return nil, fmt.Errorf("read replay shard: %w", err)
	}
	if !bytes.Equal(replayedBytes, shardBytes) {
		return nil, fmt.Errorf("replayed result bytes do not match advertised shard")
	}
	if replayed.ResultSHA256 != string(query.RESULT_SHA256()) {
		return nil, fmt.Errorf("replayed result hash %s does not match DPM %s", replayed.ResultSHA256, string(query.RESULT_SHA256()))
	}
	return &DatasetPublicationReplayResult{
		ManifestCID:  manifestCID,
		ShardCID:     shardAsset.CID,
		IndexCID:     indexAsset.CID,
		SchemaName:   replayed.SchemaName,
		RecordCount:  replayed.RecordCount,
		QuerySHA256:  replayed.QuerySHA256,
		ResultSHA256: replayed.ResultSHA256,
	}, nil
}

// MaterializeDatasetPublication verifies a signed PNM/DPM publication, resolves
// the advertised shard/index bytes, and imports the shard into the local store.
func MaterializeDatasetPublication(ctx context.Context, store *FlatSQLStore, opts DatasetPublicationReplayOptions) (*DatasetPublicationReplayResult, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if opts.FetchByCID == nil {
		return nil, fmt.Errorf("CID fetcher is required")
	}
	manifestCID, fileID, err := verifyDatasetPublicationPNM(opts.PNM, opts.ProviderPublicKey)
	if err != nil {
		return nil, err
	}
	_ = fileID
	manifestBytes, err := fetchDatasetPublicationCID(ctx, opts.FetchByCID, opts.FetchRetryDelays, manifestCID)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest CID %s: %w", manifestCID, err)
	}
	if err := verifyBytesCIDAndHash("manifest", manifestBytes, manifestCID, ""); err != nil {
		return nil, err
	}
	manifest, _, err := parseAndVerifyDatasetManifest(manifestBytes, opts.ProviderPublicKey)
	if err != nil {
		return nil, err
	}
	if dpmFileID := strings.TrimSpace(string(manifest.FILE_ID())); dpmFileID != fileID {
		return nil, fmt.Errorf("PNM FILE_ID %q does not match DPM FILE_ID %q", fileID, dpmFileID)
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
	if opts.FetchByCIDToFile != nil {
		return materializeDatasetPublicationFromFiles(ctx, store, opts, manifest, manifestCID, shardAsset, indexAsset)
	}
	return materializeDatasetPublicationFromBytes(ctx, store, opts, manifest, manifestCID, shardAsset, indexAsset)
}

func materializeDatasetPublicationFromBytes(ctx context.Context, store *FlatSQLStore, opts DatasetPublicationReplayOptions, manifest *dpm.DPM, manifestCID string, shardAsset, indexAsset publicationAsset) (*DatasetPublicationReplayResult, error) {
	shardBytes, err := fetchDatasetPublicationCID(ctx, opts.FetchByCID, opts.FetchRetryDelays, shardAsset.CID)
	if err != nil {
		return nil, fmt.Errorf("fetch shard CID %s: %w", shardAsset.CID, err)
	}
	if err := verifyBytesCIDAndHash("shard", shardBytes, shardAsset.CID, shardAsset.SHA256); err != nil {
		return nil, err
	}
	indexBytes, err := fetchDatasetPublicationCID(ctx, opts.FetchByCID, opts.FetchRetryDelays, indexAsset.CID)
	if err != nil {
		return nil, fmt.Errorf("fetch index CID %s: %w", indexAsset.CID, err)
	}
	if err := verifyBytesCIDAndHash("index", indexBytes, indexAsset.CID, indexAsset.SHA256); err != nil {
		return nil, err
	}
	imported, index, err := store.ImportDatasetShard(shardBytes, indexBytes, string(manifest.PROVIDER_PEER_ID()))
	if err != nil {
		return nil, err
	}
	if err := recordManifestSourceBatchLicenses(store, manifest, index.SchemaName); err != nil {
		return nil, err
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

func materializeDatasetPublicationFromFiles(ctx context.Context, store *FlatSQLStore, opts DatasetPublicationReplayOptions, manifest *dpm.DPM, manifestCID string, shardAsset, indexAsset publicationAsset) (*DatasetPublicationReplayResult, error) {
	workDir, cleanup, err := datasetPublicationMaterializationWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	shardPath := filepath.Join(workDir, datasetPublicationPathComponent(shardAsset.CID)+".fbshard")
	indexPath := filepath.Join(workDir, datasetPublicationPathComponent(indexAsset.CID)+".index.json")
	if err := fetchDatasetPublicationCIDToFile(ctx, opts.FetchByCIDToFile, opts.FetchRetryDelays, shardAsset.CID, shardPath); err != nil {
		return nil, fmt.Errorf("fetch shard CID %s: %w", shardAsset.CID, err)
	}
	if _, _, err := verifyFileCIDAndHash("shard", shardPath, shardAsset.CID, shardAsset.SHA256); err != nil {
		return nil, err
	}
	if err := fetchDatasetPublicationCIDToFile(ctx, opts.FetchByCIDToFile, opts.FetchRetryDelays, indexAsset.CID, indexPath); err != nil {
		return nil, fmt.Errorf("fetch index CID %s: %w", indexAsset.CID, err)
	}
	if _, _, err := verifyFileCIDAndHash("index", indexPath, indexAsset.CID, indexAsset.SHA256); err != nil {
		return nil, err
	}
	imported, index, err := store.ImportDatasetShardFromFiles(shardPath, indexPath, string(manifest.PROVIDER_PEER_ID()))
	if err != nil {
		return nil, err
	}
	if err := recordManifestSourceBatchLicenses(store, manifest, index.SchemaName); err != nil {
		return nil, err
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

// recordManifestSourceBatchLicenses copies the licence terms a verified DPM
// binds into the importing node's own batch-licence table, so that a
// subscriber that later republishes these records carries the same terms. The
// schema's own words: "share-alike terms propagate from this batch to every
// derived record" — that propagation has to survive the hop, or the second
// publisher strips the licence off data it did not originate.
//
// DPMSourceBatch carries no provider id of its own, so provider attribution
// follows the same rule the signature round-trip uses (rebuildUnsignedDataset
// Manifest): the query binding's first PROVIDER_ID, else the provider peer id.
func recordManifestSourceBatchLicenses(store *FlatSQLStore, manifest *dpm.DPM, schemaName string) error {
	if store == nil || manifest == nil || strings.TrimSpace(schemaName) == "" {
		return nil
	}
	providerID := strings.TrimSpace(string(manifest.PROVIDER_PEER_ID()))
	if query := manifest.QUERY(nil); query != nil {
		if providerIDs := dpmStringVectorValues(query.PROVIDER_IDSLength(), query.PROVIDER_IDS); len(providerIDs) > 0 {
			providerID = providerIDs[0]
		}
	}
	for i := 0; i < manifest.SOURCESLength(); i++ {
		var source dpm.DPMSourceBatch
		if !manifest.SOURCES(&source, i) {
			continue
		}
		license := SourceBatchLicense{
			SchemaName: schemaName,
			ProviderID: providerID,
			SourceName: string(source.SOURCE_NAME()),
			BatchID:    string(source.SOURCE_SHA256()),
			License:    string(source.LICENSE()),
			LicenseURL: string(source.LICENSE_URL()),
			Citation:   string(source.CITATION()),
		}
		if license.IsEmpty() {
			continue
		}
		if err := store.UpsertSourceBatchLicense(license); err != nil {
			return fmt.Errorf("record imported source batch license: %w", err)
		}
	}
	return nil
}

func datasetPublicationMaterializationWorkDir(configured string) (string, func(), error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if err := os.MkdirAll(configured, 0o700); err != nil {
			return "", func() {}, fmt.Errorf("create dataset publication work dir: %w", err)
		}
		return configured, func() {}, nil
	}
	tmpDir, err := os.MkdirTemp("", "sdn-dataset-publication-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create dataset publication temp dir: %w", err)
	}
	return tmpDir, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func fetchDatasetPublicationCID(ctx context.Context, fetch func(context.Context, string) ([]byte, error), retryDelays []time.Duration, cidValue string) ([]byte, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		data, err := fetch(ctx, cidValue)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if attempt >= len(retryDelays) {
			return nil, lastErr
		}
		delay := retryDelays[attempt]
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func fetchDatasetPublicationCIDToFile(ctx context.Context, fetch func(context.Context, string, string) error, retryDelays []time.Duration, cidValue, path string) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		err := fetch(ctx, cidValue, path)
		if err == nil {
			return nil
		}
		_ = os.Remove(path)
		lastErr = err
		if attempt >= len(retryDelays) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelays[attempt]):
		}
	}
}

func verifyDatasetPublicationPNM(pnmBytes []byte, providerPublicKey ed25519.PublicKey) (string, string, error) {
	if len(pnmBytes) == 0 {
		return "", "", fmt.Errorf("PNM bytes are required")
	}
	if len(providerPublicKey) != ed25519.PublicKeySize {
		return "", "", fmt.Errorf("ed25519 provider public key is required")
	}
	if !PNM.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		return "", "", fmt.Errorf("PNM buffer missing identifier")
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	manifestCID := strings.TrimSpace(string(pnm.CID()))
	fileID := strings.TrimSpace(string(pnm.FILE_ID()))
	if manifestCID == "" {
		return "", "", fmt.Errorf("PNM missing manifest CID")
	}
	if fileID == "" {
		return "", "", fmt.Errorf("PNM missing FILE_ID")
	}
	if sigType := strings.TrimSpace(string(pnm.SIGNATURE_TYPE())); sigType != "Ed25519" {
		return "", "", fmt.Errorf("PNM SIGNATURE_TYPE = %q, want Ed25519", sigType)
	}
	pnmSignature, err := hex.DecodeString(strings.TrimSpace(string(pnm.SIGNATURE())))
	if err != nil {
		return "", "", fmt.Errorf("decode PNM signature: %w", err)
	}
	if !ed25519.Verify(providerPublicKey, datasetPublicationPNMSignaturePayload(manifestCID, fileID), pnmSignature) {
		return "", "", fmt.Errorf("invalid PNM signature")
	}
	return manifestCID, fileID, nil
}

// ImportDatasetShard imports a native FlatSQL size-prefixed dataset shard using
// its materialized export index. It is idempotent because records are content
// addressed in the underlying FlatSQL tables.
func (s *FlatSQLStore) ImportDatasetShard(shardBytes, indexBytes []byte, providerPeerID string) (int, *DatasetExportIndex, error) {
	if s == nil {
		return 0, nil, fmt.Errorf("store is required")
	}
	index, err := parseDatasetExportIndexBytes(indexBytes)
	if err != nil {
		return 0, nil, err
	}
	if err := verifyBytesCIDAndHash("shard", shardBytes, index.ShardCID, index.ShardSHA256); err != nil {
		return 0, nil, err
	}
	if index.ResultSHA256 != "" && sha256Hex(shardBytes) != index.ResultSHA256 {
		return 0, nil, fmt.Errorf("shard result SHA-256 does not match index")
	}
	imported, importedIndex, err := s.importDatasetShardRecords(index, providerPeerID, func(record DatasetExportIndexRecord) ([]byte, error) {
		if record.Offset < 0 || record.Length < 0 || record.Offset+4+record.Length > int64(len(shardBytes)) {
			return nil, fmt.Errorf("record %s offset/length outside shard", record.CID)
		}
		frame := shardBytes[record.Offset:]
		length := int64(binary.LittleEndian.Uint32(frame[:4]))
		if length != record.Length {
			return nil, fmt.Errorf("record %s frame length = %d, want %d", record.CID, length, record.Length)
		}
		return frame[4 : 4+length], nil
	})
	if err != nil {
		return imported, nil, err
	}
	return imported, importedIndex, nil
}

// ImportDatasetShardFromFiles imports a native FlatSQL size-prefixed dataset
// shard from disk using its materialized export index. The shard payload is
// verified from the file and records are read one at a time, avoiding whole
// shard hydration in memory.
func (s *FlatSQLStore) ImportDatasetShardFromFiles(shardPath, indexPath, providerPeerID string) (int, *DatasetExportIndex, error) {
	if s == nil {
		return 0, nil, fmt.Errorf("store is required")
	}
	shardPath = strings.TrimSpace(shardPath)
	indexPath = strings.TrimSpace(indexPath)
	if shardPath == "" {
		return 0, nil, fmt.Errorf("shard path is required")
	}
	if indexPath == "" {
		return 0, nil, fmt.Errorf("index path is required")
	}
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		return 0, nil, fmt.Errorf("read dataset export index: %w", err)
	}
	index, err := parseDatasetExportIndexBytes(indexBytes)
	if err != nil {
		return 0, nil, err
	}
	shardSHA, shardSize, err := verifyFileCIDAndHash("shard", shardPath, index.ShardCID, index.ShardSHA256)
	if err != nil {
		return 0, nil, err
	}
	if index.ResultSHA256 != "" && shardSHA != index.ResultSHA256 {
		return 0, nil, fmt.Errorf("shard result SHA-256 does not match index")
	}
	shardFile, err := os.Open(shardPath)
	if err != nil {
		return 0, nil, fmt.Errorf("open dataset shard: %w", err)
	}
	defer shardFile.Close()

	imported, importedIndex, err := s.importDatasetShardRecords(index, providerPeerID, func(record DatasetExportIndexRecord) ([]byte, error) {
		if record.Offset < 0 || record.Length < 0 || record.Offset+4+record.Length > shardSize {
			return nil, fmt.Errorf("record %s offset/length outside shard", record.CID)
		}
		var frameLength [4]byte
		if _, err := shardFile.ReadAt(frameLength[:], record.Offset); err != nil {
			return nil, fmt.Errorf("read record %s frame length: %w", record.CID, err)
		}
		length := int64(binary.LittleEndian.Uint32(frameLength[:]))
		if length != record.Length {
			return nil, fmt.Errorf("record %s frame length = %d, want %d", record.CID, length, record.Length)
		}
		if record.Length > int64(^uint(0)>>1) {
			return nil, fmt.Errorf("record %s length exceeds addressable memory", record.CID)
		}
		data := make([]byte, int(record.Length))
		if _, err := shardFile.ReadAt(data, record.Offset+4); err != nil {
			return nil, fmt.Errorf("read record %s payload: %w", record.CID, err)
		}
		return data, nil
	})
	if err != nil {
		return imported, nil, err
	}
	return imported, importedIndex, nil
}

type datasetShardRecordReader func(record DatasetExportIndexRecord) ([]byte, error)

func parseDatasetExportIndexBytes(indexBytes []byte) (*DatasetExportIndex, error) {
	var index DatasetExportIndex
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return nil, fmt.Errorf("parse dataset export index: %w", err)
	}
	if index.Version != 1 {
		return nil, fmt.Errorf("dataset export index version = %d, want 1", index.Version)
	}
	if strings.TrimSpace(index.SchemaName) == "" {
		return nil, fmt.Errorf("dataset export index missing schema name")
	}
	if index.RecordCount != len(index.Records) {
		return nil, fmt.Errorf("dataset export index record count = %d, want %d records", index.RecordCount, len(index.Records))
	}
	return &index, nil
}

func (s *FlatSQLStore) importDatasetShardRecords(index *DatasetExportIndex, providerPeerID string, readRecord datasetShardRecordReader) (int, *DatasetExportIndex, error) {
	if index == nil {
		return 0, nil, fmt.Errorf("dataset export index is required")
	}
	if _, err := sds.SchemaNameToTable(index.SchemaName); err != nil {
		return 0, nil, fmt.Errorf("invalid schema name: %w", err)
	}

	// Chunked lock windows (storeWriteChunkSize records per store write-lock
	// hold + transaction). The pre-chunking shape — ONE lock hold + ONE tx
	// spanning the entire shard — blacked out the whole data API for the
	// import duration when a peer announced a full-catalog dataset
	// (2026-07-06: readers waited >11 min on s.mu.RLock behind a 31.8K-record
	// import). Each chunk commits atomically; the import is CID-idempotent
	// (existing CIDs are mirrored, not duplicated), so a mid-shard failure
	// followed by the announcement retry converges — the same consistency
	// readers already observe from the record-by-record datasync path.
	imported := 0
	records := index.Records
	for start := 0; start < len(records); start += storeWriteChunkSize {
		end := start + storeWriteChunkSize
		if end > len(records) {
			end = len(records)
		}
		n, err := s.importDatasetShardChunk(index, providerPeerID, records[start:end], readRecord)
		imported += n
		if err != nil {
			return imported, nil, err
		}
	}
	return imported, index, nil
}

// importDatasetShardChunk imports one chunk of shard records under one store
// lock, stream-appender session, and control transaction (the pre-chunking
// importDatasetShardRecords body).
func (s *FlatSQLStore) importDatasetShardChunk(index *DatasetExportIndex, providerPeerID string, records []DatasetExportIndexRecord, readRecord datasetShardRecordReader) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	appender, err := s.newFlatSQLStreamAppender(index.SchemaName)
	if err != nil {
		return 0, fmt.Errorf("open imported %s FlatSQL stream: %w", index.SchemaName, err)
	}
	defer appender.Close()

	// WS7.3d routed-only writes: imported rows land in the provider's
	// (producer, standard) table (pre-created outside the tx — no DDL inside).
	routedTable, err := s.ensureProducerStandardTable(routedProducerID(providerPeerID), index.SchemaName)
	if err != nil {
		return 0, fmt.Errorf("ensure (producer, standard) table: %w", err)
	}
	// FILTERED read source: upsertSourceTagsTx below looks each record up BY CID
	// once per imported row, inside the store write lock. An outer-only predicate
	// full-scans every (producer, standard) table per row — see
	// recordReadSourceFiltered.
	readSource, err := s.recordReadSourceFiltered(index.SchemaName, "cid = ?1")
	if err != nil {
		return 0, fmt.Errorf("record read source: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin dataset shard import: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	imported := 0
	now := time.Now().Unix()
	catalogEvents := make([]recordCatalogEvent, 0, len(records)*2)
	// Records of a ROUTED schema are mirrored into the engine record vtabs
	// after the chunk commits, exactly as StoreWithSourceTags does on the
	// local write path. Without this, a materialized dataset shard was
	// durable but INVISIBLE to every flow that reads through
	// storage.flatsql_query_stream -> QueryRawStream until the next process
	// boot rebuilt the hot window — which is precisely the cellular cache
	// lane's failure mode: host-01 materializes host-02's $TBS feed head and
	// the aggregate cache still answers empty
	// (sdn-tbs-feed-sync-for-cache-lane).
	var enginePending []engineIngest
	engineRouted := engineRoutesSchema(index.SchemaName)
	for _, record := range records {
		data, err := readRecord(record)
		if err != nil {
			return imported, err
		}
		// Accept either the current CIDv1 (raw codec, sha2-256 multihash) or
		// the legacy bare SHA-256 hex digest computeCID emitted before loop
		// A4: dataset shard bundles exported by an older build carry
		// bare-hex CIDs in their index JSON, and those bundles must remain
		// importable. Either way the record keeps the identity it already
		// carries in the (trusted, signed) index — importing never rewrites
		// record.CID to the new format.
		if computeCID(data) != record.CID && sha256Hex(data) != record.CID {
			return imported, fmt.Errorf("record CID mismatch for indexed record %s", record.CID)
		}
		tags := record.SourceTags
		if strings.TrimSpace(tags.ProviderID) == "" {
			tags.ProviderID = strings.TrimSpace(providerPeerID)
		}
		// The peer that served this shard IS the producer of record for these
		// rows, and it is the one fact this node knows first-hand. Two shapes
		// arrive without one: an empty tag, and normalizeSourceTags' back-fill
		// of the PROVIDER name — which the producer feed correctly refuses to
		// report as a peer, because a provider name is not an identity. Either
		// way the shard would fill this store while the board showed an idle
		// node. A tag that names a DIFFERENT peer is left alone: that is a
		// relayed record and its origin is not ours to rewrite.
		if producer := strings.TrimSpace(tags.ProducerPeerID); producer == "" || producer == strings.TrimSpace(tags.ProviderID) {
			tags.ProducerPeerID = strings.TrimSpace(providerPeerID)
		}

		var existing int
		err = tx.QueryRow(`SELECT 1 FROM sdn_record_index WHERE schema_name = ? AND cid = ?`, index.SchemaName, record.CID).Scan(&existing)
		switch {
		case err == nil:
			// Repeat CID: record it under this provider's table too.
			s.mirrorRoutedRecordFromExisting(tx, index.SchemaName, record.CID, strings.TrimSpace(providerPeerID), nil)
		case errors.Is(err, sql.ErrNoRows):
			streamPath, streamOffset, recordLength, err := appender.Append(data)
			if err != nil {
				return imported, fmt.Errorf("append imported %s record %s to FlatSQL stream: %w", index.SchemaName, record.CID, err)
			}
			rowID, err := insertSchemaMetadataReturningRowID(tx, routedTable, record.CID, strings.TrimSpace(providerPeerID), now, streamPath, streamOffset, recordLength, nil, now)
			if err != nil {
				return imported, fmt.Errorf("store imported %s record %s: %w", index.SchemaName, record.CID, err)
			}
			if err := upsertRecordIndexExec(tx, &s.recordIndexRowIDs, index.SchemaName, record.CID, now, data); err != nil {
				log.Warnf("Failed to index imported %s record %s: %v", index.SchemaName, record.CID[:16]+"...", err)
			}
			event, err := s.recordCatalogUpsertEvent(tx, index.SchemaName, record.CID, strings.TrimSpace(providerPeerID), now, streamPath, streamOffset, recordLength, nil, now, data)
			if err != nil {
				return imported, fmt.Errorf("record catalog event for imported %s record %s: %w", index.SchemaName, record.CID, err)
			}
			catalogEvents = append(catalogEvents, event)
			if engineRouted {
				enginePending = append(enginePending, engineIngest{data: data, source: engineSourceName(&tags)})
			}
			imported++
			if strings.TrimSpace(tags.ProviderID) != "" && strings.TrimSpace(tags.SourceName) != "" {
				if err := insertNewSourceTagsTx(tx, index.SchemaName, record.CID, tags, recordLength, rowID); err != nil {
					return imported, err
				}
				tagEvent, err := recordCatalogTagUpsertEvent(tx, index.SchemaName, record.CID, tags)
				if err != nil {
					return imported, fmt.Errorf("record catalog source tag event for imported %s record %s: %w", index.SchemaName, record.CID, err)
				}
				catalogEvents = append(catalogEvents, tagEvent)
			}
		default:
			return imported, fmt.Errorf("check imported %s record %s: %w", index.SchemaName, record.CID, err)
		}

		if err == nil && strings.TrimSpace(tags.ProviderID) != "" && strings.TrimSpace(tags.SourceName) != "" {
			if err := upsertSourceTagsTx(tx, readSource, index.SchemaName, record.CID, tags, record.Length); err != nil {
				return imported, err
			}
			tagEvent, err := recordCatalogTagUpsertEvent(tx, index.SchemaName, record.CID, tags)
			if err != nil {
				return imported, fmt.Errorf("record catalog source tag event for imported %s record %s: %w", index.SchemaName, record.CID, err)
			}
			catalogEvents = append(catalogEvents, tagEvent)
		}
	}
	if err := appender.Close(); err != nil {
		return imported, fmt.Errorf("flush imported %s FlatSQL stream: %w", index.SchemaName, err)
	}
	if err := tx.Commit(); err != nil {
		return imported, fmt.Errorf("commit dataset shard import: %w", err)
	}
	committed = true
	if err := s.appendCatalogEvents(catalogEvents); err != nil {
		return imported, fmt.Errorf("append record catalog events: %w", err)
	}
	if len(enginePending) > 0 {
		// The engine vtab is a cache over the durable substrate committed
		// above, so a mirroring failure never unwinds the import; only a
		// poisoned (trapped) runtime is returned.
		if err := s.ingestEngineRecords(index.SchemaName, enginePending); err != nil {
			return imported, fmt.Errorf("mirror imported %s records into the engine: %w", index.SchemaName, err)
		}
	}
	return imported, nil
}

// BuildSignedDatasetPublicationManifest writes a signed SDS DPM manifest that
// binds an exported shard, its materialized query index, source batches, and the
// canonical replay query.
func BuildSignedDatasetPublicationManifest(outputDir string, opts DatasetPublicationManifestOptions) (*DatasetPublicationManifest, error) {
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("output dir is required")
	}
	if opts.Export == nil {
		return nil, fmt.Errorf("dataset export is required")
	}
	if strings.TrimSpace(opts.DatasetID) == "" {
		return nil, fmt.Errorf("dataset id is required")
	}
	if strings.TrimSpace(opts.UpdateID) == "" {
		return nil, fmt.Errorf("update id is required")
	}
	if strings.TrimSpace(opts.FileID) == "" {
		opts.FileID = datasetPublicationFileID(opts)
	}
	if strings.TrimSpace(opts.ProviderPeerID) == "" {
		return nil, fmt.Errorf("provider peer id is required")
	}
	if len(opts.SigningKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519 signing key is required")
	}
	if opts.PublishedAt.IsZero() {
		opts.PublishedAt = time.Now().UTC()
	}
	if opts.QueryEngine == "" {
		opts.QueryEngine = "FlatSQL"
	}
	if opts.QueryEngineVers == "" {
		opts.QueryEngineVers = "sdn-index-v1"
	}

	unsigned, err := buildDatasetPublicationManifestBytes(opts, nil, "")
	if err != nil {
		return nil, err
	}
	payloadHash := sha256.Sum256(unsigned)
	signature := ed25519.Sign(opts.SigningKey, payloadHash[:])
	signed, err := buildDatasetPublicationManifestBytes(opts, signature, "Ed25519")
	if err != nil {
		return nil, err
	}
	manifestSHA := sha256Hex(signed)
	manifestCID, err := cidV1RawSHA256(signed)
	if err != nil {
		return nil, fmt.Errorf("compute manifest CID: %w", err)
	}
	manifestPath := filepath.Join(outputDir, "manifests", fmt.Sprintf("%s-%s.dpm", opts.DatasetID, manifestSHA[:16]))
	if err := writeImmutableExportFile(manifestPath, signed); err != nil {
		return nil, err
	}
	return &DatasetPublicationManifest{
		Path:                   manifestPath,
		CID:                    manifestCID,
		FileID:                 strings.TrimSpace(opts.FileID),
		SHA256:                 manifestSHA,
		Bytes:                  signed,
		ByteLength:             int64(len(signed)),
		Signature:              signature,
		SignaturePayloadSHA256: payloadHash,
	}, nil
}

func buildDatasetPublicationManifestBytes(opts DatasetPublicationManifestOptions, signature []byte, signatureType string) ([]byte, error) {
	export := opts.Export
	if strings.TrimSpace(export.ShardCID) == "" || strings.TrimSpace(export.IndexCID) == "" {
		return nil, fmt.Errorf("export shard and index CIDs are required")
	}
	if strings.TrimSpace(export.QuerySHA256) == "" || strings.TrimSpace(export.ResultSHA256) == "" {
		return nil, fmt.Errorf("export query and result hashes are required")
	}

	builder := flatbuffers.NewBuilder(1024)
	version := builder.CreateString("1.0.0")
	datasetID := builder.CreateString(opts.DatasetID)
	updateID := builder.CreateString(opts.UpdateID)
	fileIDValue := strings.TrimSpace(opts.FileID)
	if fileIDValue == "" {
		fileIDValue = datasetPublicationFileID(opts)
	}
	fileID := builder.CreateString(fileIDValue)
	providerPeerID := builder.CreateString(opts.ProviderPeerID)
	providerEPMCID := builder.CreateString(opts.ProviderEPMCID)
	publishedAt := builder.CreateString(opts.PublishedAt.UTC().Format(time.RFC3339Nano))
	signatureTypeOffset := builder.CreateString(signatureType)

	assetOffsets := []flatbuffers.UOffsetT{
		buildDPMAsset(builder, "data", export.ShardCID, "/ipfs/"+export.ShardCID, filepath.Base(export.ShardPath), fileIDValue, export.ShardBytes, export.ShardSHA256, export.ResultSHA256, export.SchemaName, opts.SchemaHash, export.ContentKeyID),
		buildDPMAsset(builder, "index", export.IndexCID, "/ipfs/"+export.IndexCID, filepath.Base(export.IndexPath), fileIDValue, export.IndexBytes, export.IndexSHA256, "", "DPM.index.json", opts.SchemaHash, export.ContentKeyID),
	}
	for _, aux := range opts.AuxiliaryAssets {
		if strings.TrimSpace(aux.CID) == "" {
			return nil, fmt.Errorf("auxiliary DPM asset requires a CID")
		}
		assetOffsets = append(assetOffsets, buildDPMAsset(builder, "other", aux.CID, "/ipfs/"+aux.CID, aux.FileName, aux.FileID, aux.ByteLength, aux.ByteSHA256, aux.DataRoot, aux.SchemaName, "", ""))
	}
	assetsVector := buildOffsetVector(builder, assetOffsets, dpm.DPMStartASSETSVector)

	sourceOffsets := make([]flatbuffers.UOffsetT, 0, len(export.SourceBatches))
	for _, source := range export.SourceBatches {
		sourceOffsets = append(sourceOffsets, buildDPMSourceBatch(builder, source))
	}
	sourcesVector := buildOffsetVector(builder, sourceOffsets, dpm.DPMStartSOURCESVector)
	queryOffset := buildDPMQueryBinding(builder, export, opts)
	encryptionOffset := buildDPMEncryptionBinding(builder, export)
	signatureOffset := flatbuffers.UOffsetT(0)
	if len(signature) > 0 {
		signatureOffset = builder.CreateByteVector(signature)
	}

	dpm.DPMStart(builder)
	dpm.DPMAddVERSION(builder, version)
	dpm.DPMAddDATASET_ID(builder, datasetID)
	dpm.DPMAddUPDATE_ID(builder, updateID)
	dpm.DPMAddFILE_ID(builder, fileID)
	dpm.DPMAddPROVIDER_PEER_ID(builder, providerPeerID)
	dpm.DPMAddPROVIDER_EPM_CID(builder, providerEPMCID)
	dpm.DPMAddPUBLISH_TIMESTAMP(builder, publishedAt)
	dpm.DPMAddASSETS(builder, assetsVector)
	dpm.DPMAddSOURCES(builder, sourcesVector)
	dpm.DPMAddQUERY(builder, queryOffset)
	dpm.DPMAddENCRYPTION(builder, encryptionOffset)
	if signatureOffset != 0 {
		dpm.DPMAddPROVIDER_SIGNATURE(builder, signatureOffset)
		dpm.DPMAddSIGNATURE_TYPE(builder, signatureTypeOffset)
	}
	root := dpm.DPMEnd(builder)
	dpm.FinishDPMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...), nil
}

func buildDPMAsset(builder *flatbuffers.Builder, kind, cidValue, multiaddr, fileName, fileID string, byteLength int64, byteSHA256, dataRoot, schemaName, schemaHash, contentKeyID string) flatbuffers.UOffsetT {
	cidOffset := builder.CreateString(cidValue)
	multiaddrOffset := builder.CreateString(multiaddr)
	fileNameOffset := builder.CreateString(fileName)
	fileIDOffset := builder.CreateString(fileID)
	byteSHAOffset := builder.CreateString(byteSHA256)
	dataRootOffset := builder.CreateString(dataRoot)
	schemaNameOffset := builder.CreateString(schemaName)
	schemaHashOffset := builder.CreateString(schemaHash)
	contentKeyOffset := builder.CreateString(contentKeyID)
	dpm.DPMAssetStart(builder)
	switch kind {
	case "data":
		dpm.DPMAssetAddASSET_KIND(builder, 0)
	case "index":
		dpm.DPMAssetAddASSET_KIND(builder, 1)
	case "manifest":
		dpm.DPMAssetAddASSET_KIND(builder, 2)
	default:
		dpm.DPMAssetAddASSET_KIND(builder, 3)
	}
	dpm.DPMAssetAddTRANSPORT_KIND(builder, 0)
	dpm.DPMAssetAddCID(builder, cidOffset)
	dpm.DPMAssetAddMULTIFORMAT_ADDRESS(builder, multiaddrOffset)
	dpm.DPMAssetAddFILE_NAME(builder, fileNameOffset)
	dpm.DPMAssetAddFILE_ID(builder, fileIDOffset)
	if byteLength > 0 {
		dpm.DPMAssetAddBYTE_LENGTH(builder, uint64(byteLength))
	}
	dpm.DPMAssetAddBYTE_SHA256(builder, byteSHAOffset)
	dpm.DPMAssetAddDATA_ROOT(builder, dataRootOffset)
	dpm.DPMAssetAddSCHEMA_NAME(builder, schemaNameOffset)
	dpm.DPMAssetAddSCHEMA_HASH(builder, schemaHashOffset)
	dpm.DPMAssetAddCONTENT_KEY_ID(builder, contentKeyOffset)
	return dpm.DPMAssetEnd(builder)
}

func buildDPMSourceBatch(builder *flatbuffers.Builder, source DatasetExportSourceBatch) flatbuffers.UOffsetT {
	sourceName := builder.CreateString(source.SourceName)
	sourceURL := builder.CreateString(source.SourceURL)
	sourceSHA := builder.CreateString(source.SourceSHA256)
	httpETag := builder.CreateString(source.HTTPETag)
	httpLastModified := builder.CreateString(source.HTTPLastModified)
	retrievedAt := builder.CreateString(source.RetrievedAt)
	parserVersion := builder.CreateString(source.ParserVersion)
	// Licence fields are written ONLY when the batch declared them. A batch
	// with no licence metadata must produce the exact bytes this node produced
	// before licence carriage existed: an unset FlatBuffers slot leaves the
	// vtable — and therefore the manifest CID and the provider signature —
	// unchanged. The share-alike flag has no DPM field; it stays node-side
	// (SourceBatchLicense) and in the wasm-authored provenance sidecar.
	var license, licenseURL, citation flatbuffers.UOffsetT
	if strings.TrimSpace(source.License) != "" {
		license = builder.CreateString(source.License)
	}
	if strings.TrimSpace(source.LicenseURL) != "" {
		licenseURL = builder.CreateString(source.LicenseURL)
	}
	if strings.TrimSpace(source.Citation) != "" {
		citation = builder.CreateString(source.Citation)
	}
	dpm.DPMSourceBatchStart(builder)
	dpm.DPMSourceBatchAddSOURCE_NAME(builder, sourceName)
	dpm.DPMSourceBatchAddSOURCE_URL(builder, sourceURL)
	dpm.DPMSourceBatchAddSOURCE_SHA256(builder, sourceSHA)
	dpm.DPMSourceBatchAddHTTP_ETAG(builder, httpETag)
	dpm.DPMSourceBatchAddHTTP_LAST_MODIFIED(builder, httpLastModified)
	dpm.DPMSourceBatchAddRETRIEVED_AT(builder, retrievedAt)
	dpm.DPMSourceBatchAddPARSER_VERSION(builder, parserVersion)
	dpm.DPMSourceBatchAddRECORD_COUNT(builder, source.RecordCount)
	if license != 0 {
		dpm.DPMSourceBatchAddLICENSE(builder, license)
	}
	if licenseURL != 0 {
		dpm.DPMSourceBatchAddLICENSE_URL(builder, licenseURL)
	}
	if citation != 0 {
		dpm.DPMSourceBatchAddCITATION(builder, citation)
	}
	return dpm.DPMSourceBatchEnd(builder)
}

func buildDPMQueryBinding(builder *flatbuffers.Builder, export *DatasetExport, opts DatasetPublicationManifestOptions) flatbuffers.UOffsetT {
	canonicalQuery := builder.CreateString(export.CanonicalQuery)
	querySHA := builder.CreateString(export.QuerySHA256)
	resultSHA := builder.CreateString(export.ResultSHA256)
	queryEngine := builder.CreateString(opts.QueryEngine)
	queryEngineVersion := builder.CreateString(opts.QueryEngineVers)
	canonicalOrder := builder.CreateString("FlatSQL export order v1")
	queryProtocol := builder.CreateString("")
	windowStart, windowEnd := queryWindowFromCanonical(export.CanonicalQuery)
	windowStartOffset := builder.CreateString(windowStart)
	windowEndOffset := builder.CreateString(windowEnd)
	schemaNames := buildStringVector(builder, []string{export.SchemaName}, dpm.DPMQueryBindingStartSCHEMA_NAMESVector)
	providerIDs := buildStringVector(builder, uniqueNonEmptySourceField(export.SourceBatches, func(s DatasetExportSourceBatch) string { return s.ProviderID }), dpm.DPMQueryBindingStartPROVIDER_IDSVector)
	sourceNames := buildStringVector(builder, uniqueNonEmptySourceField(export.SourceBatches, func(s DatasetExportSourceBatch) string { return s.SourceName }), dpm.DPMQueryBindingStartSOURCE_NAMESVector)
	batchIDs := buildStringVector(builder, uniqueNonEmptySourceField(export.SourceBatches, func(s DatasetExportSourceBatch) string { return s.SourceSHA256 }), dpm.DPMQueryBindingStartBATCH_IDSVector)

	dpm.DPMQueryBindingStart(builder)
	dpm.DPMQueryBindingAddCANONICAL_QUERY(builder, canonicalQuery)
	dpm.DPMQueryBindingAddQUERY_SHA256(builder, querySHA)
	dpm.DPMQueryBindingAddRESULT_SHA256(builder, resultSHA)
	dpm.DPMQueryBindingAddQUERY_ENGINE(builder, queryEngine)
	dpm.DPMQueryBindingAddQUERY_ENGINE_VERSION(builder, queryEngineVersion)
	dpm.DPMQueryBindingAddCANONICAL_ORDER(builder, canonicalOrder)
	dpm.DPMQueryBindingAddQUERY_PROTOCOL(builder, queryProtocol)
	dpm.DPMQueryBindingAddSCHEMA_NAMES(builder, schemaNames)
	dpm.DPMQueryBindingAddPROVIDER_IDS(builder, providerIDs)
	dpm.DPMQueryBindingAddSOURCE_NAMES(builder, sourceNames)
	dpm.DPMQueryBindingAddBATCH_IDS(builder, batchIDs)
	dpm.DPMQueryBindingAddWINDOW_START(builder, windowStartOffset)
	dpm.DPMQueryBindingAddWINDOW_END(builder, windowEndOffset)
	return dpm.DPMQueryBindingEnd(builder)
}

func buildDPMEncryptionBinding(builder *flatbuffers.Builder, export *DatasetExport) flatbuffers.UOffsetT {
	contentKey := builder.CreateString(export.ContentKeyID)
	policy := builder.CreateString(export.EncryptionPolicy)
	algorithm := builder.CreateString("")
	dpm.DPMEncryptionBindingStart(builder)
	dpm.DPMEncryptionBindingAddENCRYPTED(builder, export.ContentKeyID != "" && export.ContentKeyID != "public")
	dpm.DPMEncryptionBindingAddALGORITHM(builder, algorithm)
	dpm.DPMEncryptionBindingAddCONTENT_KEY_ID(builder, contentKey)
	dpm.DPMEncryptionBindingAddPOLICY_ID(builder, policy)
	return dpm.DPMEncryptionBindingEnd(builder)
}

func buildOffsetVector(builder *flatbuffers.Builder, offsets []flatbuffers.UOffsetT, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	start(builder, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		builder.PrependUOffsetT(offsets[i])
	}
	return builder.EndVector(len(offsets))
}

func buildStringVector(builder *flatbuffers.Builder, values []string, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	offsets := make([]flatbuffers.UOffsetT, 0, len(values))
	for _, value := range values {
		offsets = append(offsets, builder.CreateString(value))
	}
	return buildOffsetVector(builder, offsets, start)
}

func uniqueNonEmptySourceField(sources []DatasetExportSourceBatch, selectValue func(DatasetExportSourceBatch) string) []string {
	seen := map[string]bool{}
	values := make([]string, 0, len(sources))
	for _, source := range sources {
		value := strings.TrimSpace(selectValue(source))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	return values
}

func queryWindowFromCanonical(canonical string) (string, string) {
	// The canonical query JSON already carries the full replay filter. These
	// fields are optional DPM conveniences, so leave them empty here rather than
	// parsing the canonical document a second time.
	return "", ""
}

func datasetPublicationFileID(opts DatasetPublicationManifestOptions) string {
	parts := []string{strings.TrimSpace(opts.DatasetID), strings.TrimSpace(opts.Export.SchemaName), strings.TrimSpace(opts.UpdateID)}
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, ":")
}

type publicationAsset struct {
	Kind   string
	CID    string
	File   string
	SHA256 string
	Bytes  uint64
	Schema string
}

func parseAndVerifyDatasetManifest(manifestBytes []byte, providerPublicKey ed25519.PublicKey) (*dpm.DPM, []byte, error) {
	if !dpm.DPMBufferHasIdentifier(manifestBytes) {
		return nil, nil, fmt.Errorf("DPM buffer missing identifier")
	}
	manifest := dpm.GetRootAsDPM(manifestBytes, 0)
	if sigType := strings.TrimSpace(string(manifest.SIGNATURE_TYPE())); sigType != "Ed25519" {
		return nil, nil, fmt.Errorf("DPM SIGNATURE_TYPE = %q, want Ed25519", sigType)
	}
	signature := manifest.PROVIDER_SIGNATUREBytes()
	if len(signature) != ed25519.SignatureSize {
		return nil, nil, fmt.Errorf("DPM provider signature length = %d, want %d", len(signature), ed25519.SignatureSize)
	}
	unsigned, err := rebuildUnsignedDatasetManifest(manifest)
	if err != nil {
		return nil, nil, err
	}
	payloadHash := sha256.Sum256(unsigned)
	if !ed25519.Verify(providerPublicKey, payloadHash[:], signature) {
		return nil, nil, fmt.Errorf("invalid DPM provider signature")
	}
	return manifest, unsigned, nil
}

func rebuildUnsignedDatasetManifest(manifest *dpm.DPM) ([]byte, error) {
	query := manifest.QUERY(nil)
	if query == nil {
		return nil, fmt.Errorf("DPM missing query binding")
	}
	assetMap, err := manifestAssetMap(manifest)
	if err != nil {
		return nil, err
	}
	shard, ok := assetMap["DATA_SHARD"]
	if !ok {
		return nil, fmt.Errorf("DPM missing DATA_SHARD asset")
	}
	index, ok := assetMap["QUERY_INDEX"]
	if !ok {
		return nil, fmt.Errorf("DPM missing QUERY_INDEX asset")
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, string(manifest.PUBLISH_TIMESTAMP()))
	if err != nil {
		return nil, fmt.Errorf("parse DPM publish timestamp: %w", err)
	}
	providerIDs := dpmStringVectorValues(query.PROVIDER_IDSLength(), query.PROVIDER_IDS)
	providerID := string(manifest.PROVIDER_PEER_ID())
	if len(providerIDs) > 0 {
		providerID = providerIDs[0]
	}
	sourceBatches := make([]DatasetExportSourceBatch, 0, manifest.SOURCESLength())
	for i := 0; i < manifest.SOURCESLength(); i++ {
		var source dpm.DPMSourceBatch
		if !manifest.SOURCES(&source, i) {
			continue
		}
		sourceBatches = append(sourceBatches, DatasetExportSourceBatch{
			ProviderID:       providerID,
			SourceName:       string(source.SOURCE_NAME()),
			SourceURL:        string(source.SOURCE_URL()),
			SourceSHA256:     string(source.SOURCE_SHA256()),
			HTTPETag:         string(source.HTTP_ETAG()),
			HTTPLastModified: string(source.HTTP_LAST_MODIFIED()),
			RetrievedAt:      string(source.RETRIEVED_AT()),
			ParserVersion:    string(source.PARSER_VERSION()),
			RecordCount:      source.RECORD_COUNT(),
			// Licence terms are part of the signed bytes: a manifest that
			// carries them must be rebuilt with them or its own signature
			// stops verifying.
			License:    string(source.LICENSE()),
			LicenseURL: string(source.LICENSE_URL()),
			Citation:   string(source.CITATION()),
		})
	}
	export := &DatasetExport{
		SchemaName:     shard.Schema,
		CanonicalQuery: string(query.CANONICAL_QUERY()),
		QuerySHA256:    string(query.QUERY_SHA256()),
		ResultSHA256:   string(query.RESULT_SHA256()),
		ShardPath:      shard.File,
		ShardSHA256:    shard.SHA256,
		ShardCID:       shard.CID,
		ShardBytes:     int64(shard.Bytes),
		IndexPath:      index.File,
		IndexSHA256:    index.SHA256,
		IndexCID:       index.CID,
		IndexBytes:     int64(index.Bytes),
		SourceBatches:  sourceBatches,
	}
	if enc := manifest.ENCRYPTION(nil); enc != nil {
		export.ContentKeyID = string(enc.CONTENT_KEY_ID())
		export.EncryptionPolicy = string(enc.POLICY_ID())
	}
	// OTHER-kind auxiliary assets (e.g. archive-plane source feed-head
	// references) are part of the signed manifest bytes; rebuild them in
	// vector order or signature verification of any manifest carrying them
	// would fail.
	var auxiliaryAssets []DPMAuxiliaryAsset
	for i := 0; i < manifest.ASSETSLength(); i++ {
		var asset dpm.DPMAsset
		if !manifest.ASSETS(&asset, i) {
			continue
		}
		if asset.ASSET_KIND().String() != "OTHER" {
			continue
		}
		auxiliaryAssets = append(auxiliaryAssets, DPMAuxiliaryAsset{
			CID:        string(asset.CID()),
			FileName:   string(asset.FILE_NAME()),
			FileID:     string(asset.FILE_ID()),
			DataRoot:   string(asset.DATA_ROOT()),
			SchemaName: string(asset.SCHEMA_NAME()),
			ByteSHA256: string(asset.BYTE_SHA256()),
			ByteLength: int64(asset.BYTE_LENGTH()),
		})
	}
	return buildDatasetPublicationManifestBytes(DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       string(manifest.DATASET_ID()),
		UpdateID:        string(manifest.UPDATE_ID()),
		FileID:          string(manifest.FILE_ID()),
		ProviderPeerID:  string(manifest.PROVIDER_PEER_ID()),
		ProviderEPMCID:  string(manifest.PROVIDER_EPM_CID()),
		PublishedAt:     publishedAt,
		SchemaHash:      shardSchemaHash(manifest, shard.Kind),
		QueryEngine:     string(query.QUERY_ENGINE()),
		QueryEngineVers: string(query.QUERY_ENGINE_VERSION()),
		AuxiliaryAssets: auxiliaryAssets,
	}, nil, "")
}

func dpmStringVectorValues(length int, read func(int) []byte) []string {
	values := make([]string, 0, length)
	for i := 0; i < length; i++ {
		value := strings.TrimSpace(string(read(i)))
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func shardSchemaHash(manifest *dpm.DPM, kind string) string {
	for i := 0; i < manifest.ASSETSLength(); i++ {
		var asset dpm.DPMAsset
		if manifest.ASSETS(&asset, i) && asset.ASSET_KIND().String() == kind {
			return string(asset.SCHEMA_HASH())
		}
	}
	return ""
}

func manifestAssetMap(manifest *dpm.DPM) (map[string]publicationAsset, error) {
	assets := make(map[string]publicationAsset, manifest.ASSETSLength())
	for i := 0; i < manifest.ASSETSLength(); i++ {
		var asset dpm.DPMAsset
		if !manifest.ASSETS(&asset, i) {
			continue
		}
		kind := asset.ASSET_KIND().String()
		cidValue := strings.TrimSpace(string(asset.CID()))
		if cidValue == "" {
			return nil, fmt.Errorf("DPM %s asset missing CID", kind)
		}
		assets[kind] = publicationAsset{
			Kind:   kind,
			CID:    cidValue,
			File:   strings.TrimSpace(string(asset.FILE_NAME())),
			SHA256: strings.TrimSpace(string(asset.BYTE_SHA256())),
			Bytes:  asset.BYTE_LENGTH(),
			Schema: strings.TrimSpace(string(asset.SCHEMA_NAME())),
		}
	}
	return assets, nil
}

func verifyBytesCIDAndHash(label string, data []byte, expectedCID, expectedSHA string) error {
	localCID, err := cidV1RawSHA256(data)
	if err != nil {
		return fmt.Errorf("compute %s CID: %w", label, err)
	}
	if localCID == expectedCID {
		if expectedSHA != "" && sha256Hex(data) != expectedSHA {
			return fmt.Errorf("%s SHA-256 does not match expected hash", label)
		}
		return nil
	}
	if expectedSHA == "" {
		return fmt.Errorf("%s CID %s does not match expected CID %s", label, localCID, expectedCID)
	}
	if sha256Hex(data) != expectedSHA {
		return fmt.Errorf("%s SHA-256 does not match expected hash", label)
	}
	return nil
}

func verifyFileCIDAndHash(label, path, expectedCID, expectedSHA string) (string, int64, error) {
	localCID, err := cidV1RawSHA256File(path)
	if err != nil {
		return "", 0, fmt.Errorf("compute %s CID: %w", label, err)
	}
	localSHA, byteCount, err := sha256HexFile(path)
	if err != nil {
		return "", 0, err
	}
	if localCID == expectedCID {
		if expectedSHA != "" && localSHA != expectedSHA {
			return "", 0, fmt.Errorf("%s SHA-256 does not match expected hash", label)
		}
		return localSHA, byteCount, nil
	}
	if expectedSHA == "" {
		return "", 0, fmt.Errorf("%s CID %s does not match expected CID %s", label, localCID, expectedCID)
	}
	if localSHA != expectedSHA {
		return "", 0, fmt.Errorf("%s SHA-256 does not match expected hash", label)
	}
	return localSHA, byteCount, nil
}

func indexedRecordQueryFromCanonicalJSON(data []byte) (IndexedRecordQuery, error) {
	var payload struct {
		SchemaName          string  `json:"schemaName"`
		Day                 string  `json:"day,omitempty"`
		NoradCatID          *uint32 `json:"noradCatId,omitempty"`
		EntityID            string  `json:"entityId,omitempty"`
		ObjectType          string  `json:"objectType,omitempty"`
		OpsStatusCode       string  `json:"opsStatusCode,omitempty"`
		ActivePayloads      bool    `json:"activePayloads,omitempty"`
		CAReadyResidentSet  bool    `json:"caReadyResidentSet,omitempty"`
		From                string  `json:"from,omitempty"`
		To                  string  `json:"to,omitempty"`
		ProviderID          string  `json:"providerId,omitempty"`
		SourceName          string  `json:"sourceName,omitempty"`
		BatchID             string  `json:"batchId,omitempty"`
		Limit               int     `json:"limit,omitempty"`
		Offset              int     `json:"offset,omitempty"`
		AllowLargeResultSet bool    `json:"allowLargeResultSet,omitempty"`
		OrderByCID          bool    `json:"orderByCid,omitempty"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return IndexedRecordQuery{}, err
	}
	filter := IndexedRecordQuery{
		SchemaName:          payload.SchemaName,
		Day:                 payload.Day,
		NoradCatID:          payload.NoradCatID,
		EntityID:            payload.EntityID,
		ObjectType:          payload.ObjectType,
		OpsStatusCode:       payload.OpsStatusCode,
		ActivePayloads:      payload.ActivePayloads,
		CAReadyResidentSet:  payload.CAReadyResidentSet,
		ProviderID:          payload.ProviderID,
		SourceName:          payload.SourceName,
		BatchID:             payload.BatchID,
		Limit:               payload.Limit,
		Offset:              payload.Offset,
		AllowLargeResultSet: payload.AllowLargeResultSet,
		OrderByCID:          payload.OrderByCID,
	}
	if payload.From != "" {
		from, err := time.Parse(time.RFC3339Nano, payload.From)
		if err != nil {
			return IndexedRecordQuery{}, fmt.Errorf("parse from: %w", err)
		}
		filter.From = &from
	}
	if payload.To != "" {
		to, err := time.Parse(time.RFC3339Nano, payload.To)
		if err != nil {
			return IndexedRecordQuery{}, fmt.Errorf("parse to: %w", err)
		}
		filter.To = &to
	}
	return filter, nil
}

func readManifestBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return data, nil
}
