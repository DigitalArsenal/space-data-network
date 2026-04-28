package node

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

const PluginModuleUploadProtocolID = "/space-data-network/plugin-module-upload/1.0.0"

const (
	maxModuleUploadJSONBytes   = 75 << 20
	maxModuleUploadBundleBytes = 50 << 20
)

type ModuleUploadProtocolService struct {
	Registry       *license.PluginRegistry
	KeyLookup      func(xpub string) (string, error)
	ProviderPeerID string
	AfterUpload    func(pluginID string) error
}

type moduleUploadProtocolMetadata struct {
	ID                string   `json:"id"`
	Version           string   `json:"version"`
	RequiredScope     string   `json:"required_scope,omitempty"`
	ContentType       string   `json:"content_type,omitempty"`
	CacheControl      string   `json:"cache_control,omitempty"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	MaxGrantTimeoutMs int64    `json:"max_grant_timeout_ms,omitempty"`
}

type moduleUploadProtocolRequest struct {
	Version            int                                 `json:"version"`
	Metadata           moduleUploadProtocolMetadata        `json:"metadata"`
	UploaderXPub       string                              `json:"uploader_xpub"`
	SignerPublicKeyHex string                              `json:"signer_public_key_hex"`
	SignatureHex       string                              `json:"signature_hex"`
	ContentKeyEnvelope *license.ProviderContentKeyEnvelope `json:"content_key_envelope"`
	EncryptedBundleB64 string                              `json:"encrypted_bundle_b64"`
}

type moduleUploadProtocolResponse struct {
	OK           bool                      `json:"ok"`
	Error        string                    `json:"error,omitempty"`
	ID           string                    `json:"id,omitempty"`
	Version      string                    `json:"version,omitempty"`
	BundleSHA256 string                    `json:"bundle_sha256,omitempty"`
	SizeBytes    int64                     `json:"size_bytes,omitempty"`
	Module       *license.PluginDescriptor `json:"module,omitempty"`
}

func (s *ModuleUploadProtocolService) Register(h libp2phost.Host) {
	if s == nil || h == nil {
		return
	}
	h.SetStreamHandler(libp2pprotocol.ID(PluginModuleUploadProtocolID), s.handleStream)
}

func (s *ModuleUploadProtocolService) handleStream(stream network.Stream) {
	defer stream.Close()

	asset, err := s.handleUploadStream(stream)
	if err != nil {
		_ = json.NewEncoder(stream).Encode(moduleUploadProtocolResponse{OK: false, Error: err.Error()})
		return
	}
	descriptor := asset.Descriptor()
	_ = json.NewEncoder(stream).Encode(moduleUploadProtocolResponse{
		OK:           true,
		ID:           asset.ID,
		Version:      asset.Version,
		BundleSHA256: asset.BundleSHA256,
		SizeBytes:    asset.SizeBytes,
		Module:       &descriptor,
	})
}

func (s *ModuleUploadProtocolService) handleUploadStream(reader io.Reader) (*license.PluginAsset, error) {
	if s == nil || s.Registry == nil {
		return nil, fmt.Errorf("plugin registry unavailable")
	}
	if s.KeyLookup == nil {
		return nil, fmt.Errorf("upload authorization unavailable")
	}

	raw, err := io.ReadAll(io.LimitReader(reader, maxModuleUploadJSONBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upload request: %w", err)
	}
	if len(raw) > maxModuleUploadJSONBytes {
		return nil, fmt.Errorf("upload request exceeds 75 MB limit")
	}

	var req moduleUploadProtocolRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode upload request: %w", err)
	}
	if req.Version != 1 {
		return nil, fmt.Errorf("unsupported upload request version %d", req.Version)
	}
	uploaderXPub := strings.TrimSpace(req.UploaderXPub)
	if uploaderXPub == "" {
		return nil, fmt.Errorf("uploader_xpub is required")
	}
	encryptedBundle, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(req.EncryptedBundleB64))
	if err != nil {
		return nil, fmt.Errorf("decode encrypted_bundle_b64: %w", err)
	}
	if len(encryptedBundle) == 0 {
		return nil, fmt.Errorf("encrypted bundle is required")
	}
	if len(encryptedBundle) > maxModuleUploadBundleBytes {
		return nil, fmt.Errorf("encrypted bundle exceeds 50 MB limit")
	}

	storedSignerPubKeyHex, err := s.KeyLookup(uploaderXPub)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	normalizedStoredSignerPubKeyHex := normalizeHexKey(storedSignerPubKeyHex)
	normalizedRequestSignerPubKeyHex := normalizeHexKey(req.SignerPublicKeyHex)
	if normalizedStoredSignerPubKeyHex == "" {
		return nil, fmt.Errorf("no signing key bound to this user")
	}
	if normalizedRequestSignerPubKeyHex == "" {
		return nil, fmt.Errorf("signer_public_key_hex is required")
	}
	if !strings.EqualFold(normalizedStoredSignerPubKeyHex, normalizedRequestSignerPubKeyHex) {
		return nil, fmt.Errorf("signer_public_key_hex does not match uploader")
	}
	signerPubKey, err := hex.DecodeString(normalizedStoredSignerPubKeyHex)
	if err != nil || len(signerPubKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid stored signing key")
	}
	signature, err := hex.DecodeString(normalizeHexKey(req.SignatureHex))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature_hex must be 64-byte Ed25519 signature")
	}
	bundleHash := sha256.Sum256(encryptedBundle)
	if !ed25519.Verify(signerPubKey, bundleHash[:], signature) {
		return nil, fmt.Errorf("Ed25519 signature verification failed")
	}

	providerPeerID := strings.TrimSpace(s.ProviderPeerID)
	if providerPeerID == "" {
		return nil, fmt.Errorf("provider peer id is required")
	}
	asset, err := s.Registry.AddEncryptedPlugin(license.EncryptedPluginUpload{
		ID:                 req.Metadata.ID,
		Version:            req.Metadata.Version,
		RequiredScope:      req.Metadata.RequiredScope,
		EncryptedBundle:    encryptedBundle,
		ContentKeyEnvelope: req.ContentKeyEnvelope,
		ProviderPeerID:     providerPeerID,
		ContentType:        req.Metadata.ContentType,
		CacheControl:       req.Metadata.CacheControl,
		AllowedDomains:     req.Metadata.AllowedDomains,
		MaxGrantTimeoutMs:  req.Metadata.MaxGrantTimeoutMs,
		SignatureHex:       normalizeHexKey(req.SignatureHex),
		SignerPubKeyHex:    normalizedStoredSignerPubKeyHex,
	})
	if err != nil {
		return nil, fmt.Errorf("store encrypted plugin module: %w", err)
	}
	if s.AfterUpload != nil {
		if err := s.AfterUpload(asset.ID); err != nil {
			_ = s.Registry.SetRuntimeStatus(asset.ID, "error", err.Error())
			return nil, fmt.Errorf("publish encrypted plugin module: %w", err)
		}
	}
	return asset, nil
}

func normalizeHexKey(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "0x"))
}
