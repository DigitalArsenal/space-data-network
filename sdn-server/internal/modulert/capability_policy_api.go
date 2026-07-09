package modulert

// Loop B1 — operator approval surface for the module capability policy
// (capability_policy.go). Mirrors the small JSON REST handler pattern used
// elsewhere in this codebase for operator-controlled state (see
// internal/peers.APIHandler): a self-contained http.Handler the daemon's
// top-level mux mounts behind its existing admin-session auth, exposing
// approve/list/revoke over the persisted CapabilityPolicyStore.
//
// This handler is NOT wired into the running daemon's mux from this change
// — the daemon's route table lives in cmd/spacedatanetwork/main.go, which
// loop B1 is not permitted to edit (owned by another in-flight task). See
// the loop B1 report for the exact snippet needed there.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// CapabilityPolicyAPI provides HTTP endpoints for operators to inspect and
// manage the module capability policy.
type CapabilityPolicyAPI struct {
	policy *CapabilityPolicyStore
	mux    *http.ServeMux
}

// NewCapabilityPolicyAPI creates the admin API handler over policy. policy
// must not be nil.
func NewCapabilityPolicyAPI(policy *CapabilityPolicyStore) *CapabilityPolicyAPI {
	h := &CapabilityPolicyAPI{
		policy: policy,
		mux:    http.NewServeMux(),
	}
	h.setupRoutes()
	return h
}

// ServeHTTP implements http.Handler.
func (h *CapabilityPolicyAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *CapabilityPolicyAPI) setupRoutes() {
	h.mux.HandleFunc("/api/modules/capabilities", h.handleList)
	h.mux.HandleFunc("/api/modules/capabilities/approve", h.handleApprove)
	h.mux.HandleFunc("/api/modules/capabilities/revoke", h.handleRevoke)
	h.mux.HandleFunc("/api/modules/capabilities/tiers", h.handleTiers)
}

// handleList handles GET /api/modules/capabilities[?module_hash=<hex>] —
// every recorded approval, or only those for one module.
func (h *CapabilityPolicyAPI) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	moduleHash := strings.TrimSpace(r.URL.Query().Get("module_hash"))
	if moduleHash != "" {
		writeCapabilityJSON(w, h.policy.ForModule(moduleHash))
		return
	}
	writeCapabilityJSON(w, h.policy.List())
}

// approveRequest is the request body for POST .../approve.
type approveRequest struct {
	ModuleHash      string `json:"module_hash"`
	Capability      string `json:"capability"`
	PluginID        string `json:"plugin_id,omitempty"`
	SignerPubKeyHex string `json:"signer_pubkey_hex,omitempty"`
	ApprovedBy      string `json:"approved_by,omitempty"`
	Note            string `json:"note,omitempty"`
}

// handleApprove handles POST /api/modules/capabilities/approve — records an
// operator approval for one (module_hash, capability) pair. Persisted
// immediately; survives restart.
func (h *CapabilityPolicyAPI) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req approveRequest
	if err := readCapabilityJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	approved, err := h.policy.Approve(CapabilityApproval{
		ModuleHash:      req.ModuleHash,
		Capability:      req.Capability,
		PluginID:        req.PluginID,
		SignerPubKeyHex: req.SignerPubKeyHex,
		ApprovedBy:      req.ApprovedBy,
		Note:            req.Note,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeCapabilityJSON(w, approved)
}

// revokeRequest is the request body for POST .../revoke.
type revokeRequest struct {
	ModuleHash string `json:"module_hash"`
	Capability string `json:"capability"`
}

// handleRevoke handles POST /api/modules/capabilities/revoke — removes a
// recorded approval, persisting the change.
func (h *CapabilityPolicyAPI) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req revokeRequest
	if err := readCapabilityJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.policy.Revoke(req.ModuleHash, req.Capability); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeCapabilityJSON(w, map[string]bool{"ok": true})
}

// tiersResponse describes the fixed capability tiering (capability_policy.go)
// for an operator UI to render — which capabilities need an approval entry
// at all, and which default-allow.
type tiersResponse struct {
	Sensitive []string `json:"sensitive"`
	Benign    []string `json:"benign"`
}

// handleTiers handles GET /api/modules/capabilities/tiers.
func (h *CapabilityPolicyAPI) handleTiers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := tiersResponse{}
	for cap := range sensitiveCapabilities {
		resp.Sensitive = append(resp.Sensitive, cap)
	}
	for cap := range benignCapabilities {
		resp.Benign = append(resp.Benign, cap)
	}
	writeCapabilityJSON(w, resp)
}

func writeCapabilityJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warnf("capability policy API: encode response: %v", err)
	}
}

func readCapabilityJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}
