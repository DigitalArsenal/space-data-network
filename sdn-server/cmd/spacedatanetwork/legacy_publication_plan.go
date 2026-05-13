package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

const legacyPublicationPlanVersion = 1

type legacyPublicationPlan struct {
	Version           int                       `json:"version"`
	SourceTags        storage.SourceTags        `json:"source_tags"`
	DatastoreIdentity storage.DatastoreIdentity `json:"datastore_identity"`
	ProviderPeerID    string                    `json:"provider_peer_id"`
	ProviderEPMCID    string                    `json:"provider_epm_cid,omitempty"`
	DatasetID         string                    `json:"dataset_id"`
	SchemaHash        string                    `json:"schema_hash,omitempty"`
	Entries           []legacyPublicationEntry  `json:"entries"`
}

type legacyPublicationEntry struct {
	Offset      int                   `json:"offset"`
	Limit       int                   `json:"limit"`
	RecordCount int                   `json:"record_count"`
	PublishedAt string                `json:"published_at"`
	Export      storage.DatasetExport `json:"export"`
}

type legacyPublicationPlanRegistrationOptions struct {
	PlanPath    string
	StoragePath string
	IPFSAPIURL  string
	OutputDir   string
	SigningKey  ed25519.PrivateKey
}

type legacyPublicationPlanRegistrationResult struct {
	Publications int `json:"publications"`
	Records      int `json:"records"`
}

var datasetPublicationsCmd = &cobra.Command{
	Use:   "dataset-publications",
	Short: "Register and inspect SDN dataset publication metadata",
}

var datasetPublicationsRegisterPlanCmd = &cobra.Command{
	Use:   "register-plan",
	Short: "Sign and register a historical artifact publication plan",
	RunE:  runDatasetPublicationsRegisterPlan,
}

var (
	datasetPublicationPlanFile      string
	datasetPublicationPlanStorage   string
	datasetPublicationPlanIPFSAPI   string
	datasetPublicationPlanOutputDir string
)

func init() {
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanFile, "plan-file", "", "historical artifact publication plan JSON file")
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanStorage, "storage-path", "", "override destination SDN storage path (defaults to config.storage.path)")
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanIPFSAPI, "ipfs-api-url", "", "Kubo RPC API URL for publishing signed DPM manifests (defaults to config admin.ipfs_api_url or SDN_IPFS_API_URL)")
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanOutputDir, "publication-output-dir", "", "signed manifest output directory (default: <storage-parent>/dataset-publications/registered-plans)")
	_ = datasetPublicationsRegisterPlanCmd.MarkFlagRequired("plan-file")
	datasetPublicationsCmd.AddCommand(datasetPublicationsRegisterPlanCmd)
	rootCmd.AddCommand(datasetPublicationsCmd)
}

func runDatasetPublicationsRegisterPlan(cmd *cobra.Command, args []string) error {
	result, err := registerLegacyPublicationPlan(context.Background(), legacyPublicationPlanRegistrationOptions{
		PlanPath:    datasetPublicationPlanFile,
		StoragePath: datasetPublicationPlanStorage,
		IPFSAPIURL:  datasetPublicationPlanIPFSAPI,
		OutputDir:   datasetPublicationPlanOutputDir,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func (p *legacyArtifactPublisher) appendPublicationPlan(export *storage.DatasetExport, offset, limit int, publishedAt time.Time) error {
	if p == nil {
		return fmt.Errorf("artifact publisher is unavailable")
	}
	if strings.TrimSpace(p.planOutputPath) == "" {
		return fmt.Errorf("publication plan output path is required")
	}
	if export == nil {
		return fmt.Errorf("dataset export is required")
	}

	plan, err := readLegacyPublicationPlan(p.planOutputPath)
	if err != nil {
		return err
	}
	if plan.Version == 0 {
		plan.Version = legacyPublicationPlanVersion
		plan.SourceTags = p.sourceTags
		plan.DatastoreIdentity = legacyImportDatastoreIdentity(p.sourceTags)
		plan.ProviderPeerID = p.providerPeerID
		plan.ProviderEPMCID = p.providerEPMCID
		plan.DatasetID = p.datasetID
		plan.SchemaHash = legacyDatasetPublicationSchemaHash("OMM.fbs")
	}

	entry := legacyPublicationEntry{
		Offset:      offset,
		Limit:       limit,
		RecordCount: export.RecordCount,
		PublishedAt: publishedAt.UTC().Format(time.RFC3339Nano),
		Export:      compactLegacyPublicationExport(*export),
	}
	replaced := false
	for index := range plan.Entries {
		if plan.Entries[index].Offset == offset && plan.Entries[index].Limit == limit {
			plan.Entries[index] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		plan.Entries = append(plan.Entries, entry)
	}
	return writeLegacyPublicationPlan(p.planOutputPath, plan)
}

func registerLegacyPublicationPlan(ctx context.Context, options legacyPublicationPlanRegistrationOptions) (*legacyPublicationPlanRegistrationResult, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	planPath := strings.TrimSpace(options.PlanPath)
	if planPath == "" {
		return nil, fmt.Errorf("plan path is required")
	}
	plan, err := readLegacyPublicationPlan(planPath)
	if err != nil {
		return nil, err
	}
	if plan.Version != legacyPublicationPlanVersion {
		return nil, fmt.Errorf("unsupported publication plan version %d", plan.Version)
	}
	if len(plan.Entries) == 0 {
		return nil, fmt.Errorf("publication plan has no entries")
	}

	storagePath := strings.TrimSpace(options.StoragePath)
	if storagePath == "" && cfg != nil {
		storagePath = strings.TrimSpace(cfg.Storage.Path)
	}
	if storagePath == "" {
		return nil, fmt.Errorf("storage path is required")
	}
	ipfsAPIURL := strings.TrimSpace(options.IPFSAPIURL)
	if ipfsAPIURL == "" && cfg != nil {
		ipfsAPIURL = strings.TrimSpace(cfg.Admin.IPFSAPIURL)
	}
	if ipfsAPIURL == "" {
		ipfsAPIURL = strings.TrimSpace(os.Getenv("SDN_IPFS_API_URL"))
	}
	if ipfsAPIURL == "" {
		return nil, fmt.Errorf("ipfs api url is required")
	}
	outputDir := strings.TrimSpace(options.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(filepath.Dir(storagePath), "dataset-publications", "registered-plans")
	}

	signingKey := options.SigningKey
	if len(signingKey) == 0 {
		raw, err := datasetPublicationSigningKey(cfg, nil)
		if err != nil {
			return nil, err
		}
		signingKey, err = storefrontSigningKeyFromRaw(raw)
		if err != nil {
			return nil, err
		}
	}
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519 signing key is required")
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	identity := plan.DatastoreIdentity
	if strings.TrimSpace(identity.SchemaName) == "" {
		identity = legacyImportDatastoreIdentity(plan.SourceTags)
	}
	store, err := storage.NewFlatSQLStoreForIdentity(storagePath, validator, identity)
	if err != nil {
		return nil, fmt.Errorf("failed to open publication namespace: %w", err)
	}
	defer store.Close()

	providerPublicKey := ""
	if pubKey, ok := signingKey.Public().(ed25519.PublicKey); ok {
		providerPublicKey = hex.EncodeToString(pubKey)
	}
	result := &legacyPublicationPlanRegistrationResult{}
	for _, entry := range plan.Entries {
		export := compactLegacyPublicationExport(entry.Export)
		if export.SchemaName == "" {
			export.SchemaName = "OMM.fbs"
		}
		publishedAt := time.Now().UTC()
		if strings.TrimSpace(entry.PublishedAt) != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, entry.PublishedAt); err == nil {
				publishedAt = parsed.UTC()
			}
		}
		manifest, err := storage.BuildSignedDatasetPublicationManifest(outputDir, storage.DatasetPublicationManifestOptions{
			Export:         &export,
			DatasetID:      firstNonEmptyString(plan.DatasetID, "sdn-omm-celestrak-gp-historical"),
			UpdateID:       legacyDatasetUpdateID(plan.SourceTags.BatchID, entry.Offset, entry.Limit),
			ProviderPeerID: plan.ProviderPeerID,
			ProviderEPMCID: plan.ProviderEPMCID,
			PublishedAt:    publishedAt,
			SigningKey:     signingKey,
			SchemaHash:     plan.SchemaHash,
		})
		if err != nil {
			return nil, err
		}
		manifestCID, err := storage.PublishDatasetPublicationManifestToIPFS(ctx, ipfsAPIURL, manifest)
		if err != nil {
			return nil, err
		}
		manifest.CID = manifestCID
		pnmBytes, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
			PublishedAt: publishedAt,
			SigningKey:  signingKey,
		})
		if err != nil {
			return nil, err
		}
		pnmCID, err := store.Store("PNM.fbs", pnmBytes, plan.ProviderPeerID, nil)
		if err != nil {
			return nil, fmt.Errorf("store dataset publication PNM: %w", err)
		}
		publication := storage.DatasetShardPublication{
			SchemaName:   export.SchemaName,
			ProviderID:   plan.SourceTags.ProviderID,
			SourceName:   plan.SourceTags.SourceName,
			BatchID:      plan.SourceTags.BatchID,
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       entry.Offset,
			Limit:        entry.Limit,
			RecordCount:  export.RecordCount,
			ByteCount:    export.ShardBytes,
			ShardCID:     export.ShardCID,
			IndexCID:     export.IndexCID,
			ManifestCID:  manifest.CID,
			PNMCID:       pnmCID,
			ShardSHA256:  export.ShardSHA256,
			IndexSHA256:  export.IndexSHA256,
			QuerySHA256:  export.QuerySHA256,
			ResultSHA256: export.ResultSHA256,
			PublishedAt:  publishedAt,
		}
		if err := store.UpsertDatasetShardPublication(publication); err != nil {
			return nil, fmt.Errorf("record dataset shard publication: %w", err)
		}
		publishedShard, found, err := store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
			SchemaName:   export.SchemaName,
			ProviderID:   plan.SourceTags.ProviderID,
			SourceName:   plan.SourceTags.SourceName,
			BatchID:      plan.SourceTags.BatchID,
			QueryProfile: storage.DatasetPublicationQueryProfile,
			Offset:       entry.Offset,
			Limit:        entry.Limit,
			RecordCount:  export.RecordCount,
		})
		if err != nil {
			return nil, fmt.Errorf("load registered publication: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("registered publication was not found")
		}
		if err := recordRegisteredPublicationPins(store, publishedShard, &export, manifest, pnmCID, pnmBytes, plan.ProviderPeerID, providerPublicKey); err != nil {
			return nil, err
		}
		result.Publications++
		result.Records += export.RecordCount
	}
	return result, nil
}

func recordRegisteredPublicationPins(store *storage.FlatSQLStore, pub storage.DatasetShardPublication, export *storage.DatasetExport, manifest *storage.DatasetPublicationManifest, pnmCID string, pnmBytes []byte, providerPeerID, providerPublicKey string) error {
	if store == nil || export == nil || manifest == nil {
		return fmt.Errorf("publication pin registration requires store, export, and manifest")
	}
	highWaterMark := datasync.PublishedFeedHighWaterMark([]storage.DatasetShardPublication{pub}, int64(pub.RecordCount), pub.ByteCount)
	entries := []storage.PinLedgerEntry{
		{CID: pub.ShardCID, ByteHash: export.ShardSHA256, Role: "shard", RowCount: int64(pub.RecordCount), ByteCount: export.ShardBytes, VerificationState: "announced"},
		{CID: pub.IndexCID, ByteHash: export.IndexSHA256, Role: "index", ByteCount: export.IndexBytes, VerificationState: "announced"},
		{CID: pub.ManifestCID, ByteHash: manifest.SHA256, Role: "manifest", ByteCount: manifest.ByteLength, VerificationState: "verified", VerifiedAt: pub.PublishedAt},
		{CID: pnmCID, ByteHash: legacySHA256Hex(pnmBytes), Role: "pnm", ByteCount: int64(len(pnmBytes)), VerificationState: "verified", VerifiedAt: pub.PublishedAt},
	}
	for _, entry := range entries {
		entry.SchemaName = pub.SchemaName
		entry.ProviderPeerID = providerPeerID
		entry.ProviderPublicKey = providerPublicKey
		entry.ProviderID = pub.ProviderID
		entry.SourceName = pub.SourceName
		entry.BatchID = pub.BatchID
		entry.QueryProfile = pub.QueryProfile
		entry.SnapshotID = pub.FeedHead
		entry.Head = pub.FeedHead
		entry.HighWaterMark = highWaterMark
		entry.UpdatedAt = pub.PublishedAt
		if err := store.UpsertPinLedgerEntry(entry); err != nil {
			return fmt.Errorf("record registered publication pin %s %s: %w", entry.Role, entry.CID, err)
		}
	}
	return nil
}

func readLegacyPublicationPlan(path string) (legacyPublicationPlan, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return legacyPublicationPlan{}, fmt.Errorf("publication plan path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return legacyPublicationPlan{}, nil
		}
		return legacyPublicationPlan{}, fmt.Errorf("read publication plan: %w", err)
	}
	if len(data) == 0 {
		return legacyPublicationPlan{}, nil
	}
	var plan legacyPublicationPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return legacyPublicationPlan{}, fmt.Errorf("parse publication plan: %w", err)
	}
	return plan, nil
}

func writeLegacyPublicationPlan(path string, plan legacyPublicationPlan) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func compactLegacyPublicationExport(export storage.DatasetExport) storage.DatasetExport {
	export.ShardPath = compactExportPath(export.ShardPath, export.ShardCID, ".fbshard")
	export.IndexPath = compactExportPath(export.IndexPath, export.IndexCID, ".index.json")
	return export
}

func compactExportPath(pathValue, cidValue, suffix string) string {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue != "" {
		return filepath.Base(pathValue)
	}
	cidValue = strings.TrimSpace(cidValue)
	if cidValue == "" {
		return "artifact" + suffix
	}
	return cidValue + suffix
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
