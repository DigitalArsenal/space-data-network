package api

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/abac"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/logservice"
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
	store        *storage.FlatSQLStore
	validator    *sds.Validator
	quotas       *StorageQuotaManager
	cfg          *config.PublishingConfig
	authHandler  *auth.Handler
	logService   *logservice.Service
	policyEngine auth.PolicyEngine // optional; nil = policies disabled
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

// SetPolicyEngine attaches an ABAC policy engine.  When set, every publish
// request is evaluated against the policy after the trust-level gate.
// Pass nil to disable (default).
func (h *PublishHandler) SetPolicyEngine(engine auth.PolicyEngine) {
	h.policyEngine = engine
}

// SetLogService sets the publication log service for PLG entry creation.
func (h *PublishHandler) SetLogService(ls *logservice.Service) {
	h.logService = ls
}

// RegisterRoutes registers publish API routes.
func (h *PublishHandler) RegisterRoutes(mux *http.ServeMux) {
	if h.authHandler == nil {
		panic("PublishHandler.RegisterRoutes requires an authentication handler")
	}
	minTrust := peers.Standard
	if h.cfg.MinTrustLevel != "" {
		if parsed, err := peers.ParseTrustLevel(h.cfg.MinTrustLevel); err == nil {
			minTrust = parsed
		}
	}

	h.registerRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return h.authHandler.RequireAuth(minTrust, next)
	})
}

// RegisterUnauthenticatedRoutes explicitly mounts publishing for a node whose
// operator disabled admin authentication. The supplied principal is recorded
// as the publisher identity for quotas, provenance, and audit instead of
// silently falling through to another /api/v1/data/ handler.
func (h *PublishHandler) RegisterUnauthenticatedRoutes(mux *http.ServeMux, principal string) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		panic("PublishHandler.RegisterUnauthenticatedRoutes requires an audit principal")
	}
	session := &auth.Session{XPub: principal, TrustLevel: peers.Untrusted}
	h.registerRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.ContextWithSession(r.Context(), session)
			next(w, r.WithContext(ctx))
		}
	})
}

// RegisterLocalLaneRoutes mounts the publish routes on a PRIVATE mux that is
// served by a separate, loopback-bound listener (config publishing.local_publish_addr)
// for a data pipeline running ON this host. It is the ONLY unauthenticated write
// lane on a node that has admin auth enabled.
//
// SECURITY — why this is a second socket and not an exemption on the public one:
// nginx reverse-proxies the public listener, so a request from the internet already
// reaches the daemon with RemoteAddr 127.0.0.1. Any "loopback ⇒ trusted" rule on
// the public listener would hand writes to the entire internet, and X-Forwarded-For
// / X-Real-IP are attacker-controlled and can never carry an auth decision. The
// authority here comes from the SOCKET — a listener bound to a loopback address that
// the reverse proxy does not forward to — not from an inspected client address.
//
// The loopback check below is defence in depth, not the primary control: it fails
// closed if the listener is ever misconfigured onto a routable interface, and it
// rejects any request bearing proxy headers, which would mean someone has put a
// reverse proxy in front of this lane.
func (h *PublishHandler) RegisterLocalLaneRoutes(mux *http.ServeMux, principal string) {
	principal = strings.TrimSpace(principal)
	if principal == "" {
		panic("PublishHandler.RegisterLocalLaneRoutes requires an audit principal")
	}
	session := &auth.Session{XPub: principal, TrustLevel: peers.Admin}

	protect := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := requireLoopbackClient(r); err != nil {
				writeError(w, http.StatusForbidden, "local publish lane: "+err.Error())
				return
			}
			ctx := auth.ContextWithSession(r.Context(), session)
			next(w, r.WithContext(ctx))
		}
	}

	h.registerRoutes(mux, protect)

	// Query-param alias so an operator/pipeline can name the schema without
	// building a path: POST /api/v1/admin/publish?schema=OMM.fbs&source_name=...
	mux.HandleFunc("/api/v1/admin/publish", protect(h.aliasPublish("/api/v1/data/publish/", h.handlePublish)))
	mux.HandleFunc("/api/v1/admin/publish/batch", protect(h.aliasPublish("/api/v1/data/publish/batch/", h.handlePublishBatch)))
}

// aliasPublish adapts ?schema=NAME to the path-based publish handlers.
func (h *PublishHandler) aliasPublish(prefix string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		schema := strings.TrimSpace(r.URL.Query().Get("schema"))
		if schema == "" {
			writeError(w, http.StatusBadRequest, "missing schema query parameter")
			return
		}
		if err := sds.ValidateSchemaName(schema); err != nil {
			writeError(w, http.StatusBadRequest, "invalid schema name: "+err.Error())
			return
		}
		rewritten := r.Clone(r.Context())
		rewritten.URL.Path = prefix + schema
		next(w, rewritten)
	}
}

// requireLoopbackClient rejects anything that did not come from a process on this
// host over the loopback interface. See RegisterLocalLaneRoutes for why this is a
// backstop rather than the primary control.
func requireLoopbackClient(r *http.Request) error {
	// A proxy header on the private lane means a reverse proxy has been placed in
	// front of it — exactly the misconfiguration this lane must never survive.
	// We do not parse these headers (they are forgeable); their mere presence is
	// disqualifying.
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "X-Forwarded-Host"} {
		if r.Header.Get(header) != "" {
			return fmt.Errorf("request carries %s: this lane must never be reverse-proxied", header)
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("client %q is not on the loopback interface", r.RemoteAddr)
	}
	return nil
}

func (h *PublishHandler) registerRoutes(mux *http.ServeMux, protect func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/v1/data/publish/", protect(h.handlePublish))
	mux.HandleFunc("/api/v1/data/publish/batch/", protect(h.handlePublishBatch))
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

	// ABAC policy check — runs after trust gate (defence in depth).
	if h.policyEngine != nil {
		sub := abac.Subject{
			XPub:       session.XPub,
			TrustLevel: int(session.TrustLevel),
			Attrs:      map[string]string{},
		}
		res := abac.Resource{Schema: schema}
		decision := h.policyEngine.Evaluate(sub, abac.ActionPublish, res)
		if !decision.Allowed {
			writeError(w, http.StatusForbidden, "access denied by policy: "+decision.Reason)
			return
		}
	}

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

	// ROUTE ON THE HEADER. The URL path segment is a COMMITMENT about the
	// bytes that follow, so the record's own size prefix + file_identifier
	// decides where it goes and a disagreement is a 400 naming both. The
	// declared name has already gated the allow-list and ABAC above, and the
	// route can only equal it or fail, so a caller can never reach another
	// schema's table through an allowed path.
	if h.validator != nil {
		decision, routeErr := h.validator.RouteBuffer(schema, data)
		if routeErr != nil {
			writeError(w, http.StatusBadRequest, routeErr.Error())
			return
		}
		if err := decision.MismatchError(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		schema = decision.Schema
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

	// Store — with source tags when the publisher identifies its lane
	// (?source_name=&provider_id=&batch_id=&source_url=). Tagged records feed
	// the per-(source,batch) progress aggregates on /api/v1/stats; untagged
	// publishes keep the legacy path.
	cid, err := h.storeWithOptionalTags(schema, data, peerID, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store record: "+err.Error())
		return
	}

	// Append PLG entry for this published record (non-blocking on failure)
	if h.logService != nil && schema != "PLOG.fbs" && schema != "PLHD.fbs" {
		if _, _, logErr := h.logService.AppendEntry(schema, cid, nil, ""); logErr != nil {
			// Log but don't fail the publish
			_ = logErr
		}
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

	// ABAC policy check — runs after trust gate (defence in depth).
	if h.policyEngine != nil {
		sub := abac.Subject{
			XPub:       session.XPub,
			TrustLevel: int(session.TrustLevel),
			Attrs:      map[string]string{},
		}
		res := abac.Resource{Schema: schema}
		decision := h.policyEngine.Evaluate(sub, abac.ActionPublish, res)
		if !decision.Allowed {
			writeError(w, http.StatusForbidden, "access denied by policy: "+decision.Reason)
			return
		}
	}

	// Read native FlatSQL little-endian uint32 size-prefixed stream.
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

		recLen := binary.LittleEndian.Uint32(lenBuf[:])
		if recLen == 0 || int64(recLen) > int64(h.cfg.MaxRecordBytes) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid record size: %d", recLen))
			return
		}

		data := make([]byte, recLen)
		if _, err := io.ReadFull(body, data); err != nil {
			writeError(w, http.StatusBadRequest, "truncated record data")
			return
		}

		// Route each frame on its own header; the batch URL is a commitment
		// for every record in the stream, so a frame whose identifier
		// disagrees is reported and skipped rather than stored under the
		// declared name.
		recordSchema := schema
		if h.validator != nil {
			decision, routeErr := h.validator.RouteBuffer(schema, data)
			if routeErr != nil {
				results = append(results, map[string]interface{}{
					"error": routeErr.Error(),
					"bytes": len(data),
				})
				continue
			}
			if err := decision.MismatchError(); err != nil {
				results = append(results, map[string]interface{}{
					"error": err.Error(),
					"bytes": len(data),
				})
				continue
			}
			recordSchema = decision.Schema
		}

		if h.validator != nil {
			if err := h.validator.Validate(r.Context(), recordSchema, data); err != nil {
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

		cid, err := h.storeWithOptionalTags(recordSchema, data, peerID, r)
		if err != nil {
			results = append(results, map[string]interface{}{
				"error": "store failed: " + err.Error(),
				"bytes": len(data),
			})
			continue
		}

		// Append PLG entry for this record (non-blocking on failure)
		if h.logService != nil && recordSchema != "PLOG.fbs" && recordSchema != "PLHD.fbs" {
			if _, _, logErr := h.logService.AppendEntry(recordSchema, cid, nil, ""); logErr != nil {
				_ = logErr
			}
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

// storeWithOptionalTags stores a published record, attaching SourceTags when
// the request identifies its lane via query params (?source_name= and/or
// ?provider_id=, plus optional batch_id / source_url). The producer peer is
// always recorded on tagged stores so tagged publishes stay attributable.
func (h *PublishHandler) storeWithOptionalTags(schema string, data []byte, peerID string, r *http.Request) (string, error) {
	q := r.URL.Query()
	sourceName := strings.TrimSpace(q.Get("source_name"))
	providerID := strings.TrimSpace(q.Get("provider_id"))
	if sourceName == "" && providerID == "" {
		return h.store.Store(schema, data, peerID, nil)
	}
	return h.store.StoreWithSourceTags(schema, data, peerID, nil, storage.SourceTags{
		ProviderID:     providerID,
		SourceName:     sourceName,
		SourceURL:      strings.TrimSpace(q.Get("source_url")),
		BatchID:        strings.TrimSpace(q.Get("batch_id")),
		ProducerPeerID: peerID,
	})
}
