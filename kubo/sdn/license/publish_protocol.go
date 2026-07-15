package license

// TRIMMED PORT (kubo rebase Phase 2b): the ApplyModulePublishRequest /
// ApplyModulePublishJSON / ServeModulePublishJSON / WriteModulePublishResponse
// functions were dropped here — they consume the heavy license PluginRegistry
// runtime (encrypted catalog store) that is not on the module-load/invoke path
// this checkpoint proves. Only the request TYPES and the Sign/Verify signing
// surface that sdn/modulert needs are retained.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	// ModulePublishProtocolID is the libp2p protocol used by deployment
	// clients to replace encrypted module-delivery catalog entries.
	ModulePublishProtocolID = "/space-data-network/module-publish/1.0.0"

	modulePublishRequestType  = "module-publish.v1"
	maxModulePublishJSONBytes = 256 << 20
)

// ModulePublishPrincipal is the wallet identity authorized to publish modules.
// The provider checks this against its local admin user store.
type ModulePublishPrincipal struct {
	XPub             string
	SigningPubKeyHex string
	Admin            bool
}

// ModulePublishAuthorizer resolves a signer xpub to the provider's local
// authorization state. It must return Admin=true only for admin-trusted wallets.
type ModulePublishAuthorizer func(xpub string) (ModulePublishPrincipal, error)

// ModulePublishEntry is one encrypted module artifact in a publish request.
// Byte slices are encoded as base64 by encoding/json.
type ModulePublishEntry struct {
	ID                string                `json:"id"`
	Version           string                `json:"version"`
	RequiredScope     string                `json:"required_scope,omitempty"`
	EncryptedBundle   []byte                `json:"encrypted_bundle"`
	KeyMaterial       []byte                `json:"key_material"`
	ContentType       string                `json:"content_type,omitempty"`
	CacheControl      string                `json:"cache_control,omitempty"`
	AllowedXpubs      []string              `json:"allowed_xpubs,omitempty"`
	Dependencies      []PluginDependencyRef `json:"dependencies,omitempty"`
	MaxGrantTimeoutMs int64                 `json:"max_grant_timeout_ms,omitempty"`
	SignatureHex      string                `json:"signature_hex,omitempty"`
	SignerPubKeyHex   string                `json:"signer_pubkey_hex,omitempty"`
}

// ModulePublishRequest is signed by an admin wallet. The signer xpub is the
// stable wallet identity and SignerPubKeyHex is the Ed25519 key bound to that
// xpub by the provider's admin auth store.
type ModulePublishRequest struct {
	Type            string               `json:"type"`
	IssuedAtMs      int64                `json:"issued_at_ms"`
	Nonce           string               `json:"nonce"`
	Modules         []ModulePublishEntry `json:"modules"`
	SignerXPub      string               `json:"signer_xpub"`
	SignerPubKeyHex string               `json:"signer_pubkey_hex"`
	SignatureHex    string               `json:"signature_hex"`
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

// SignModulePublishRequest signs req in place using the admin wallet's Ed25519
// signing key.
func SignModulePublishRequest(req *ModulePublishRequest, signerXPub string, signingPubKey ed25519.PublicKey, signingPrivateKey ed25519.PrivateKey) error {
	if req == nil {
		return errors.New("module publish request is required")
	}
	if len(signingPubKey) != ed25519.PublicKeySize {
		return errors.New("signing public key must be 32 bytes")
	}
	if len(signingPrivateKey) == ed25519.SeedSize {
		signingPrivateKey = ed25519.NewKeyFromSeed(signingPrivateKey)
	}
	if len(signingPrivateKey) != ed25519.PrivateKeySize {
		return errors.New("signing private key must be 32-byte seed or 64-byte Ed25519 private key")
	}
	req.SignerXPub = strings.TrimSpace(signerXPub)
	req.SignerPubKeyHex = hex.EncodeToString(signingPubKey)
	payload, err := modulePublishSigningPayload(*req)
	if err != nil {
		return err
	}
	req.SignatureHex = hex.EncodeToString(ed25519.Sign(signingPrivateKey, payload))
	return nil
}

// VerifyModulePublishRequest checks the request's wallet signature and confirms
// the signer is an admin in the provider's local auth store.
func VerifyModulePublishRequest(req ModulePublishRequest, authorizer ModulePublishAuthorizer) error {
	got := strings.TrimSpace(req.SignatureHex)
	if got == "" {
		return errors.New("signature_hex is required")
	}
	if authorizer == nil {
		return errors.New("module publish authorizer is required")
	}
	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return errors.New("signature_hex must be hex")
	}
	if len(gotBytes) != ed25519.SignatureSize {
		return errors.New("signature_hex must be 64-byte Ed25519 signature")
	}
	signerXPub := strings.TrimSpace(req.SignerXPub)
	if signerXPub == "" {
		return errors.New("signer_xpub is required")
	}
	principal, err := authorizer(signerXPub)
	if err != nil {
		return err
	}
	if !principal.Admin {
		return errors.New("module publish signer is not an admin")
	}
	if strings.TrimSpace(principal.SigningPubKeyHex) == "" {
		return errors.New("module publish signer has no bound signing key")
	}
	if !strings.EqualFold(strings.TrimSpace(principal.SigningPubKeyHex), strings.TrimSpace(req.SignerPubKeyHex)) {
		return errors.New("module publish signer key is not bound to the signer xpub")
	}
	pubKey, err := hex.DecodeString(strings.TrimSpace(req.SignerPubKeyHex))
	if err != nil || len(pubKey) != ed25519.PublicKeySize {
		return errors.New("signer_pubkey_hex must be 32-byte Ed25519 public key")
	}
	payload, err := modulePublishSigningPayload(req)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pubKey), payload, gotBytes) {
		return errors.New("module publish signature mismatch")
	}
	return nil
}

func modulePublishSigningPayload(req ModulePublishRequest) ([]byte, error) {
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
	if strings.TrimSpace(req.SignerXPub) == "" {
		return nil, errors.New("signer_xpub is required")
	}
	if strings.TrimSpace(req.SignerPubKeyHex) == "" {
		return nil, errors.New("signer_pubkey_hex is required")
	}
	if pubKey, err := hex.DecodeString(strings.TrimSpace(req.SignerPubKeyHex)); err != nil || len(pubKey) != ed25519.PublicKeySize {
		return nil, errors.New("signer_pubkey_hex must be 32-byte Ed25519 public key")
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
	return json.Marshal(req)
}
