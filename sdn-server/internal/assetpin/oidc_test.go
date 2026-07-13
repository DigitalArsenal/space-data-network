package assetpin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

const (
	testAudience         = "sdn-asset-models"
	testRepository       = "DigitalArsenal/asset-models"
	testRef              = "refs/heads/main"
	testPinWorkflow      = "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main"
	testDecisionWorkflow = "DigitalArsenal/asset-models/.github/workflows/review-decision.yml@refs/heads/main"
	testActor            = "asset-maintainer"
	testRunID            = "123456789"
	testRunAttempt       = "2"
	testSHA              = "0123456789abcdef0123456789abcdef01234567"
)

func TestVerifierAcceptsValidUploadAndDecisionTokens(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name     string
		kind     WorkflowKind
		workflow string
	}{
		{name: "upload", kind: WorkflowPin, workflow: testPinWorkflow},
		{name: "decision", kind: WorkflowDecision, workflow: testDecisionWorkflow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receipts []testTokenReceipt
			verifier := newTestVerifier(t, provider, func(_ context.Context, digest string, expiresAt time.Time, claims Claims) error {
				receipts = append(receipts, testTokenReceipt{
					digest:    digest,
					expiresAt: expiresAt,
					claims:    claims,
				})
				return nil
			})
			expiresAt := now.Add(5 * time.Minute)
			tokenClaims := provider.validClaims(now, expiresAt, tt.workflow)
			rawToken := provider.sign(t, tokenClaims, provider.privateKey)

			got, err := verifier.VerifyAndConsume(context.Background(), rawToken, tt.kind)
			if err != nil {
				t.Fatalf("VerifyAndConsume() = %v", err)
			}
			want := expectedClaims(expiresAt, tt.workflow)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("VerifyAndConsume() claims = %#v, want %#v", got, want)
			}
			if len(receipts) != 1 {
				t.Fatalf("receipt consumer calls = %d, want 1", len(receipts))
			}
			wantDigestBytes := sha256.Sum256([]byte(rawToken))
			wantDigest := hex.EncodeToString(wantDigestBytes[:])
			if receipts[0].digest != wantDigest {
				t.Fatalf("receipt digest = %q, want SHA-256 digest %q", receipts[0].digest, wantDigest)
			}
			if len(receipts[0].digest) != 64 || receipts[0].digest != strings.ToLower(receipts[0].digest) {
				t.Fatalf("receipt digest = %q, want 64 lowercase hexadecimal characters", receipts[0].digest)
			}
			if receipts[0].expiresAt.Location() != time.UTC || !receipts[0].expiresAt.Equal(expiresAt) {
				t.Fatalf("receipt expiry = %v, want %v in UTC", receipts[0].expiresAt, expiresAt)
			}
			if !reflect.DeepEqual(receipts[0].claims, want) {
				t.Fatalf("receipt claims = %#v, want %#v", receipts[0].claims, want)
			}
		})
	}
}

func TestVerifierRejectsInvalidTokensWithoutLeakingClaims(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() = %v", err)
	}

	tests := []struct {
		name   string
		kind   WorkflowKind
		mutate func(map[string]any)
		signer *rsa.PrivateKey
		raw    string
	}{
		{
			name: "wrong audience",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["aud"] = "sensitive-wrong-audience"
			},
		},
		{
			name: "wrong issuer",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["iss"] = "https://sensitive-wrong-issuer.invalid"
			},
		},
		{
			name: "wrong repository",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["repository"] = "sensitive/wrong-repository"
			},
		},
		{
			name: "wrong ref",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["ref"] = "refs/heads/sensitive-wrong-ref"
			},
		},
		{
			name: "wrong upload workflow",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["workflow_ref"] = "sensitive/wrong-upload-workflow"
			},
		},
		{
			name: "wrong decision workflow",
			kind: WorkflowDecision,
			mutate: func(claims map[string]any) {
				claims["workflow_ref"] = "sensitive/wrong-decision-workflow"
			},
		},
		{
			name: "expired",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["exp"] = now.Add(-time.Minute).Unix()
			},
		},
		{
			name: "not yet valid",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["nbf"] = now.Add(10 * time.Minute).Unix()
			},
		},
		{
			name: "missing actor",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["actor"] = ""
			},
		},
		{
			name: "missing run id",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				delete(claims, "run_id")
			},
		},
		{
			name: "missing run attempt",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["run_attempt"] = ""
			},
		},
		{
			name: "missing sha",
			kind: WorkflowPin,
			mutate: func(claims map[string]any) {
				claims["sha"] = ""
			},
		},
		{
			name:   "wrong signature",
			kind:   WorkflowPin,
			signer: otherKey,
		},
		{
			name: "unknown workflow kind",
			kind: WorkflowKind("sensitive-unknown-kind"),
		},
		{
			name: "malformed token",
			kind: WorkflowPin,
			raw:  "sensitive-not-a-jwt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consumerCalls := 0
			verifier := newTestVerifier(t, provider, func(context.Context, string, time.Time, Claims) error {
				consumerCalls++
				return nil
			})
			workflow := testPinWorkflow
			if tt.kind == WorkflowDecision {
				workflow = testDecisionWorkflow
			}
			tokenClaims := provider.validClaims(now, now.Add(5*time.Minute), workflow)
			if tt.mutate != nil {
				tt.mutate(tokenClaims)
			}
			rawToken := tt.raw
			if rawToken == "" {
				signer := tt.signer
				if signer == nil {
					signer = provider.privateKey
				}
				rawToken = provider.sign(t, tokenClaims, signer)
			}

			got, err := verifier.VerifyAndConsume(context.Background(), rawToken, tt.kind)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyAndConsume() error = %v, want ErrInvalidToken", err)
			}
			if err.Error() != ErrInvalidToken.Error() {
				t.Fatalf("VerifyAndConsume() error = %q, want sanitized %q", err, ErrInvalidToken)
			}
			if got != (Claims{}) {
				t.Fatalf("VerifyAndConsume() claims = %#v, want zero claims", got)
			}
			if consumerCalls != 0 {
				t.Fatalf("receipt consumer calls = %d, want 0", consumerCalls)
			}
			assertNoSensitiveTokenData(t, err, rawToken, tokenClaims)
		})
	}
}

func TestVerifierRejectsMissingToken(t *testing.T) {
	provider := newTestOIDCProvider(t)
	consumerCalls := 0
	verifier := newTestVerifier(t, provider, func(context.Context, string, time.Time, Claims) error {
		consumerCalls++
		return nil
	})

	for _, rawToken := range []string{"", " \t\n"} {
		got, err := verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin)
		if !errors.Is(err, ErrMissingToken) {
			t.Fatalf("VerifyAndConsume(%q) error = %v, want ErrMissingToken", rawToken, err)
		}
		if err.Error() != ErrMissingToken.Error() {
			t.Fatalf("VerifyAndConsume(%q) error = %q, want sanitized %q", rawToken, err, ErrMissingToken)
		}
		if got != (Claims{}) {
			t.Fatalf("VerifyAndConsume(%q) claims = %#v, want zero claims", rawToken, got)
		}
	}
	if consumerCalls != 0 {
		t.Fatalf("receipt consumer calls = %d, want 0", consumerCalls)
	}
}

func TestVerifierRejectsSecondUseOfTokenDigest(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	rawToken := provider.sign(t, provider.validClaims(now, now.Add(5*time.Minute), testPinWorkflow), provider.privateKey)
	seen := make(map[string]struct{})
	consumerCalls := 0
	verifier := newTestVerifier(t, provider, func(_ context.Context, digest string, _ time.Time, _ Claims) error {
		consumerCalls++
		if _, exists := seen[digest]; exists {
			return fmt.Errorf("duplicate receipt: %w", ErrTokenReplay)
		}
		seen[digest] = struct{}{}
		return nil
	})

	if _, err := verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin); err != nil {
		t.Fatalf("first VerifyAndConsume() = %v", err)
	}
	got, err := verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin)
	if !errors.Is(err, ErrTokenReplay) {
		t.Fatalf("second VerifyAndConsume() error = %v, want ErrTokenReplay", err)
	}
	if err.Error() != ErrTokenReplay.Error() {
		t.Fatalf("second VerifyAndConsume() error = %q, want sanitized %q", err, ErrTokenReplay)
	}
	if got != (Claims{}) {
		t.Fatalf("second VerifyAndConsume() claims = %#v, want zero claims", got)
	}
	if consumerCalls != 2 {
		t.Fatalf("receipt consumer calls = %d, want 2", consumerCalls)
	}
	if strings.Contains(err.Error(), rawToken) {
		t.Fatal("replay error leaked the raw token")
	}
}

type testTokenReceipt struct {
	digest    string
	expiresAt time.Time
	claims    Claims
}

type testOIDCProvider struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	keyID      string
}

func newTestOIDCProvider(t *testing.T) *testOIDCProvider {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() = %v", err)
	}
	provider := &testOIDCProvider{
		privateKey: privateKey,
		keyID:      "test-key",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                provider.server.URL,
			"authorization_endpoint":                provider.server.URL + "/authorize",
			"token_endpoint":                        provider.server.URL + "/token",
			"jwks_uri":                              provider.server.URL + "/keys",
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}); err != nil {
			t.Errorf("encode discovery document: %v", err)
		}
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		publicKey := provider.privateKey.PublicKey
		if err := json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"use": "sig",
				"kid": provider.keyID,
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
			}},
		}); err != nil {
			t.Errorf("encode JWKS: %v", err)
		}
	})
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *testOIDCProvider) config() config.AssetPinConfig {
	return config.AssetPinConfig{
		Issuer:           p.server.URL,
		Audience:         testAudience,
		Repository:       testRepository,
		Ref:              testRef,
		PinWorkflow:      testPinWorkflow,
		DecisionWorkflow: testDecisionWorkflow,
	}
}

func (p *testOIDCProvider) validClaims(now, expiresAt time.Time, workflow string) map[string]any {
	return map[string]any{
		"iss":          p.server.URL,
		"sub":          "repo:" + testRepository + ":ref:" + testRef,
		"aud":          testAudience,
		"iat":          now.Add(-time.Minute).Unix(),
		"nbf":          now.Add(-time.Minute).Unix(),
		"exp":          expiresAt.Unix(),
		"repository":   testRepository,
		"ref":          testRef,
		"workflow_ref": workflow,
		"actor":        testActor,
		"run_id":       testRunID,
		"run_attempt":  testRunAttempt,
		"sha":          testSHA,
	}
}

func (p *testOIDCProvider) sign(t *testing.T, claims map[string]any, privateKey *rsa.PrivateKey) string {
	t.Helper()

	headerJSON, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"kid": p.keyID,
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("json.Marshal(header) = %v", err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("json.Marshal(claims) = %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(payloadJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("rsa.SignPKCS1v15() = %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func newTestVerifier(t *testing.T, provider *testOIDCProvider, consumer TokenReceiptConsumer) *Verifier {
	t.Helper()

	verifier, err := NewVerifier(context.Background(), provider.config(), consumer)
	if err != nil {
		t.Fatalf("NewVerifier() = %v", err)
	}
	return verifier
}

func expectedClaims(expiresAt time.Time, workflow string) Claims {
	return Claims{
		Repository:  testRepository,
		Ref:         testRef,
		WorkflowRef: workflow,
		Actor:       testActor,
		RunID:       testRunID,
		RunAttempt:  testRunAttempt,
		SHA:         testSHA,
		ExpiresAt:   expiresAt.Unix(),
	}
}

func assertNoSensitiveTokenData(t *testing.T, err error, rawToken string, claims map[string]any) {
	t.Helper()

	errorText := err.Error()
	if strings.Contains(errorText, rawToken) {
		t.Fatal("verification error leaked the raw token")
	}
	for _, claimName := range []string{"repository", "ref", "workflow_ref", "actor", "run_id", "run_attempt", "sha"} {
		value, _ := claims[claimName].(string)
		if value != "" && strings.Contains(errorText, value) {
			t.Fatalf("verification error leaked %s", claimName)
		}
	}
}
