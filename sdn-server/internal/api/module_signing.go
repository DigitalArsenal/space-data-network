package api

// Content-bound module signing endpoint — POST /api/v1/admin/modules/sign.
//
// SEAL COUNCIL RULING 2026-07-30, JOINT CONCUR (Hermes in-node mechanics +
// Hephaestus ship-time): graph/tasks/sdn-module-signing-endpoint.md.
//
// The build box builds LOCALLY with signing off, authenticates to the node as
// an admin principal, POSTs the ARTIFACT BYTES, and gets back a detached
// Ed25519 signature made by the node's own bonded signing key over a
// domain-separated statement. The key never leaves the host; the build never
// lands on it. See package modulesign for the signing mechanics and package
// sigdomain for the domain-separation contract.
//
// WHY THE ROUTE IS REGISTERED HERE and not in cmd/spacedatanetwork/main.go
// alongside its siblings: main.go's route block was under an active claim by
// another agent's task when this landed, and contending a claimed write scope
// is a graph-protocol violation, not a merge inconvenience. RegisterRoutes is
// already the api package's own registration seam — main.go calls it — so the
// route lands with no edit to a file this task does not own. The signing key
// arrives the same way: by OPTIONAL-INTERFACE assertion on the node handle the
// package is already given, which also keeps internal/api free of an
// internal/node import (that import would be a cycle).
//
// THE GATES, in the order a request meets them:
//  1. the top-level admin wall — /api/v1/admin/ is Admin-classified by
//     isAdminOnlyAPIPath (cmd/spacedatanetwork/main.go:3396). Deliberately NOT
//     added to isLoopbackSelfGatedAdminPath: this door is not machine-local
//     control, it is operator authority, and it stays behind the session wall.
//  2. this file's own strict Admin gate, which — unlike CoreAPIHandler.
//     requireAuth (coreapi.go:127-130) — REFUSES when no auth handler is
//     configured instead of falling through to the raw handler. That
//     fallthrough is correct for a pubsub publish; on a signing oracle over the
//     bonded publisher key it would be an open door on a require_auth:false
//     daemon.
//  3. no-digest-in-any-position (below).
//  4. modulesign's structural checks: real wasm module, trailer stripped by the
//     node, hash computed by the node.

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/modulesign"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// ModuleSigningRoute is the single path this surface occupies.
const ModuleSigningRoute = "/api/v1/admin/modules/sign"

// nodeSigningKeyProvider is the optional interface a node handle satisfies when
// it can sign as the publisher of record. *node.Node implements it
// (internal/node/node.go:4607). Asserting for it rather than importing the node
// package keeps the dependency direction api <- node, which is the direction
// that compiles.
type nodeSigningKeyProvider interface {
	SigningKey() []byte
}

// digestQueryParams and digestHeaders are every position a caller might try to
// hand the node a precomputed hash. A request carrying ANY of them is refused
// outright — the node will not accept a caller-supplied digest even as an
// expected-value cross-check, because a surface that reads a caller's hash in
// one mode is one refactor away from signing it.
var (
	digestQueryParams = []string{"content_hash", "contenthash", "hash", "digest", "sha256", "sha_256", "signed_hash", "signedhashhex"}
	digestHeaders     = []string{"X-Sdn-Content-Hash", "X-Content-Hash", "X-Content-Sha256", "X-Sdn-Digest", "Digest", "Want-Digest", "X-Signed-Hash"}
)

// registerModuleSigningRoutes mounts the signing endpoint when this node can
// act as a publisher, and reports what it decided so an operator never has to
// distinguish a fail-closed 404 from a network fault (Hephaestus concurrence
// condition 1, 2026-07-30).
func (h *CoreAPIHandler) registerModuleSigningRoutes(mux *http.ServeMux) {
	provider, ok := h.publisher.(nodeSigningKeyProvider)
	if !ok || provider == nil {
		log.Warnf("Module signing endpoint NOT registered at %s: this node handle exposes no signing key. Module re-signing is unavailable on this daemon.", ModuleSigningRoute)
		return
	}
	rawKey := provider.SigningKey()
	if len(rawKey) == 0 {
		log.Warnf("Module signing endpoint NOT registered at %s: the node has no signing key loaded. Module re-signing is unavailable on this daemon.", ModuleSigningRoute)
		return
	}

	auditPath := modulesign.DefaultAuditPath()
	if auditPath == "" {
		log.Errorf("Module signing endpoint NOT registered at %s: no audit log path could be resolved (set SDN_MODULE_SIGNING_AUDIT_LOG). An unauditable signature over the node publisher key is not permitted.", ModuleSigningRoute)
		return
	}

	signer, err := modulesign.NewSigner(rawKey, h.peerID.String(), modulesign.NewAuditLog(auditPath))
	if err != nil {
		log.Errorf("Module signing endpoint NOT registered at %s: %v", ModuleSigningRoute, err)
		return
	}
	h.moduleSigner = signer

	mux.HandleFunc(ModuleSigningRoute, h.withRL(h.requireAdminStrict(h.handleModuleSign)))

	pub := signer.PublicKeyHex()
	log.Infof(
		"Module signing endpoint registered at %s (POST, Admin session required, signer %s…, audit %s). The node hashes submitted artifact bytes itself and signs a domain-separated statement; no caller-supplied digest is ever accepted.",
		ModuleSigningRoute, pub[:12], auditPath,
	)
}

// requireAdminStrict is requireAuth WITHOUT the no-auth-handler fallthrough.
// A missing auth handler means this node cannot establish who is asking, and
// the only safe answer on this surface is "no one".
func (h *CoreAPIHandler) requireAdminStrict(next http.HandlerFunc) http.HandlerFunc {
	if h.authHandler == nil {
		return func(w http.ResponseWriter, r *http.Request) {
			writeCoreAPIError(w, http.StatusServiceUnavailable, "AUTH_NOT_CONFIGURED",
				"module signing requires an authenticated admin session, and this daemon has no authentication configured")
		}
	}
	return h.authHandler.RequireAuth(peers.Admin, next)
}

// moduleSignResponse is the endpoint's success body.
//
// Top-level keys are lowercase snake_case: they are node-synthesized API
// fields, not SDS record fields, so the IDL-capitalization law does not reach
// them. The nested signature_entry is the ONE exception and is camelCase on
// purpose — it is the module SDK's own on-wire JSON object, reproduced verbatim
// so the build box can hand it straight to the SDK's publication-trailer writer
// without transcribing field names (space-data-module-sdk/src/bundle/
// signing.js:362-369).
type moduleSignResponse struct {
	ContentHash     string                    `json:"content_hash"`
	StatementDomain string                    `json:"statement_domain"`
	Algorithm       string                    `json:"algorithm"`
	PublicKeyHex    string                    `json:"public_key_hex"`
	SignatureHex    string                    `json:"signature_hex"`
	PortableBytes   int                       `json:"portable_bytes"`
	TrailerStripped bool                      `json:"trailer_stripped"`
	SignedAt        string                    `json:"signed_at"`
	SignatureEntry  modulesign.SignatureEntry `json:"signature_entry"`
}

func (h *CoreAPIHandler) handleModuleSign(w http.ResponseWriter, r *http.Request) {
	if h.moduleSigner == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SIGNER_UNAVAILABLE", "module signing is not available on this node")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "module signing accepts POST only")
		return
	}
	if code, msg, bad := digestBearingRequest(r); bad {
		writeCoreAPIError(w, http.StatusBadRequest, code, msg)
		return
	}

	// One byte over the cap is enough to detect an oversized body without
	// buffering the whole thing.
	body, err := io.ReadAll(io.LimitReader(r.Body, modulesign.MaxArtifactBytes+1))
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "BODY_READ_FAILED", "could not read the request body")
		return
	}
	if len(body) > modulesign.MaxArtifactBytes {
		writeCoreAPIError(w, http.StatusRequestEntityTooLarge, modulesign.CodePayloadTooLarge, "module artifact exceeds the signing size limit")
		return
	}

	result, err := h.moduleSigner.Sign(modulesign.Request{
		Artifact:  body,
		Requester: modulesign.FingerprintPrincipal(sessionPrincipal(r)),
		RemoteIP:  requestRemoteIP(r),
	})
	if err != nil {
		var refusal *modulesign.Refusal
		if errors.As(err, &refusal) {
			// A refusal is always the caller's fault and never leaks node
			// state: Code/Message are fixed vocabulary from modulesign.
			writeCoreAPIError(w, http.StatusBadRequest, refusal.Code, refusal.Message)
			return
		}
		log.Errorf("module signing failed: %v", err)
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNING_FAILED", "the node could not issue a signature")
		return
	}

	writeJSON(w, http.StatusOK, moduleSignResponse{
		ContentHash:     result.ContentHash,
		StatementDomain: result.StatementDomain,
		Algorithm:       result.Algorithm,
		PublicKeyHex:    result.PublicKeyHex,
		SignatureHex:    result.SignatureHex,
		PortableBytes:   result.PortableBytes,
		TrailerStripped: result.TrailerStripped,
		SignedAt:        result.SignedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		SignatureEntry:  result.Entry,
	})
}

// digestBearingRequest reports whether the request tries to hand the node a
// precomputed hash in ANY position — query string, header, or a JSON body
// envelope. All three are refused before the body is even read.
//
// A JSON content type is refused wholesale rather than parsed: the only reason
// to wrap artifact bytes in JSON is to put fields beside them, and the field
// this endpoint must never grow is a digest. Raw bytes have nowhere to hide one.
func digestBearingRequest(r *http.Request) (code, message string, bad bool) {
	query := r.URL.Query()
	for _, key := range digestQueryParams {
		if query.Has(key) {
			return modulesign.CodeDigestNotAccepted,
				"query parameter " + key + " is not accepted: the node hashes the submitted bytes itself and never signs a caller-supplied digest",
				true
		}
	}
	for _, header := range digestHeaders {
		if r.Header.Get(header) != "" {
			return modulesign.CodeDigestNotAccepted,
				"header " + header + " is not accepted: the node hashes the submitted bytes itself and never signs a caller-supplied digest",
				true
		}
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if strings.HasSuffix(mediaType, "/json") || strings.HasSuffix(mediaType, "+json") {
		return modulesign.CodeDigestNotAccepted,
			"a JSON request body is not accepted: POST the raw module artifact bytes (application/wasm or application/octet-stream) so there is no field in which a digest could be supplied",
			true
	}
	return "", "", false
}

// sessionPrincipal returns the calling session's xpub, or "" when unavailable.
// The raw value never leaves this function un-fingerprinted.
func sessionPrincipal(r *http.Request) string {
	if session := auth.SessionFromContext(r.Context()); session != nil {
		return session.XPub
	}
	return ""
}

// requestRemoteIP is the peer address without the ephemeral port.
func requestRemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
