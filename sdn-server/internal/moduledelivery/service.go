package moduledelivery

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/network"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	schema "github.com/spacedatanetwork/sdn-server/internal/moduledelivery/schema/v1"
)

const (
	defaultChallengeTTL = 60 * time.Second
	requestMaxLen       = 64 * 1024
)

type pendingGrant struct {
	reqID                        string
	moduleID                     string
	moduleVersion                string
	requesterPeerID              string
	requesterXPub                string
	requesterSigningPublicKey    ed25519.PublicKey
	requesterEncryptionPublicKey []byte
	challenge                    []byte
	expiresAt                    time.Time
	remotePeerID                 string
}

// Service serves the FlatBuffer module-delivery grant flow.
type Service struct {
	registry          *Registry
	licenseService    *license.Service
	providerPeerID    string
	providerPublicKey []byte
	challengeTTL      time.Duration

	mu      sync.Mutex
	pending map[string]pendingGrant
}

// NewService initializes the module-delivery service on top of the existing entitlement/token store.
func NewService(baseDataPath, issuer, providerPeerID string, providerPublicKey []byte, ipfsAPIURL string) (*Service, error) {
	if err := validateProviderPublicKey(providerPublicKey); err != nil {
		return nil, err
	}
	if strings.TrimSpace(providerPeerID) == "" {
		return nil, fmt.Errorf("provider peer id is required")
	}

	licenseService, err := license.NewService(baseDataPath, issuer)
	if err != nil {
		return nil, err
	}
	registry, err := NewRegistry(baseDataPath, ipfsAPIURL)
	if err != nil {
		_ = licenseService.Close()
		return nil, err
	}

	return &Service{
		registry:          registry,
		licenseService:    licenseService,
		providerPeerID:    strings.TrimSpace(providerPeerID),
		providerPublicKey: append([]byte(nil), providerPublicKey...),
		challengeTTL:      defaultChallengeTTL,
		pending:           make(map[string]pendingGrant),
	}, nil
}

// Close releases resources.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	return s.licenseService.Close()
}

// LicenseService exposes the embedded entitlement/token service.
func (s *Service) LicenseService() *license.Service {
	if s == nil {
		return nil
	}
	return s.licenseService
}

// Registry exposes encrypted module publication state.
func (s *Service) Registry() *Registry {
	if s == nil {
		return nil
	}
	return s.registry
}

// ProviderPublicKey returns the provider compressed secp256k1 public key.
func (s *Service) ProviderPublicKey() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.providerPublicKey...)
}

// ProviderPeerID returns the provider peer ID.
func (s *Service) ProviderPeerID() string {
	if s == nil {
		return ""
	}
	return s.providerPeerID
}

// HandleStream implements the public FlatBuffer request-response protocol.
func (s *Service) HandleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))

	requestBytes, err := io.ReadAll(io.LimitReader(stream, requestMaxLen))
	if err != nil {
		return
	}

	responseBytes, err := s.handleMessage(requestBytes, stream.Conn().RemotePeer().String())
	if err != nil {
		responseBytes = s.encodeErrorResponse("", "server_error", err.Error(), false)
	}
	if len(responseBytes) == 0 {
		return
	}
	_, _ = stream.Write(responseBytes)
}

func (s *Service) handleMessage(requestBytes []byte, remotePeerID string) ([]byte, error) {
	if len(requestBytes) == 0 {
		return s.encodeErrorResponse("", "invalid_request", "empty module delivery request", false), nil
	}
	if !schema.ModuleDeliveryMessageBufferHasIdentifier(requestBytes) {
		return s.encodeErrorResponse("", "invalid_request", "invalid module delivery envelope", false), nil
	}

	message := schema.GetRootAsModuleDeliveryMessage(requestBytes, 0)
	switch message.MessageType() {
	case schema.ModuleDeliveryMessageTypeGRANT_REQUEST:
		request := message.GrantRequest(nil)
		if request == nil {
			return s.encodeErrorResponse("", "invalid_request", "missing grant request payload", false), nil
		}
		return s.handleGrantRequest(request, remotePeerID)
	case schema.ModuleDeliveryMessageTypeGRANT_PROOF:
		proof := message.GrantProof(nil)
		if proof == nil {
			return s.encodeErrorResponse("", "invalid_request", "missing grant proof payload", false), nil
		}
		return s.handleGrantProof(proof, remotePeerID)
	default:
		return s.encodeErrorResponse("", "unsupported_type", "unsupported module delivery message type", false), nil
	}
}

func (s *Service) handleGrantRequest(request *schema.GrantRequest, remotePeerID string) ([]byte, error) {
	reqID := strings.TrimSpace(string(request.ReqId()))
	moduleID := strings.TrimSpace(string(request.ModuleId()))
	moduleVersion := strings.TrimSpace(string(request.ModuleVersion()))
	requesterPeerID := strings.TrimSpace(string(request.RequesterPeerId()))
	requesterXPub := strings.TrimSpace(string(request.RequesterXpub()))
	signingPublicKey := append([]byte(nil), request.RequesterSigningPublicKeyBytes()...)
	encryptionPublicKey := append([]byte(nil), request.RequesterEncryptionPublicKeyBytes()...)

	if reqID == "" || moduleID == "" || requesterPeerID == "" || requesterXPub == "" {
		return s.encodeErrorResponse(reqID, "invalid_request", "req_id, module_id, requester_peer_id, and requester_xpub are required", false), nil
	}
	if len(signingPublicKey) != ed25519.PublicKeySize {
		return s.encodeErrorResponse(reqID, "invalid_request", "requester signing public key must be 32-byte Ed25519", false), nil
	}
	if len(encryptionPublicKey) != 32 {
		return s.encodeErrorResponse(reqID, "invalid_request", "requester encryption public key must be 32-byte X25519", false), nil
	}
	derivedPeerID, err := license.PeerIDFromEd25519PublicKey(signingPublicKey)
	if err != nil {
		return s.encodeErrorResponse(reqID, "invalid_request", "invalid requester signing public key", false), nil
	}
	if derivedPeerID != requesterPeerID {
		return s.encodeErrorResponse(reqID, "peer_id_mismatch", "requester peer id does not match requester signing public key", false), nil
	}
	if _, ok := s.registry.PluginRegistry().Get(moduleID); !ok {
		return s.encodeErrorResponse(reqID, "module_not_found", "requested module is not available", false), nil
	}

	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return nil, fmt.Errorf("generate challenge: %w", err)
	}

	now := time.Now().UTC()
	s.cleanupPending(now)

	s.mu.Lock()
	s.pending[reqID] = pendingGrant{
		reqID:                        reqID,
		moduleID:                     moduleID,
		moduleVersion:                moduleVersion,
		requesterPeerID:              requesterPeerID,
		requesterXPub:                requesterXPub,
		requesterSigningPublicKey:    ed25519.PublicKey(signingPublicKey),
		requesterEncryptionPublicKey: encryptionPublicKey,
		challenge:                    challenge,
		expiresAt:                    now.Add(s.challengeTTL),
		remotePeerID:                 remotePeerID,
	}
	s.mu.Unlock()

	builder := flatbuffers.NewBuilder(256)
	reqIDOffset := builder.CreateString(reqID)
	challengeOffset := builder.CreateByteVector(challenge)
	providerPeerIDOffset := builder.CreateString(s.providerPeerID)
	providerPubOffset := builder.CreateByteVector(s.providerPublicKey)

	schema.GrantChallengeStart(builder)
	schema.GrantChallengeAddSchemaVersion(builder, schemaVersion)
	schema.GrantChallengeAddReqId(builder, reqIDOffset)
	schema.GrantChallengeAddChallenge(builder, challengeOffset)
	schema.GrantChallengeAddExpiresAtMs(builder, uint64(now.Add(s.challengeTTL).UnixMilli()))
	schema.GrantChallengeAddProviderPeerId(builder, providerPeerIDOffset)
	schema.GrantChallengeAddProviderPublicKey(builder, providerPubOffset)
	challengeMessageOffset := schema.GrantChallengeEnd(builder)

	schema.ModuleDeliveryMessageStart(builder)
	schema.ModuleDeliveryMessageAddSchemaVersion(builder, schemaVersion)
	schema.ModuleDeliveryMessageAddMessageType(builder, schema.ModuleDeliveryMessageTypeGRANT_CHALLENGE)
	schema.ModuleDeliveryMessageAddGrantChallenge(builder, challengeMessageOffset)
	messageOffset := schema.ModuleDeliveryMessageEnd(builder)
	schema.FinishModuleDeliveryMessageBuffer(builder, messageOffset)
	return builder.FinishedBytes(), nil
}

func (s *Service) handleGrantProof(proof *schema.GrantProof, remotePeerID string) ([]byte, error) {
	reqID := strings.TrimSpace(string(proof.ReqId()))
	moduleID := strings.TrimSpace(string(proof.ModuleId()))
	moduleVersion := strings.TrimSpace(string(proof.ModuleVersion()))
	requesterPeerID := strings.TrimSpace(string(proof.RequesterPeerId()))
	signingPublicKey := append([]byte(nil), proof.RequesterSigningPublicKeyBytes()...)
	encryptionPublicKey := append([]byte(nil), proof.RequesterEncryptionPublicKeyBytes()...)
	challenge := append([]byte(nil), proof.ChallengeBytes()...)
	signature := append([]byte(nil), proof.SignatureBytes()...)

	if reqID == "" || moduleID == "" || requesterPeerID == "" {
		return s.encodeErrorResponse(reqID, "invalid_request", "req_id, module_id, and requester_peer_id are required", false), nil
	}
	if len(signingPublicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return s.encodeErrorResponse(reqID, "invalid_request", "invalid requester signature payload", false), nil
	}
	if len(encryptionPublicKey) != 32 {
		return s.encodeErrorResponse(reqID, "invalid_request", "requester encryption public key must be 32-byte X25519", false), nil
	}

	now := time.Now().UTC()
	s.cleanupPending(now)

	s.mu.Lock()
	pending, ok := s.pending[reqID]
	if ok {
		delete(s.pending, reqID)
	}
	s.mu.Unlock()
	if !ok {
		return s.encodeErrorResponse(reqID, "challenge_not_found", "challenge not found or expired", true), nil
	}
	if pending.expiresAt.Before(now) {
		return s.encodeErrorResponse(reqID, "challenge_expired", "challenge expired", true), nil
	}
	if pending.remotePeerID != "" && remotePeerID != "" && pending.remotePeerID != remotePeerID {
		return s.encodeErrorResponse(reqID, "peer_id_mismatch", "remote peer changed between challenge and proof", false), nil
	}
	if pending.moduleID != moduleID || pending.requesterPeerID != requesterPeerID {
		return s.encodeErrorResponse(reqID, "challenge_mismatch", "proof context does not match challenge request", false), nil
	}
	if pending.moduleVersion != "" && moduleVersion != "" && pending.moduleVersion != moduleVersion {
		return s.encodeErrorResponse(reqID, "challenge_mismatch", "module version does not match challenge request", false), nil
	}
	if !bytes.Equal(signingPublicKey, pending.requesterSigningPublicKey) ||
		!bytes.Equal(encryptionPublicKey, pending.requesterEncryptionPublicKey) ||
		!bytes.Equal(challenge, pending.challenge) {
		return s.encodeErrorResponse(reqID, "challenge_mismatch", "proof bytes do not match challenge request", false), nil
	}
	if !ed25519.Verify(pending.requesterSigningPublicKey, challenge, signature) {
		return s.encodeErrorResponse(reqID, "invalid_signature", "challenge signature verification failed", false), nil
	}

	entitlement, claims, capabilityToken, err := s.licenseService.IssueCapabilityGrant(pending.requesterXPub, pending.requesterPeerID)
	if err != nil {
		return s.encodeErrorResponse(reqID, "grant_denied", err.Error(), false), nil
	}

	publicationCID, asset, err := s.registry.EnsurePublicationCID(context.Background(), pending.moduleID)
	if err != nil {
		return nil, fmt.Errorf("ensure publication cid: %w", err)
	}
	contentKey, err := s.registry.PluginRegistry().ReadBundleKey(pending.moduleID)
	if err != nil {
		return nil, fmt.Errorf("read bundle key: %w", err)
	}
	wrappedKey, err := wrapBundleKey(contentKey, pending.requesterEncryptionPublicKey)
	if err != nil {
		return nil, fmt.Errorf("wrap bundle key: %w", err)
	}

	grantSignature, err := s.signGrant(reqID, capabilityToken, publicationCID, wrappedKey.ciphertext)
	if err != nil {
		return nil, fmt.Errorf("sign grant response: %w", err)
	}

	builder := flatbuffers.NewBuilder(512)
	reqIDOffset := builder.CreateString(reqID)
	entitlementStatusOffset := builder.CreateString(entitlement.Status)
	capabilityTokenOffset := builder.CreateString(capabilityToken)
	grantSignatureOffset := builder.CreateByteVector(grantSignature)
	bundleDescriptorOffset := s.buildBundleDescriptor(builder, asset, publicationCID)
	wrappedKeyOffset := s.buildWrappedContentKey(builder, wrappedKey, pending.requesterEncryptionPublicKey)

	schema.GrantResponseStart(builder)
	schema.GrantResponseAddSchemaVersion(builder, schemaVersion)
	schema.GrantResponseAddReqId(builder, reqIDOffset)
	schema.GrantResponseAddEntitlementStatus(builder, entitlementStatusOffset)
	schema.GrantResponseAddCapabilityToken(builder, capabilityTokenOffset)
	schema.GrantResponseAddExpiresAtMs(builder, uint64(claims.Exp*1000))
	schema.GrantResponseAddGrantSignature(builder, grantSignatureOffset)
	schema.GrantResponseAddBundleDescriptor(builder, bundleDescriptorOffset)
	schema.GrantResponseAddWrappedContentKey(builder, wrappedKeyOffset)
	grantResponseOffset := schema.GrantResponseEnd(builder)

	schema.ModuleDeliveryMessageStart(builder)
	schema.ModuleDeliveryMessageAddSchemaVersion(builder, schemaVersion)
	schema.ModuleDeliveryMessageAddMessageType(builder, schema.ModuleDeliveryMessageTypeGRANT_RESPONSE)
	schema.ModuleDeliveryMessageAddGrantResponse(builder, grantResponseOffset)
	messageOffset := schema.ModuleDeliveryMessageEnd(builder)
	schema.FinishModuleDeliveryMessageBuffer(builder, messageOffset)
	return builder.FinishedBytes(), nil
}

func (s *Service) cleanupPending(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for reqID, pending := range s.pending {
		if pending.expiresAt.Before(now) {
			delete(s.pending, reqID)
		}
	}
}

func (s *Service) encodeErrorResponse(reqID, code, message string, retryable bool) []byte {
	builder := flatbuffers.NewBuilder(192)
	reqIDOffset := builder.CreateString(strings.TrimSpace(reqID))
	codeOffset := builder.CreateString(strings.TrimSpace(code))
	messageOffset := builder.CreateString(strings.TrimSpace(message))

	schema.ErrorResponseStart(builder)
	schema.ErrorResponseAddSchemaVersion(builder, schemaVersion)
	schema.ErrorResponseAddReqId(builder, reqIDOffset)
	schema.ErrorResponseAddCode(builder, codeOffset)
	schema.ErrorResponseAddMessage(builder, messageOffset)
	schema.ErrorResponseAddRetryable(builder, retryable)
	errorOffset := schema.ErrorResponseEnd(builder)

	schema.ModuleDeliveryMessageStart(builder)
	schema.ModuleDeliveryMessageAddSchemaVersion(builder, schemaVersion)
	schema.ModuleDeliveryMessageAddMessageType(builder, schema.ModuleDeliveryMessageTypeERROR_RESPONSE)
	schema.ModuleDeliveryMessageAddErrorResponse(builder, errorOffset)
	messageOffsetRoot := schema.ModuleDeliveryMessageEnd(builder)
	schema.FinishModuleDeliveryMessageBuffer(builder, messageOffsetRoot)
	return builder.FinishedBytes()
}

type wrappedBundleKey struct {
	ephemeralPublicKey []byte
	nonce              []byte
	ciphertext         []byte
	tag                []byte
}

func wrapBundleKey(contentKey, recipientPublicKey []byte) (*wrappedBundleKey, error) {
	if len(contentKey) == 0 {
		return nil, fmt.Errorf("content key is required")
	}
	if len(recipientPublicKey) != 32 {
		return nil, fmt.Errorf("recipient public key must be 32 bytes")
	}

	ephemeralPrivateKey := make([]byte, 32)
	if _, err := rand.Read(ephemeralPrivateKey); err != nil {
		return nil, fmt.Errorf("generate ephemeral private key: %w", err)
	}
	ephemeralPrivateKey[0] &= 248
	ephemeralPrivateKey[31] &= 127
	ephemeralPrivateKey[31] |= 64

	ephemeralPublicKey, err := curve25519.X25519(ephemeralPrivateKey, curve25519.Basepoint)
	if err != nil {
		return nil, fmt.Errorf("derive ephemeral public key: %w", err)
	}
	sharedSecret, err := curve25519.X25519(ephemeralPrivateKey, recipientPublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}

	derivedKey := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, sharedSecret, nil, wrapInfo), derivedKey); err != nil {
		return nil, fmt.Errorf("derive wrapping key: %w", err)
	}

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return nil, fmt.Errorf("create wrapping cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create wrapping gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate wrapping nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, contentKey, nil)
	tagSize := gcm.Overhead()
	return &wrappedBundleKey{
		ephemeralPublicKey: ephemeralPublicKey,
		nonce:              nonce,
		ciphertext:         sealed[:len(sealed)-tagSize],
		tag:                sealed[len(sealed)-tagSize:],
	}, nil
}

func (s *Service) signGrant(reqID, capabilityToken, publicationCID string, ciphertext []byte) ([]byte, error) {
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte(reqID),
		[]byte(capabilityToken),
		[]byte(publicationCID),
		ciphertext,
	}, []byte{0}))
	return s.licenseService.Sign(digest[:])
}

func (s *Service) buildBundleDescriptor(builder *flatbuffers.Builder, asset *license.PluginAsset, publicationCID string) flatbuffers.UOffsetT {
	cidOffset := builder.CreateString(publicationCID)
	contentHash, _ := hex.DecodeString(strings.TrimSpace(asset.BundleSHA256))
	contentHashOffset := builder.CreateByteVector(contentHash)
	moduleIDOffset := builder.CreateString(asset.ID)
	moduleVersionOffset := builder.CreateString(asset.Version)
	runtimeOffset := builder.CreateString("wasm")
	abiOffset := builder.CreateString("module-sdk/async-host-v1")
	entrypointOffset := builder.CreateString("plugin_invoke_stream")
	publicationCIDOffset := builder.CreateString(publicationCID)
	contentCodec := strings.TrimSpace(asset.ContentType)
	if contentCodec == "" {
		contentCodec = "application/wasm+encrypted"
	}
	contentCodecOffset := builder.CreateString(contentCodec)
	encryptionCodecOffset := builder.CreateString("x25519-hkdf-sha256-aes-256-gcm")

	schema.BundleDescriptorStart(builder)
	schema.BundleDescriptorAddSchemaVersion(builder, schemaVersion)
	schema.BundleDescriptorAddCid(builder, cidOffset)
	schema.BundleDescriptorAddContentHash(builder, contentHashOffset)
	schema.BundleDescriptorAddSizeBytes(builder, uint64(asset.SizeBytes))
	schema.BundleDescriptorAddModuleId(builder, moduleIDOffset)
	schema.BundleDescriptorAddModuleVersion(builder, moduleVersionOffset)
	schema.BundleDescriptorAddRuntime(builder, runtimeOffset)
	schema.BundleDescriptorAddAbi(builder, abiOffset)
	schema.BundleDescriptorAddEntrypoint(builder, entrypointOffset)
	schema.BundleDescriptorAddPublicationCid(builder, publicationCIDOffset)
	schema.BundleDescriptorAddContentCodec(builder, contentCodecOffset)
	schema.BundleDescriptorAddEncryptionCodec(builder, encryptionCodecOffset)
	return schema.BundleDescriptorEnd(builder)
}

func (s *Service) buildWrappedContentKey(builder *flatbuffers.Builder, wrappedKey *wrappedBundleKey, recipientPublicKey []byte) flatbuffers.UOffsetT {
	algorithmOffset := builder.CreateString(wrapAlgorithm)
	recipientKeyIDOffset := builder.CreateString(hex.EncodeToString(recipientPublicKey))
	recipientPubOffset := builder.CreateByteVector(recipientPublicKey)
	ephemeralPubOffset := builder.CreateByteVector(wrappedKey.ephemeralPublicKey)
	nonceOffset := builder.CreateByteVector(wrappedKey.nonce)
	ciphertextOffset := builder.CreateByteVector(wrappedKey.ciphertext)
	tagOffset := builder.CreateByteVector(wrappedKey.tag)

	schema.WrappedContentKeyStart(builder)
	schema.WrappedContentKeyAddSchemaVersion(builder, schemaVersion)
	schema.WrappedContentKeyAddWrappingAlgorithm(builder, algorithmOffset)
	schema.WrappedContentKeyAddRecipientKeyId(builder, recipientKeyIDOffset)
	schema.WrappedContentKeyAddRecipientPublicKey(builder, recipientPubOffset)
	schema.WrappedContentKeyAddEphemeralPublicKey(builder, ephemeralPubOffset)
	schema.WrappedContentKeyAddNonce(builder, nonceOffset)
	schema.WrappedContentKeyAddCiphertext(builder, ciphertextOffset)
	schema.WrappedContentKeyAddTag(builder, tagOffset)
	return schema.WrappedContentKeyEnd(builder)
}
