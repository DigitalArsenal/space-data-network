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

// UploadHandler handles signed WASM plugin uploads.
type UploadHandler struct {
	reg         *PluginRegistry
	keyLookup   func(xpub string) (string, error)     // returns signing_pubkey_hex
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
			Type: errorResponseType, Code: "unauthorized", Message: "session required",
		})
		return
	}

	const maxUploadSize = 50 << 20 // 50 MB
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "invalid multipart form: " + err.Error(),
		})
		return
	}

	// Read bundle file.
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "missing bundle file",
		})
		return
	}
	defer file.Close()

	bundleData, err := io.ReadAll(io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "failed to read bundle",
		})
		return
	}
	if int64(len(bundleData)) > maxUploadSize {
		writeLicenseJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{
			Type: errorResponseType, Code: "too_large", Message: "bundle exceeds 50 MB limit",
		})
		return
	}

	// Parse metadata.
	metaStr := r.FormValue("metadata")
	if metaStr == "" {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "missing metadata field",
		})
		return
	}
	var meta uploadMetadata
	if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "invalid metadata JSON: " + err.Error(),
		})
		return
	}
	if !pluginIDPattern.MatchString(strings.TrimSpace(meta.ID)) {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "invalid plugin id (allowed: A-Za-z0-9._-)",
		})
		return
	}

	// Parse signature.
	sigHex := strings.TrimSpace(r.FormValue("signature_hex"))
	if sigHex == "" {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "missing signature_hex field",
		})
		return
	}
	signature, err := hex.DecodeString(sigHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		writeLicenseJSON(w, http.StatusBadRequest, ErrorResponse{
			Type: errorResponseType, Code: "bad_request", Message: "signature_hex must be 64-byte Ed25519 signature (128 hex chars)",
		})
		return
	}

	// Look up signer's bound public key.
	pubKeyHex, err := h.keyLookup(xpub)
	if err != nil {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: errorResponseType, Code: "forbidden", Message: "user not found",
		})
		return
	}
	if pubKeyHex == "" {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: errorResponseType, Code: "forbidden", Message: "no signing key bound to this user (login with wallet first)",
		})
		return
	}
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKey) != ed25519.PublicKeySize {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type: errorResponseType, Code: "server_error", Message: "invalid stored signing key",
		})
		return
	}

	// Verify Ed25519 signature over SHA-256(bundle).
	bundleHash := sha256.Sum256(bundleData)
	if !ed25519.Verify(pubKey, bundleHash[:], signature) {
		writeLicenseJSON(w, http.StatusForbidden, ErrorResponse{
			Type: errorResponseType, Code: "signature_invalid", Message: "Ed25519 signature verification failed",
		})
		return
	}

	if _, err := h.reg.AddPlugin(meta.ID, meta.Version, bundleData, sigHex, pubKeyHex); err != nil {
		writeLicenseJSON(w, http.StatusInternalServerError, ErrorResponse{
			Type: errorResponseType, Code: "server_error", Message: fmt.Sprintf("failed to store plugin: %v", err),
		})
		return
	}

	writeLicenseJSON(w, http.StatusOK, map[string]interface{}{
		"id":            meta.ID,
		"version":       meta.Version,
		"bundle_sha256": hex.EncodeToString(bundleHash[:]),
		"size_bytes":    len(bundleData),
	})
}

func writeLicenseJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
