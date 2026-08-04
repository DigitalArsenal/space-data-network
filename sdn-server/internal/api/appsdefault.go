package api

// The DEFAULT-$APP surface.
//
//	GET /api/v1/apps/default        — which $APP each runtime class opens
//	GET /api/v1/apps/records/<id>   — that app's $APP record bytes
//
// Owner ruling 2026-08-04 (verbatim): "there needs to be a default $APP for
// the SDN node software (server or browser), it's the Dashboard for the server
// and the orbital-console for the browser, with both loaded, and just like in
// the design there's a link to each one in the other."
//
// A browser client pointed at a node has no session and no identity yet — it
// is asking "what do I open?" before it can be anybody. So this surface is
// ANONYMOUS by construction, and safe to be: it reports app identity (ID,
// NAME, VERSION), where each app is served, and page content hashes. Every one
// of those is already public — the dashboard's bytes are served at "/" to
// anyone who asks, and a declared browser app is a URL, not a secret. No
// operator identity, no local paths, no session state.
//
// DISTINCT FROM /api/apps. That feed reports what the node RUNS (timer-served
// flow bundles and their retrieval metrics). This one reports what the node
// OFFERS to open — $APP records. Same word, different question.

import (
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/apps"
)

// AppsRecordPrefix is the origin-relative route the $APP record bytes are
// served under. Exported so the registry can build RecordURLs that match the
// route this package actually mounts — one constant, no drift.
const AppsRecordPrefix = "/api/v1/apps/records/"

// AppsDefaultPath is the anonymous defaults document.
const AppsDefaultPath = "/api/v1/apps/default"

// DefaultAppsHandler serves the default-$APP contract from an apps.Registry.
type DefaultAppsHandler struct {
	registry *apps.Registry
	peerID   string
	rl       *rateLimiter
}

// NewDefaultAppsHandler constructs the handler. A nil registry is legal: the
// surface then answers with no defaults rather than 404ing, so a client can
// always tell "this node has no default app" apart from "this node is too old
// to have the route".
func NewDefaultAppsHandler(registry *apps.Registry) *DefaultAppsHandler {
	return &DefaultAppsHandler{registry: registry, rl: newRateLimiter()}
}

// WithNodePeerID stamps the node's libp2p identity into the document so a
// client can bind the app it loaded to the node it loaded it from. Optional.
func (h *DefaultAppsHandler) WithNodePeerID(peerID string) *DefaultAppsHandler {
	h.peerID = strings.TrimSpace(peerID)
	return h
}

// RegisterRoutes mounts both routes.
func (h *DefaultAppsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(AppsDefaultPath, func(w http.ResponseWriter, r *http.Request) {
		if !h.rl.Allow(w, r) {
			return
		}
		h.handleDefault(w, r)
	})
	mux.HandleFunc(AppsRecordPrefix, func(w http.ResponseWriter, r *http.Request) {
		if !h.rl.Allow(w, r) {
			return
		}
		h.handleRecord(w, r)
	})
}

// defaultAppsResponse is the defaults document.
//
// Two capitalizations on purpose (JSON-capitalization law): keys inside an app
// entry that come from the $APP FlatBuffer keep the IDL's spelling (ID, NAME,
// VERSION, UI, CONTENT_SHA256 …); everything this API synthesizes — the
// envelope, runtime classes, URLs, cross-links — is lowercase.
type defaultAppsResponse struct {
	GeneratedAt string `json:"generated_at"`
	NodePeerID  string `json:"node_peer_id,omitempty"`
	// RuntimeClasses is the fixed vocabulary, so a client can enumerate what
	// this node could have a default for without hard-coding the list.
	RuntimeClasses []string `json:"runtime_classes"`
	// Defaults is keyed by runtime class. A class with no default is ABSENT
	// rather than null — the node is saying nothing about it, which is not
	// the same as saying it has none configured.
	Defaults map[string]apps.Entry `json:"defaults"`
}

func (h *DefaultAppsHandler) handleDefault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	classes := make([]string, 0, len(apps.RuntimeClasses))
	for _, c := range apps.RuntimeClasses {
		classes = append(classes, string(c))
	}
	out := defaultAppsResponse{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		NodePeerID:     h.peerID,
		RuntimeClasses: classes,
		Defaults:       map[string]apps.Entry{},
	}
	if h.registry != nil {
		for class, entry := range h.registry.Defaults() {
			out.Defaults[string(class)] = entry
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (h *DefaultAppsHandler) handleRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, AppsRecordPrefix)
	// One flat segment. An app ID is a stable name, never a path.
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if h.registry == nil {
		http.NotFound(w, r)
		return
	}
	record, ok := h.registry.Record(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", apps.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The record is immutable for the life of the process: it is built once
	// from artifact bytes baked into the binary. A rebuilt node serves a new
	// record under a new build, so revalidation costs nothing and a stale
	// cached record can never outlive its app.
	w.Header().Set("Cache-Control", "public, no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(record)
	}
}
