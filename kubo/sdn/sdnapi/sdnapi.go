// Package sdnapi is the read-only HTTP surface over a live SDN node's state —
// Phase 7 of the kubo rebase. It exposes exactly what the 5-screen SDN UI
// needs (node identity, peers, the stored (source, type) catalog, bounded
// record listings, channels, and installed apps) as small, bare JSON.
//
// # Zero core patch
//
// This package is pure: NewHandler(Deps) returns an http.Handler and nothing
// more. The kubo-side mount (kubo/plugin/plugins/sdnapi) starts its OWN
// dedicated http.Server on a loopback address and serves this handler — it does
// NOT register a corehttp.ServeOption or touch kubo's API/gateway mux, so the
// SDN HTTP surface adds no kubo core patch, the same rule that held for the
// sdnflag and sdnruntime plugins.
//
// # Live, not snapshot
//
// Every source in Deps is a function, resolved per request. The API listener
// can therefore start before the runtime services or the peer set exist and
// still report the node's CURRENT state as those appear — nil returns are
// rendered as empty results, never a crash.
//
// # Read-only
//
// This phase adds no mutating endpoint. Every route is GET; anything else is
// 405. The store is queried through its cheap, read-only accessors (Catalog,
// ReadBySourceType) — no engine mutation, no write path.
package sdnapi

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/ipfs/kubo/sdn/appmanifest"
	"github.com/ipfs/kubo/sdn/channels"
	"github.com/ipfs/kubo/sdn/sdnstore"
)

// DefaultDataLimit is the number of records /sdn/v1/data returns when no limit
// is given. MaxDataLimit caps an explicit limit so a response stays bounded.
const (
	DefaultDataLimit = 100
	MaxDataLimit     = 1000
)

// NodeInfo is the node identity summary reported by /sdn/v1/node.
type NodeInfo struct {
	PeerID        string
	FlagNamespace string
	PubSubEnabled bool
}

// Deps are the live sources the API reads. Each is a function so the handler
// reflects the node's CURRENT state at request time (services and peers may
// appear after the listener starts) and so tests can supply in-memory fakes.
// A nil function is treated as "nothing yet" (empty result), never a panic.
type Deps struct {
	// Node returns the node identity summary. Required in practice; a nil Node
	// yields a zero-valued /sdn/v1/node object.
	Node func() NodeInfo
	// Store returns the live record store, or nil when the runtime is disabled
	// or not started (storage endpoints then return empty).
	Store func() *sdnstore.Store
	// Channels returns the live channel fan-out, or nil in storage-only mode
	// (channels are then reported as known-but-inactive).
	Channels func() *channels.Channels
	// IPFSPeers returns the node's currently connected swarm peer IDs.
	IPFSPeers func() []string
	// SDNPeers returns the peers discovered via the SDN flag namespace.
	SDNPeers func() []string
	// Blockstore returns the node's content-addressed block store, or nil when
	// it is not available yet. It backs GET /sdn/v1/module?hash=<sha256hex>,
	// which resolves an $APP APPModuleRef.CONTENT_HASH to the exact module WASM
	// bytes so the PAGE-side harness can load a module by content hash over the
	// SAME blockstore the node serves modules from (verify-by-hash). A nil
	// Blockstore (or nil return) makes the module endpoint report 503.
	Blockstore func() appmanifest.ModuleBlockstore
}

// NewHandler builds the read-only SDN HTTP API over deps. Routes are method-
// scoped (GET only); the returned handler is safe to serve directly.
func NewHandler(deps Deps) http.Handler {
	h := &handler{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sdn/v1/node", h.node)
	mux.HandleFunc("GET /sdn/v1/peers", h.peers)
	mux.HandleFunc("GET /sdn/v1/data/sources", h.dataSources)
	mux.HandleFunc("GET /sdn/v1/data", h.data)
	mux.HandleFunc("GET /sdn/v1/channels", h.channels)
	mux.HandleFunc("GET /sdn/v1/apps", h.apps)
	mux.HandleFunc("GET /sdn/v1/module", h.module)
	return mux
}

type handler struct {
	deps Deps
}

func (h *handler) store() *sdnstore.Store {
	if h.deps.Store == nil {
		return nil
	}
	return h.deps.Store()
}

func (h *handler) blockstore() appmanifest.ModuleBlockstore {
	if h.deps.Blockstore == nil {
		return nil
	}
	return h.deps.Blockstore()
}

// ---------------------------------------------------------------------------
// Response shapes (bare JSON — the 5-screen UI reads these directly).
// ---------------------------------------------------------------------------

type nodeResp struct {
	PeerID           string      `json:"peer_id"`
	SDNFlagNamespace string      `json:"sdn_flag_namespace"`
	Storage          storageResp `json:"storage"`
	PubSubEnabled    bool        `json:"pubsub_enabled"`
}

type storageResp struct {
	// Sources is the number of distinct (source, type) pairs in the catalog —
	// cheap (catalog keyspace only).
	Sources int `json:"sources"`
	// Records is a total record count, included only when it is cheap to know.
	// A full record count requires scanning the index, which is NOT cheap, so
	// it is omitted this phase (per the "records: N-if-cheap" contract).
	Records *int `json:"records,omitempty"`
}

type peersResp struct {
	IPFS []string `json:"ipfs"`
	SDN  []string `json:"sdn"`
}

type recordResp struct {
	CID    string `json:"cid"`
	Size   int    `json:"size"`
	FileID string `json:"file_id,omitempty"`
}

type dataResp struct {
	Source   string       `json:"source"`
	Type     string       `json:"type"`
	Total    int          `json:"total"`
	Returned int          `json:"returned"`
	Limit    int          `json:"limit"`
	Records  []recordResp `json:"records"`
}

type channelResp struct {
	Source   string `json:"source"`
	Standard string `json:"standard"`
	Topic    string `json:"topic"`
	Active   bool   `json:"active"`
}

type appResp struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
	CID     string `json:"cid"`
	Size    int    `json:"size"`
	Modules int    `json:"modules"`
	Pages   int    `json:"pages"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func (h *handler) node(w http.ResponseWriter, r *http.Request) {
	var info NodeInfo
	if h.deps.Node != nil {
		info = h.deps.Node()
	}
	resp := nodeResp{
		PeerID:           info.PeerID,
		SDNFlagNamespace: info.FlagNamespace,
		PubSubEnabled:    info.PubSubEnabled,
	}
	if st := h.store(); st != nil {
		cat, err := st.Catalog(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp.Storage.Sources = len(cat)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) peers(w http.ResponseWriter, _ *http.Request) {
	resp := peersResp{IPFS: []string{}, SDN: []string{}}
	if h.deps.IPFSPeers != nil {
		if p := h.deps.IPFSPeers(); p != nil {
			resp.IPFS = p
		}
	}
	if h.deps.SDNPeers != nil {
		if p := h.deps.SDNPeers(); p != nil {
			resp.SDN = p
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) dataSources(w http.ResponseWriter, r *http.Request) {
	out := []sdnstore.CatalogEntry{}
	if st := h.store(); st != nil {
		cat, err := st.Catalog(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if cat != nil {
			out = cat
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) data(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	sdsType := q.Get("type")
	if source == "" || sdsType == "" {
		writeErr(w, http.StatusBadRequest, "source and type query parameters are required")
		return
	}
	limit := parseLimit(q.Get("limit"))

	st := h.store()
	resp := dataResp{Source: source, Type: sdsType, Limit: limit, Records: []recordResp{}}
	if st == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	recs, err := st.ReadBySourceType(r.Context(), source, sdsType)
	if err != nil {
		// A malformed 3-letter type is a client error; anything else is server.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	resp.Total = len(recs)
	if len(recs) > limit {
		recs = recs[:limit]
	}
	for _, fb := range recs {
		c, err := channels.CIDOf(fb)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp.Records = append(resp.Records, recordResp{
			CID:    c.String(),
			Size:   len(fb),
			FileID: fileIdentifier(fb),
		})
	}
	resp.Returned = len(resp.Records)
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) channels(w http.ResponseWriter, r *http.Request) {
	out := []channelResp{}
	st := h.store()
	if st == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cat, err := st.Catalog(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	active := map[string]bool{}
	var ch *channels.Channels
	if h.deps.Channels != nil {
		ch = h.deps.Channels()
	}
	if ch != nil {
		for _, t := range ch.Topics() {
			active[t] = true
		}
	}

	for _, ce := range cat {
		topic, err := channels.WireTopic(ce.Source, ce.Type)
		if err != nil {
			// Non-conforming type is not a channel; skip rather than fail.
			continue
		}
		out = append(out, channelResp{
			Source:   ce.Source,
			Standard: ce.Type,
			Topic:    topic,
			Active:   active[topic],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) apps(w http.ResponseWriter, r *http.Request) {
	out := []appResp{}
	st := h.store()
	if st == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cat, err := st.Catalog(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, ce := range cat {
		if ce.Type != "APP" {
			continue
		}
		recs, err := st.ReadBySourceType(r.Context(), ce.Source, "APP")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, fb := range recs {
			c, err := channels.CIDOf(fb)
			cidStr := ""
			if err == nil {
				cidStr = c.String()
			}
			entry := appResp{Source: ce.Source, CID: cidStr, Size: len(fb)}
			// Best-effort decode: an $APP record summarizes to id/name/version.
			// A record that does not parse still surfaces (source, cid, size).
			if m, err := appmanifest.FromAPP(fb); err == nil && m != nil {
				entry.ID = m.ID
				entry.Name = m.Name
				entry.Version = m.Version
				entry.Modules = len(m.Modules)
				entry.Pages = len(m.Pages)
			}
			out = append(out, entry)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// module serves the raw WASM bytes of a module addressed by its
// APPModuleRef.CONTENT_HASH (64 lowercase hex chars of the SHA-256 of the
// portable module artifact). It is the PAGE-side counterpart to the node's own
// module resolution: the page harness fetches these exact bytes and loads the
// module in-browser under the SAME plugin_invoke_stream ABI the node runs it
// under, so "modules load into the JS harness the same as SDN nodes".
//
// The bytes are resolved through appmanifest.ResolveModuleByContentHash, which
// re-hashes the fetched block and rejects any mismatch — the response is
// verify-by-hash at the source, and the page re-verifies the digest again after
// download (defense in depth). A well-formed hash that is not stored is a plain
// 404; a malformed hash is a 400; no block store yet is a 503.
func (h *handler) module(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hash")))
	if hash == "" {
		writeErr(w, http.StatusBadRequest, "hash query parameter is required")
		return
	}
	// Validate the CONTENT_HASH shape up front so a malformed hash is a client
	// error (400), distinct from a well-formed-but-absent module (404).
	if len(hash) != 64 {
		writeErr(w, http.StatusBadRequest, "hash must be 64 hex characters (sha-256)")
		return
	}
	if _, err := hex.DecodeString(hash); err != nil {
		writeErr(w, http.StatusBadRequest, "hash must be valid hex")
		return
	}

	bs := h.blockstore()
	if bs == nil {
		writeErr(w, http.StatusServiceUnavailable, "module store unavailable")
		return
	}

	wasm, err := appmanifest.ResolveModuleByContentHash(r.Context(), bs, hash)
	if err != nil {
		// A validated hash that does not resolve means the block is not present
		// (or was substituted and failed the verify-by-hash check): 404, not 500.
		writeErr(w, http.StatusNotFound, "module not found for content hash")
		return
	}

	w.Header().Set("Content-Type", "application/wasm")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wasm)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func parseLimit(s string) int {
	if s == "" {
		return DefaultDataLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return DefaultDataLimit
	}
	if n > MaxDataLimit {
		return MaxDataLimit
	}
	return n
}

// fileIdentifier returns the 4-byte FlatBuffer file identifier carried at
// bytes [4:8] of a non-size-prefixed FlatBuffer (e.g. "$OMM"), when those bytes
// are printable ASCII. It is a cheap, schema-free "what kind of record is this"
// hint — NOT a decode of the record body. Returns "" when unavailable.
func fileIdentifier(fb []byte) string {
	if len(fb) < 8 {
		return ""
	}
	id := fb[4:8]
	for _, b := range id {
		if b < 0x20 || b > 0x7e {
			return ""
		}
	}
	return string(id)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
