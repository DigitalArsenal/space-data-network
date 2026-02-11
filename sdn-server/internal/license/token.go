package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultVerifyLeeway = 30 * time.Second
)

var (
	// ErrMissingAuthorization indicates Authorization header is missing.
	ErrMissingAuthorization = errors.New("missing authorization header")
	// ErrInvalidAuthorization indicates Authorization header format is invalid.
	ErrInvalidAuthorization = errors.New("invalid authorization header")
	// ErrInvalidTokenFormat indicates compact token format is invalid.
	ErrInvalidTokenFormat = errors.New("invalid token format")
	// ErrInvalidTokenSignature indicates signature verification failed.
	ErrInvalidTokenSignature = errors.New("invalid token signature")
	// ErrTokenExpired indicates token has expired.
	ErrTokenExpired = errors.New("token expired")
	// ErrTokenNotYetValid indicates token iat is in the future.
	ErrTokenNotYetValid = errors.New("token not yet valid")
	// ErrTokenIssuerMismatch indicates token issuer does not match verifier expectation.
	ErrTokenIssuerMismatch = errors.New("token issuer mismatch")
	// ErrTokenPeerIDMismatch indicates token peer_id does not match expected peer.
	ErrTokenPeerIDMismatch = errors.New("token peer_id mismatch")
	// ErrTokenMissingScope indicates one or more required scopes are absent.
	ErrTokenMissingScope = errors.New("token missing required scope")
)

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// CapabilityClaims are server-signed claims used to authorize paid capabilities.
type CapabilityClaims struct {
	Iss    string   `json:"iss"`
	Sub    string   `json:"sub"`
	PeerID string   `json:"peer_id"`
	Plan   string   `json:"plan"`
	Scopes []string `json:"scopes"`
	Iat    int64    `json:"iat"`
	Exp    int64    `json:"exp"`
	JTI    string   `json:"jti"`
}

// VerifyOptions controls claim verification.
type VerifyOptions struct {
	Now            time.Time
	Leeway         time.Duration
	Issuer         string
	ExpectedPeerID string
	RequiredScopes []string
}

// TokenVerifier validates capability tokens against a known public key.
type TokenVerifier struct {
	publicKey ed25519.PublicKey
	issuer    string
	leeway    time.Duration
}

// NewTokenVerifier creates a token verifier.
func NewTokenVerifier(publicKey ed25519.PublicKey, issuer string) *TokenVerifier {
	return &TokenVerifier{
		publicKey: append(ed25519.PublicKey(nil), publicKey...),
		issuer:    issuer,
		leeway:    defaultVerifyLeeway,
	}
}

// VerifyAuthorizationHeader verifies a Bearer token from an HTTP Authorization header.
func (v *TokenVerifier) VerifyAuthorizationHeader(authHeader, expectedPeerID string, requiredScopes []string) (*CapabilityClaims, error) {
	token, err := ExtractBearerToken(authHeader)
	if err != nil {
		return nil, err
	}
	return v.VerifyToken(token, expectedPeerID, requiredScopes)
}

// VerifyToken verifies a compact capability token.
func (v *TokenVerifier) VerifyToken(token, expectedPeerID string, requiredScopes []string) (*CapabilityClaims, error) {
	opts := VerifyOptions{
		Issuer:         v.issuer,
		ExpectedPeerID: expectedPeerID,
		RequiredScopes: requiredScopes,
		Leeway:         v.leeway,
	}
	return VerifyCapabilityToken(token, v.publicKey, opts)
}

// ExtractBearerToken parses an Authorization header and returns the bearer token.
func ExtractBearerToken(authHeader string) (string, error) {
	header := strings.TrimSpace(authHeader)
	if header == "" {
		return "", ErrMissingAuthorization
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", ErrInvalidAuthorization
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", ErrInvalidAuthorization
	}
	return token, nil
}

// SignCapabilityToken signs capability claims with Ed25519 in compact JWT-like form.
func SignCapabilityToken(claims CapabilityClaims, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid private key size: %d", len(privateKey))
	}
	header := tokenHeader{
		Alg: "EdDSA",
		Typ: "JWT",
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := headerB64 + "." + payloadB64
	signature := ed25519.Sign(privateKey, []byte(signingInput))
	sigB64 := base64.RawURLEncoding.EncodeToString(signature)
	return signingInput + "." + sigB64, nil
}

// VerifyCapabilityToken verifies signature and registered claims.
func VerifyCapabilityToken(token string, publicKey ed25519.PublicKey, opts VerifyOptions) (*CapabilityClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidTokenFormat
	}
	signingInput := parts[0] + "." + parts[1]

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidTokenFormat
	}
	if !ed25519.Verify(publicKey, []byte(signingInput), signature) {
		return nil, ErrInvalidTokenSignature
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidTokenFormat
	}
	var claims CapabilityClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrInvalidTokenFormat
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	leeway := opts.Leeway
	if leeway <= 0 {
		leeway = defaultVerifyLeeway
	}

	if opts.Issuer != "" && claims.Iss != opts.Issuer {
		return nil, ErrTokenIssuerMismatch
	}
	if opts.ExpectedPeerID != "" && claims.PeerID != opts.ExpectedPeerID {
		return nil, ErrTokenPeerIDMismatch
	}

	nowUnix := now.Unix()
	if claims.Iat > nowUnix+int64(leeway.Seconds()) {
		return nil, ErrTokenNotYetValid
	}
	if claims.Exp <= nowUnix-int64(leeway.Seconds()) {
		return nil, ErrTokenExpired
	}

	for _, required := range opts.RequiredScopes {
		if !hasScope(claims.Scopes, required) {
			return nil, fmt.Errorf("%w: %s", ErrTokenMissingScope, required)
		}
	}

	return &claims, nil
}

func hasScope(scopes []string, required string) bool {
	for _, s := range scopes {
		if s == required {
			return true
		}
	}
	return false
}
