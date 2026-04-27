package license

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ModuleUploadHandler handles encrypted plugin-module catalog uploads.
type ModuleUploadHandler struct {
	reg         *PluginRegistry
	keyLookup   func(xpub string) (string, error)
	xpubFromReq func(r *http.Request) (string, error)
	afterUpload func(asset *PluginAsset) error
}

// NewModuleUploadHandler creates a handler for encrypted plugin-module uploads.
func NewModuleUploadHandler(
	reg *PluginRegistry,
	keyLookup func(string) (string, error),
	xpubFromReq func(*http.Request) (string, error),
) *ModuleUploadHandler {
	return &ModuleUploadHandler{reg: reg, keyLookup: keyLookup, xpubFromReq: xpubFromReq}
}

// SetAfterUpload registers a callback invoked after a module is stored. Servers
// use this to publish the new catalog entry through the live licensing runtime.
func (h *ModuleUploadHandler) SetAfterUpload(fn func(asset *PluginAsset) error) {
	if h == nil {
		return
	}
	h.afterUpload = fn
}

type moduleUploadMetadata struct {
	ID                string   `json:"id"`
	Version           string   `json:"version"`
	RequiredScope     string   `json:"required_scope,omitempty"`
	ContentType       string   `json:"content_type,omitempty"`
	CacheControl      string   `json:"cache_control,omitempty"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	MaxGrantTimeoutMs int64    `json:"max_grant_timeout_ms,omitempty"`
}

func (h *ModuleUploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleList(w)
	case http.MethodPost:
		h.handleUpload(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ModuleUploadHandler) handleList(w http.ResponseWriter) {
	modules := h.reg.ListPublic()
	writeLicenseJSON(w, http.StatusOK, map[string]interface{}{
		"modules": modules,
		"count":   len(modules),
	})
}

func (h *ModuleUploadHandler) handleUpload(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.reg == nil {
		writeLicenseJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Type: errorResponseType, Code: "unavailable", Message: "plugin registry unavailable",
		})
		return
	}

	xpub, err := h.xpubFromReq(r)
	if err != nil {
		writeLicenseJSON(w, http.StatusUnauthorized, ErrorResponse{
			Type: errorResponseType, Code: "unauthorized", Message: "session required",
		})
		return
	}

	const maxUploadSize = 50 << 20
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "invalid multipart form: " + err.Error(),
		})
		return
	}

	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "missing bundle file",
		})
		return
	}
	defer file.Close()

	encryptedBundle, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "failed to read bundle",
		})
		return
	}
	if int64(len(encryptedBundle)) > maxUploadSize {
		writeLicenseJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{
			Type: errorResponseType, Code: "too_large", Message: "bundle exceeds 50 MB limit",
		})
		return
	}

	meta, err := parseModuleUploadMetadata(r.FormValue("metadata"))
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: err.Error(),
		})
		return
	}
	contentKey, err := parseBundleKey([]byte(r.FormValue("content_key_hex")))
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "invalid content_key_hex: " + err.Error(),
		})
		return
	}
	defer zeroBytes(contentKey)

	sigHex := strings.TrimSpace(r.FormValue("signature_hex"))
	signature, err := hex.DecodeString(sigHex)
	if sigHex == "" || err != nil || len(signature) != ed25519.SignatureSize {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "signature_hex must be 64-byte Ed25519 signature (128 hex chars)",
		})
		return
	}

	pubKeyHex, err := h.keyLookup(xpub)
	if err != nil {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: errorResponseType, Code: "forbidden", Message: "user not found",
		})
		return
	}
	if pubKeyHex == "" {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: errorResponseType, Code: "forbidden", Message: "no signing key bound to this user",
		})
		return
	}
	pubKey, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(pubKeyHex), "0x"))
	if err != nil || len(pubKey) != ed25519.PublicKeySize {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type: errorResponseType, Code: "server_error", Message: "invalid stored signing key",
		})
		return
	}

	bundleHash := sha256.Sum256(encryptedBundle)
	if !ed25519.Verify(pubKey, bundleHash[:], signature) {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: errorResponseType, Code: "signature_invalid", Message: "Ed25519 signature verification failed",
		})
		return
	}

	asset, err := h.reg.AddEncryptedPlugin(EncryptedPluginUpload{
		ID:                meta.ID,
		Version:           meta.Version,
		RequiredScope:     meta.RequiredScope,
		EncryptedBundle:   encryptedBundle,
		ContentKey:        contentKey,
		ContentType:       meta.ContentType,
		CacheControl:      meta.CacheControl,
		AllowedDomains:    meta.AllowedDomains,
		MaxGrantTimeoutMs: meta.MaxGrantTimeoutMs,
		SignatureHex:      sigHex,
		SignerPubKeyHex:   strings.TrimSpace(pubKeyHex),
	})
	if err != nil {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type: errorResponseType, Code: "server_error", Message: fmt.Sprintf("failed to store encrypted plugin module: %v", err),
		})
		return
	}
	if h.afterUpload != nil {
		if err := h.afterUpload(asset); err != nil {
			_ = h.reg.SetRuntimeStatus(asset.ID, pluginRuntimeStatusError, err.Error())
			writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
				Type: errorResponseType, Code: "publish_failed", Message: fmt.Sprintf("stored encrypted plugin module but failed to publish it: %v", err),
			})
			return
		}
	}

	writeLicenseJSON(w, http.StatusOK, map[string]interface{}{
		"id":            asset.ID,
		"version":       asset.Version,
		"bundle_sha256": asset.BundleSHA256,
		"size_bytes":    asset.SizeBytes,
		"module":        asset.Descriptor(),
	})
}

func parseModuleUploadMetadata(raw string) (moduleUploadMetadata, error) {
	if strings.TrimSpace(raw) == "" {
		return moduleUploadMetadata{}, fmt.Errorf("missing metadata field")
	}
	var meta moduleUploadMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return moduleUploadMetadata{}, fmt.Errorf("invalid metadata JSON: %w", err)
	}
	meta.ID = strings.TrimSpace(meta.ID)
	meta.Version = strings.TrimSpace(meta.Version)
	if !pluginIDPattern.MatchString(meta.ID) {
		return moduleUploadMetadata{}, fmt.Errorf("invalid plugin id (allowed: A-Za-z0-9._-)")
	}
	if meta.Version == "" {
		return moduleUploadMetadata{}, fmt.Errorf("version is required")
	}
	return meta, nil
}
