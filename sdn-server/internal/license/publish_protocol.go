package license

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	// ModulePublishProtocolID is the libp2p protocol used by deployment
	// clients to replace encrypted module-delivery catalog entries.
	ModulePublishProtocolID = "/space-data-network/module-publish/1.0.0"

	modulePublishRequestType  = "module-publish.v1"
	maxModulePublishJSONBytes = 256 << 20
)

// ModulePublishEntry is one encrypted module artifact in a publish request.
// Byte slices are encoded as base64 by encoding/json.
type ModulePublishEntry struct {
	ID                string   `json:"id"`
	Version           string   `json:"version"`
	RequiredScope     string   `json:"required_scope,omitempty"`
	EncryptedBundle   []byte   `json:"encrypted_bundle"`
	KeyMaterial       []byte   `json:"key_material"`
	ContentType       string   `json:"content_type,omitempty"`
	CacheControl      string   `json:"cache_control,omitempty"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	MaxGrantTimeoutMs int64    `json:"max_grant_timeout_ms,omitempty"`
	SignatureHex      string   `json:"signature_hex,omitempty"`
	SignerPubKeyHex   string   `json:"signer_pubkey_hex,omitempty"`
}

// ModulePublishRequest is signed with an HMAC shared by deployment automation
// and the provider node. The deployment token is never sent on the wire.
type ModulePublishRequest struct {
	Type         string               `json:"type"`
	IssuedAtMs   int64                `json:"issued_at_ms"`
	Nonce        string               `json:"nonce"`
	Modules      []ModulePublishEntry `json:"modules"`
	SignatureHex string               `json:"signature_hex"`
}

// ModulePublishResult reports one catalog replacement.
type ModulePublishResult struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	BundleSHA256 string `json:"bundle_sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// ModulePublishResponse is returned by the provider over the libp2p stream.
type ModulePublishResponse struct {
	OK      bool                  `json:"ok"`
	Error   string                `json:"error,omitempty"`
	Results []ModulePublishResult `json:"results,omitempty"`
}

// ApplyModulePublishRequest verifies req and replaces the matching encrypted
// catalog entries in reg. It returns a wire-safe response instead of exposing
// partial filesystem errors to stream callers.
func ApplyModulePublishRequest(reg *PluginRegistry, req ModulePublishRequest, token string) ModulePublishResponse {
	if reg == nil {
		return ModulePublishResponse{OK: false, Error: "plugin registry is unavailable"}
	}
	if err := VerifyModulePublishRequest(req, token); err != nil {
		return ModulePublishResponse{OK: false, Error: err.Error()}
	}

	uploadedAt := time.Now().UTC().Format(time.RFC3339)
	results := make([]ModulePublishResult, 0, len(req.Modules))
	for _, module := range req.Modules {
		asset, err := reg.AddEncryptedPlugin(EncryptedPluginUpload{
			ID:                 module.ID,
			Version:            module.Version,
			RequiredScope:      module.RequiredScope,
			EncryptedBundle:    module.EncryptedBundle,
			KeyMaterial:        module.KeyMaterial,
			ContentType:        module.ContentType,
			CacheControl:       module.CacheControl,
			AllowedDomains:     module.AllowedDomains,
			MaxGrantTimeoutMs:  module.MaxGrantTimeoutMs,
			SignatureHex:       module.SignatureHex,
			SignerPubKeyHex:    module.SignerPubKeyHex,
			UploadedAtOverride: uploadedAt,
		})
		if err != nil {
			return ModulePublishResponse{OK: false, Error: err.Error()}
		}
		results = append(results, ModulePublishResult{
			ID:           asset.ID,
			Version:      asset.Version,
			BundleSHA256: asset.BundleSHA256,
			SizeBytes:    asset.SizeBytes,
		})
	}
	return ModulePublishResponse{OK: true, Results: results}
}

// ApplyModulePublishJSON decodes one JSON publish request from r and applies it
// to reg. The caller owns response timing so it can refresh runtime state before
// acknowledging success.
func ApplyModulePublishJSON(r io.Reader, reg *PluginRegistry, token string) ModulePublishResponse {
	var req ModulePublishRequest
	data, err := io.ReadAll(io.LimitReader(r, maxModulePublishJSONBytes+1))
	if err != nil {
		return ModulePublishResponse{OK: false, Error: "read request: " + err.Error()}
	}
	if len(data) > maxModulePublishJSONBytes {
		return ModulePublishResponse{OK: false, Error: "module publish request exceeds 256 MiB"}
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return ModulePublishResponse{OK: false, Error: "decode request: " + err.Error()}
	}
	return ApplyModulePublishRequest(reg, req, token)
}

// ServeModulePublishJSON decodes one JSON publish request from r and writes one
// JSON response to w. It is intentionally transport-agnostic so HTTP tests,
// libp2p streams, and future CLI integrations use identical request handling.
func ServeModulePublishJSON(r io.Reader, w io.Writer, reg *PluginRegistry, token string) {
	WriteModulePublishResponse(w, ApplyModulePublishJSON(r, reg, token))
}

// WriteModulePublishResponse writes a newline-terminated JSON response.
func WriteModulePublishResponse(w io.Writer, resp ModulePublishResponse) {
	_ = json.NewEncoder(w).Encode(resp)
}

// SignModulePublishRequest signs req in place using HMAC-SHA256.
func SignModulePublishRequest(req *ModulePublishRequest, token string) error {
	if req == nil {
		return errors.New("module publish request is required")
	}
	if strings.TrimSpace(req.Type) == "" {
		req.Type = modulePublishRequestType
	}
	mac, err := modulePublishMAC(*req, token)
	if err != nil {
		return err
	}
	req.SignatureHex = hex.EncodeToString(mac)
	return nil
}

// VerifyModulePublishRequest checks the request HMAC without mutating req.
func VerifyModulePublishRequest(req ModulePublishRequest, token string) error {
	got := strings.TrimSpace(req.SignatureHex)
	if got == "" {
		return errors.New("signature_hex is required")
	}
	expected, err := modulePublishMAC(req, token)
	if err != nil {
		return err
	}
	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return errors.New("signature_hex must be hex")
	}
	if !hmac.Equal(gotBytes, expected) {
		return errors.New("module publish signature mismatch")
	}
	return nil
}

func modulePublishMAC(req ModulePublishRequest, token string) ([]byte, error) {
	secret := strings.TrimSpace(token)
	if secret == "" {
		return nil, errors.New("module publish token is required")
	}
	req.SignatureHex = ""
	if strings.TrimSpace(req.Type) == "" {
		req.Type = modulePublishRequestType
	}
	if req.Type != modulePublishRequestType {
		return nil, errors.New("unsupported module publish request type")
	}
	if req.IssuedAtMs <= 0 {
		return nil, errors.New("issued_at_ms is required")
	}
	if strings.TrimSpace(req.Nonce) == "" {
		return nil, errors.New("nonce is required")
	}
	if len(req.Modules) == 0 {
		return nil, errors.New("at least one module is required")
	}
	for _, module := range req.Modules {
		if strings.TrimSpace(module.ID) == "" {
			return nil, errors.New("module id is required")
		}
		if strings.TrimSpace(module.Version) == "" {
			return nil, errors.New("module version is required")
		}
		if len(module.EncryptedBundle) == 0 {
			return nil, errors.New("encrypted bundle is required")
		}
		if len(module.KeyMaterial) == 0 {
			return nil, errors.New("key material is required")
		}
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write(canonical)
	return h.Sum(nil), nil
}
