package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// CredentialsHandler exposes the node's named-credential keystore to the
// operator over the AUTHENTICATED admin surface.
//
// # LANES ARE OPERATOR-DEFINED (owner 2026-08-04)
//
// A "lane" is a named credential slot. Three are WELL-KNOWN — spacetrack,
// edc_cpf, myintelsat (credstore.AllIDs) — because the node ships a verifier
// and documented semantics for them. Every other lane id is the operator's to
// invent: PUT a credential under any id satisfying credstore.ValidateLaneID and
// the lane exists, is sealed with the same node-key envelope, is enumerated
// here, and is reachable by a module the operator approves for
// "secrets:<that id>". There is nothing special about the well-known three
// except that this node knows how to PROBE them.
//
// Consequently the listing is well-known ∪ stored, and each row states whether
// a verifier exists — because "stored, never verified" is the permanent and
// honest state of a lane the node cannot probe, and an operator must be able to
// tell that apart from "verification failed".
//
// # WRITE-ONLY BY CONSTRUCTION
//
// There is deliberately no route, and no code path below, that returns a stored
// secret to any caller. The only shapes ever written to a response are
// credstore.Status values (configured / masked username / timestamps). The
// keystore's Reveal() is never called from this file — see the
// TestNoHandlerRevealsPlaintext guard in credentials_test.go.
//
// # FAIL CLOSED
//
// nginx on the public host has no /api/ location block: a catch-all forwards
// every /api/** path to this daemon, so these routes are reachable from the
// internet and the daemon's own auth is the only thing in front of them.
// Therefore gate() below refuses to serve at all unless authentication is
// actually enabled and available. It does not matter whether the top-level
// admin wall is active, nor whether an operator has widened
// gateway.anonymous.allow — the gate is inside the handler, so it always runs.
type CredentialsHandler struct {
	store       *credstore.Store
	authHandler *auth.Handler
	requireAuth bool

	// verifiers probe a credential against its upstream provider, keyed by
	// lane id. VERIFIERS ARE OPTIONAL: a lane with no registered verifier —
	// which is every operator-defined lane — is stored and reported as "saved
	// but unverified", forever. Absence of a verifier never blocks a write and
	// is never papered over as success.
	verifiers map[string]Verifier
}

// Verifier performs a single lightweight authentication probe against a
// provider. Implementations MUST NOT pull data and MUST NOT retry.
type Verifier interface {
	Verify(ctx context.Context, username, secret string) error
}

// NewCredentialsHandler builds the handler. A nil store or nil authHandler
// leaves the routes refusing to serve rather than falling open.
func NewCredentialsHandler(store *credstore.Store, authHandler *auth.Handler, requireAuth bool, verifiers map[string]Verifier) *CredentialsHandler {
	return &CredentialsHandler{
		store:       store,
		authHandler: authHandler,
		requireAuth: requireAuth,
		verifiers:   verifiers,
	}
}

// RegisterRoutes mounts the credential routes.
//
// Unlike most admin routes, these are registered unconditionally and gated
// inside gate(). Registering them only when auth is on would make them 404 on
// an auth-disabled node, which is fail-closed but silent; an explicit 503 tells
// the operator WHY the credential surface is unavailable.
func (h *CredentialsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/admin/credentials", h.gate(h.handleCollection))
	mux.HandleFunc("/api/v1/admin/credentials/", h.gate(h.handleByID))
}

// gate enforces admin authentication and fails closed.
//
// Refusal (503) rather than pass-through when auth is disabled or unavailable:
// a credential-entry surface that anyone on the internet can POST to is worse
// than no credential-entry surface at all.
func (h *CredentialsHandler) gate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.requireAuth || h.authHandler == nil {
			writeError(w, http.StatusServiceUnavailable,
				"credential management is unavailable because node authentication is disabled; enable admin.require_auth")
			return
		}
		if h.store == nil {
			writeError(w, http.StatusServiceUnavailable, "credential store is unavailable")
			return
		}
		h.authHandler.RequireAuth(peers.Admin, next)(w, r)
	}
}

// laneStatus is one row of the admin listing: the secret-free credstore.Status
// (id, configured, masked username, timestamps) plus the two facts an operator
// needs in order to read that status honestly.
//
// The Status is EMBEDDED, so its fields stay at the top level of the JSON
// object exactly as before — "id", "configured", "username_masked",
// "updated_at", "verified_at" — and this stays additive for existing callers.
//
// An absent "verified_at" means the credential was stored and never
// successfully probed. Combined with "has_verifier": false that is the normal,
// permanent state of an operator-defined lane, not a failure — the node has no
// way to verify a service it knows nothing about, and it does not pretend
// otherwise.
type laneStatus struct {
	credstore.Status
	// WellKnown marks the lanes the node ships knowing about (credstore.AllIDs)
	// as opposed to lanes the operator defined.
	WellKnown bool `json:"well_known"`
	// HasVerifier reports whether this node can probe the credential at all
	// (a Verifier is registered for the lane). false means a PUT with
	// "verify": true will honestly report "unverified", never "verified".
	HasVerifier bool `json:"has_verifier"`
}

// handleCollection: GET /api/v1/admin/credentials — status of every lane.
//
// The listing is the well-known set UNION whatever the operator has actually
// stored (credstore.Store.ListLanes), so operator-defined lanes appear here
// alongside spacetrack/edc_cpf/myintelsat, and a well-known lane that has not
// been filled in yet appears as configured=false rather than vanishing.
func (h *CredentialsHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	list, err := h.store.ListLanes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential store unreadable")
		return
	}
	lanes := make([]laneStatus, 0, len(list))
	for _, st := range list {
		lanes = append(lanes, h.decorate(st))
	}
	// Every element embeds a credstore.Status: no secret material.
	writeJSON(w, http.StatusOK, map[string]any{"credentials": lanes})
}

// decorate annotates a secret-free status with the lane's provenance
// (well-known vs operator-defined) and whether a verifier exists for it.
func (h *CredentialsHandler) decorate(st credstore.Status) laneStatus {
	_, hasVerifier := h.verifiers[st.ID]
	return laneStatus{
		Status:      st,
		WellKnown:   credstore.IsWellKnown(st.ID),
		HasVerifier: hasVerifier,
	}
}

// credentialRequest is the PUT body. The secret is accepted, never returned.
type credentialRequest struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
	// Verify requests a single authentication probe before reporting success.
	Verify bool `json:"verify"`
}

// credentialWriteResponse reports the outcome of a write. It carries a Status
// (no secret) plus the verification result.
type credentialWriteResponse struct {
	Status laneStatus `json:"status"`
	// Verification is "verified", "unverified", or "failed".
	//
	// "unverified" is the CORRECT and permanent answer for a lane with no
	// verifier — every operator-defined lane. It is never upgraded to
	// "verified" on the strength of a successful store.
	Verification string `json:"verification"`
	// VerificationError is a short, provider-supplied reason. It never contains
	// the submitted credential.
	VerificationError string `json:"verification_error,omitempty"`
}

// handleByID: GET (status) / PUT (save|replace) / DELETE (clear) on one lane.
//
// The lane id is ARBITRARY (owner 2026-08-04): any service the operator needs a
// credential for, not just the well-known three. It must satisfy
// credstore.ValidateLaneID, which is enforced on every method — an id that
// cannot be stored can never exist, so rejecting it uniformly keeps GET/DELETE
// from implying otherwise.
func (h *CredentialsHandler) handleByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/credentials/")
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "credential lane id required")
		return
	}
	if err := credstore.ValidateLaneID(id); err != nil {
		// ValidateLaneID's message states the RULE and never echoes the
		// submitted id, which arrives from the URL path.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch r.Method {
	case http.MethodGet:
		// STATUS ONLY. There is no route that returns the secret.
		status, err := h.store.Status(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "credential store unreadable")
			return
		}
		writeJSON(w, http.StatusOK, h.decorate(status))

	case http.MethodPut, http.MethodPost:
		h.handlePut(w, r, id)

	case http.MethodDelete:
		if err := h.store.Clear(id); err != nil {
			writeError(w, http.StatusInternalServerError, "could not clear credential")
			return
		}
		writeJSON(w, http.StatusOK, h.decorate(credstore.Status{ID: id, Configured: false}))

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *CredentialsHandler) handlePut(w http.ResponseWriter, r *http.Request, id string) {
	var req credentialRequest
	// The body carries a password. Decode errors are reported WITHOUT echoing
	// the body or the decoder's error text, which can quote input.
	if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if req.Secret == "" {
		writeError(w, http.StatusBadRequest, "secret is required")
		return
	}

	if err := h.store.Put(id, req.Username, req.Secret); err != nil {
		// store.Put's errors are validation-only and never quote the secret.
		// The lane id was already validated above, so a rejection here means a
		// rule the caller can act on — report it as such rather than as a 500.
		if errors.Is(err, credstore.ErrInvalidLaneID) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "could not store credential")
		return
	}

	resp := credentialWriteResponse{Verification: "unverified"}

	// Optional single authentication probe. Fetch policy: one lightweight login
	// check, no data pull, no retry.
	//
	// VERIFIERS ARE OPTIONAL. A lane the node has no verifier for — every
	// operator-defined lane, and any well-known lane whose verifier is not
	// wired on this node — is stored and reported as "unverified". The node
	// does NOT fabricate a verification it did not perform, and it does not
	// refuse the write: the operator knows the credential is good, the node
	// simply cannot confirm it.
	if req.Verify {
		verifier, ok := h.verifiers[id]
		if !ok {
			resp.Verification = "unverified"
			resp.VerificationError = "this node has no verifier for this lane; the credential is stored but unverified"
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()

			if err := verifier.Verify(ctx, req.Username, req.Secret); err != nil {
				// The credential stays stored (the operator may be offline, or
				// the provider may be down) but is explicitly NOT verified.
				resp.Verification = "failed"
				resp.VerificationError = err.Error()
			} else {
				if err := h.store.MarkVerified(id, time.Now()); err != nil {
					resp.Verification = "unverified"
				} else {
					resp.Verification = "verified"
				}
			}
		}
	}

	status, err := h.store.Status(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential stored but status unreadable")
		return
	}
	resp.Status = h.decorate(status)

	// resp contains a credstore.Status and short strings only — no secret.
	writeJSON(w, http.StatusOK, resp)
}

// ErrVerifierUnavailable is returned by a Verifier that cannot run a probe.
var ErrVerifierUnavailable = errors.New("verification unavailable")
