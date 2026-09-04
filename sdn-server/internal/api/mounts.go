package api

// Admin mount seam for the FlatBuffer-only dashboard lanes (fbcs program).
//
// main.go builds ONE AdminMountDeps from what the daemon already holds (store,
// config, publication key, ledger, channel handler, flow runtime hooks) and
// calls MountRegisteredAdmin once. Lane files register themselves from init()
// through RegisterAdminMount, so adding a lane never edits main.go again.
//
// Nil func fields mean "unavailable on this node": a handler answers with a
// plain-language $QRP/$DSS error frame, never a panic.

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// FlowServiceInfo is one timer-served flow as the node runs it.
type FlowServiceInfo struct {
	// ProgramID is the flow program id — the sourcemetrics app_id the
	// storage connector stamps on every batch the flow ingests.
	ProgramID string
	// Running reports whether the plugin manager has the flow scheduled.
	Running bool
	// TimerIntervalMs is the shortest timer trigger interval, 0 when the
	// flow declares none.
	TimerIntervalMs int64
	// RetrievalInterval is flows.services[].retrieval_interval when set,
	// else 0 (the node default debounce applies).
	RetrievalInterval time.Duration
}

// AdminMountDeps is everything a registered admin mount may need.
type AdminMountDeps struct {
	Store  *storage.FlatSQLStore
	Config *config.Config

	// NodePeerID is this node's libp2p peer id; NodeEPMCID the CID of its
	// signed $EPM (empty when the directory has none yet).
	NodePeerID string
	NodeEPMCID string
	// SigningKey is the dataset-publication key main.go derives (nil when
	// signing is unavailable).
	SigningKey ed25519.PrivateKey
	// IPFSAPIURL is admin.ipfs_api_url; empty means no Kubo on this node.
	IPFSAPIURL string

	SourceMetrics *sourcemetrics.Store
	Channels      *ChannelHandler
	Publications  DatasetPublicationService

	// RequireAdmin wraps a handler behind the admin wall (the same gate the
	// core API uses). Nil means the wall is down (admin.require_auth false).
	RequireAdmin func(http.HandlerFunc) http.HandlerFunc

	// RunFlowNow fires a flow service's timer once, honouring the retrieval
	// gate: skipped=true (with the gate's reason) when the debounce refused
	// it; err when the flow is unknown or failed to start.
	RunFlowNow func(ctx context.Context, programID string) (skipped bool, reason string, err error)
	// FlowServices lists the timer-served flows the node runs.
	FlowServices func() []FlowServiceInfo

	// SyncLane runs one bounded trusted-peer catch-up pass for a lane and
	// returns how many shards were materialized.
	SyncLane func(ctx context.Context, schema, providerID, sourceName string) (int, error)
	// PinCID / UnpinCID pin or unpin one CID on the node's Kubo.
	PinCID   func(ctx context.Context, cid string) error
	UnpinCID func(ctx context.Context, cid string) error

	TrustEvaluator *trust.Evaluator
}

// AdminMountFunc mounts one lane's routes.
type AdminMountFunc func(mux *http.ServeMux, deps *AdminMountDeps)

var (
	adminMountsMu sync.Mutex
	adminMounts   = map[string]AdminMountFunc{}
)

// RegisterAdminMount registers a lane under name. Registering the same name
// twice replaces the earlier function, so a lane is mounted exactly once.
func RegisterAdminMount(name string, m AdminMountFunc) {
	if name == "" || m == nil {
		return
	}
	adminMountsMu.Lock()
	adminMounts[name] = m
	adminMountsMu.Unlock()
}

// MountRegisteredAdmin calls every registered mount once, sorted by name.
func MountRegisteredAdmin(mux *http.ServeMux, deps *AdminMountDeps) {
	if mux == nil {
		return
	}
	if deps == nil {
		deps = &AdminMountDeps{}
	}
	adminMountsMu.Lock()
	names := make([]string, 0, len(adminMounts))
	for name := range adminMounts {
		names = append(names, name)
	}
	mounts := make([]AdminMountFunc, 0, len(names))
	sort.Strings(names)
	for _, name := range names {
		mounts = append(mounts, adminMounts[name])
	}
	adminMountsMu.Unlock()
	for _, mount := range mounts {
		mount(mux, deps)
	}
}

// RegisteredAdminMountNames lists the registered lanes, sorted.
func RegisteredAdminMountNames() []string {
	adminMountsMu.Lock()
	defer adminMountsMu.Unlock()
	names := make([]string, 0, len(adminMounts))
	for name := range adminMounts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// adminGate wraps h with deps.RequireAdmin when the wall is up.
func (d *AdminMountDeps) adminGate(h http.HandlerFunc) http.HandlerFunc {
	if d == nil || d.RequireAdmin == nil {
		return h
	}
	return d.RequireAdmin(h)
}
