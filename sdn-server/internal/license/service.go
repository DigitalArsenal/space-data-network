package license

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	logging "github.com/ipfs/go-log/v2"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	defaultChallengeTTL  = 60 * time.Second
	defaultTokenTTL      = 15 * time.Minute
	defaultClockSkew     = 2 * time.Minute
	defaultRequestMaxLen = 64 * 1024
)

var log = logging.Logger("sdn-license")

type serviceOptions struct {
	entitlementDBPath string
	signingKeyPath    string
	challengeTTL      time.Duration
	tokenTTL          time.Duration
	clockSkew         time.Duration
}

type pendingChallenge struct {
	reqID      string
	xpub       string
	peerID     string
	pubKey     ed25519.PublicKey
	challenge  []byte
	expiresAt  time.Time
	createdAt  time.Time
	remotePeer string
}

// Service handles challenge/proof/grant exchange over libp2p streams.
type Service struct {
	store      *EntitlementStore
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	issuer     string
	verifier   *TokenVerifier
	plugins    *PluginRegistry

	challengeTTL time.Duration
	tokenTTL     time.Duration
	clockSkew    time.Duration

	mu         sync.Mutex
	challenges map[string]pendingChallenge
}

// NewService creates a license service rooted in baseDataPath.
func NewService(baseDataPath, issuer string) (*Service, error) {
	baseDataPath = strings.TrimSpace(baseDataPath)
	if baseDataPath == "" {
		return nil, errors.New("base data path is required")
	}
	licenseDir := filepath.Join(baseDataPath, "license")
	opts := serviceOptions{
		entitlementDBPath: filepath.Join(licenseDir, defaultEntitlementDB),
		signingKeyPath:    filepath.Join(licenseDir, "token_signing_ed25519.seed"),
		challengeTTL:      defaultChallengeTTL,
		tokenTTL:          defaultTokenTTL,
		clockSkew:         defaultClockSkew,
	}
	return newServiceWithOptions(baseDataPath, issuer, opts)
}

func newServiceWithOptions(baseDataPath, issuer string, opts serviceOptions) (*Service, error) {
	if err := os.MkdirAll(filepath.Join(baseDataPath, "license"), 0700); err != nil {
		return nil, fmt.Errorf("create license dir: %w", err)
	}
	priv, err := loadOrCreateEd25519Key(opts.signingKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load token signing key: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	store, err := NewEntitlementStore(opts.entitlementDBPath)
	if err != nil {
		return nil, err
	}
	if issuer == "" {
		issuer = "spaceaware-license"
	}
	pluginRoot := strings.TrimSpace(os.Getenv("SDN_PLUGIN_ROOT"))
	if pluginRoot == "" {
		pluginRoot = DefaultPluginRoot(baseDataPath)
	}
	plugins, err := LoadPluginRegistry(pluginRoot)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("load plugin registry: %w", err)
	}
	svc := &Service{
		store:        store,
		privateKey:   priv,
		publicKey:    pub,
		issuer:       issuer,
		plugins:      plugins,
		challengeTTL: opts.challengeTTL,
		tokenTTL:     opts.tokenTTL,
		clockSkew:    opts.clockSkew,
		challenges:   make(map[string]pendingChallenge),
	}
	svc.verifier = NewTokenVerifier(pub, issuer)
	if plugins.Count() > 0 {
		log.Infof("Loaded %d encrypted plugin bundle(s) from %s", plugins.Count(), pluginRoot)
	}
	return svc, nil
}

// Close releases resources.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	return s.store.Close()
}

// Verifier exposes token verification for HTTP/API layer.
func (s *Service) Verifier() *TokenVerifier {
	return s.verifier
}

// PluginRegistry exposes encrypted plugin catalog metadata.
func (s *Service) PluginRegistry() *PluginRegistry {
	return s.plugins
}

// PublicKeyHex returns the token verification key for distribution.
func (s *Service) PublicKeyHex() string {
	return hex.EncodeToString(s.publicKey)
}

// GetEntitlement returns entitlement for xpub.
func (s *Service) GetEntitlement(xpub string) (*Entitlement, error) {
	return s.store.GetEntitlement(xpub)
}

// UpsertEntitlement updates entitlement state.
func (s *Service) UpsertEntitlement(ent *Entitlement) error {
	return s.store.UpsertEntitlement(ent)
}

// HandleStream handles newline-delimited JSON messages over /orbpro/license/1.0.0.
func (s *Service) HandleStream(stream network.Stream) {
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(io.LimitReader(stream, defaultRequestMaxLen))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		if !errors.Is(err, io.EOF) {
			log.Debugf("license stream read error from %s: %v", stream.Conn().RemotePeer().ShortString(), err)
			return
		}
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		_ = s.writeResponse(stream, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "invalid_request",
			Message: "empty request body",
		})
		return
	}

	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		_ = s.writeResponse(stream, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "invalid_json",
			Message: "malformed JSON request",
		})
		return
	}

	switch envelope.Type {
	case msgTypeChallengeRequest:
		var req ChallengeRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = s.writeResponse(stream, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "invalid challenge_request payload"})
			return
		}
		resp, apiErr := s.handleChallengeRequest(req, stream.Conn().LocalPeer().String(), stream.Conn().RemotePeer().String())
		if apiErr != nil {
			_ = s.writeResponse(stream, *apiErr)
			return
		}
		_ = s.writeResponse(stream, *resp)
	case msgTypeProofRequest:
		var req ProofRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = s.writeResponse(stream, ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "invalid proof_request payload"})
			return
		}
		resp, apiErr := s.handleProofRequest(req)
		if apiErr != nil {
			_ = s.writeResponse(stream, *apiErr)
			return
		}
		_ = s.writeResponse(stream, *resp)
	default:
		_ = s.writeResponse(stream, ErrorResponse{
			Type:    msgTypeErrorResponse,
			Code:    "unsupported_type",
			Message: "unsupported message type",
		})
	}
}

func (s *Service) handleChallengeRequest(req ChallengeRequest, serverPeerID, remotePeerID string) (*ChallengeResponse, *ErrorResponse) {
	req.ReqID = strings.TrimSpace(req.ReqID)
	req.XPub = strings.TrimSpace(req.XPub)
	req.PeerID = strings.TrimSpace(req.PeerID)
	req.ClientPubKeyHex = strings.TrimPrefix(strings.TrimSpace(req.ClientPubKeyHex), "0x")

	if req.ReqID == "" || req.XPub == "" || req.PeerID == "" || req.ClientPubKeyHex == "" {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "req_id, xpub, peer_id, and client_pubkey_hex are required"}
	}
	if !withinClockSkew(req.TS, s.clockSkew) {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_timestamp", Message: "timestamp outside allowable skew"}
	}

	pubRaw, err := hex.DecodeString(req.ClientPubKeyHex)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_public_key", Message: "client_pubkey_hex must be 32-byte Ed25519 hex"}
	}
	derivedPeerID, err := peerIDFromEd25519(pubRaw)
	if err != nil {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_public_key", Message: "unable to derive peer_id from public key"}
	}
	if derivedPeerID != req.PeerID {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "peer_id_mismatch", Message: "peer_id does not match client public key"}
	}

	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "server_error", Message: "failed to generate challenge"}
	}

	now := time.Now().UTC()
	s.cleanupChallenges(now)

	s.mu.Lock()
	s.challenges[req.ReqID] = pendingChallenge{
		reqID:      req.ReqID,
		xpub:       req.XPub,
		peerID:     req.PeerID,
		pubKey:     append(ed25519.PublicKey(nil), pubRaw...),
		challenge:  challenge,
		createdAt:  now,
		expiresAt:  now.Add(s.challengeTTL),
		remotePeer: remotePeerID,
	}
	s.mu.Unlock()

	resp := &ChallengeResponse{
		Type:         msgTypeChallengeResponse,
		ReqID:        req.ReqID,
		Challenge:    base64.RawStdEncoding.EncodeToString(challenge),
		ExpiresAt:    now.Add(s.challengeTTL).Unix(),
		ServerPeerID: serverPeerID,
	}
	return resp, nil
}

func (s *Service) handleProofRequest(req ProofRequest) (*GrantResponse, *ErrorResponse) {
	req.ReqID = strings.TrimSpace(req.ReqID)
	req.XPub = strings.TrimSpace(req.XPub)
	req.PeerID = strings.TrimSpace(req.PeerID)
	req.SignatureHex = strings.TrimPrefix(strings.TrimSpace(req.SignatureHex), "0x")
	req.Challenge = strings.TrimSpace(req.Challenge)

	if req.ReqID == "" || req.XPub == "" || req.PeerID == "" || req.SignatureHex == "" || req.Challenge == "" {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_request", Message: "req_id, xpub, peer_id, challenge, and signature_hex are required"}
	}
	if !withinClockSkew(req.TS, s.clockSkew) {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_timestamp", Message: "timestamp outside allowable skew"}
	}

	challengeRaw, err := base64.RawStdEncoding.DecodeString(req.Challenge)
	if err != nil {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_challenge", Message: "challenge must be base64"}
	}
	signature, err := hex.DecodeString(req.SignatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "invalid_signature", Message: "signature_hex must be 64-byte Ed25519 signature hex"}
	}

	now := time.Now().UTC()
	s.cleanupChallenges(now)

	s.mu.Lock()
	pending, ok := s.challenges[req.ReqID]
	if ok {
		delete(s.challenges, req.ReqID) // single-use challenge
	}
	s.mu.Unlock()
	if !ok {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "challenge_not_found", Message: "challenge not found or expired"}
	}
	if pending.expiresAt.Before(now) {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "challenge_expired", Message: "challenge expired"}
	}
	if pending.xpub != req.XPub || pending.peerID != req.PeerID {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "challenge_mismatch", Message: "challenge context mismatch"}
	}
	if !bytes.Equal(pending.challenge, challengeRaw) {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "challenge_mismatch", Message: "challenge bytes mismatch"}
	}
	if !ed25519.Verify(pending.pubKey, challengeRaw, signature) {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "signature_invalid", Message: "signature verification failed"}
	}

	ent, err := s.store.GetOrCreateEntitlement(req.XPub, req.PeerID)
	if err != nil {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "server_error", Message: "failed to load entitlement"}
	}
	if !ent.IsActive(now) {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "entitlement_inactive", Message: "subscription is not active"}
	}

	exp := now.Add(s.tokenTTL)
	if ent.ExpiresAt > 0 {
		entExp := time.Unix(ent.ExpiresAt, 0)
		if entExp.Before(exp) {
			exp = entExp
		}
	}
	claims := CapabilityClaims{
		Iss:    s.issuer,
		Sub:    req.XPub,
		PeerID: req.PeerID,
		Plan:   ent.Plan,
		Scopes: scopesForPlan(ent.Plan),
		Iat:    now.Unix(),
		Exp:    exp.Unix(),
		JTI:    uuid.NewString(),
	}
	token, err := SignCapabilityToken(claims, s.privateKey)
	if err != nil {
		return nil, &ErrorResponse{Type: msgTypeErrorResponse, Code: "server_error", Message: "failed to sign capability token"}
	}

	return &GrantResponse{
		Type:            msgTypeGrantResponse,
		ReqID:           req.ReqID,
		Entitlement:     *ent,
		CapabilityToken: token,
		ExpiresAt:       claims.Exp,
	}, nil
}

func (s *Service) cleanupChallenges(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for reqID, entry := range s.challenges {
		if entry.expiresAt.Before(now) {
			delete(s.challenges, reqID)
		}
	}
}

func scopesForPlan(plan string) []string {
	normalized := strings.ToLower(strings.TrimSpace(plan))
	scopes := []string{"api:data:read:free", "orbpro:base"}
	switch normalized {
	case "free", "":
		return scopes
	default:
		return append(scopes, "api:data:read:premium", "orbpro:premium")
	}
}

func withinClockSkew(ts int64, skew time.Duration) bool {
	if ts <= 0 {
		return false
	}
	now := time.Now().Unix()
	maxSkew := int64(skew.Seconds())
	if maxSkew <= 0 {
		maxSkew = int64(defaultClockSkew.Seconds())
	}
	diff := now - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= maxSkew
}

func peerIDFromEd25519(raw []byte) (string, error) {
	pub, err := libp2pcrypto.UnmarshalEd25519PublicKey(raw)
	if err != nil {
		return "", err
	}
	id, err := peer.IDFromPublicKey(pub)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (s *Service) writeResponse(stream network.Stream, payload interface{}) error {
	writer := bufio.NewWriter(stream)
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		return err
	}
	return writer.Flush()
}
