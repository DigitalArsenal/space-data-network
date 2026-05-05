package storage

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
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
	builder := flatbuffers.NewBuilder(256)
	addr := builder.CreateString("/ipfs/" + manifest.CID)
	publishedAt := builder.CreateString(opts.PublishedAt.UTC().Format(time.RFC3339))
	cidOffset := builder.CreateString(manifest.CID)
	fileName := builder.CreateString(opts.FileName)
	fileID := builder.CreateString("DPM")
	signature := builder.CreateString(hex.EncodeToString(manifest.Signature))
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
	providerPeerID := builder.CreateString(opts.ProviderPeerID)
	providerEPMCID := builder.CreateString(opts.ProviderEPMCID)
	publishedAt := builder.CreateString(opts.PublishedAt.UTC().Format(time.RFC3339Nano))
	signatureTypeOffset := builder.CreateString(signatureType)

	assetOffsets := []flatbuffers.UOffsetT{
		buildDPMAsset(builder, "data", export.ShardCID, "/ipfs/"+export.ShardCID, filepath.Base(export.ShardPath), export.ShardBytes, export.ShardSHA256, export.SchemaName, opts.SchemaHash, export.ContentKeyID),
		buildDPMAsset(builder, "index", export.IndexCID, "/ipfs/"+export.IndexCID, filepath.Base(export.IndexPath), export.IndexBytes, export.IndexSHA256, "DPM.index.json", opts.SchemaHash, export.ContentKeyID),
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

func buildDPMAsset(builder *flatbuffers.Builder, kind, cidValue, multiaddr, fileName string, byteLength int64, byteSHA256, schemaName, schemaHash, contentKeyID string) flatbuffers.UOffsetT {
	cidOffset := builder.CreateString(cidValue)
	multiaddrOffset := builder.CreateString(multiaddr)
	fileNameOffset := builder.CreateString(fileName)
	byteSHAOffset := builder.CreateString(byteSHA256)
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
	dpm.DPMAssetAddCID(builder, cidOffset)
	dpm.DPMAssetAddMULTIFORMAT_ADDRESS(builder, multiaddrOffset)
	dpm.DPMAssetAddFILE_NAME(builder, fileNameOffset)
	if byteLength > 0 {
		dpm.DPMAssetAddBYTE_LENGTH(builder, uint64(byteLength))
	}
	dpm.DPMAssetAddBYTE_SHA256(builder, byteSHAOffset)
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

func readManifestBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return data, nil
}
