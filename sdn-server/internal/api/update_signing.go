package api

// Content-bound update-manifest signing endpoint —
// POST /api/v1/admin/updates/sign-manifest.
//
// SEAL COUNCIL 2026-07-30/31: graph/tasks/sdn-signed-updater.md (Hephaestus,
// ship-time producer) with Hermes's in-node CONCUR on this being a SIBLING
// route rather than a mode of the module endpoint.
//
// WHY IT IS NOT /api/v1/admin/modules/sign WITH A FLAG. That endpoint refuses
// anything that is not a real wasm module, and refuses a JSON body outright —
// both correct for a module artifact and both fatal for a manifest, which IS
// JSON. Adding a "kind" switch would mean relaxing the module endpoint's
// structural check for one branch, i.e. weakening the exact gate that makes it
// not a blind signing oracle. A second door with its own lock is strictly safer
// than one door with two keys.
//
// THE GATES, in the order a request meets them:
//  1. the top-level admin wall — /api/v1/admin/ is Admin-classified by
//     isAdminOnlyAPIPath (cmd/spacedatanetwork/main.go). Deliberately NOT
//     loopback-self-gated: this is operator authority over what the fleet will
//     install, not machine-local control.
//  2. requireAdminStrict (module_signing.go) — REFUSES when no auth handler is
//     configured rather than falling through to the raw handler.
//  3. no-digest-in-any-position, below. The JSON-body refusal the module
//     endpoint uses cannot apply here, so the digest positions are checked
//     explicitly and the structural check in updatesign carries the rest: a
//     digest is not a well-formed manifest.
//  4. updatesign's validation, canonicalization and audit gate.

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/updatesign"
)

// UpdateManifestSigningRoute is the single path this surface occupies.
const UpdateManifestSigningRoute = "/api/v1/admin/updates/sign-manifest"

// registerUpdateSigningRoutes mounts the update-manifest signing endpoint when
// this node can act as the publisher of record, and reports what it decided so
// an operator never has to distinguish a fail-closed 404 from a network fault.
func (h *CoreAPIHandler) registerUpdateSigningRoutes(mux *http.ServeMux) {
	provider, ok := h.publisher.(nodeSigningKeyProvider)
	if !ok || provider == nil {
		log.Warnf("Update manifest signing endpoint NOT registered at %s: this node handle exposes no signing key. Signed update releases are unavailable on this daemon.", UpdateManifestSigningRoute)
		return
	}
	rawKey := provider.SigningKey()
	if len(rawKey) == 0 {
		log.Warnf("Update manifest signing endpoint NOT registered at %s: the node has no signing key loaded. Signed update releases are unavailable on this daemon.", UpdateManifestSigningRoute)
		return
	}

	auditPath := updatesign.DefaultAuditPath()
	if auditPath == "" {
		log.Errorf("Update manifest signing endpoint NOT registered at %s: no audit log path could be resolved (set SDN_UPDATE_SIGNING_AUDIT_LOG). An unauditable signature over the node publisher key is not permitted.", UpdateManifestSigningRoute)
		return
	}

	signer, err := updatesign.NewSigner(rawKey, updatesign.NewAuditLog(auditPath))
	if err != nil {
		log.Errorf("Update manifest signing endpoint NOT registered at %s: %v", UpdateManifestSigningRoute, err)
		return
	}
	h.updateSigner = signer

	mux.HandleFunc(UpdateManifestSigningRoute, h.withRL(h.requireAdminStrict(h.handleUpdateManifestSign)))

	// The key_id and the SPKI public key are logged on purpose: they are the
	// two values an operator must put in a bundle's trust/update-roots.json for
	// this node's releases to verify anywhere, and hunting for them later has
	// historically meant re-deriving them by hand.
	log.Infof(
		"Update manifest signing endpoint registered at %s (POST, Admin session required, key_id %s, audit %s). Trust root for this signer: {%q: %q}",
		UpdateManifestSigningRoute, signer.KeyID(), auditPath, signer.KeyID(), signer.PublicKeyB64(),
	)
}

// updateManifestSignResponse is the endpoint's success body. Keys are lowercase
// snake_case: node-synthesized API fields, not SDS record fields.
//
// The response returns a SIGNATURE, never a document. The caller already holds
// the exact bytes that were signed; handing back a re-serialized manifest would
// invite it to publish a document that differs from the one the signature
// covers.
type updateManifestSignResponse struct {
	ContentHash     string `json:"content_hash"`
	StatementDomain string `json:"statement_domain"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	PublicKeyB64    string `json:"public_key"`
	PublicKeyHex    string `json:"public_key_hex"`
	Signature       string `json:"signature"`
	CanonicalBytes  int    `json:"canonical_bytes"`
	Resigned        bool   `json:"resigned,omitempty"`
	SignedAt        string `json:"signed_at"`

	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	Channel  string `json:"channel"`
	Target   string `json:"target"`
}

func (h *CoreAPIHandler) handleUpdateManifestSign(w http.ResponseWriter, r *http.Request) {
	if h.updateSigner == nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "SIGNER_UNAVAILABLE", "update manifest signing is not available on this node")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "update manifest signing accepts POST only")
		return
	}
	if code, msg, bad := digestBearingUpdateRequest(r); bad {
		writeCoreAPIError(w, http.StatusBadRequest, code, msg)
		return
	}

	// One byte over the cap is enough to detect an oversized body without
	// buffering the whole thing.
	body, err := io.ReadAll(io.LimitReader(r.Body, updatesign.MaxManifestBytes+1))
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "BODY_READ_FAILED", "could not read the request body")
		return
	}
	if len(body) > updatesign.MaxManifestBytes {
		writeCoreAPIError(w, http.StatusRequestEntityTooLarge, updatesign.CodePayloadTooLarge, "update manifest exceeds the signing size limit")
		return
	}

	result, err := h.updateSigner.Sign(updatesign.Request{
		Manifest:  body,
		Requester: updatesign.FingerprintPrincipal(sessionPrincipal(r)),
		RemoteIP:  requestRemoteIP(r),
	})
	if err != nil {
		var refusal *updatesign.Refusal
		if errors.As(err, &refusal) {
			writeCoreAPIError(w, http.StatusBadRequest, refusal.Code, refusal.Message)
			return
		}
		log.Errorf("update manifest signing failed: %v", err)
		writeCoreAPIError(w, http.StatusInternalServerError, "SIGNING_FAILED", "the node could not issue a signature")
		return
	}

	writeJSON(w, http.StatusOK, updateManifestSignResponse{
		ContentHash:     result.ContentHash,
		StatementDomain: result.StatementDomain,
		Algorithm:       result.Algorithm,
		KeyID:           h.updateSigner.KeyID(),
		PublicKeyB64:    result.PublicKeyB64,
		PublicKeyHex:    result.PublicKeyHex,
		Signature:       result.SignatureB64,
		CanonicalBytes:  result.CanonicalBytes,
		Resigned:        result.Resigned,
		SignedAt:        result.SignedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdateID:        result.UpdateID,
		Version:         result.Version,
		Sequence:        result.Sequence,
		Channel:         result.Channel,
		Target:          result.Target,
	})
}

// digestBearingUpdateRequest refuses every position a caller might use to hand
// the node a precomputed hash.
//
// It reuses the module endpoint's query/header vocabulary (digestQueryParams,
// digestHeaders in module_signing.go) so the two doors cannot drift into
// accepting different sets. What it CANNOT reuse is that endpoint's wholesale
// refusal of JSON bodies — a manifest is JSON — so the compensating control is
// updatesign.validate: the body must be a well-formed
// org.spacedatanetwork.update.v1 document naming the reserved statement domain,
// which a digest can never be.
func digestBearingUpdateRequest(r *http.Request) (code, message string, bad bool) {
	query := r.URL.Query()
	for _, key := range digestQueryParams {
		if query.Has(key) {
			return updatesign.CodeDigestNotAccepted,
				"query parameter " + key + " is not accepted: the node canonicalizes and hashes the submitted manifest itself and never signs a caller-supplied digest",
				true
		}
	}
	for _, header := range digestHeaders {
		if r.Header.Get(header) != "" {
			return updatesign.CodeDigestNotAccepted,
				"header " + header + " is not accepted: the node canonicalizes and hashes the submitted manifest itself and never signs a caller-supplied digest",
				true
		}
	}
	// A wasm body is refused for the mirror-image reason the module endpoint
	// refuses JSON: whichever artifact arrives at the wrong door would be
	// signed under the wrong domain if the door were lenient.
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if mediaType == "application/wasm" {
		return updatesign.CodeNotAManifest,
			"a wasm body is not accepted here: module artifacts are signed at " + ModuleSigningRoute + " under a different statement domain",
			true
	}
	return "", "", false
}
