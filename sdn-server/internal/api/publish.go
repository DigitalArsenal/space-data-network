package api

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// StorageQuotaManager enforces per-peer storage limits.
type StorageQuotaManager struct {
	store             *storage.FlatSQLStore
	defaultQuotaBytes int64
	schemaMaxBytes    map[string]int64
	peerQuotas        map[string]int64
	mu                sync.RWMutex
}

// NewStorageQuotaManager creates a new quota manager.
func NewStorageQuotaManager(store *storage.FlatSQLStore, defaultQuota int64) *StorageQuotaManager {
	return &StorageQuotaManager{
		store:             store,
		defaultQuotaBytes: defaultQuota,
		schemaMaxBytes:    make(map[string]int64),
		peerQuotas:        make(map[string]int64),
	}
}

// SetPeerQuota sets a per-peer storage quota override.
func (q *StorageQuotaManager) SetPeerQuota(peerID string, bytes int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.peerQuotas[peerID] = bytes
}

// CheckQuota verifies a peer has quota remaining for a write of dataSize bytes.
func (q *StorageQuotaManager) CheckQuota(peerID string, dataSize int) error {
	q.mu.RLock()
	quota, ok := q.peerQuotas[peerID]
	if !ok {
		quota = q.defaultQuotaBytes
	}
	q.mu.RUnlock()

	used, err := q.store.PeerStorageBytes(peerID)
	if err != nil {
		return fmt.Errorf("failed to check storage usage: %w", err)
	}

	if used+int64(dataSize) > quota {
		return fmt.Errorf("storage quota exceeded: %d used + %d new > %d limit", used, dataSize, quota)
	}

	return nil
}

// TipPublisher is an optional interface for announcing new data via PNM.
type TipPublisher interface {
	PublishTip(ctx context.Context, schema, cid string) error
}

// PublishHandler accepts data writes from authenticated peers.
type PublishHandler struct {
	store     *storage.FlatSQLStore
	validator *sds.Validator
	quotas    *StorageQuotaManager
	cfg       *config.PublishingConfig
	authHandler *auth.Handler
}

// NewPublishHandler creates a new publish handler.
func NewPublishHandler(
	store *storage.FlatSQLStore,
	validator *sds.Validator,
	quotas *StorageQuotaManager,
	cfg *config.PublishingConfig,
	authHandler *auth.Handler,
) *PublishHandler {
	return &PublishHandler{
		store:       store,
		validator:   validator,
		quotas:      quotas,
		cfg:         cfg,
		authHandler: authHandler,
	}
}

// RegisterRoutes registers publish API routes.
func (h *PublishHandler) RegisterRoutes(mux *http.ServeMux) {
	minTrust := peers.Standard
	if h.cfg.MinTrustLevel != "" {
		if parsed, err := peers.ParseTrustLevel(h.cfg.MinTrustLevel); err == nil {
			minTrust = parsed
		}
	}

	mux.HandleFunc("/api/v1/data/publish/", h.authHandler.RequireAuth(minTrust, h.handlePublish))
	mux.HandleFunc("/api/v1/data/publish/batch/", h.authHandler.RequireAuth(minTrust, h.handlePublishBatch))
}

func (h *PublishHandler) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.cfg.Enabled {
		writeError(w, http.StatusForbidden, "data publishing is disabled on this node")
		return
	}

	// Extract schema from URL: /api/v1/data/publish/{schema}
	schema := strings.TrimPrefix(r.URL.Path, "/api/v1/data/publish/")
	schema = strings.TrimSuffix(schema, "/")
	if schema == "" {
		writeError(w, http.StatusBadRequest, "missing schema in URL path")
		return
	}

	if err := sds.ValidateSchemaName(schema); err != nil {
		writeError(w, http.StatusBadRequest, "invalid schema name: "+err.Error())
		return
	}

	if !h.isSchemaAllowed(schema) {
		writeError(w, http.StatusForbidden, "schema not allowed for publishing: "+schema)
		return
	}

	session := auth.SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "no session")
		return
	}
	peerID := session.XPub // use xpub as peer identifier for published records

	// Read body with size limit
	maxBytes := int64(h.cfg.MaxRecordBytes)
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	// Validate FlatBuffer
	if h.validator != nil {
		if err := h.validator.Validate(r.Context(), schema, data); err != nil {
			writeError(w, http.StatusBadRequest, "validation failed: "+err.Error())
			return
		}
	}

	// Check quota
	if h.quotas != nil {
		if err := h.quotas.CheckQuota(peerID, len(data)); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
	}

	// Store
	cid, err := h.store.Store(schema, data, peerID, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store record: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"cid":       cid,
		"schema":    schema,
		"stored_at": time.Now().UTC().Format(time.RFC3339),
		"bytes":     len(data),
	})
}

func (h *PublishHandler) handlePublishBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.cfg.Enabled {
		writeError(w, http.StatusForbidden, "data publishing is disabled on this node")
		return
	}

	// Extract schema from URL: /api/v1/data/publish/batch/{schema}
	schema := strings.TrimPrefix(r.URL.Path, "/api/v1/data/publish/batch/")
	schema = strings.TrimSuffix(schema, "/")
	if schema == "" {
		writeError(w, http.StatusBadRequest, "missing schema in URL path")
		return
	}

	if err := sds.ValidateSchemaName(schema); err != nil {
		writeError(w, http.StatusBadRequest, "invalid schema name: "+err.Error())
		return
	}

	if !h.isSchemaAllowed(schema) {
		writeError(w, http.StatusForbidden, "schema not allowed for publishing: "+schema)
		return
	}

	session := auth.SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "no session")
		return
	}
	peerID := session.XPub

	// Read uint32BE-length-prefixed stream.
	// Total body limit: 10x single record max.
	maxTotal := int64(h.cfg.MaxRecordBytes) * 10
	if maxTotal <= 0 {
		maxTotal = 100 * 1024 * 1024
	}
	body := http.MaxBytesReader(w, r.Body, maxTotal)

	var results []map[string]interface{}
	var lenBuf [4]byte

	for {
		if _, err := io.ReadFull(body, lenBuf[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			writeError(w, http.StatusBadRequest, "failed to read record length: "+err.Error())
			return
		}

		recLen := binary.BigEndian.Uint32(lenBuf[:])
		if recLen == 0 || int64(recLen) > int64(h.cfg.MaxRecordBytes) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid record size: %d", recLen))
			return
		}

		data := make([]byte, recLen)
		if _, err := io.ReadFull(body, data); err != nil {
			writeError(w, http.StatusBadRequest, "truncated record data")
			return
		}

		if h.validator != nil {
			if err := h.validator.Validate(r.Context(), schema, data); err != nil {
				results = append(results, map[string]interface{}{
					"error": "validation failed: " + err.Error(),
					"bytes": len(data),
				})
				continue
			}
		}

		if h.quotas != nil {
			if err := h.quotas.CheckQuota(peerID, len(data)); err != nil {
				results = append(results, map[string]interface{}{
					"error": err.Error(),
					"bytes": len(data),
				})
				break // stop processing on quota exceeded
			}
		}

		cid, err := h.store.Store(schema, data, peerID, nil)
		if err != nil {
			results = append(results, map[string]interface{}{
				"error": "store failed: " + err.Error(),
				"bytes": len(data),
			})
			continue
		}

		results = append(results, map[string]interface{}{
			"cid":   cid,
			"bytes": len(data),
		})
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"schema":    schema,
		"stored_at": time.Now().UTC().Format(time.RFC3339),
		"results":   results,
		"count":     len(results),
	})
}

func (h *PublishHandler) isSchemaAllowed(schema string) bool {
	if len(h.cfg.AllowedSchemas) == 0 {
		return true
	}
	for _, allowed := range h.cfg.AllowedSchemas {
		if strings.EqualFold(allowed, schema) {
			return true
		}
	}
	return false
}

