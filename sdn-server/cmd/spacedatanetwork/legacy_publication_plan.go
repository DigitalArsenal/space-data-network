package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

type datasetPublicationCARRebuildOptions struct {
	StoragePath       string
	IPFSAPIURL        string
	OutputDir         string
	Schema            string
	ProviderID        string
	SourceName        string
	BatchID           string
	QueryProfile      string
	ProviderPeerID    string
	ProviderPublicKey string
	Force             bool
}

type datasetPublicationCARRebuildResult struct {
	Schema       string `json:"schema"`
	ProviderID   string `json:"provider_id,omitempty"`
	SourceName   string `json:"source_name,omitempty"`
	BatchID      string `json:"batch_id,omitempty"`
	QueryProfile string `json:"query_profile"`
	Publications int    `json:"publications"`
	Records      int    `json:"records"`
	Bundles      int    `json:"bundles"`
	Bytes        int64  `json:"bytes"`
	Head         string `json:"head,omitempty"`
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

var datasetPublicationsRebuildShardCARsCmd = &cobra.Command{
	Use:   "rebuild-shard-cars",
	Short: "Rebuild IPFS CAR bundles for stored dataset shard publications",
	RunE:  runDatasetPublicationsRebuildShardCARs,
}

var (
	datasetPublicationPlanFile      string
	datasetPublicationPlanStorage   string
	datasetPublicationPlanIPFSAPI   string
	datasetPublicationPlanOutputDir string

	datasetPublicationCARStorage           string
	datasetPublicationCARIPFSAPI           string
	datasetPublicationCAROutputDir         string
	datasetPublicationCARSchema            string
	datasetPublicationCARProviderID        string
	datasetPublicationCARSourceName        string
	datasetPublicationCARBatchID           string
	datasetPublicationCARQueryProfile      string
	datasetPublicationCARProviderPeerID    string
	datasetPublicationCARProviderPublicKey string
	datasetPublicationCARForce             bool
)

func init() {
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanFile, "plan-file", "", "historical artifact publication plan JSON file")
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanStorage, "storage-path", "", "override destination SDN storage path (defaults to config.storage.path)")
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanIPFSAPI, "ipfs-api-url", "", "Kubo RPC API URL for publishing signed DPM manifests (defaults to config admin.ipfs_api_url or SDN_IPFS_API_URL)")
	datasetPublicationsRegisterPlanCmd.Flags().StringVar(&datasetPublicationPlanOutputDir, "publication-output-dir", "", "signed manifest output directory (default: <storage-parent>/dataset-publications/registered-plans)")
	_ = datasetPublicationsRegisterPlanCmd.MarkFlagRequired("plan-file")
	datasetPublicationsCmd.AddCommand(datasetPublicationsRegisterPlanCmd)

	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARStorage, "storage-path", "", "override SDN storage path (defaults to config.storage.path)")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARIPFSAPI, "ipfs-api-url", "", "Kubo RPC API URL for exporting shard DAGs and publishing CAR bundles (defaults to config admin.ipfs_api_url or SDN_IPFS_API_URL)")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCAROutputDir, "publication-output-dir", "", "CAR output directory (default: <storage-parent>/dataset-publications/rebuilt-shard-cars)")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARSchema, "schema", "", "dataset schema to rebuild, e.g. OMM.fbs")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARProviderID, "provider-id", "", "filter by provider id")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARSourceName, "source-name", "", "filter by source name")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARBatchID, "batch-id", "", "filter by batch id")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARQueryProfile, "query-profile", storage.DatasetPublicationQueryProfile, "dataset publication query profile")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARProviderPeerID, "provider-peer-id", "", "provider peer ID to record on CAR bundle pins")
	datasetPublicationsRebuildShardCARsCmd.Flags().StringVar(&datasetPublicationCARProviderPublicKey, "provider-public-key", "", "provider public key to record on CAR bundle pins")
	datasetPublicationsRebuildShardCARsCmd.Flags().BoolVar(&datasetPublicationCARForce, "force", false, "mark existing verified CAR bundle pins stale before rebuilding")
	_ = datasetPublicationsRebuildShardCARsCmd.MarkFlagRequired("schema")
	datasetPublicationsCmd.AddCommand(datasetPublicationsRebuildShardCARsCmd)
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

func runDatasetPublicationsRebuildShardCARs(cmd *cobra.Command, args []string) error {
	result, err := rebuildDatasetPublicationShardGroupCARBundles(context.Background(), datasetPublicationCARRebuildOptions{
		StoragePath:       datasetPublicationCARStorage,
		IPFSAPIURL:        datasetPublicationCARIPFSAPI,
		OutputDir:         datasetPublicationCAROutputDir,
		Schema:            datasetPublicationCARSchema,
		ProviderID:        datasetPublicationCARProviderID,
		SourceName:        datasetPublicationCARSourceName,
		BatchID:           datasetPublicationCARBatchID,
		QueryProfile:      datasetPublicationCARQueryProfile,
		ProviderPeerID:    datasetPublicationCARProviderPeerID,
		ProviderPublicKey: datasetPublicationCARProviderPublicKey,
		Force:             datasetPublicationCARForce,
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
	registeredGroups := map[string]storage.DatasetShardPublication{}
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
			DatasetID:      firstNonEmptyString(plan.DatasetID, "sdn-omm-legacy-gp-historical"),
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
		registeredGroups[legacyPublicationGroupKey(publishedShard)] = publishedShard
		result.Publications++
		result.Records += export.RecordCount
	}
	for _, group := range registeredGroups {
		publications, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
			SchemaName:   group.SchemaName,
			ProviderID:   group.ProviderID,
			SourceName:   group.SourceName,
			BatchID:      group.BatchID,
			QueryProfile: group.QueryProfile,
		})
		if err != nil {
			return nil, fmt.Errorf("load registered publication group: %w", err)
		}
		if err := recordRegisteredShardGroupCARBundle(ctx, store, ipfsAPIURL, outputDir, publications, plan.ProviderPeerID, providerPublicKey); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func rebuildDatasetPublicationShardGroupCARBundles(ctx context.Context, options datasetPublicationCARRebuildOptions) (*datasetPublicationCARRebuildResult, error) {
	cfg, err := config.Load(configPath)
	if err != nil && strings.TrimSpace(options.StoragePath) == "" {
		return nil, fmt.Errorf("failed to load config: %w", err)
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
		outputDir = filepath.Join(filepath.Dir(storagePath), "dataset-publications", "rebuilt-shard-cars")
	}
	schema := strings.TrimSpace(options.Schema)
	if schema == "" {
		return nil, fmt.Errorf("schema is required")
	}
	queryProfile := strings.TrimSpace(options.QueryProfile)
	if queryProfile == "" {
		queryProfile = storage.DatasetPublicationQueryProfile
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(storagePath, validator)
	if err != nil {
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	query := storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   strings.TrimSpace(options.ProviderID),
		SourceName:   strings.TrimSpace(options.SourceName),
		BatchID:      strings.TrimSpace(options.BatchID),
		QueryProfile: queryProfile,
	}
	publications, err := store.ListDatasetShardPublications(query)
	if err != nil {
		return nil, fmt.Errorf("load stored dataset shard publications: %w", err)
	}
	if len(publications) == 0 {
		return nil, fmt.Errorf("no stored dataset shard publications found for %s", schema)
	}
	if options.Force {
		if err := markShardGroupCARsStale(store, query, nil); err != nil {
			return nil, err
		}
	}
	if err := recordRegisteredShardGroupCARBundle(ctx, store, ipfsAPIURL, outputDir, publications, strings.TrimSpace(options.ProviderPeerID), strings.TrimSpace(options.ProviderPublicKey)); err != nil {
		return nil, err
	}

	head, _, records, sourceBytes := datasetPublicationAggregate(publications)
	current, err := currentShardGroupCARBundles(store, query, head)
	if err != nil {
		return nil, err
	}
	if err := verifyShardGroupCARCoverage(current, len(publications), records); err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(current))
	var carBytes int64
	for _, entry := range current {
		keep[entry.CID] = true
		carBytes += entry.ByteCount
	}
	if err := markShardGroupCARsStale(store, query, keep); err != nil {
		return nil, err
	}
	return &datasetPublicationCARRebuildResult{
		Schema:       schema,
		ProviderID:   query.ProviderID,
		SourceName:   query.SourceName,
		BatchID:      query.BatchID,
		QueryProfile: query.QueryProfile,
		Publications: len(publications),
		Records:      int(records),
		Bundles:      len(current),
		Bytes:        firstPositiveInt64(carBytes, sourceBytes),
		Head:         head,
	}, nil
}

func datasetPublicationAggregate(publications []storage.DatasetShardPublication) (string, string, int64, int64) {
	if len(publications) == 0 {
		return "", "", 0, 0
	}
	sort.Slice(publications, func(i, j int) bool {
		if publications[i].FeedSequence != publications[j].FeedSequence {
			return publications[i].FeedSequence < publications[j].FeedSequence
		}
		return publications[i].Offset < publications[j].Offset
	})
	first := publications[0]
	last := publications[len(publications)-1]
	head := last.FeedHead
	if head == "" {
		head = datasync.PublishedFeedHead(first.SchemaName, first.ProviderID, first.SourceName, first.BatchID, first.QueryProfile, publications)
	}
	var totalRows int64
	var totalBytes int64
	for _, publication := range publications {
		totalRows += int64(publication.RecordCount)
		totalBytes += publication.ByteCount
	}
	return head, datasync.PublishedFeedHighWaterMark(publications, totalRows, totalBytes), totalRows, totalBytes
}

func currentShardGroupCARBundles(store *storage.FlatSQLStore, query storage.DatasetShardPublicationQuery, head string) ([]storage.PinLedgerEntry, error) {
	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:        query.SchemaName,
		ProviderID:        query.ProviderID,
		SourceName:        query.SourceName,
		BatchID:           query.BatchID,
		QueryProfile:      query.QueryProfile,
		Role:              "shard-group-car",
		VerificationState: "verified",
	})
	if err != nil {
		return nil, fmt.Errorf("list rebuilt shard-group CAR bundle pins: %w", err)
	}
	current := make([]storage.PinLedgerEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.CID == "" || entry.ByteHash == "" || entry.ByteCount <= 0 || entry.SegmentCount <= 0 {
			continue
		}
		if entry.Head == head || entry.SnapshotID == head {
			current = append(current, entry)
		}
	}
	sort.Slice(current, func(i, j int) bool {
		if current[i].SegmentStart != current[j].SegmentStart {
			return current[i].SegmentStart < current[j].SegmentStart
		}
		return current[i].CID < current[j].CID
	})
	return current, nil
}

func verifyShardGroupCARCoverage(entries []storage.PinLedgerEntry, segmentCount int, totalRows int64) error {
	if segmentCount <= 0 {
		return fmt.Errorf("segment count must be positive")
	}
	covered := make([]bool, segmentCount)
	var coveredRows int64
	for _, entry := range entries {
		if entry.SegmentStart < 0 || entry.SegmentStart >= segmentCount || entry.SegmentCount <= 0 {
			continue
		}
		end := entry.SegmentStart + entry.SegmentCount
		if end > segmentCount {
			end = segmentCount
		}
		for index := entry.SegmentStart; index < end; index++ {
			covered[index] = true
		}
		coveredRows += entry.RowCount
	}
	for index, ok := range covered {
		if !ok {
			return fmt.Errorf("rebuilt shard-group CAR bundles do not cover segment %d of %d", index, segmentCount)
		}
	}
	if totalRows > 0 && coveredRows < totalRows {
		return fmt.Errorf("rebuilt shard-group CAR bundles cover %d rows, want at least %d", coveredRows, totalRows)
	}
	return nil
}

func markShardGroupCARsStale(store *storage.FlatSQLStore, query storage.DatasetShardPublicationQuery, keep map[string]bool) error {
	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:        query.SchemaName,
		ProviderID:        query.ProviderID,
		SourceName:        query.SourceName,
		BatchID:           query.BatchID,
		QueryProfile:      query.QueryProfile,
		Role:              "shard-group-car",
		VerificationState: "verified",
	})
	if err != nil {
		return fmt.Errorf("list superseded shard-group CAR bundle pins: %w", err)
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if keep != nil && keep[entry.CID] {
			continue
		}
		entry.VerificationState = "stale"
		entry.UpdatedAt = now
		if err := store.UpsertPinLedgerEntry(entry); err != nil {
			return fmt.Errorf("mark superseded shard-group CAR %s stale: %w", entry.CID, err)
		}
	}
	return nil
}

func legacyPublicationGroupKey(pub storage.DatasetShardPublication) string {
	return strings.Join([]string{pub.SchemaName, pub.ProviderID, pub.SourceName, pub.BatchID, pub.QueryProfile}, "\x00")
}

func recordRegisteredShardGroupCARBundle(ctx context.Context, store *storage.FlatSQLStore, ipfsAPIURL, outputDir string, publications []storage.DatasetShardPublication, providerPeerID, providerPublicKey string) error {
	if store == nil {
		return fmt.Errorf("publication pin registration requires store")
	}
	if len(publications) == 0 {
		return nil
	}
	sort.Slice(publications, func(i, j int) bool {
		if publications[i].FeedSequence != publications[j].FeedSequence {
			return publications[i].FeedSequence < publications[j].FeedSequence
		}
		return publications[i].Offset < publications[j].Offset
	})

	first := publications[0]
	last := publications[len(publications)-1]
	head := last.FeedHead
	if head == "" {
		head = datasync.PublishedFeedHead(first.SchemaName, first.ProviderID, first.SourceName, first.BatchID, first.QueryProfile, publications)
	}
	existing, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:        first.SchemaName,
		ProviderPeerID:    providerPeerID,
		ProviderID:        first.ProviderID,
		SourceName:        first.SourceName,
		BatchID:           first.BatchID,
		QueryProfile:      first.QueryProfile,
		Role:              "shard-group-car",
		VerificationState: "verified",
	})
	if err != nil {
		return fmt.Errorf("list existing registered shard-group CAR bundle pins: %w", err)
	}
	var totalRows int64
	var totalBytes int64
	for _, publication := range publications {
		totalRows += int64(publication.RecordCount)
		totalBytes += publication.ByteCount
	}
	var existingHeadRows int64
	for _, entry := range existing {
		if entry.Head == head && entry.CID != "" && entry.ByteHash != "" && entry.ByteCount > 0 {
			existingHeadRows += entry.RowCount
		}
	}
	if totalRows > 0 && existingHeadRows >= totalRows {
		return nil
	}

	verifiedAt := last.PublishedAt
	if verifiedAt.IsZero() {
		verifiedAt = time.Now().UTC()
	}
	highWaterMark := datasync.PublishedFeedHighWaterMark(publications, totalRows, totalBytes)
	carOutputDir := filepath.Join(outputDir, legacyPublicationSafePathComponent(first.SchemaName), "car")
	groups := storage.DatasetShardPublicationCARGroups(publications, storage.DefaultShardGroupCARMaxSourceBytes)
	segmentStart := 0
	for _, group := range groups {
		groupSegmentStart := segmentStart
		groupSegmentCount := len(group)
		segmentStart += groupSegmentCount
		rootCIDs := make([]string, 0, len(group))
		var groupRows int64
		for _, publication := range group {
			if publication.ShardCID != "" {
				rootCIDs = append(rootCIDs, publication.ShardCID)
			}
			groupRows += int64(publication.RecordCount)
		}
		publishedCAR, err := storage.PublishShardGroupCARToIPFS(ctx, ipfsAPIURL, carOutputDir, rootCIDs)
		if err != nil {
			return fmt.Errorf("publish registered shard-group CAR bundle: %w", err)
		}
		if err := store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
			CID:               publishedCAR.CID,
			SchemaName:        first.SchemaName,
			ProviderPeerID:    providerPeerID,
			ProviderPublicKey: providerPublicKey,
			ProviderID:        first.ProviderID,
			SourceName:        first.SourceName,
			BatchID:           first.BatchID,
			QueryProfile:      first.QueryProfile,
			SnapshotID:        head,
			Head:              head,
			HighWaterMark:     highWaterMark,
			ByteHash:          publishedCAR.SHA256,
			Role:              "shard-group-car",
			SegmentStart:      groupSegmentStart,
			SegmentCount:      groupSegmentCount,
			RowCount:          groupRows,
			ByteCount:         publishedCAR.ByteCount,
			VerificationState: "verified",
			VerifiedAt:        verifiedAt,
			UpdatedAt:         verifiedAt,
		}); err != nil {
			return fmt.Errorf("record registered shard-group CAR pin ledger: %w", err)
		}
	}
	return nil
}

func legacyPublicationSafePathComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".fbs")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	value = replacer.Replace(value)
	if value == "" {
		return "dataset"
	}
	return value
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

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
