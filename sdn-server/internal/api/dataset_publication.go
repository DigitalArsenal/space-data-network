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
	"path/filepath"
	"strings"
	"time"

	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const defaultDatasetPublicationLimit = 250

// DatasetPublicationRequest describes a local request to export, pin, sign,
// and announce a dataset update from the daemon's current FlatSQL store.
type DatasetPublicationRequest struct {
	Schema            string `json:"schema"`
	ProviderID        string `json:"providerId,omitempty"`
	SourceName        string `json:"sourceName,omitempty"`
	BatchID           string `json:"batchId,omitempty"`
	DatasetID         string `json:"datasetId,omitempty"`
	Limit             int    `json:"limit,omitempty"`
	CombinedCelesTrak bool   `json:"combinedCelesTrak,omitempty"`
}

// DatasetPublicationResult is the safe summary returned after publication.
type DatasetPublicationResult struct {
	Schema      string `json:"schema"`
	RecordCount int    `json:"recordCount"`
	ShardCID    string `json:"shardCid"`
	IndexCID    string `json:"indexCid"`
	ManifestCID string `json:"manifestCid"`
	PNMCID      string `json:"pnmCid,omitempty"`
}

// DatasetPublicationService publishes dataset updates.
type DatasetPublicationService interface {
	PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error)
}

// DatasetUpdatePublisher is implemented by the running SDN node.
type DatasetUpdatePublisher interface {
	PublishDatasetUpdatePNM(context.Context, sdnpubsub.DatasetUpdateAnnouncement) error
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
	writeJSON(w, http.StatusAccepted, result)
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
	store          *storage.FlatSQLStore
	publisher      DatasetUpdatePublisher
	signingKey     ed25519.PrivateKey
	providerPeerID string
	providerEPMCID string
	ipfsAPIURL     string
	outputDir      string
	now            func() time.Time
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

	schema := normalizeDatasetPublicationSchema(req.Schema)
	if err := sds.ValidateSchemaName(schema); err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultDatasetPublicationLimit
	}

	export, err := s.store.ExportDatasetWindow(filepath.Join(s.outputDir, safeDatasetPathComponent(schema)), storage.IndexedRecordQuery{
		SchemaName:          schema,
		ProviderID:          strings.TrimSpace(req.ProviderID),
		SourceName:          strings.TrimSpace(req.SourceName),
		BatchID:             strings.TrimSpace(req.BatchID),
		Limit:               limit,
		AllowLargeResultSet: true,
	})
	if err != nil {
		return nil, fmt.Errorf("export dataset window: %w", err)
	}
	published, err := storage.PublishDatasetExportToIPFS(ctx, s.ipfsAPIURL, export)
	if err != nil {
		return nil, err
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
		UpdateID:       publishedAt.UTC().Format("20060102T150405.000000000Z"),
		ProviderPeerID: s.providerPeerID,
		ProviderEPMCID: s.providerEPMCID,
		PublishedAt:    publishedAt,
		SigningKey:     s.signingKey,
		SchemaHash:     datasetPublicationSchemaHash(schema),
	})
	if err != nil {
		return nil, err
	}
	manifestCID, err := storage.PublishDatasetPublicationManifestToIPFS(ctx, s.ipfsAPIURL, manifest)
	if err != nil {
		return nil, err
	}
	manifest.CID = manifestCID
	pnmBytes, err := storage.BuildDatasetPublicationPNM(manifest, storage.DatasetPublicationPNMOptions{
		PublishedAt: publishedAt,
		SigningKey:  s.signingKey,
	})
	if err != nil {
		return nil, err
	}
	var pnmCID string
	if s.store != nil {
		var err error
		pnmCID, err = s.store.Store("PNM.fbs", pnmBytes, s.providerPeerID, nil)
		if err != nil {
			return nil, fmt.Errorf("store dataset publication PNM: %w", err)
		}
	}
	if err := s.publisher.PublishDatasetUpdatePNM(ctx, sdnpubsub.DatasetUpdateAnnouncement{
		PNM:               pnmBytes,
		Schemas:           []string{schema},
		CombinedCelesTrak: req.CombinedCelesTrak,
	}); err != nil {
		return nil, err
	}
	return &DatasetPublicationResult{
		Schema:      schema,
		RecordCount: export.RecordCount,
		ShardCID:    export.ShardCID,
		IndexCID:    export.IndexCID,
		ManifestCID: manifest.CID,
		PNMCID:      pnmCID,
	}, nil
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
