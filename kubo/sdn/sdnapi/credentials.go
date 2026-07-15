package sdnapi

// Credential-entry admin API — the operator surface for entering the
// third-party data-source credentials (Space-Track today; EDC/DGFI CPF and
// MyIntelsat next) that ephemeris modules fetch through the capability-gated
// "secrets" hostcall. It is deliberately a SEPARATE handler from the read-only
// NewHandler surface in sdnapi.go: the read-only API is public-safe, this one
// mutates node secrets and is admin/loopback-only + fail closed.
//
// # WRITE-ONLY — no route ever returns plaintext
//
// The store (sdn/credstore) is write-only from the outside: PUT accepts a
// username+secret and returns only the masked status; GET lists masked
// statuses; DELETE clears. The credstore.Status shape carries no secret, and
// credstore.Secret redacts itself under encoding/json, so even an accidental
// struct embed cannot serialize the plaintext. This is asserted by
// credentials_test.go (serialize the whole response, grep for the canary).
//
// # FAIL CLOSED
//
// Every route is guarded by Authorized. A nil Authorized (auth misconfigured)
// refuses every request — the credential surface is never accidentally open.
// The kubo plugin wires Authorized to a loopback-only check, so these routes
// serve only same-host operators even if the listener is (mis)configured to a
// public address.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ipfs/kubo/sdn/credstore"
)

// CredentialStore is the write-only credential keystore the admin routes drive.
// *credstore.Store satisfies it. Only status-returning and mutating methods are
// exposed here — Reveal (the only plaintext accessor) is deliberately NOT part
// of this interface, so no HTTP handler can ever reach it.
type CredentialStore interface {
	// List returns the masked status of every configured credential.
	List() ([]credstore.Status, error)
	// Status returns the masked status of one lane (Configured=false if absent).
	Status(id string) (credstore.Status, error)
	// Put stores or replaces the credential under id (write-only).
	Put(id, username, secret string) error
	// Clear removes the credential under id (a no-op if absent).
	Clear(id string) error
}

// CredentialsDeps are the live sources the credential admin routes read.
type CredentialsDeps struct {
	// Store returns the live credential keystore, or nil when the runtime is
	// disabled, not started, or the store could not be opened (fail closed: a
	// nil store makes every route report 503 rather than pretend success).
	Store func() CredentialStore
	// Authorized reports whether a request may access the credential admin
	// routes. REQUIRED: a nil Authorized fails closed (every request refused),
	// so a misconfigured deployment cannot accidentally expose credential
	// management. The plugin wires this to a loopback-only check.
	Authorized func(r *http.Request) bool
}

// credentialsHandler serves the credential admin routes.
type credentialsHandler struct {
	deps CredentialsDeps
}

// NewCredentialsHandler builds the credential-entry admin API:
//
//	GET    /sdn/v1/admin/credentials       — list masked statuses
//	PUT    /sdn/v1/admin/credentials/{id}  — save/replace (write-only)
//	DELETE /sdn/v1/admin/credentials/{id}  — clear
//
// The returned handler is intended to be mounted on the plugin's loopback
// listener. Every route is authorization-guarded and fail closed.
func NewCredentialsHandler(deps CredentialsDeps) http.Handler {
	h := &credentialsHandler{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sdn/v1/admin/credentials", h.guard(h.list))
	mux.HandleFunc("PUT /sdn/v1/admin/credentials/{id}", h.guard(h.put))
	mux.HandleFunc("DELETE /sdn/v1/admin/credentials/{id}", h.guard(h.clear))
	return mux
}

// guard enforces authorization ahead of every credential route. It fails closed:
// a nil Authorized (auth misconfigured) refuses unconditionally; otherwise the
// request must satisfy Authorized. Only then does the wrapped handler run.
func (h *credentialsHandler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.deps.Authorized == nil {
			// Auth is not configured — refuse rather than default-open.
			writeErr(w, http.StatusForbidden, "credential administration is not enabled (authorization not configured)")
			return
		}
		if !h.deps.Authorized(r) {
			writeErr(w, http.StatusForbidden, "credential administration is restricted to the local operator")
			return
		}
		next(w, r)
	}
}

func (h *credentialsHandler) store() CredentialStore {
	if h.deps.Store == nil {
		return nil
	}
	return h.deps.Store()
}

// credentialPutRequest is the PUT body: the operator-entered credential. Both
// fields are required. This is the ONLY direction plaintext travels — inbound.
type credentialPutRequest struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// list returns the masked status of every configured credential. It NEVER
// returns a secret (credstore.Status carries none).
func (h *credentialsHandler) list(w http.ResponseWriter, _ *http.Request) {
	st := h.store()
	if st == nil {
		writeErr(w, http.StatusServiceUnavailable, "credential store unavailable")
		return
	}
	list, err := st.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "credential store is unreadable")
		return
	}
	if list == nil {
		list = []credstore.Status{}
	}
	writeJSON(w, http.StatusOK, list)
}

// put saves or replaces a credential (write-only). The response is the masked
// status of the just-saved lane — never the secret that was submitted.
func (h *credentialsHandler) put(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "credential id is required")
		return
	}
	st := h.store()
	if st == nil {
		writeErr(w, http.StatusServiceUnavailable, "credential store unavailable")
		return
	}
	var req credentialPutRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed request: body must be a JSON object with username and secret")
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Secret == "" {
		writeErr(w, http.StatusBadRequest, "username and secret are required")
		return
	}
	if err := st.Put(id, req.Username, req.Secret); err != nil {
		// credstore.Put errors are validation messages that never echo the
		// secret, but stay conservative and do not surface the raw error.
		writeErr(w, http.StatusBadRequest, "credential rejected: "+sanitizePutError(err, req.Secret))
		return
	}
	// Respond with the masked status only — the write-only contract.
	status, err := st.Status(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "credential saved but status is unreadable")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// clear removes a credential. Clearing an absent lane is a success (idempotent).
func (h *credentialsHandler) clear(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "credential id is required")
		return
	}
	st := h.store()
	if st == nil {
		writeErr(w, http.StatusServiceUnavailable, "credential store unavailable")
		return
	}
	if err := st.Clear(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not clear credential")
		return
	}
	status, err := st.Status(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "credential cleared but status is unreadable")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// sanitizePutError returns the store's error text unless it somehow contains the
// submitted secret, in which case it is replaced with a generic message. The
// store never embeds the secret in its errors; this is defense in depth so a
// future change cannot turn a validation error into a leak channel.
func sanitizePutError(err error, secret string) string {
	if err == nil {
		return "invalid credential"
	}
	msg := err.Error()
	if secret != "" && strings.Contains(msg, secret) {
		return "invalid credential"
	}
	return msg
}

// ensure the concrete store satisfies the interface at compile time — the kubo
// plugin can therefore pass a *credstore.Store directly with no adapter.
var _ CredentialStore = (*credstore.Store)(nil)
