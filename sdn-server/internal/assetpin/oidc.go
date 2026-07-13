// Package assetpin implements the security boundary for the asset pin service.
package assetpin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// WorkflowKind identifies the GitHub Actions workflow authorized to call an
// asset pin endpoint.
type WorkflowKind string

const (
	// WorkflowPin authorizes the asset upload and pin workflow.
	WorkflowPin WorkflowKind = "pin"
	// WorkflowDecision authorizes the asset review decision workflow.
	WorkflowDecision WorkflowKind = "decision"
)

var (
	// ErrMissingToken indicates that no OIDC token was supplied.
	ErrMissingToken = errors.New("assetpin: missing token")
	// ErrInvalidToken indicates that token verification or claim validation failed.
	ErrInvalidToken = errors.New("assetpin: invalid token")
	// ErrTokenReplay indicates that a previously consumed token was used again.
	ErrTokenReplay = errors.New("assetpin: token replay")

	errTokenReceiptConsumer = errors.New("assetpin: token receipt consumer failed")
)

// Claims contains the GitHub Actions claims retained for an accepted asset
// workflow request.
type Claims struct {
	Repository  string `json:"repository"`
	Ref         string `json:"ref"`
	WorkflowRef string `json:"workflow_ref"`
	Actor       string `json:"actor"`
	RunID       string `json:"run_id"`
	RunAttempt  string `json:"run_attempt"`
	SHA         string `json:"sha"`
	ExpiresAt   int64  `json:"exp"`
}

// TokenReceiptConsumer atomically records a verified token digest. It should
// return ErrTokenReplay when digest was already consumed.
type TokenReceiptConsumer func(ctx context.Context, digest string, expiresAt time.Time, claims Claims) error

// Verifier verifies GitHub Actions OIDC tokens and consumes each accepted token
// digest exactly once through its TokenReceiptConsumer.
type Verifier struct {
	tokenVerifier    *oidc.IDTokenVerifier
	repository       string
	ref              string
	pinWorkflow      string
	decisionWorkflow string
	consumer         TokenReceiptConsumer
}

// NewVerifier discovers the configured OIDC provider and constructs an asset
// workflow token verifier.
func NewVerifier(ctx context.Context, cfg config.AssetPinConfig, consumer TokenReceiptConsumer) (*Verifier, error) {
	if ctx == nil {
		return nil, errors.New("assetpin: context is required")
	}
	if consumer == nil {
		return nil, errors.New("assetpin: token receipt consumer is required")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "issuer", value: cfg.Issuer},
		{name: "audience", value: cfg.Audience},
		{name: "repository", value: cfg.Repository},
		{name: "ref", value: cfg.Ref},
		{name: "pin workflow", value: cfg.PinWorkflow},
		{name: "decision workflow", value: cfg.DecisionWorkflow},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("assetpin: %s is required", field.name)
		}
	}

	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("assetpin: discover OIDC provider: %w", err)
	}

	return &Verifier{
		tokenVerifier:    provider.Verifier(&oidc.Config{ClientID: cfg.Audience}),
		repository:       cfg.Repository,
		ref:              cfg.Ref,
		pinWorkflow:      cfg.PinWorkflow,
		decisionWorkflow: cfg.DecisionWorkflow,
		consumer:         consumer,
	}, nil
}

// VerifyAndConsume verifies rawToken for kind, validates its GitHub Actions
// claims, and records its SHA-256 digest before returning the accepted claims.
func (v *Verifier) VerifyAndConsume(ctx context.Context, rawToken string, kind WorkflowKind) (Claims, error) {
	if strings.TrimSpace(rawToken) == "" {
		return Claims{}, ErrMissingToken
	}

	wantWorkflow, ok := v.workflow(kind)
	if !ok {
		return Claims{}, ErrInvalidToken
	}

	token, err := v.tokenVerifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := token.Claims(&claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.Repository != v.repository ||
		claims.Ref != v.ref ||
		claims.WorkflowRef != wantWorkflow ||
		strings.TrimSpace(claims.Actor) == "" ||
		strings.TrimSpace(claims.RunID) == "" ||
		strings.TrimSpace(claims.RunAttempt) == "" ||
		strings.TrimSpace(claims.SHA) == "" {
		return Claims{}, ErrInvalidToken
	}

	digestBytes := sha256.Sum256([]byte(rawToken))
	digest := hex.EncodeToString(digestBytes[:])
	if err := v.consumer(ctx, digest, token.Expiry.UTC(), claims); err != nil {
		if errors.Is(err, ErrTokenReplay) {
			return Claims{}, ErrTokenReplay
		}
		return Claims{}, errTokenReceiptConsumer
	}

	return claims, nil
}

func (v *Verifier) workflow(kind WorkflowKind) (string, bool) {
	switch kind {
	case WorkflowPin:
		return v.pinWorkflow, true
	case WorkflowDecision:
		return v.decisionWorkflow, true
	default:
		return "", false
	}
}
