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
	"sync"
	"sync/atomic"
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

func TestVerifierRejectsEquivalentNonCanonicalCompactToken(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	canonicalToken := provider.sign(t, provider.validClaims(now, now.Add(5*time.Minute), testPinWorkflow), provider.privateKey)
	nonCanonicalToken := equivalentNonCanonicalSignatureToken(t, canonicalToken)
	if canonicalToken == nonCanonicalToken {
		t.Fatal("non-canonical token must differ from canonical token")
	}
	canonicalDigest := sha256.Sum256([]byte(canonicalToken))
	nonCanonicalDigest := sha256.Sum256([]byte(nonCanonicalToken))
	if canonicalDigest == nonCanonicalDigest {
		t.Fatal("differently serialized tokens must have different receipt digests")
	}

	consumerCalls := 0
	verifier := newTestVerifier(t, provider, func(_ context.Context, _ string, _ time.Time, _ Claims) error {
		consumerCalls++
		return nil
	})

	got, err := verifier.VerifyAndConsume(context.Background(), nonCanonicalToken, WorkflowPin)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("non-canonical VerifyAndConsume() error = %v, want ErrInvalidToken", err)
	}
	if err.Error() != ErrInvalidToken.Error() {
		t.Fatalf("non-canonical VerifyAndConsume() error = %q, want sanitized %q", err, ErrInvalidToken)
	}
	if got != (Claims{}) {
		t.Fatalf("non-canonical VerifyAndConsume() claims = %#v, want zero claims", got)
	}
	if consumerCalls != 0 {
		t.Fatalf("receipt consumer calls = %d, want 0", consumerCalls)
	}
}

func TestVerifierConcurrentSameTokenHasOneSuccessAndOneDigest(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	rawToken := provider.sign(t, provider.validClaims(now, now.Add(5*time.Minute), testPinWorkflow), provider.privateKey)
	digestBytes := sha256.Sum256([]byte(rawToken))
	wantDigest := hex.EncodeToString(digestBytes[:])

	var accepted atomic.Bool
	var digestMismatch atomic.Bool
	verifier := newTestVerifier(t, provider, func(_ context.Context, digest string, _ time.Time, _ Claims) error {
		if digest != wantDigest {
			digestMismatch.Store(true)
		}
		if accepted.CompareAndSwap(false, true) {
			return nil
		}
		return ErrTokenReplay
	})

	const attempts = 32
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	replays := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrTokenReplay):
			replays++
		default:
			t.Fatalf("VerifyAndConsume() error = %v, want success or ErrTokenReplay", err)
		}
	}
	if successes != 1 || replays != attempts-1 {
		t.Fatalf("concurrent results = %d successes, %d replays; want 1 success, %d replays", successes, replays, attempts-1)
	}
	if digestMismatch.Load() {
		t.Fatal("concurrent receipt consumers received different token digests")
	}
}

func TestVerifierJWKSFetchTimeoutDoesNotWedgeRetries(t *testing.T) {
	var jwksRequests atomic.Int32
	provider := newTestOIDCProviderWithJWKSHandler(t, func(provider *testOIDCProvider, w http.ResponseWriter, r *http.Request) {
		if jwksRequests.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		provider.serveJWKS(t, w)
	})
	now := time.Now().UTC().Truncate(time.Second)
	rawToken := provider.sign(t, provider.validClaims(now, now.Add(5*time.Minute), testPinWorkflow), provider.privateKey)
	consumerCalls := 0

	const testHTTPTimeout = 50 * time.Millisecond
	verifier, err := newVerifier(context.Background(), provider.config(), func(context.Context, string, time.Time, Claims) error {
		consumerCalls++
		return nil
	}, testHTTPTimeout)
	if err != nil {
		t.Fatalf("newVerifier() = %v", err)
	}

	started := time.Now()
	if _, err := verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("stalled JWKS VerifyAndConsume() error = %v, want ErrInvalidToken", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled JWKS VerifyAndConsume() took %v, want bounded by HTTP timeout", elapsed)
	}
	if consumerCalls != 0 {
		t.Fatalf("receipt consumer calls after stalled JWKS = %d, want 0", consumerCalls)
	}

	deadline := time.Now().Add(time.Second)
	for {
		_, err = verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("VerifyAndConsume() remained wedged after timed-out JWKS request: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if consumerCalls != 1 {
		t.Fatalf("receipt consumer calls after JWKS recovery = %d, want 1", consumerCalls)
	}
	if got := jwksRequests.Load(); got < 2 {
		t.Fatalf("JWKS requests = %d, want at least 2", got)
	}
}

func TestVerifierSanitizesTokenReceiptConsumerFailure(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	rawToken := provider.sign(t, provider.validClaims(now, now.Add(5*time.Minute), testPinWorkflow), provider.privateKey)
	consumerErr := errors.New("sensitive consumer failure: " + rawToken + " actor=" + testActor)
	verifier := newTestVerifier(t, provider, func(context.Context, string, time.Time, Claims) error {
		return consumerErr
	})

	got, err := verifier.VerifyAndConsume(context.Background(), rawToken, WorkflowPin)
	if !errors.Is(err, ErrTokenReceipt) {
		t.Fatalf("VerifyAndConsume() error = %v, want ErrTokenReceipt", err)
	}
	if errors.Is(err, consumerErr) {
		t.Fatal("VerifyAndConsume() exposed the underlying consumer error")
	}
	if err.Error() != ErrTokenReceipt.Error() {
		t.Fatalf("VerifyAndConsume() error = %q, want sanitized %q", err, ErrTokenReceipt)
	}
	if strings.Contains(err.Error(), rawToken) || strings.Contains(err.Error(), testActor) {
		t.Fatal("VerifyAndConsume() error leaked token or claim data")
	}
	if got != (Claims{}) {
		t.Fatalf("VerifyAndConsume() claims = %#v, want zero claims", got)
	}
}

func TestVerifierRejectsNilVerifyContext(t *testing.T) {
	provider := newTestOIDCProvider(t)
	now := time.Now().UTC().Truncate(time.Second)
	rawToken := provider.sign(t, provider.validClaims(now, now.Add(5*time.Minute), testPinWorkflow), provider.privateKey)
	consumerCalls := 0
	verifier := newTestVerifier(t, provider, func(context.Context, string, time.Time, Claims) error {
		consumerCalls++
		return nil
	})

	got, err := verifier.VerifyAndConsume(nil, rawToken, WorkflowPin)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyAndConsume(nil) error = %v, want ErrInvalidToken", err)
	}
	if err.Error() != ErrInvalidToken.Error() {
		t.Fatalf("VerifyAndConsume(nil) error = %q, want sanitized %q", err, ErrInvalidToken)
	}
	if got != (Claims{}) {
		t.Fatalf("VerifyAndConsume(nil) claims = %#v, want zero claims", got)
	}
	if consumerCalls != 0 {
		t.Fatalf("receipt consumer calls = %d, want 0", consumerCalls)
	}
	if strings.Contains(err.Error(), rawToken) || strings.Contains(err.Error(), testActor) {
		t.Fatal("VerifyAndConsume(nil) error leaked token or claim data")
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
	return newTestOIDCProviderWithJWKSHandler(t, nil)
}

func newTestOIDCProviderWithJWKSHandler(t *testing.T, jwksHandler func(*testOIDCProvider, http.ResponseWriter, *http.Request)) *testOIDCProvider {
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
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		if jwksHandler != nil {
			jwksHandler(provider, w, r)
			return
		}
		provider.serveJWKS(t, w)
	})
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *testOIDCProvider) serveJWKS(t *testing.T, w http.ResponseWriter) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	publicKey := p.privateKey.PublicKey
	if err := json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"kid": p.keyID,
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
		}},
	}); err != nil {
		t.Errorf("encode JWKS: %v", err)
	}
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

func equivalentNonCanonicalSignatureToken(t *testing.T, rawToken string) string {
	t.Helper()

	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		t.Fatalf("compact token segments = %d, want 3", len(parts))
	}
	if len(parts[2])%4 != 2 {
		t.Fatalf("RSA-2048 signature segment length mod 4 = %d, want 2", len(parts[2])%4)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := len(parts[2]) - 1
	index := strings.IndexByte(alphabet, parts[2][last])
	if index < 0 || index%16 != 0 || index+1 >= len(alphabet) {
		t.Fatalf("canonical signature suffix %q cannot be made non-canonical", parts[2][last])
	}
	canonicalSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode canonical signature: %v", err)
	}
	parts[2] = parts[2][:last] + string(alphabet[index+1])
	nonCanonicalSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode non-canonical signature: %v", err)
	}
	if string(canonicalSignature) != string(nonCanonicalSignature) {
		t.Fatal("test mutation changed decoded signature bytes")
	}
	return strings.Join(parts, ".")
}
