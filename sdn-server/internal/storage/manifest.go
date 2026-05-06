package storage

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
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
	WorkDir           string
}

// DatasetPublicationReplayResult summarizes a verified publication replay.
type DatasetPublicationReplayResult struct {
	ManifestCID  string
	ShardCID     string
	IndexCID     string
	SchemaName   string
	RecordCount  int
	QuerySHA256  string
	ResultSHA256 string
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

	manifestBytes, err := opts.FetchByCID(ctx, manifestCID)
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
	shardBytes, err := opts.FetchByCID(ctx, shardAsset.CID)
	if err != nil {
		return nil, fmt.Errorf("fetch shard CID %s: %w", shardAsset.CID, err)
	}
	if err := verifyBytesCIDAndHash("shard", shardBytes, shardAsset.CID, shardAsset.SHA256); err != nil {
		return nil, err
	}
	indexBytes, err := opts.FetchByCID(ctx, indexAsset.CID)
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
	dpm.DPMSourceBatchStart(builder)
	dpm.DPMSourceBatchAddSOURCE_NAME(builder, sourceName)
	dpm.DPMSourceBatchAddSOURCE_URL(builder, sourceURL)
	dpm.DPMSourceBatchAddSOURCE_SHA256(builder, sourceSHA)
	dpm.DPMSourceBatchAddHTTP_ETAG(builder, httpETag)
	dpm.DPMSourceBatchAddHTTP_LAST_MODIFIED(builder, httpLastModified)
	dpm.DPMSourceBatchAddRETRIEVED_AT(builder, retrievedAt)
	dpm.DPMSourceBatchAddPARSER_VERSION(builder, parserVersion)
	dpm.DPMSourceBatchAddRECORD_COUNT(builder, source.RecordCount)
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
	sourceBatches := make([]DatasetExportSourceBatch, 0, manifest.SOURCESLength())
	for i := 0; i < manifest.SOURCESLength(); i++ {
		var source dpm.DPMSourceBatch
		if !manifest.SOURCES(&source, i) {
			continue
		}
		sourceBatches = append(sourceBatches, DatasetExportSourceBatch{
			ProviderID:       string(manifest.PROVIDER_PEER_ID()),
			SourceName:       string(source.SOURCE_NAME()),
			SourceURL:        string(source.SOURCE_URL()),
			SourceSHA256:     string(source.SOURCE_SHA256()),
			HTTPETag:         string(source.HTTP_ETAG()),
			HTTPLastModified: string(source.HTTP_LAST_MODIFIED()),
			RetrievedAt:      string(source.RETRIEVED_AT()),
			ParserVersion:    string(source.PARSER_VERSION()),
			RecordCount:      source.RECORD_COUNT(),
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
	}, nil, "")
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
	if localCID != expectedCID {
		return fmt.Errorf("%s CID %s does not match expected CID %s", label, localCID, expectedCID)
	}
	if expectedSHA != "" && sha256Hex(data) != expectedSHA {
		return fmt.Errorf("%s SHA-256 does not match expected hash", label)
	}
	return nil
}

func indexedRecordQueryFromCanonicalJSON(data []byte) (IndexedRecordQuery, error) {
	var payload struct {
		SchemaName         string  `json:"schemaName"`
		Day                string  `json:"day,omitempty"`
		NoradCatID         *uint32 `json:"noradCatId,omitempty"`
		EntityID           string  `json:"entityId,omitempty"`
		ObjectType         string  `json:"objectType,omitempty"`
		OpsStatusCode      string  `json:"opsStatusCode,omitempty"`
		ActivePayloads     bool    `json:"activePayloads,omitempty"`
		CAReadyResidentSet bool    `json:"caReadyResidentSet,omitempty"`
		From               string  `json:"from,omitempty"`
		To                 string  `json:"to,omitempty"`
		ProviderID         string  `json:"providerId,omitempty"`
		SourceName         string  `json:"sourceName,omitempty"`
		BatchID            string  `json:"batchId,omitempty"`
		Limit              int     `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return IndexedRecordQuery{}, err
	}
	filter := IndexedRecordQuery{
		SchemaName:         payload.SchemaName,
		Day:                payload.Day,
		NoradCatID:         payload.NoradCatID,
		EntityID:           payload.EntityID,
		ObjectType:         payload.ObjectType,
		OpsStatusCode:      payload.OpsStatusCode,
		ActivePayloads:     payload.ActivePayloads,
		CAReadyResidentSet: payload.CAReadyResidentSet,
		ProviderID:         payload.ProviderID,
		SourceName:         payload.SourceName,
		BatchID:            payload.BatchID,
		Limit:              payload.Limit,
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
