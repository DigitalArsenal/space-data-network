package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	channelmodel "github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	defaultDatasetPublicationLimit         = 250
	maxDatasetPublicationChunkSize         = 50000
	defaultFullCatalogPublicationChunkSize = maxDatasetPublicationChunkSize
)

// DatasetPublicationRequest describes a local request to export, pin, sign,
// and announce a dataset update from the daemon's current FlatSQL store.
type DatasetPublicationRequest struct {
	DatastoreKey      string `json:"datastoreKey,omitempty"`
	Schema            string `json:"schema"`
	ProviderID        string `json:"providerId,omitempty"`
	SourceName        string `json:"sourceName,omitempty"`
	BatchID           string `json:"batchId,omitempty"`
	DatasetID         string `json:"datasetId,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	ChunkSize         int    `json:"chunkSize,omitempty"`
	FullCatalog       bool   `json:"fullCatalog,omitempty"`
	AnnounceExisting  bool   `json:"announceExisting,omitempty"`
	CombinedCelesTrak bool   `json:"combinedCelesTrak,omitempty"`
}

// DatasetPublicationResult is the safe summary returned after publication.
type DatasetPublicationResult struct {
	Schema       string                     `json:"schema"`
	RecordCount  int                        `json:"recordCount"`
	ShardCID     string                     `json:"shardCid"`
	IndexCID     string                     `json:"indexCid"`
	ManifestCID  string                     `json:"manifestCid"`
	PNMCID       string                     `json:"pnmCid,omitempty"`
	Publications []DatasetPublicationResult `json:"publications,omitempty"`
}

type datasetPublicationResponse struct {
	StandardCode string                       `json:"standardCode"`
	RecordCount  int                          `json:"recordCount"`
	ShardCID     string                       `json:"shardCid"`
	IndexCID     string                       `json:"indexCid"`
	ManifestCID  string                       `json:"manifestCid"`
	PNMCID       string                       `json:"pnmCid,omitempty"`
	Publications []datasetPublicationResponse `json:"publications,omitempty"`
}

type datasetPublicationSourceIdentity struct {
	ProviderID string
	SourceName string
	BatchID    string
}

// DatasetPublicationService publishes dataset updates.
type DatasetPublicationService interface {
	PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error)
}

// DatasetUpdatePublisher is implemented by the running SDN node.
type DatasetUpdatePublisher interface {
	PublishDatasetUpdatePNM(context.Context, sdnpubsub.DatasetUpdateAnnouncement) error
}

type DatasetFeedHeadPublisher interface {
	PublishDatasetFeedHead(context.Context, sdnpubsub.DatasetFeedHeadAnnouncement) error
}

type DatasetPublicationChannelUpdate struct {
	Schema            string
	SourceID          string
	PNMBytes          []byte
	ManifestBytes     []byte
	ProviderPublicKey ed25519.PublicKey
	PublishedShard    storage.DatasetShardPublication
}

type DatasetPublicationChannelRecorder interface {
	RecordDatasetPublicationChannelUpdate(DatasetPublicationChannelUpdate) error
}

// DatasetPublicationHandler exposes local-only dataset publication operations.
type DatasetPublicationHandler struct {
	service DatasetPublicationService
}

// NewDatasetPublicationHandler creates a local-only dataset publication handler.
func NewDatasetPublicationHandler(service DatasetPublicationService) *DatasetPublicationHandler {
	return &DatasetPublicationHandler{service: service}
}

// RegisterRoutes registers local admin dataset publication routes.
func (h *DatasetPublicationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/dataset-updates/publish", h.handlePublish)
}

func (h *DatasetPublicationHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "dataset publication is only available to local daemon clients")
		return
	}
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "dataset publication service unavailable")
		return
	}

	var req DatasetPublicationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	result, err := h.service.PublishDatasetUpdate(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, publicDatasetPublicationResult(result))
}

func publicDatasetPublicationResult(result *DatasetPublicationResult) *datasetPublicationResponse {
	if result == nil {
		return nil
	}
	public := datasetPublicationResponse{
		StandardCode: publicDatasetPublicationStandardCode(result.Schema),
		RecordCount:  result.RecordCount,
		ShardCID:     result.ShardCID,
		IndexCID:     result.IndexCID,
		ManifestCID:  result.ManifestCID,
		PNMCID:       result.PNMCID,
	}
	if len(result.Publications) > 0 {
		public.Publications = make([]datasetPublicationResponse, 0, len(result.Publications))
		for _, publication := range result.Publications {
			public.Publications = append(public.Publications, datasetPublicationResponse{
				StandardCode: publicDatasetPublicationStandardCode(publication.Schema),
				RecordCount:  publication.RecordCount,
				ShardCID:     publication.ShardCID,
				IndexCID:     publication.IndexCID,
				ManifestCID:  publication.ManifestCID,
				PNMCID:       publication.PNMCID,
			})
		}
	}
	return &public
}

func publicDatasetPublicationStandardCode(schema string) string {
	standardCode, err := channelmodel.StandardCodeFromSchemaName(schema)
	if err == nil {
		return standardCode
	}
	return strings.TrimSpace(schema)
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ConcreteDatasetPublicationService exports local records and publishes one
// signed DPM announcement PNM through the running node.
type ConcreteDatasetPublicationService struct {
	store           *storage.FlatSQLStore
	publisher       DatasetUpdatePublisher
	signingKey      ed25519.PrivateKey
	providerPeerID  string
	providerEPMCID  string
	ipfsAPIURL      string
	outputDir       string
	channelRecorder DatasetPublicationChannelRecorder
	now             func() time.Time
}

// NewConcreteDatasetPublicationService creates the production publication service.
func NewConcreteDatasetPublicationService(
	store *storage.FlatSQLStore,
	publisher DatasetUpdatePublisher,
	signingKey []byte,
	providerPeerID string,
	providerEPMCID string,
	ipfsAPIURL string,
	outputDir string,
) *ConcreteDatasetPublicationService {
	return &ConcreteDatasetPublicationService{
		store:          store,
		publisher:      publisher,
		signingKey:     ed25519.PrivateKey(append([]byte(nil), signingKey...)),
		providerPeerID: strings.TrimSpace(providerPeerID),
		providerEPMCID: strings.TrimSpace(providerEPMCID),
		ipfsAPIURL:     strings.TrimSpace(ipfsAPIURL),
		outputDir:      strings.TrimSpace(outputDir),
		now:            func() time.Time { return time.Now().UTC() },
	}
}

func (s *ConcreteDatasetPublicationService) SetChannelRecorder(recorder DatasetPublicationChannelRecorder) {
	if s != nil {
		s.channelRecorder = recorder
	}
}

// PublishDatasetUpdate exports a dataset window, pins shard/index/DPM to IPFS,
// builds a signed PNM, and announces it over SDN pub/sub.
func (s *ConcreteDatasetPublicationService) PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("dataset store is unavailable")
	}
	if s.publisher == nil {
		return nil, fmt.Errorf("dataset update publisher is unavailable")
	}
	if len(s.signingKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519 signing key is unavailable")
	}
	if s.providerPeerID == "" {
		return nil, fmt.Errorf("provider peer id is required")
	}
	if s.ipfsAPIURL == "" {
		return nil, fmt.Errorf("ipfs api url is required")
	}
	if s.outputDir == "" {
		return nil, fmt.Errorf("dataset publication output dir is required")
	}

	activeStore, activeReq, closeStore, err := s.publicationStoreForRequest(req)
	if err != nil {
		return nil, err
	}
	defer closeStore()
	activeService := *s
	activeService.store = activeStore

	schema := normalizeDatasetPublicationSchema(activeReq.Schema)
	if err := sds.ValidateSchemaName(schema); err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	if activeReq.AnnounceExisting {
		return activeService.announceExistingDatasetPublications(ctx, activeReq, schema)
	}
	if activeReq.FullCatalog || activeReq.ChunkSize > 0 {
		return activeService.publishDatasetUpdateSeries(ctx, activeReq, schema)
	}
	limit := activeReq.Limit
	if limit <= 0 {
		limit = defaultDatasetPublicationLimit
	}

	filter := datasetPublicationExportFilter(activeReq, schema, limit, 0)
	sourceIdentity := datasetPublicationSourceIdentityFromRequest(activeReq)
	result, err := activeService.publishDatasetUpdatePart(ctx, activeReq, schema, filter, sourceIdentity, datasetUpdateID(activeReq, activeService.now(), 0))
	if err != nil {
		return nil, err
	}
	published, err := activeService.publishedShardFromResult(schema, filter, sourceIdentity, result)
	if err != nil {
		return nil, err
	}
	if err := activeService.recordShardGroupCARBundle(ctx, []storage.DatasetShardPublication{published}, schema); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ConcreteDatasetPublicationService) announceExistingDatasetPublications(ctx context.Context, req DatasetPublicationRequest, schema string) (*DatasetPublicationResult, error) {
	publications, err := s.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   strings.TrimSpace(req.ProviderID),
		SourceName:   strings.TrimSpace(req.SourceName),
		BatchID:      strings.TrimSpace(req.BatchID),
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("load existing dataset shard publications: %w", err)
	}
	if len(publications) == 0 {
		return nil, fmt.Errorf("no existing dataset shard publications match %s", schema)
	}

	result := &DatasetPublicationResult{Schema: schema}
	rowLimit := req.Limit
	workCtx := context.WithoutCancel(ctx)
	for _, publication := range publications {
		if rowLimit > 0 && result.RecordCount >= rowLimit {
			break
		}
		repaired, err := s.ensureDatasetPublicationAssets(workCtx, req, publication)
		if err != nil {
			log.Warnf("Skipping existing dataset publication %s %s offset=%d limit=%d during announceExisting repair: %v", publication.SchemaName, publication.ShardCID, publication.Offset, publication.Limit, err)
			continue
		}
		publication = repaired
		if publication.PNMCID != "" {
			if pnmRecord, err := s.store.GetRecord("PNM.fbs", publication.PNMCID); err == nil && len(pnmRecord.Data) > 0 {
				if err := s.publisher.PublishDatasetUpdatePNM(workCtx, sdnpubsub.DatasetUpdateAnnouncement{
					PNM:               pnmRecord.Data,
					Schemas:           []string{publication.SchemaName},
					CombinedCelesTrak: req.CombinedCelesTrak,
				}); err != nil {
					return nil, err
				}
			}
		}
		if err := s.announceDatasetFeedHead(workCtx, publication); err != nil {
			return nil, err
		}
		result.Publications = append(result.Publications, DatasetPublicationResult{
			Schema:      publication.SchemaName,
			RecordCount: publication.RecordCount,
			ShardCID:    publication.ShardCID,
			IndexCID:    publication.IndexCID,
			ManifestCID: publication.ManifestCID,
			PNMCID:      publication.PNMCID,
		})
		result.RecordCount += publication.RecordCount
	}
	if len(result.Publications) == 0 {
		return nil, fmt.Errorf("no servable existing dataset shard publications selected for %s", schema)
	}
	first := result.Publications[0]
	result.ShardCID = first.ShardCID
	result.IndexCID = first.IndexCID
	result.ManifestCID = first.ManifestCID
	result.PNMCID = first.PNMCID
	return result, nil
}

func (s *ConcreteDatasetPublicationService) ensureDatasetPublicationAssets(ctx context.Context, req DatasetPublicationRequest, publication storage.DatasetShardPublication) (storage.DatasetShardPublication, error) {
	if s == nil || s.store == nil {
		return publication, fmt.Errorf("dataset store is unavailable")
	}
	shardPath, shardErr := s.store.DatasetPublicationShardPath(publication)
	indexPath, indexErr := s.store.DatasetPublicationIndexPath(publication)
	if shardErr == nil && indexErr == nil && fileExists(shardPath) && fileExists(indexPath) {
		return publication, nil
	}
	if shardErr == nil {
		export, err := s.store.RepairDatasetPublicationIndexFromShard(filepath.Join(s.outputDir, safeDatasetPathComponent(publication.SchemaName)), publication)
		if err != nil {
			return publication, fmt.Errorf("repair dataset publication %s index from shard: %w", publication.ShardCID, err)
		}
		if export.RecordCount != publication.RecordCount {
			return publication, fmt.Errorf("repair dataset publication %s record count = %d, want %d", publication.ShardCID, export.RecordCount, publication.RecordCount)
		}
		if export.ShardSHA256 != publication.ShardSHA256 || export.ResultSHA256 != publication.ResultSHA256 {
			return publication, fmt.Errorf("repair dataset publication %s would change shard identity", publication.ShardCID)
		}
		if export.ShardCID == publication.ShardCID &&
			export.IndexCID == publication.IndexCID &&
			export.IndexSHA256 == publication.IndexSHA256 &&
			export.QuerySHA256 == publication.QuerySHA256 {
			return publication, nil
		}
		repaired, err := s.republishRepairedDatasetPublication(ctx, req, publication, export)
		if err != nil {
			return publication, err
		}
		return repaired, nil
	}
	return publication, fmt.Errorf("repair dataset publication %s: deterministic shard file is missing", publication.ShardCID)
}

func (s *ConcreteDatasetPublicationService) republishRepairedDatasetPublication(ctx context.Context, req DatasetPublicationRequest, publication storage.DatasetShardPublication, export *storage.DatasetExport) (storage.DatasetShardPublication, error) {
	if publication.Limit <= 0 {
		return publication, fmt.Errorf("repair dataset publication %s: window limit is required", publication.ShardCID)
	}
	repairReq := req
	repairReq.Schema = publication.SchemaName
	repairReq.ProviderID = publication.ProviderID
	repairReq.SourceName = publication.SourceName
	repairReq.BatchID = publication.BatchID
	sourceIdentity := datasetPublicationSourceIdentity{
		ProviderID: publication.ProviderID,
		SourceName: publication.SourceName,
		BatchID:    publication.BatchID,
	}
	filter := storage.IndexedRecordQuery{
		SchemaName:          publication.SchemaName,
		ProviderID:          publication.ProviderID,
		SourceName:          publication.SourceName,
		BatchID:             publication.BatchID,
		Limit:               publication.Limit,
		Offset:              publication.Offset,
		AllowLargeResultSet: true,
		OrderByCID:          true,
	}
	part := publication.Offset/publication.Limit + 1
	updateID := datasetUpdateID(repairReq, s.now(), part)
	_, repaired, err := s.publishDatasetExport(ctx, repairReq, publication.SchemaName, filter, sourceIdentity, updateID, export, false)
	if err != nil {
		return publication, fmt.Errorf("republish repaired dataset publication %s: %w", publication.ShardCID, err)
	}
	return repaired, nil
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *ConcreteDatasetPublicationService) publicationStoreForRequest(req DatasetPublicationRequest) (*storage.FlatSQLStore, DatasetPublicationRequest, func(), error) {
	datastoreKey := strings.TrimSpace(req.DatastoreKey)
	if datastoreKey == "" {
		return s.store, req, func() {}, nil
	}
	store, err := s.store.OpenRegisteredDatastore(datastoreKey)
	if err != nil {
		return nil, req, nil, fmt.Errorf("open publication datastore %s: %w", datastoreKey, err)
	}
	closeStore := func() { _ = store.Close() }
	identity, ok, err := store.DatastoreIdentity()
	if err != nil {
		closeStore()
		return nil, req, nil, err
	}
	if ok {
		if strings.TrimSpace(req.Schema) == "" {
			req.Schema = identity.SchemaName
		}
		req.ProviderID = identity.ProviderID
		req.SourceName = identity.SourceName
		req.BatchID = identity.BatchHead
	}
	return store, req, closeStore, nil
}

func datasetPublicationExportFilter(req DatasetPublicationRequest, schema string, limit, offset int) storage.IndexedRecordQuery {
	filter := storage.IndexedRecordQuery{
		SchemaName:          schema,
		Limit:               limit,
		Offset:              offset,
		AllowLargeResultSet: true,
	}
	if strings.TrimSpace(req.DatastoreKey) == "" {
		filter.ProviderID = strings.TrimSpace(req.ProviderID)
		filter.SourceName = strings.TrimSpace(req.SourceName)
		filter.BatchID = strings.TrimSpace(req.BatchID)
	}
	return filter
}

func datasetPublicationSourceIdentityFromRequest(req DatasetPublicationRequest) datasetPublicationSourceIdentity {
	return datasetPublicationSourceIdentity{
		ProviderID: strings.TrimSpace(req.ProviderID),
		SourceName: strings.TrimSpace(req.SourceName),
		BatchID:    strings.TrimSpace(req.BatchID),
	}
}

func (s *ConcreteDatasetPublicationService) publishDatasetUpdateSeries(ctx context.Context, req DatasetPublicationRequest, schema string) (*DatasetPublicationResult, error) {
	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		if req.FullCatalog {
			chunkSize = defaultFullCatalogPublicationChunkSize
		} else {
			chunkSize = defaultDatasetPublicationLimit
		}
	}
	if chunkSize > maxDatasetPublicationChunkSize {
		chunkSize = maxDatasetPublicationChunkSize
	}
	totalLimit := req.Limit
	if totalLimit <= 0 {
		totalLimit = unlimitedDatasetPublicationLimit()
	}
	datasetID := strings.TrimSpace(req.DatasetID)
	if datasetID == "" {
		datasetID = "sdn-" + safeDatasetPathComponent(strings.TrimSuffix(schema, ".fbs")) + "-full"
	}
	series := &DatasetPublicationResult{Schema: schema}
	pruneStale := req.FullCatalog && req.Limit <= 0
	pruneOffset := 0
	for offset, part := 0, 1; offset < totalLimit; offset, part = offset+chunkSize, part+1 {
		limit := chunkSize
		if remaining := totalLimit - offset; remaining < limit {
			limit = remaining
		}
		partReq := req
		partReq.DatasetID = datasetID
		filter := datasetPublicationExportFilter(partReq, schema, limit, offset)
		publication, err := s.publishDatasetUpdatePart(ctx, partReq, schema, filter, datasetPublicationSourceIdentityFromRequest(partReq), datasetUpdateID(partReq, s.now(), part))
		if err != nil {
			if offset > 0 && strings.Contains(err.Error(), "no records match export query") {
				if pruneStale {
					pruneOffset = offset
				}
				break
			}
			return nil, err
		}
		series.Publications = append(series.Publications, *publication)
		series.RecordCount += publication.RecordCount
		if pruneStale {
			pruneOffset = offset + limit
		}
		if publication.RecordCount < limit {
			break
		}
	}
	if len(series.Publications) == 0 {
		return nil, fmt.Errorf("export dataset window: no records match export query")
	}
	if pruneStale {
		if err := s.pruneStaleDatasetShardPublications(req, schema, pruneOffset); err != nil {
			return nil, err
		}
	}
	publications, err := s.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   strings.TrimSpace(req.ProviderID),
		SourceName:   strings.TrimSpace(req.SourceName),
		BatchID:      strings.TrimSpace(req.BatchID),
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("load dataset shard publications for CAR bundle: %w", err)
	}
	if err := s.recordShardGroupCARBundle(ctx, publications, schema); err != nil {
		return nil, err
	}
	first := series.Publications[0]
	series.ShardCID = first.ShardCID
	series.IndexCID = first.IndexCID
	series.ManifestCID = first.ManifestCID
	series.PNMCID = first.PNMCID
	return series, nil
}

func (s *ConcreteDatasetPublicationService) pruneStaleDatasetShardPublications(req DatasetPublicationRequest, schema string, staleOffset int) error {
	if staleOffset < 0 {
		return fmt.Errorf("prune stale dataset shard publications: stale offset must be non-negative")
	}
	_, err := s.store.DeleteDatasetShardPublicationsAtOrAfterOffset(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   strings.TrimSpace(req.ProviderID),
		SourceName:   strings.TrimSpace(req.SourceName),
		BatchID:      strings.TrimSpace(req.BatchID),
		QueryProfile: storage.DatasetPublicationQueryProfile,
	}, staleOffset)
	if err != nil {
		return fmt.Errorf("prune stale dataset shard publications: %w", err)
	}
	return nil
}

func unlimitedDatasetPublicationLimit() int {
	return int(^uint(0) >> 1)
}

func (s *ConcreteDatasetPublicationService) publishDatasetUpdatePart(ctx context.Context, req DatasetPublicationRequest, schema string, filter storage.IndexedRecordQuery, sourceIdentity datasetPublicationSourceIdentity, updateID string) (*DatasetPublicationResult, error) {
	export, err := s.store.ExportDatasetWindow(filepath.Join(s.outputDir, safeDatasetPathComponent(schema)), storage.IndexedRecordQuery{
		SchemaName:          filter.SchemaName,
		ProviderID:          filter.ProviderID,
		SourceName:          filter.SourceName,
		BatchID:             filter.BatchID,
		Limit:               filter.Limit,
		Offset:              filter.Offset,
		AllowLargeResultSet: filter.AllowLargeResultSet,
		OrderByCID:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("export dataset window: %w", err)
	}
	if reusable, ok, err := s.reusableDatasetPublicationResult(ctx, req, schema, filter, sourceIdentity, export); err != nil {
		return nil, err
	} else if ok {
		return reusable, nil
	}
	result, _, err := s.publishDatasetExport(ctx, req, schema, filter, sourceIdentity, updateID, export, true)
	return result, err
}

func (s *ConcreteDatasetPublicationService) publishDatasetExport(
	ctx context.Context,
	req DatasetPublicationRequest,
	schema string,
	filter storage.IndexedRecordQuery,
	sourceIdentity datasetPublicationSourceIdentity,
	updateID string,
	export *storage.DatasetExport,
	announce bool,
) (*DatasetPublicationResult, storage.DatasetShardPublication, error) {
	if export == nil {
		return nil, storage.DatasetShardPublication{}, fmt.Errorf("dataset export is required")
	}
	published, err := storage.PublishDatasetExportToIPFS(ctx, s.ipfsAPIURL, export)
	if err != nil {
		return nil, storage.DatasetShardPublication{}, err
	}
	export.ShardCID = published.ShardCID
	export.IndexCID = published.IndexCID

	publishedAt := s.now()
	datasetID := strings.TrimSpace(req.DatasetID)
	if datasetID == "" {
		datasetID = "sdn-" + safeDatasetPathComponent(strings.TrimSuffix(schema, ".fbs"))
	}
	manifest, err := storage.BuildSignedDatasetPublicationManifest(s.outputDir, storage.DatasetPublicationManifestOptions{
		Export:         export,
		DatasetID:      datasetID,
		UpdateID:       updateID,
		ProviderPeerID: s.providerPeerID,
		ProviderEPMCID: s.providerEPMCID,
		PublishedAt:    publishedAt,
		SigningKey:     s.signingKey,
		SchemaHash:     datasetPublicationSchemaHash(schema),
	})
	if err != nil {
		return nil, storage.DatasetShardPublication{}, err
	}
	manifestCID, err := storage.PublishDatasetPublicationManifestToIPFS(ctx, s.ipfsAPIURL, manifest)
	if err != nil {
		return nil, storage.DatasetShardPublication{}, err
	}
	manifest.CID = manifestCID
	pnmBytes, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  s.signingKey,
	})
	if err != nil {
		return nil, storage.DatasetShardPublication{}, err
	}
	var pnmCID string
	if s.store != nil {
		var err error
		pnmCID, err = s.store.Store("PNM.fbs", pnmBytes, s.providerPeerID, nil)
		if err != nil {
			return nil, storage.DatasetShardPublication{}, fmt.Errorf("store dataset publication PNM: %w", err)
		}
	}
	shardPublication := storage.DatasetShardPublication{
		SchemaName:   schema,
		ProviderID:   sourceIdentity.ProviderID,
		SourceName:   sourceIdentity.SourceName,
		BatchID:      sourceIdentity.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       filter.Offset,
		Limit:        filter.Limit,
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
	if err := s.store.UpsertDatasetShardPublication(shardPublication); err != nil {
		return nil, storage.DatasetShardPublication{}, fmt.Errorf("record dataset shard publication: %w", err)
	}
	publishedShard, found, err := s.store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   sourceIdentity.ProviderID,
		SourceName:   sourceIdentity.SourceName,
		BatchID:      sourceIdentity.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       filter.Offset,
		Limit:        filter.Limit,
		RecordCount:  export.RecordCount,
	})
	if err != nil {
		return nil, storage.DatasetShardPublication{}, fmt.Errorf("load dataset feed head: %w", err)
	}
	if !found {
		return nil, storage.DatasetShardPublication{}, fmt.Errorf("load dataset feed head: published shard was not found")
	}
	if err := s.recordDatasetPublicationPins(publishedShard, export, manifest, pnmCID, pnmBytes); err != nil {
		return nil, storage.DatasetShardPublication{}, err
	}
	if err := s.recordDatasetPublicationChannel(req, sourceIdentity, publishedShard, manifest.Bytes, pnmBytes); err != nil {
		return nil, storage.DatasetShardPublication{}, err
	}
	if announce {
		if err := s.announceDatasetPublication(ctx, req, publishedShard, pnmBytes); err != nil {
			return nil, storage.DatasetShardPublication{}, err
		}
	}
	return &DatasetPublicationResult{
		Schema:      schema,
		RecordCount: export.RecordCount,
		ShardCID:    export.ShardCID,
		IndexCID:    export.IndexCID,
		ManifestCID: manifest.CID,
		PNMCID:      pnmCID,
	}, publishedShard, nil
}

func (s *ConcreteDatasetPublicationService) recordDatasetPublicationChannel(
	req DatasetPublicationRequest,
	sourceIdentity datasetPublicationSourceIdentity,
	publishedShard storage.DatasetShardPublication,
	manifestBytes []byte,
	pnmBytes []byte,
) error {
	if s == nil || s.channelRecorder == nil {
		return nil
	}
	providerPublicKey, ok := s.signingKey.Public().(ed25519.PublicKey)
	if !ok || len(providerPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("record dataset publication channel: provider public key is unavailable")
	}
	sourceID := datasetPublicationChannelSourceID(req, sourceIdentity)
	if sourceID == "" {
		return nil
	}
	return s.channelRecorder.RecordDatasetPublicationChannelUpdate(DatasetPublicationChannelUpdate{
		Schema:            publishedShard.SchemaName,
		SourceID:          sourceID,
		PNMBytes:          pnmBytes,
		ManifestBytes:     manifestBytes,
		ProviderPublicKey: append(ed25519.PublicKey(nil), providerPublicKey...),
		PublishedShard:    publishedShard,
	})
}

func datasetPublicationChannelSourceID(req DatasetPublicationRequest, sourceIdentity datasetPublicationSourceIdentity) string {
	for _, value := range []string{req.SourceName, sourceIdentity.SourceName} {
		sourceName := strings.ToLower(strings.TrimSpace(value))
		if sourceName == "celestrak" || strings.HasPrefix(sourceName, "celestrak-") {
			return "celestrak"
		}
	}
	if providerID := strings.TrimSpace(sourceIdentity.ProviderID); providerID != "" {
		return providerID
	}
	return strings.TrimSpace(req.ProviderID)
}

func (s *ConcreteDatasetPublicationService) publishedShardFromResult(
	schema string,
	filter storage.IndexedRecordQuery,
	sourceIdentity datasetPublicationSourceIdentity,
	result *DatasetPublicationResult,
) (storage.DatasetShardPublication, error) {
	if result == nil {
		return storage.DatasetShardPublication{}, fmt.Errorf("load dataset shard publication: result is required")
	}
	published, found, err := s.store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   sourceIdentity.ProviderID,
		SourceName:   sourceIdentity.SourceName,
		BatchID:      sourceIdentity.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       filter.Offset,
		Limit:        filter.Limit,
		RecordCount:  result.RecordCount,
	})
	if err != nil {
		return storage.DatasetShardPublication{}, fmt.Errorf("load dataset shard publication: %w", err)
	}
	if !found {
		return storage.DatasetShardPublication{}, fmt.Errorf("load dataset shard publication: published shard was not found")
	}
	return published, nil
}

func (s *ConcreteDatasetPublicationService) reusableDatasetPublicationResult(
	ctx context.Context,
	req DatasetPublicationRequest,
	schema string,
	filter storage.IndexedRecordQuery,
	sourceIdentity datasetPublicationSourceIdentity,
	export *storage.DatasetExport,
) (*DatasetPublicationResult, bool, error) {
	if s == nil || s.store == nil || export == nil {
		return nil, false, nil
	}
	existing, found, err := s.store.FindDatasetShardPublication(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   sourceIdentity.ProviderID,
		SourceName:   sourceIdentity.SourceName,
		BatchID:      sourceIdentity.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       filter.Offset,
		Limit:        filter.Limit,
		RecordCount:  export.RecordCount,
	})
	if err != nil {
		return nil, false, fmt.Errorf("find reusable dataset shard publication: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	if strings.TrimSpace(existing.PNMCID) == "" {
		return nil, false, nil
	}
	if existing.RecordCount != export.RecordCount ||
		existing.ByteCount != export.ShardBytes ||
		existing.ShardSHA256 != export.ShardSHA256 ||
		existing.IndexSHA256 != export.IndexSHA256 ||
		existing.QuerySHA256 != export.QuerySHA256 ||
		existing.ResultSHA256 != export.ResultSHA256 ||
		existing.ShardCID == "" ||
		existing.IndexCID == "" ||
		existing.ManifestCID == "" {
		return nil, false, nil
	}
	pnmRecord, err := s.store.GetRecord("PNM.fbs", existing.PNMCID)
	if err != nil {
		return nil, false, fmt.Errorf("load reusable dataset publication PNM: %w", err)
	}
	if len(pnmRecord.Data) == 0 {
		return nil, false, fmt.Errorf("load reusable dataset publication PNM: empty record %s", existing.PNMCID)
	}
	manifestBytes, err := storage.FetchIPFSBlockByCID(ctx, s.ipfsAPIURL, existing.ManifestCID)
	if err != nil {
		return nil, false, fmt.Errorf("load reusable dataset publication DPM: %w", err)
	}
	if len(manifestBytes) == 0 {
		return nil, false, fmt.Errorf("load reusable dataset publication DPM: empty CID %s", existing.ManifestCID)
	}
	if err := s.recordDatasetPublicationChannel(req, sourceIdentity, existing, manifestBytes, pnmRecord.Data); err != nil {
		return nil, false, err
	}
	if err := s.announceDatasetPublication(ctx, req, existing, pnmRecord.Data); err != nil {
		return nil, false, err
	}
	return &DatasetPublicationResult{
		Schema:      schema,
		RecordCount: existing.RecordCount,
		ShardCID:    existing.ShardCID,
		IndexCID:    existing.IndexCID,
		ManifestCID: existing.ManifestCID,
		PNMCID:      existing.PNMCID,
	}, true, nil
}

func (s *ConcreteDatasetPublicationService) announceDatasetPublication(ctx context.Context, req DatasetPublicationRequest, publishedShard storage.DatasetShardPublication, pnmBytes []byte) error {
	if err := s.publisher.PublishDatasetUpdatePNM(ctx, sdnpubsub.DatasetUpdateAnnouncement{
		PNM:               pnmBytes,
		Schemas:           []string{publishedShard.SchemaName},
		CombinedCelesTrak: req.CombinedCelesTrak,
	}); err != nil {
		return err
	}
	return s.announceDatasetFeedHead(ctx, publishedShard)
}

func (s *ConcreteDatasetPublicationService) announceDatasetFeedHead(ctx context.Context, publishedShard storage.DatasetShardPublication) error {
	if headPublisher, ok := s.publisher.(DatasetFeedHeadPublisher); ok {
		if err := headPublisher.PublishDatasetFeedHead(ctx, sdnpubsub.DatasetFeedHeadAnnouncement{
			Schema:       publishedShard.SchemaName,
			ProviderID:   publishedShard.ProviderID,
			SourceName:   publishedShard.SourceName,
			BatchID:      publishedShard.BatchID,
			QueryProfile: publishedShard.QueryProfile,
			Offset:       publishedShard.Offset,
			Limit:        publishedShard.Limit,
			FeedSequence: publishedShard.FeedSequence,
			PreviousHead: publishedShard.PreviousHead,
			FeedHead:     publishedShard.FeedHead,
			RecordCount:  publishedShard.RecordCount,
			ByteCount:    publishedShard.ByteCount,
			ShardCID:     publishedShard.ShardCID,
			IndexCID:     publishedShard.IndexCID,
			ManifestCID:  publishedShard.ManifestCID,
			PNMCID:       publishedShard.PNMCID,
			PublishedAt:  publishedShard.PublishedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *ConcreteDatasetPublicationService) recordShardGroupCARBundle(ctx context.Context, publications []storage.DatasetShardPublication, schema string) error {
	if len(publications) == 0 {
		return nil
	}
	last := publications[len(publications)-1]
	head := last.FeedHead
	if head == "" {
		head = datasync.PublishedFeedHead(schema, last.ProviderID, last.SourceName, last.BatchID, last.QueryProfile, publications)
	}
	carOutputDir := filepath.Join(s.outputDir, safeDatasetPathComponent(schema), "car")
	existing, err := s.store.ListPinLedgerEntries(storage.PinLedgerQuery{
		SchemaName:        schema,
		ProviderPeerID:    s.providerPeerID,
		ProviderID:        last.ProviderID,
		SourceName:        last.SourceName,
		BatchID:           last.BatchID,
		QueryProfile:      last.QueryProfile,
		Role:              "shard-group-car",
		VerificationState: "verified",
	})
	if err != nil {
		return fmt.Errorf("list existing shard-group CAR bundle pins: %w", err)
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
		return s.retireStaleShardGroupCARBundles(ctx, existing, head, carOutputDir)
	}

	providerPublicKey := ""
	if len(s.signingKey) == ed25519.PrivateKeySize {
		if pubKey, ok := s.signingKey.Public().(ed25519.PublicKey); ok {
			providerPublicKey = hex.EncodeToString(pubKey)
		}
	}
	verifiedAt := last.PublishedAt
	if verifiedAt.IsZero() {
		verifiedAt = s.now()
	}
	highWaterMark := datasync.PublishedFeedHighWaterMark(publications, totalRows, totalBytes)
	currentCARPaths := make([]string, 0)
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
		publishedCAR, err := storage.PublishShardGroupCARToIPFS(ctx, s.ipfsAPIURL, carOutputDir, rootCIDs)
		if err != nil {
			return fmt.Errorf("publish shard-group CAR bundle: %w", err)
		}
		currentCARPaths = append(currentCARPaths, publishedCAR.Path)
		if err := s.store.UpsertPinLedgerEntry(storage.PinLedgerEntry{
			CID:               publishedCAR.CID,
			SchemaName:        schema,
			ProviderPeerID:    s.providerPeerID,
			ProviderPublicKey: providerPublicKey,
			ProviderID:        last.ProviderID,
			SourceName:        last.SourceName,
			BatchID:           last.BatchID,
			QueryProfile:      last.QueryProfile,
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
			return fmt.Errorf("record shard-group CAR pin ledger: %w", err)
		}
	}
	if err := s.retireStaleShardGroupCARBundles(ctx, existing, head, carOutputDir, currentCARPaths...); err != nil {
		return err
	}
	return nil
}

func (s *ConcreteDatasetPublicationService) retireStaleShardGroupCARBundles(ctx context.Context, entries []storage.PinLedgerEntry, currentHead, carOutputDir string, currentCARPaths ...string) error {
	for _, entry := range entries {
		if entry.CID == "" {
			continue
		}
		if entry.Head == currentHead || entry.SnapshotID == currentHead {
			continue
		}
		if err := storage.UnpinIPFSCID(ctx, s.ipfsAPIURL, entry.CID); err != nil {
			return fmt.Errorf("unpin stale shard-group CAR %s: %w", entry.CID, err)
		}
		entry.VerificationState = "stale"
		entry.UpdatedAt = s.now()
		if err := s.store.UpsertPinLedgerEntry(entry); err != nil {
			return fmt.Errorf("mark stale shard-group CAR %s: %w", entry.CID, err)
		}
	}
	if len(currentCARPaths) > 0 {
		if err := storage.RemoveStaleShardGroupCARFiles(carOutputDir, currentCARPaths...); err != nil {
			return fmt.Errorf("remove stale shard-group CAR files: %w", err)
		}
	}
	return nil
}

func (s *ConcreteDatasetPublicationService) recordDatasetPublicationPins(
	pub storage.DatasetShardPublication,
	export *storage.DatasetExport,
	manifest *storage.DatasetPublicationManifest,
	pnmCID string,
	pnmBytes []byte,
) error {
	if export == nil {
		return fmt.Errorf("record publication pin ledger: export is required")
	}
	if manifest == nil {
		return fmt.Errorf("record publication pin ledger: manifest is required")
	}
	providerPublicKey := ""
	if len(s.signingKey) == ed25519.PrivateKeySize {
		if pubKey, ok := s.signingKey.Public().(ed25519.PublicKey); ok {
			providerPublicKey = hex.EncodeToString(pubKey)
		}
	}
	verifiedAt := pub.PublishedAt
	if verifiedAt.IsZero() {
		verifiedAt = s.now()
	}
	snapshotID := pub.FeedHead
	if snapshotID == "" {
		snapshotID = pub.ManifestCID
	}
	highWaterMark := datasync.PublishedFeedHighWaterMark([]storage.DatasetShardPublication{pub}, int64(pub.RecordCount), pub.ByteCount)
	entries := []storage.PinLedgerEntry{
		{
			CID:           pub.ShardCID,
			ByteHash:      export.ShardSHA256,
			Role:          "shard",
			RowCount:      int64(pub.RecordCount),
			ByteCount:     export.ShardBytes,
			HighWaterMark: highWaterMark,
		},
		{
			CID:           pub.IndexCID,
			ByteHash:      export.IndexSHA256,
			Role:          "index",
			ByteCount:     export.IndexBytes,
			HighWaterMark: highWaterMark,
		},
		{
			CID:           pub.ManifestCID,
			ByteHash:      manifest.SHA256,
			Role:          "manifest",
			ByteCount:     manifest.ByteLength,
			HighWaterMark: highWaterMark,
		},
		{
			CID:           pnmCID,
			ByteHash:      sha256Hex(pnmBytes),
			Role:          "pnm",
			ByteCount:     int64(len(pnmBytes)),
			HighWaterMark: highWaterMark,
		},
	}
	for _, entry := range entries {
		if entry.CID == "" {
			continue
		}
		entry.SchemaName = pub.SchemaName
		entry.ProviderPeerID = s.providerPeerID
		entry.ProviderPublicKey = providerPublicKey
		entry.ProviderID = pub.ProviderID
		entry.SourceName = pub.SourceName
		entry.BatchID = pub.BatchID
		entry.QueryProfile = pub.QueryProfile
		entry.SnapshotID = snapshotID
		entry.Head = pub.FeedHead
		entry.VerificationState = "verified"
		entry.VerifiedAt = verifiedAt
		entry.UpdatedAt = verifiedAt
		if err := s.store.UpsertPinLedgerEntry(entry); err != nil {
			return fmt.Errorf("record publication pin ledger %s %s: %w", entry.Role, entry.CID, err)
		}
	}
	return nil
}

func datasetUpdateID(req DatasetPublicationRequest, publishedAt time.Time, part int) string {
	base := strings.TrimSpace(req.BatchID)
	if base == "" {
		base = publishedAt.UTC().Format("20060102T150405.000000000Z")
	}
	if part > 0 {
		return fmt.Sprintf("%s:part-%06d", base, part)
	}
	return base
}

func datasetPublicationSchemaHash(schema string) string {
	registry, err := sds.NewSchemaRegistry()
	if err != nil {
		return ""
	}
	content, ok := registry.Get(schema)
	if !ok || len(content) == 0 {
		return ""
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeDatasetPublicationSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" || strings.HasSuffix(schema, ".fbs") {
		return schema
	}
	return schema + ".fbs"
}

func safeDatasetPathComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".fbs")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	value = replacer.Replace(value)
	if value == "" {
		return "dataset"
	}
	return value
}
