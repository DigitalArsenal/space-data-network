package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/abac"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulesign"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// topicPublisher is the minimal interface needed to publish to a named topic.
type topicPublisher interface {
	PublishToTopic(ctx context.Context, topicName string, data []byte) error
}

type schemaPublisher interface {
	Publish(schema string, data []byte) error
}

func publishSchemaPubSubMessage(ctx context.Context, publisher topicPublisher, schema string, data []byte) (string, error) {
	topicName := sdnpubsub.TopicName(schema)
	if schemaPub, ok := publisher.(schemaPublisher); ok {
		return topicName, schemaPub.Publish(schema, data)
	}
	return topicName, publisher.PublishToTopic(ctx, topicName, data)
}

// CoreAPIHandler handles the /api/v1/ identity, stats, peers, and pubsub endpoints.
type CoreAPIHandler struct {
	peerID       peer.ID
	h2pHost      host.Host
	pubsubSvc    *pubsub.PubSub
	publisher    topicPublisher
	store        *storage.FlatSQLStore
	validator    *sds.Validator
	cfg          *config.AdminConfig
	authHandler  *auth.Handler
	rl           *rateLimiter
	listenAddrs  func() []multiaddr.Multiaddr
	policyEngine auth.PolicyEngine // optional; nil = policies disabled

	// sdnPeerCounter reports how many of the connected/known peers are actual
	// SDN nodes (as opposed to the raw libp2p/DHT swarm). Injected from the
	// node (epm.CountSDNPeers) so this package stays free of a node import.
	// nil = SDN peer state unavailable; the stats surface then reports 0.
	sdnPeerCounter func() SDNPeerCounts

	// statsCache bounds the two store reads behind /api/v1/stats so an
	// anonymous poll never queues behind the ingest writer. See boundedread.go.
	statsCache *boundedReader

	// uiCacheDir is where the read-surface caches persist their last-known-good
	// answers so a restart does not blank them (see boundedpersist.go and
	// status/snapshot_persist.go). "" = RAM-only. Set with SetUICacheDir before
	// StartDashboardSnapshots.
	uiCacheDir string

	// moduleSigner is the node's content-bound module signer, set by
	// registerModuleSigningRoutes when this node holds a publisher key. nil
	// means the signing endpoint was never mounted — see module_signing.go.
	moduleSigner *modulesign.Signer

	// dashboard owns the background snapshot lanes behind the dashboard data
	// plane. nil until StartDashboardSnapshots runs (a store-less handler, or
	// a test that never starts it); both stats surfaces degrade gracefully.
	dashboard *dashboardSnapshots

	// updateSigner is the node's content-bound update-manifest signer, set by
	// registerUpdateSigningRoutes when this node holds a publisher key. nil
	// means the endpoint was never mounted — see update_signing.go. It is a
	// separate signer from moduleSigner over the SAME key: one publisher key,
	// two disjoint statement domains (council Q7, internal/sigdomain).
	updateSigner *updatesign.Signer
}

// SDNPeerCounts mirrors epm.SDNPeerCounts for the /api/v1/stats peers block:
// Connected = SDN peers with a live connection now (the headline number),
// Known = every observed SDN peer including advertisement-discovered ones that
// are not currently connected. Both are strict subsets of the IPFS swarm count.
type SDNPeerCounts struct {
	Connected int `json:"connected"`
	Known     int `json:"known"`
}

// SetSDNPeerCounter attaches the SDN peer-state source. Pass nil to disable.
func (h *CoreAPIHandler) SetSDNPeerCounter(fn func() SDNPeerCounts) {
	h.sdnPeerCounter = fn
}

// NewCoreAPIHandler constructs a CoreAPIHandler from the individual dependencies.
// Any field may be nil — handlers degrade gracefully.
func NewCoreAPIHandler(
	peerID peer.ID,
	h2pHost host.Host,
	ps *pubsub.PubSub,
	publisher topicPublisher,
	store *storage.FlatSQLStore,
	validator *sds.Validator,
	cfg *config.AdminConfig,
	authHandler *auth.Handler,
	listenAddrs func() []multiaddr.Multiaddr,
) *CoreAPIHandler {
	return &CoreAPIHandler{
		peerID:      peerID,
		h2pHost:     h2pHost,
		pubsubSvc:   ps,
		publisher:   publisher,
		store:       store,
		validator:   validator,
		cfg:         cfg,
		authHandler: authHandler,
		rl:          newRateLimiter(),
		listenAddrs: listenAddrs,
		// Three fixed keys (summary, source progress, physical storage usage);
		// the ceiling is
		// nominal, this surface takes no parameters.
		statsCache: newBoundedReader(8),
	}
}

// SetUICacheDir points the read-surface caches at a directory that survives a
// restart (config.UICacheDir). Call it BEFORE StartDashboardSnapshots and
// before serving: it rebuilds the stats cache with its file loaded, so
// /api/v1/stats answers with the numbers this node last knew rather than
// nothing while the store hydrates. An empty dir leaves the handler RAM-only.
func (h *CoreAPIHandler) SetUICacheDir(dir string) {
	if h == nil {
		return
	}
	h.uiCacheDir = dir
	if dir == "" {
		return
	}
	h.statsCache = newBoundedReaderPersisted(8, filepath.Join(dir, statsCacheFileName), decodeStatsValue)
}

// writeCoreAPIError writes an error response using the code+message error envelope.
func writeCoreAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// requireAuth returns a middleware that enforces minTrust authentication if an
// authHandler is available, otherwise it falls back to the raw handler. This
// allows CoreAPIHandler to be registered even when auth is disabled.
func (h *CoreAPIHandler) requireAuth(minTrust peers.TrustLevel, next http.HandlerFunc) http.HandlerFunc {
	if h.authHandler == nil {
		return next
	}
	return h.authHandler.RequireAuth(minTrust, next)
}

// SetPolicyEngine attaches an ABAC policy engine.  When set, pubsub publish
// requests are evaluated after the trust-level gate.  Pass nil to disable.
func (h *CoreAPIHandler) SetPolicyEngine(engine auth.PolicyEngine) {
	h.policyEngine = engine
}

// RegisterRoutes registers all core API routes onto mux.
func (h *CoreAPIHandler) RegisterRoutes(mux *http.ServeMux) {
	h.RegisterRoutesWithFlowMounts(mux, nil)
}

// RegisterRoutesWithFlowMounts registers the core API routes, yielding the
// peer read surface to a mounted gateway flow when one claims it (gateway
// loop G.2: the peers-discovery flow REPLACES the native /api/v1/peers
// listing — bare-array/$EPM-stream response). flowClaimed reports whether a
// config flow mount owns the given mux path; nil means no flows are mounted.
//
// When the flow owns /api/v1/peers, the admin control plane stays native via
// method-scoped Go 1.22 mux patterns that are more specific than the flow's
// subtree mount: POST /api/v1/peers/connect and DELETE /api/v1/peers/{peerID}
// (disconnect). GET traffic flows to the wasm mount.
func (h *CoreAPIHandler) RegisterRoutesWithFlowMounts(mux *http.ServeMux, flowClaimed func(path string) bool) {
	// Public GET endpoints — no auth required.
	mux.HandleFunc("/api/v1/id", h.withRL(h.handleID))
	mux.HandleFunc("/api/v1/version", h.withRL(h.handleVersion))
	mux.HandleFunc("/api/v1/stats", h.withRL(h.handleStats))
	// Instant binary twin of /api/v1/stats: the pre-built $NDS frame, served
	// from RAM. See dashboard_stats.go.
	mux.HandleFunc(DashboardStatsPath, h.withRL(h.handleDashboardStats))

	// PubSub topic listing — public GET.
	mux.HandleFunc("/api/v1/pubsub/topics", h.withRL(h.handleTopics))
	mux.HandleFunc("/api/v1/standards", h.withRL(h.handleStandards))
	// Engine DDL per standard for browser-hosted engines — public GET.
	mux.HandleFunc("/api/v1/standards/", h.withRL(h.handleStandardSchemaText))

	// PubSub publish — requires standard auth when authHandler is present.
	mux.HandleFunc("/api/v1/pubsub/publish", h.withRL(h.requireAuth(peers.Standard, h.handlePubSubPublish)))

	// PubSub messages — public GET.
	mux.HandleFunc("/api/v1/pubsub/messages", h.withRL(h.handlePubSubMessages))

	// Content-bound module signing — POST /api/v1/admin/modules/sign, Admin
	// session required, mounted only when this node holds a publisher key.
	// Registered BEFORE the flow-mount branch below returns, so a node that
	// yields its peer read surface to a gateway flow still gets the signing
	// door. See module_signing.go for why this route lives here.
	h.registerModuleSigningRoutes(mux)

	// Update-manifest signing (POST, Admin) and the public update feed (GET).
	// Same reason as above for living here rather than in main.go's route
	// block. Both are registered before the flow-mount branch returns, so a
	// node that yields its peer read surface to a gateway flow still publishes
	// and still signs. See update_signing.go and update_feed.go.
	h.registerUpdateSigningRoutes(mux)
	h.registerUpdateFeedRoutes(mux)
	// The PUSH half of the update lane (owner ruling 2026-08-09). Registered
	// after the signing routes because it needs the signer they construct.
	h.registerUpdateSignalRoutes(mux)

	peersClaimedByFlow := flowClaimed != nil &&
		(flowClaimed("/api/v1/peers") || flowClaimed("/api/v1/peers/"))
	if peersClaimedByFlow {
		// The discovery flow serves the read surface; keep the admin
		// control plane native with method-scoped patterns (more specific
		// than the flow's "/api/v1/peers/" subtree, so no mux conflict).
		mux.HandleFunc("POST /api/v1/peers/connect", h.withRL(h.requireAuth(peers.Admin, h.handlePeerConnect)))
		mux.HandleFunc("DELETE /api/v1/peers/{peerID}", h.withRL(func(w http.ResponseWriter, r *http.Request) {
			h.requireAuth(peers.Admin, func(w http.ResponseWriter, r *http.Request) {
				h.deletePeer(w, r, r.PathValue("peerID"))
			})(w, r)
		}))
		return
	}

	// Legacy native peer surface (no discovery flow mounted).
	mux.HandleFunc("/api/v1/peers", h.withRL(h.handlePeers))
	mux.HandleFunc("/api/v1/peers/connect", h.withRL(h.requireAuth(peers.Admin, h.handlePeerConnect)))
	mux.HandleFunc("/api/v1/peers/", h.withRL(h.handlePeerByID))
}

// withRL wraps a handler with rate-limit checking.
func (h *CoreAPIHandler) withRL(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.rl.Allow(w, r) {
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Identity & Version
// ---------------------------------------------------------------------------

func (h *CoreAPIHandler) handleID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	addrs := []string{}
	if h.listenAddrs != nil {
		for _, ma := range h.listenAddrs() {
			addrs = append(addrs, ma.String())
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":           h.peerID.String(),
		"listen_addresses":  addrs,
		"agent_version":     versioninfo.AgentVersion,
		"suite_version":     versioninfo.SuiteVersion,
		"standards_version": versioninfo.SpaceDataStandardsVersion,
	})
}

func (h *CoreAPIHandler) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"agent_version":     versioninfo.AgentVersion,
		"suite_version":     versioninfo.SuiteVersion,
		"standards_version": versioninfo.SpaceDataStandardsVersion,
		// The Kubo fork this node is based on (kubo/version.go, read at
		// generation time) — the dashboard header renders it beside the
		// suite version (owner 2026-07-31).
		"kubo_version": versioninfo.KuboVersion,
	})
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func (h *CoreAPIHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	// IPFS peers: the raw libp2p/DHT swarm connection count.
	connectedPeers := 0
	if h.h2pHost != nil {
		connectedPeers = len(h.h2pHost.Network().Peers())
	}

	// SDN peers: the subset of the swarm that are actual Space Data Network
	// nodes (SDN protocol / SDN agent version / SDN advertisement evidence —
	// epm.CountSDNPeers, the same source as the dashboard's /api/peers/sdn).
	sdnCounts := SDNPeerCounts{}
	if h.sdnPeerCounter != nil {
		sdnCounts = h.sdnPeerCounter()
	}

	resp := map[string]interface{}{
		// connected_peers is the historical IPFS/libp2p swarm count; kept
		// verbatim for backward compatibility. New consumers read peers.*.
		"connected_peers": connectedPeers,
		"peers": map[string]interface{}{
			"ipfs":      connectedPeers,
			"sdn":       sdnCounts.Connected,
			"sdn_known": sdnCounts.Known,
		},
		"total_records": int64(0),
		"total_bytes":   int64(0),
		"schemas":       []interface{}{},
		"sources":       []interface{}{},
	}

	// The peers block above is host state and is always instantaneous. The
	// store-derived blocks below are NOT: they take the record store's read
	// lock, which a running ingest holds for minutes. They come from the
	// background stats lane (dashboard_stats.go) so this request runs no query
	// at all; only a node whose lane has never built pays the bounded read
	// inline, and even then it answers from last-known-good under a budget.
	//
	// stale/as_of are API-synthesized fields and stay lowercase (SDS record
	// keys keep their IDL capitalization; these are not record fields).
	stats := h.cachedStoreStats()
	if !stats.Built && h.store != nil {
		stats = h.readStoreStats()
	}

	if stats.Built {
		schemaList := make([]map[string]interface{}, 0, len(stats.Schemas))
		for _, sc := range stats.Schemas {
			schemaList = append(schemaList, map[string]interface{}{
				"schema":      sc.SchemaName,
				"count":       sc.Count,
				"total_bytes": sc.TotalBytes,
			})
		}
		resp["total_records"] = stats.TotalRecords
		resp["total_bytes"] = stats.TotalBytes
		resp["schemas"] = schemaList

		// Per-(schema, provider, source, batch) live pipeline progress. This
		// schema-neutral read-only aggregate reports counts, bytes, and arrival
		// timestamps without interpreting application records.
		sources := make([]map[string]interface{}, 0, len(stats.Sources))
		for _, p := range stats.Sources {
			row := map[string]interface{}{
				"schema":      p.SchemaName,
				"provider_id": p.ProviderID,
				"source_name": p.SourceName,
				"batch_id":    p.BatchID,
				"count":       p.Count,
				"total_bytes": p.TotalBytes,
			}
			if p.FirstSeenUnix > 0 {
				row["first_seen"] = time.Unix(p.FirstSeenUnix, 0).UTC().Format(time.RFC3339)
			}
			if p.LastSeenUnix > 0 {
				row["last_seen"] = time.Unix(p.LastSeenUnix, 0).UTC().Format(time.RFC3339)
			}
			if p.UpdatedAtUnix > 0 {
				row["updated_at"] = time.Unix(p.UpdatedAtUnix, 0).UTC().Format(time.RFC3339)
			}
			sources = append(sources, row)
		}
		resp["sources"] = sources
	}

	resp["stale"] = stats.Stale
	if !stats.AsOf.IsZero() {
		resp["as_of"] = stats.AsOf.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Peers
// ---------------------------------------------------------------------------

func (h *CoreAPIHandler) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if h.h2pHost == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"peers": []interface{}{}})
		return
	}

	connected := h.h2pHost.Network().Peers()
	list := make([]map[string]interface{}, 0, len(connected))
	for _, pid := range connected {
		addrs := h.h2pHost.Peerstore().Addrs(pid)
		addrStrs := make([]string, 0, len(addrs))
		for _, ma := range addrs {
			addrStrs = append(addrStrs, ma.String())
		}
		list = append(list, map[string]interface{}{
			"peer_id": pid.String(),
			"addrs":   addrStrs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"peers": list})
}

func (h *CoreAPIHandler) handlePeerByID(w http.ResponseWriter, r *http.Request) {
	peerIDStr := strings.TrimPrefix(r.URL.Path, "/api/v1/peers/")
	peerIDStr = strings.TrimSuffix(peerIDStr, "/")

	switch r.Method {
	case http.MethodGet:
		h.getPeer(w, peerIDStr)
	case http.MethodDelete:
		// Require admin auth for disconnecting.
		h.requireAuth(peers.Admin, func(w http.ResponseWriter, r *http.Request) {
			h.deletePeer(w, r, peerIDStr)
		})(w, r)
	default:
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *CoreAPIHandler) getPeer(w http.ResponseWriter, peerIDStr string) {
	if h.h2pHost == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "libp2p host not running")
		return
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_PEER_ID", "invalid peer id: "+err.Error())
		return
	}

	conns := h.h2pHost.Network().ConnsToPeer(pid)
	if len(conns) == 0 {
		writeCoreAPIError(w, http.StatusNotFound, "PEER_NOT_FOUND", "peer not connected")
		return
	}

	addrs := h.h2pHost.Peerstore().Addrs(pid)
	addrStrs := make([]string, 0, len(addrs))
	for _, ma := range addrs {
		addrStrs = append(addrStrs, ma.String())
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":          pid.String(),
		"addrs":            addrStrs,
		"connection_count": len(conns),
	})
}

func (h *CoreAPIHandler) deletePeer(w http.ResponseWriter, r *http.Request, peerIDStr string) {
	if h.h2pHost == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "libp2p host not running")
		return
	}

	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_PEER_ID", "invalid peer id: "+err.Error())
		return
	}

	if err := h.h2pHost.Network().ClosePeer(pid); err != nil {
		writeCoreAPIError(w, http.StatusInternalServerError, "DISCONNECT_FAILED", "failed to disconnect: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":      pid.String(),
		"disconnected": true,
	})
}

func (h *CoreAPIHandler) handlePeerConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if h.h2pHost == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "libp2p host not running")
		return
	}

	var req struct {
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if req.Addr == "" {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "addr is required")
		return
	}

	ma, err := multiaddr.NewMultiaddr(req.Addr)
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_MULTIADDR", "invalid multiaddr: "+err.Error())
		return
	}

	ai, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_MULTIADDR", "cannot extract peer info: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.h2pHost.Connect(ctx, *ai); err != nil {
		writeCoreAPIError(w, http.StatusBadGateway, "CONNECT_FAILED", "failed to connect: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"peer_id":   ai.ID.String(),
		"connected": true,
	})
}

// ---------------------------------------------------------------------------
// PubSub
// ---------------------------------------------------------------------------

func (h *CoreAPIHandler) handleTopics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	topics := []string{}
	if h.pubsubSvc != nil {
		topics = h.pubsubSvc.GetTopics()
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"topics": topics})
}

func (h *CoreAPIHandler) handlePubSubPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if h.publisher == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "pubsub publishing not available")
		return
	}

	var req struct {
		Schema string `json:"schema"`
		Data   string `json:"data"` // base64-encoded FlatBuffer bytes
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 10*1024*1024)).Decode(&req); err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if req.Schema == "" {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "schema is required")
		return
	}
	if req.Data == "" {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "data is required")
		return
	}

	if err := sds.ValidateSchemaName(req.Schema); err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_SCHEMA", "invalid schema name: "+err.Error())
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		// Try URL-safe base64.
		data, err = base64.URLEncoding.DecodeString(req.Data)
		if err != nil {
			writeCoreAPIError(w, http.StatusBadRequest, "INVALID_DATA", "data must be base64-encoded")
			return
		}
	}
	if len(data) == 0 {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_DATA", "data is empty after decoding")
		return
	}

	// Validate FlatBuffer data against the schema.
	if h.validator != nil {
		if err := h.validator.Validate(r.Context(), req.Schema, data); err != nil {
			writeCoreAPIError(w, http.StatusBadRequest, "VALIDATION_FAILED", "validation failed: "+err.Error())
			return
		}
	}

	// ABAC policy check — runs after trust gate (defence in depth).
	if h.policyEngine != nil {
		session := auth.SessionFromContext(r.Context())
		if session == nil {
			writeCoreAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
			return
		}
		sub := abac.Subject{
			XPub:       session.XPub,
			TrustLevel: int(session.TrustLevel),
			Attrs:      map[string]string{},
		}
		res := abac.Resource{Schema: req.Schema}
		decision := h.policyEngine.Evaluate(sub, abac.ActionPublish, res)
		if !decision.Allowed {
			writeCoreAPIError(w, http.StatusForbidden, "POLICY_DENIED", "access denied by policy: "+decision.Reason)
			return
		}
	}

	topicName, err := publishSchemaPubSubMessage(r.Context(), h.publisher, req.Schema, data)
	if err != nil {
		writeCoreAPIError(w, http.StatusInternalServerError, "PUBLISH_FAILED", "failed to publish: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":       req.Schema,
		"topic":        topicName,
		"bytes":        len(data),
		"published_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *CoreAPIHandler) handlePubSubMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	schema := strings.TrimSpace(r.URL.Query().Get("schema"))
	limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))

	limit := 50
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	if schema == "" {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "schema query parameter is required")
		return
	}
	if err := sds.ValidateSchemaName(schema); err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_SCHEMA", "invalid schema name: "+err.Error())
		return
	}

	if h.store == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"schema": schema, "records": []interface{}{}})
		return
	}

	raw, err := h.store.QueryAll(schema, limit)
	if err != nil {
		writeCoreAPIError(w, http.StatusInternalServerError, "QUERY_FAILED", "query failed: "+err.Error())
		return
	}

	// Return base64-encoded records.
	encoded := make([]string, 0, len(raw))
	for _, b := range raw {
		encoded = append(encoded, base64.StdEncoding.EncodeToString(b))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"schema":  schema,
		"count":   len(encoded),
		"records": encoded,
	})
}
