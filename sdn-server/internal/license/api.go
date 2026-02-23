package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// APIHandler exposes minimal HTTP APIs for token verification and entitlement management.
type APIHandler struct {
	service  *Service
	verifier *TokenVerifier
}

// NewAPIHandler creates a new license API handler.
func NewAPIHandler(service *Service) *APIHandler {
	if service == nil {
		return nil
	}
	return &APIHandler{
		service:  service,
		verifier: service.Verifier(),
	}
}

// RegisterRoutes mounts HTTP routes.
func (h *APIHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("/api/v1/license/verify", h.handleVerifyToken)
	mux.HandleFunc("/api/v1/license/entitlements", h.handleEntitlements)
	mux.HandleFunc("/api/v1/plugins/manifest", h.handlePluginManifest)
	mux.HandleFunc("/api/v1/plugins/", h.handlePluginRoute)
}

func (h *APIHandler) handleVerifyToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authHeader := r.Header.Get("Authorization")
	expectedPeerID := strings.TrimSpace(r.Header.Get("X-SDN-Peer-ID"))
	required := r.URL.Query()["scope"]

	claims, err := h.verifier.VerifyAuthorizationHeader(authHeader, expectedPeerID, required)
	if err != nil {
		writeLicenseJSON(w, http.StatusUnauthorized, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "token_invalid",
			Message: err.Error(),
		})
		return
	}
	writeLicenseJSON(w, http.StatusOK, claims)
}

func (h *APIHandler) handleEntitlements(w http.ResponseWriter, r *http.Request) {
	adminToken := strings.TrimSpace(os.Getenv("SDN_LICENSE_ADMIN_TOKEN"))
	if adminToken == "" {
		writeLicenseJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "admin_token_missing",
			Message: "SDN_LICENSE_ADMIN_TOKEN is not configured",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.Header.Get("X-License-Admin-Token"))), []byte(adminToken)) != 1 {
		writeLicenseJSON(w, http.StatusUnauthorized, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "unauthorized",
			Message: "invalid admin token",
		})
		return
	}

	switch r.Method {
	case http.MethodGet:
		xpub := strings.TrimSpace(r.URL.Query().Get("xpub"))
		if xpub == "" {
			writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "missing xpub query parameter"})
			return
		}
		ent, err := h.service.GetEntitlement(xpub)
		if err != nil {
			writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{Type: msgTypeErrorResponse, Code: "server_error", Message: err.Error()})
			return
		}
		if ent == nil {
			writeLicenseJSON(w, http.StatusNotFound, ErrorResponse{Type: msgTypeErrorResponse, Code: "not_found", Message: "entitlement not found"})
			return
		}
		writeLicenseJSON(w, http.StatusOK, ent)
	case http.MethodPost, http.MethodPut:
		var ent Entitlement
		if err := json.NewDecoder(r.Body).Decode(&ent); err != nil {
			writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_json", Message: "invalid entitlement payload"})
			return
		}
		if strings.TrimSpace(ent.XPub) == "" {
			writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "xpub is required"})
			return
		}
		if ent.Status != "" {
			switch ent.Status {
			case entitlementStatusActive, entitlementStatusCancelled, entitlementStatusPastDue, entitlementStatusSuspended:
			default:
				writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "invalid entitlement status"})
				return
			}
		}
		// Validate plan field to prevent arbitrary values.
		if p := strings.TrimSpace(ent.Plan); p != "" {
			switch p {
			case "free", "starter", "pro", "enterprise":
			default:
				writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "invalid plan value"})
				return
			}
		}
		if err := h.service.UpsertEntitlement(&ent); err != nil {
			writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{Type: msgTypeErrorResponse, Code: "server_error", Message: err.Error()})
			return
		}
		updated, err := h.service.GetEntitlement(ent.XPub)
		if err != nil || updated == nil {
			writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{Type: msgTypeErrorResponse, Code: "server_error", Message: "failed to reload entitlement"})
			return
		}
		writeLicenseJSON(w, http.StatusOK, updated)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handlePluginManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reg := h.service.PluginRegistry()
	descriptors := make([]PluginDescriptor, 0)
	if reg != nil {
		descriptors = reg.ListPublic()
	}
	w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=300, stale-while-revalidate=600")
	writeLicenseJSON(w, http.StatusOK, map[string]interface{}{
		"plugins": descriptors,
		"count":   len(descriptors),
	})
}

func (h *APIHandler) handlePluginRoute(w http.ResponseWriter, r *http.Request) {
	pluginID, action, ok := parsePluginRoute(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "bundle":
		h.handlePluginBundle(w, r, pluginID)
	case "key-envelope":
		h.handlePluginKeyEnvelope(w, r, pluginID)
	default:
		http.NotFound(w, r)
	}
}

func (h *APIHandler) handlePluginBundle(w http.ResponseWriter, r *http.Request, pluginID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reg := h.service.PluginRegistry()
	if reg == nil {
		writeLicenseJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "plugins_unavailable",
			Message: "plugin registry is not configured",
		})
		return
	}
	data, asset, err := reg.ReadEncryptedBundle(pluginID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeLicenseJSON(w, http.StatusNotFound, ErrorResponse{
				Type:    msgTypeErrorResponse,
				Code:    "not_found",
				Message: "plugin not found",
			})
			return
		}
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "server_error",
			Message: err.Error(),
		})
		return
	}

	etag := `"` + asset.BundleSHA256 + `"`
	w.Header().Set("ETag", etag)
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Cache-Control", asset.CacheControl)
	w.Header().Set("X-SDN-Plugin-ID", asset.ID)
	w.Header().Set("X-SDN-Plugin-Version", asset.Version)
	w.Header().Set("X-SDN-Plugin-SHA256", asset.BundleSHA256)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

type pluginKeyEnvelopeRequest struct {
	ClientX25519PubKey string `json:"client_x25519_pubkey"`
	BundleSHA256       string `json:"bundle_sha256,omitempty"`
}

func (h *APIHandler) handlePluginKeyEnvelope(w http.ResponseWriter, r *http.Request, pluginID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.verifier == nil {
		writeLicenseJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "token_verifier_unavailable",
			Message: "token verifier is not configured",
		})
		return
	}
	reg := h.service.PluginRegistry()
	if reg == nil {
		writeLicenseJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "plugins_unavailable",
			Message: "plugin registry is not configured",
		})
		return
	}

	asset, ok := reg.Get(pluginID)
	if !ok {
		writeLicenseJSON(w, http.StatusNotFound, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "not_found",
			Message: "plugin not found",
		})
		return
	}

	var req pluginKeyEnvelopeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "invalid_json",
			Message: "invalid key envelope request payload",
		})
		return
	}
	clientPub, err := ParseX25519PublicKey(req.ClientX25519PubKey)
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "invalid_request",
			Message: err.Error(),
		})
		return
	}
	if expectedHash := strings.TrimSpace(req.BundleSHA256); expectedHash != "" && !strings.EqualFold(expectedHash, asset.BundleSHA256) {
		writeLicenseJSON(w, http.StatusConflict, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "bundle_mismatch",
			Message: "requested bundle hash does not match active plugin bundle",
		})
		return
	}

	requiredScope := strings.TrimSpace(asset.RequiredScope)
	if requiredScope == "" {
		requiredScope = defaultPluginRequiredScope
	}
	expectedPeerID := strings.TrimSpace(r.Header.Get("X-SDN-Peer-ID"))
	claims, err := h.verifier.VerifyAuthorizationHeader(r.Header.Get("Authorization"), expectedPeerID, []string{requiredScope})
	if err != nil {
		writeLicenseJSON(w, http.StatusUnauthorized, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "token_invalid",
			Message: err.Error(),
		})
		return
	}

	pluginKey, err := reg.ReadBundleKey(pluginID)
	if err != nil {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "server_error",
			Message: err.Error(),
		})
		return
	}
	defer zeroBytes(pluginKey)
	envelope, err := BuildPluginKeyEnvelope(asset, pluginKey, clientPub, claims, h.service.issuer, time.Now().UTC())
	if err != nil {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "server_error",
			Message: err.Error(),
		})
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Vary", "Authorization, X-SDN-Peer-ID")
	writeLicenseJSON(w, http.StatusOK, envelope)
}

func parsePluginRoute(path string) (pluginID, action string, ok bool) {
	const prefix = "/api/v1/plugins/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	pluginID = strings.TrimSpace(parts[0])
	action = strings.TrimSpace(parts[1])
	if pluginID == "" || action == "" {
		return "", "", false
	}
	return pluginID, action, true
}

func writeLicenseJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func isTokenError(err error) bool {
	return errors.Is(err, ErrMissingAuthorization) ||
		errors.Is(err, ErrInvalidAuthorization) ||
		errors.Is(err, ErrInvalidTokenFormat) ||
		errors.Is(err, ErrInvalidTokenSignature) ||
		errors.Is(err, ErrTokenExpired) ||
		errors.Is(err, ErrTokenIssuerMismatch) ||
		errors.Is(err, ErrTokenPeerIDMismatch) ||
		errors.Is(err, ErrTokenMissingScope)
}

// UploadHandler handles signed WASM plugin uploads.
type UploadHandler struct {
	reg         *PluginRegistry
	keyLookup   func(xpub string) (string, error)    // returns signing_pubkey_hex
	xpubFromReq func(r *http.Request) (string, error) // extracts xpub from session
}

// NewUploadHandler creates a handler for plugin uploads.
func NewUploadHandler(reg *PluginRegistry, keyLookup func(string) (string, error), xpubFromReq func(*http.Request) (string, error)) *UploadHandler {
	return &UploadHandler{reg: reg, keyLookup: keyLookup, xpubFromReq: xpubFromReq}
}

type uploadMetadata struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

func (h *UploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract the uploader's xpub from the authenticated session.
	xpub, err := h.xpubFromReq(r)
	if err != nil {
		writeLicenseJSON(w, http.StatusUnauthorized, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "unauthorized", Message: "session required",
		})
		return
	}

	const maxUploadSize = 50 << 20 // 50 MB
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "invalid multipart form: " + err.Error(),
		})
		return
	}

	// Read bundle file.
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "missing bundle file",
		})
		return
	}
	defer file.Close()

	bundleData, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "failed to read bundle",
		})
		return
	}
	if int64(len(bundleData)) > maxUploadSize {
		writeLicenseJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "too_large", Message: "bundle exceeds 50 MB limit",
		})
		return
	}

	// Parse metadata.
	metaStr := r.FormValue("metadata")
	if metaStr == "" {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "missing metadata field",
		})
		return
	}
	var meta uploadMetadata
	if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "invalid metadata JSON: " + err.Error(),
		})
		return
	}
	if !pluginIDPattern.MatchString(strings.TrimSpace(meta.ID)) {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "invalid plugin id (allowed: A-Za-z0-9._-)",
		})
		return
	}

	// Parse signature.
	sigHex := strings.TrimSpace(r.FormValue("signature_hex"))
	if sigHex == "" {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "missing signature_hex field",
		})
		return
	}
	signature, err := hex.DecodeString(sigHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "bad_request", Message: "signature_hex must be 64-byte Ed25519 signature (128 hex chars)",
		})
		return
	}

	// Look up signer's bound public key.
	pubKeyHex, err := h.keyLookup(xpub)
	if err != nil {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "forbidden", Message: "user not found",
		})
		return
	}
	if pubKeyHex == "" {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "forbidden", Message: "no signing key bound to this user (login with wallet first)",
		})
		return
	}
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKey) != ed25519.PublicKeySize {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "server_error", Message: "invalid stored signing key",
		})
		return
	}

	// Verify Ed25519 signature over SHA-256(bundle).
	bundleHash := sha256.Sum256(bundleData)
	if !ed25519.Verify(pubKey, bundleHash[:], signature) {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "signature_invalid", Message: "Ed25519 signature verification failed",
		})
		return
	}

	// Store the plugin.
	asset, err := h.reg.AddPlugin(meta.ID, meta.Version, bundleData, sigHex, pubKeyHex)
	if err != nil {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type: msgTypeErrorResponse, Code: "server_error", Message: "failed to store plugin: " + err.Error(),
		})
		return
	}

	writeLicenseJSON(w, http.StatusCreated, map[string]interface{}{
		"status":        "ok",
		"plugin_id":     asset.ID,
		"version":       asset.Version,
		"bundle_sha256": asset.BundleSHA256,
		"size_bytes":    asset.SizeBytes,
	})
}
