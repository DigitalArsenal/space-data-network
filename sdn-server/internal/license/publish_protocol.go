package license

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// ModulePublishProtocolID is the libp2p protocol used by deployment
	// clients to replace encrypted module-delivery catalog entries, and to
	// change who a published module is leased to.
	ModulePublishProtocolID = "/space-data-network/module-publish/1.0.0"

	// ModulePublishRequestType replaces modules' encrypted bytes. An empty
	// type means this one.
	ModulePublishRequestType = "module-publish.v1"

	// ModuleEntitlementRequestType changes modules' allowed_xpubs and nothing
	// else: the bytes and their content keys stay as published.
	ModuleEntitlementRequestType = "module-entitlement.v1"

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
	GrantPolicy       string                `json:"grant_policy,omitempty"`
	SignatureHex      string                `json:"signature_hex,omitempty"`
	SignerPubKeyHex   string                `json:"signer_pubkey_hex,omitempty"`
}

// ModuleEntitlementChange adds and removes requester xpubs on published
// modules. "*" is an entry like any other: entitling it leases the modules to
// any signed-in account, and revoking it is how a module becomes paid-only.
type ModuleEntitlementChange struct {
	ModuleIDs []string `json:"module_ids"`
	Entitle   []string `json:"entitle,omitempty"`
	Revoke    []string `json:"revoke,omitempty"`
}

// ModulePublishRequest is signed by an admin wallet. The signer xpub is the
// stable wallet identity and SignerPubKeyHex is the Ed25519 key bound to that
// xpub by the provider's admin auth store. Type selects the body: Modules for
// ModulePublishRequestType, Entitlement for ModuleEntitlementRequestType.
//
// Replay protection (SEC-030): Audience is the peer ID of the one node the
// request is for, IssuedAtMs must be within ModulePublishMaxClockSkew of that
// node's clock, and Nonce is accepted once per signer. All three are signed.
// Rollback, also signed, is the only way to publish a module version lower
// than the one the node holds.
type ModulePublishRequest struct {
	Type            string                   `json:"type"`
	Audience        string                   `json:"audience"`
	IssuedAtMs      int64                    `json:"issued_at_ms"`
	Nonce           string                   `json:"nonce"`
	Rollback        bool                     `json:"rollback,omitempty"`
	Modules         []ModulePublishEntry     `json:"modules"`
	Entitlement     *ModuleEntitlementChange `json:"entitlement,omitempty"`
	SignerXPub      string                   `json:"signer_xpub"`
	SignerPubKeyHex string                   `json:"signer_pubkey_hex"`
	SignatureHex    string                   `json:"signature_hex"`
}

// ModulePublishResult reports one changed catalog entry. AllowedFingerprints
// is the module's allowlist after the change, as fingerprints, never xpubs.
type ModulePublishResult struct {
	ID                  string   `json:"id"`
	Version             string   `json:"version"`
	BundleSHA256        string   `json:"bundle_sha256"`
	SizeBytes           int64    `json:"size_bytes"`
	GrantPolicy         string   `json:"grant_policy,omitempty"`
	AllowedFingerprints []string `json:"allowed_fingerprints,omitempty"`
}

// ModulePublishResponse is returned by the provider over the libp2p stream.
type ModulePublishResponse struct {
	OK      bool                  `json:"ok"`
	Error   string                `json:"error,omitempty"`
	Results []ModulePublishResult `json:"results,omitempty"`
}

// ModulePublishGuard is the receiving node's side of replay protection.
type ModulePublishGuard struct {
	// Audience is this node's peer ID. A request addressed to any other node
	// is refused. Required.
	Audience string
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

func (g ModulePublishGuard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// ApplyModulePublishRequest verifies req and applies it to reg: a publish
// replaces the matching encrypted catalog entries, an entitlement change
// rewrites their allowed_xpubs. Results name every changed module, which the
// caller then re-provisions into the licensing runtime. It returns a wire-safe
// response instead of exposing partial filesystem errors to stream callers.
//
// A request is applied at most once, on the node it names, while fresh: the
// (signer, nonce) pair is consumed in reg's replay ledger before anything
// changes.
func ApplyModulePublishRequest(reg *PluginRegistry, req ModulePublishRequest, authorizer ModulePublishAuthorizer, guard ModulePublishGuard) ModulePublishResponse {
	if reg == nil {
		return ModulePublishResponse{OK: false, Error: "plugin registry is unavailable"}
	}
	if err := VerifyModulePublishRequest(req, authorizer, guard); err != nil {
		return ModulePublishResponse{OK: false, Error: err.Error()}
	}
	if err := reg.PublishReplayStore().Consume(strings.TrimSpace(req.SignerXPub), req.Nonce, req.IssuedAtMs, guard.now()); err != nil {
		return ModulePublishResponse{OK: false, Error: err.Error()}
	}
	if req.Type == ModuleEntitlementRequestType {
		return applyModuleEntitlementChange(reg, req)
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
			AllowedXpubs:       module.AllowedXpubs,
			Dependencies:       module.Dependencies,
			MaxGrantTimeoutMs:  module.MaxGrantTimeoutMs,
			GrantPolicy:        module.GrantPolicy,
			SignatureHex:       module.SignatureHex,
			SignerPubKeyHex:    module.SignerPubKeyHex,
			UploadedAtOverride: uploadedAt,
			AllowRollback:      req.Rollback,
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

func applyModuleEntitlementChange(reg *PluginRegistry, req ModulePublishRequest) ModulePublishResponse {
	change := req.Entitlement
	assets, err := reg.ChangeAllowedXpubs(change.ModuleIDs, change.Entitle, change.Revoke)
	if err != nil {
		return ModulePublishResponse{OK: false, Error: err.Error()}
	}
	results := make([]ModulePublishResult, 0, len(assets))
	for _, asset := range assets {
		decision := EvaluatePublication(asset, reg.GrantPolicyConfig())
		AuditEntitlementChange(req.SignerXPub, asset, decision, change.Entitle, change.Revoke)
		results = append(results, ModulePublishResult{
			ID:                  asset.ID,
			Version:             asset.Version,
			BundleSHA256:        asset.BundleSHA256,
			SizeBytes:           asset.SizeBytes,
			GrantPolicy:         decision.Policy,
			AllowedFingerprints: xpubFingerprints(asset.AllowedXpubs),
		})
	}
	return ModulePublishResponse{OK: true, Results: results}
}

// ApplyModulePublishJSON decodes one JSON publish request from r and applies it
// to reg. The caller owns response timing so it can refresh runtime state before
// acknowledging success.
func ApplyModulePublishJSON(r io.Reader, reg *PluginRegistry, authorizer ModulePublishAuthorizer, guard ModulePublishGuard) ModulePublishResponse {
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
	return ApplyModulePublishRequest(reg, req, authorizer, guard)
}

// ServeModulePublishJSON decodes one JSON publish request from r and writes one
// JSON response to w. It is intentionally transport-agnostic so HTTP tests,
// libp2p streams, and future CLI integrations use identical request handling.
func ServeModulePublishJSON(r io.Reader, w io.Writer, reg *PluginRegistry, authorizer ModulePublishAuthorizer, guard ModulePublishGuard) {
	WriteModulePublishResponse(w, ApplyModulePublishJSON(r, reg, authorizer, guard))
}

// WriteModulePublishResponse writes a newline-terminated JSON response.
func WriteModulePublishResponse(w io.Writer, resp ModulePublishResponse) {
	_ = json.NewEncoder(w).Encode(resp)
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

// VerifyModulePublishRequest checks the request's wallet signature, confirms
// the signer is an admin in the provider's local auth store, and that the
// request is addressed to this node (guard.Audience) and fresh. It does not
// consume the nonce; ApplyModulePublishRequest does.
func VerifyModulePublishRequest(req ModulePublishRequest, authorizer ModulePublishAuthorizer, guard ModulePublishGuard) error {
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
	audience := strings.TrimSpace(guard.Audience)
	if audience == "" {
		return errors.New("module publish audience is not configured on this node")
	}
	if strings.TrimSpace(req.Audience) != audience {
		return fmt.Errorf("module publish request is addressed to %q, not this node", req.Audience)
	}
	issued := time.UnixMilli(req.IssuedAtMs)
	now := guard.now()
	if issued.Before(now.Add(-ModulePublishMaxClockSkew)) || issued.After(now.Add(ModulePublishMaxClockSkew)) {
		return fmt.Errorf("module publish request issued_at %s is outside %s of this node's clock", issued.UTC().Format(time.RFC3339), ModulePublishMaxClockSkew)
	}
	return nil
}

func modulePublishSigningPayload(req ModulePublishRequest) ([]byte, error) {
	req.SignatureHex = ""
	if strings.TrimSpace(req.Type) == "" {
		req.Type = ModulePublishRequestType
	}
	if req.IssuedAtMs <= 0 {
		return nil, errors.New("issued_at_ms is required")
	}
	if strings.TrimSpace(req.Nonce) == "" {
		return nil, errors.New("nonce is required")
	}
	if len(req.Nonce) > 128 {
		return nil, errors.New("nonce is too long")
	}
	if strings.TrimSpace(req.Audience) == "" {
		return nil, errors.New("audience (the target node's peer id) is required")
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
	var bodyErr error
	switch req.Type {
	case ModulePublishRequestType:
		bodyErr = validateModulePublishBody(req)
	case ModuleEntitlementRequestType:
		bodyErr = validateModuleEntitlementBody(req)
	default:
		bodyErr = errors.New("unsupported module publish request type")
	}
	if bodyErr != nil {
		return nil, bodyErr
	}
	return json.Marshal(req)
}

func validateModulePublishBody(req ModulePublishRequest) error {
	if req.Entitlement != nil {
		return errors.New("a module publish request carries no entitlement change")
	}
	if len(req.Modules) == 0 {
		return errors.New("at least one module is required")
	}
	for _, module := range req.Modules {
		if strings.TrimSpace(module.ID) == "" {
			return errors.New("module id is required")
		}
		if strings.TrimSpace(module.Version) == "" {
			return errors.New("module version is required")
		}
		if len(module.EncryptedBundle) == 0 {
			return errors.New("encrypted bundle is required")
		}
		if len(module.KeyMaterial) == 0 {
			return errors.New("key material is required")
		}
	}
	return nil
}

func validateModuleEntitlementBody(req ModulePublishRequest) error {
	if len(req.Modules) > 0 {
		return errors.New("a module entitlement request carries no module bytes")
	}
	change := req.Entitlement
	if change == nil || len(normalizeAllowedXpubs(change.ModuleIDs)) == 0 {
		return errors.New("at least one module id is required")
	}
	entitle := normalizeAllowedXpubs(change.Entitle)
	revoke := normalizeAllowedXpubs(change.Revoke)
	if len(entitle) == 0 && len(revoke) == 0 {
		return errors.New("an entitlement change needs at least one xpub to entitle or revoke")
	}
	for _, xpub := range revoke {
		for _, other := range entitle {
			if xpub == other {
				return fmt.Errorf("%s is both entitled and revoked", XpubFingerprint(xpub))
			}
		}
	}
	return nil
}
