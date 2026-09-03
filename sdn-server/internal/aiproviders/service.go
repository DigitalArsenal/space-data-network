package aiproviders

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseBody = 128 << 10
)

var (
	ErrUnknownProvider = errors.New("unknown AI provider")
	ErrUnsupportedAuth = errors.New("authentication method is not supported")
	ErrNotConnected    = errors.New("AI provider is not connected")
	ErrDeviceExpired   = errors.New("device sign-in expired")
	ErrDeviceDenied    = errors.New("device sign-in was denied")
	ErrInvalidFlow     = errors.New("device sign-in was not found")
)

// Store is the narrow encrypted-credential surface used by the service.
type Store interface {
	Put(id, username, secret string) error
	Clear(id string) error
	Reveal(id string) (credstore.Credential, error)
	Status(id string) (credstore.Status, error)
	MarkVerified(id string, at time.Time) error
}

// Connection is the secret-free state returned to the dashboard.
type Connection struct {
	Method      AuthMethod `json:"method"`
	Label       string     `json:"label"`
	ConnectedAt time.Time  `json:"connected_at"`
	TestedAt    *time.Time `json:"tested_at,omitempty"`
}

// ProviderStatus combines a registry row with its current connection state.
type ProviderStatus struct {
	Provider
	Connected  bool        `json:"connected"`
	Connection *Connection `json:"connection,omitempty"`
}

// DeviceStart is the browser-safe result of starting a device login. The
// upstream device code stays only in the service's in-memory pending map.
type DeviceStart struct {
	FlowID                  string    `json:"flow_id"`
	VerificationURI         string    `json:"verification_uri"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	UserCode                string    `json:"user_code"`
	Interval                int       `json:"interval"`
	ExpiresAt               time.Time `json:"expires_at"`
}

// DevicePoll is one non-blocking poll result.
type DevicePoll struct {
	Status   string          `json:"status"`
	Provider *ProviderStatus `json:"provider,omitempty"`
	Interval int             `json:"interval,omitempty"`
}

type pendingDevice struct {
	providerID   string
	deviceCode   string
	deviceAuthID string
	userCode     string
	interval     int
	expiresAt    time.Time
}

type tokenCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ConnectedAt  time.Time `json:"connected_at"`
	Label        string    `json:"label"`
	AccountID    string    `json:"account_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Email        string    `json:"email,omitempty"`
}

// Service manages provider connections without exposing credential plaintext.
type Service struct {
	store     Store
	client    *http.Client
	providers []Provider
	now       func() time.Time

	mu      sync.Mutex
	pending map[string]pendingDevice
}

// Option customizes a service for tests or alternate first-party endpoints.
type Option func(*Service)

// WithProviders replaces the registry definitions.
func WithProviders(providers []Provider) Option {
	return func(service *Service) {
		service.providers = make([]Provider, len(providers))
		for i, provider := range providers {
			service.providers[i] = cloneProvider(provider)
		}
	}
}

// WithHTTPClient supplies the transport used for provider calls.
func WithHTTPClient(client *http.Client) Option {
	return func(service *Service) {
		if client != nil {
			service.client = client
		}
	}
}

// NewService creates an AI provider connection service.
func NewService(store Store, options ...Option) *Service {
	service := &Service{
		store:     store,
		client:    &http.Client{},
		providers: Registry(),
		now:       func() time.Time { return time.Now().UTC() },
		pending:   make(map[string]pendingDevice),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

// Providers returns the current secret-free provider list and status.
func (s *Service) Providers() ([]ProviderStatus, error) {
	out := make([]ProviderStatus, 0, len(s.providers))
	for _, provider := range s.providers {
		status, err := s.status(provider)
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}

// ProviderStatus returns one provider's secret-free status.
func (s *Service) ProviderStatus(providerID string) (ProviderStatus, error) {
	provider, err := s.provider(providerID)
	if err != nil {
		return ProviderStatus{}, err
	}
	return s.status(provider)
}

func (s *Service) status(provider Provider) (ProviderStatus, error) {
	result := ProviderStatus{Provider: cloneProvider(provider)}
	for _, method := range []AuthMethod{AuthMethodDeviceCode, AuthMethodAPIKey} {
		if !supports(provider, method) {
			continue
		}
		key := CredentialKey(provider.ID, method)
		status, err := s.store.Status(key)
		if err != nil {
			return ProviderStatus{}, err
		}
		if !status.Configured {
			continue
		}

		connectedAt := time.Time{}
		label := "API key"
		if status.UpdatedAt != nil {
			connectedAt = status.UpdatedAt.UTC()
		}
		if method == AuthMethodDeviceCode {
			credential, revealErr := s.store.Reveal(key)
			if revealErr != nil {
				return ProviderStatus{}, revealErr
			}
			tokens, decodeErr := decodeTokens(credential.Secret.Reveal())
			if decodeErr != nil {
				return ProviderStatus{}, decodeErr
			}
			label = tokens.Label
			if label == "" {
				label = provider.AccountLabel
			}
			if !tokens.ConnectedAt.IsZero() {
				connectedAt = tokens.ConnectedAt.UTC()
			}
		}
		result.Connected = true
		result.Connection = &Connection{
			Method:      method,
			Label:       label,
			ConnectedAt: connectedAt,
			TestedAt:    status.VerifiedAt,
		}
		return result, nil
	}
	return result, nil
}

// ConnectAPIKey stores an API key and makes it the provider's active method.
func (s *Service) ConnectAPIKey(providerID, apiKey string) (ProviderStatus, error) {
	provider, err := s.provider(providerID)
	if err != nil {
		return ProviderStatus{}, err
	}
	if !supports(provider, AuthMethodAPIKey) {
		return ProviderStatus{}, ErrUnsupportedAuth
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ProviderStatus{}, errors.New("API key is required")
	}
	if err := s.store.Put(CredentialKey(provider.ID, AuthMethodAPIKey), "API key", apiKey); err != nil {
		return ProviderStatus{}, err
	}
	if err := s.store.Clear(CredentialKey(provider.ID, AuthMethodDeviceCode)); err != nil {
		return ProviderStatus{}, err
	}
	return s.status(provider)
}

// Disconnect clears every credential method for a provider.
func (s *Service) Disconnect(providerID string) (ProviderStatus, error) {
	provider, err := s.provider(providerID)
	if err != nil {
		return ProviderStatus{}, err
	}
	for _, method := range []AuthMethod{AuthMethodAPIKey, AuthMethodDeviceCode} {
		if err := s.store.Clear(CredentialKey(provider.ID, method)); err != nil {
			return ProviderStatus{}, err
		}
	}
	return s.status(provider)
}

// StartDevice begins one provider device-code flow.
func (s *Service) StartDevice(ctx context.Context, providerID string) (DeviceStart, error) {
	provider, err := s.provider(providerID)
	if err != nil {
		return DeviceStart{}, err
	}
	if !supports(provider, AuthMethodDeviceCode) || provider.Device == nil {
		return DeviceStart{}, ErrUnsupportedAuth
	}

	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	start, pending, err := s.requestDeviceCode(callCtx, provider)
	if err != nil {
		return DeviceStart{}, err
	}
	flowID, err := randomID()
	if err != nil {
		return DeviceStart{}, errors.New("could not start device sign-in")
	}
	pending.providerID = provider.ID

	s.mu.Lock()
	for id, flow := range s.pending {
		if !flow.expiresAt.After(s.now()) {
			delete(s.pending, id)
		}
	}
	s.pending[flowID] = pending
	s.mu.Unlock()

	start.FlowID = flowID
	return start, nil
}

func (s *Service) requestDeviceCode(ctx context.Context, provider Provider) (DeviceStart, pendingDevice, error) {
	device := provider.Device
	var request *http.Request
	var err error
	if device.Protocol == DeviceProtocolOpenAI {
		body, marshalErr := json.Marshal(map[string]string{"client_id": device.ClientID})
		if marshalErr != nil {
			return DeviceStart{}, pendingDevice{}, marshalErr
		}
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, device.StartURL, strings.NewReader(string(body)))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
		}
	} else {
		form := url.Values{
			"client_id": {device.ClientID},
			"scope":     {device.Scope},
			"referrer":  {"grok-build"},
		}
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, device.StartURL, strings.NewReader(form.Encode()))
		if err == nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept", "application/json")
			request.Header.Set("x-grok-client-surface", "ui")
		}
	}
	if err != nil {
		return DeviceStart{}, pendingDevice{}, errors.New("could not create device sign-in request")
	}
	response, err := s.client.Do(request)
	if err != nil {
		return DeviceStart{}, pendingDevice{}, errors.New("the provider did not answer")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return DeviceStart{}, pendingDevice{}, fmt.Errorf("device sign-in request failed with status %d", response.StatusCode)
	}

	if device.Protocol == DeviceProtocolOpenAI {
		var body struct {
			DeviceAuthID string `json:"device_auth_id"`
			UserCode     string `json:"user_code"`
			UserCodeAlt  string `json:"usercode"`
			Interval     any    `json:"interval"`
		}
		if err := decodeJSON(response.Body, &body); err != nil {
			return DeviceStart{}, pendingDevice{}, errors.New("the provider returned an invalid device sign-in response")
		}
		if body.UserCode == "" {
			body.UserCode = body.UserCodeAlt
		}
		interval := parseInterval(body.Interval, 5)
		expiresAt := s.now().Add(15 * time.Minute)
		if body.DeviceAuthID == "" || body.UserCode == "" {
			return DeviceStart{}, pendingDevice{}, errors.New("the provider returned an incomplete device sign-in response")
		}
		return DeviceStart{
				VerificationURI: device.VerificationURL,
				UserCode:        body.UserCode,
				Interval:        interval,
				ExpiresAt:       expiresAt,
			}, pendingDevice{
				deviceAuthID: body.DeviceAuthID,
				userCode:     body.UserCode,
				interval:     interval,
				expiresAt:    expiresAt,
			}, nil
	}

	var body struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := decodeJSON(response.Body, &body); err != nil {
		return DeviceStart{}, pendingDevice{}, errors.New("the provider returned an invalid device sign-in response")
	}
	if body.DeviceCode == "" || body.UserCode == "" || body.VerificationURI == "" {
		return DeviceStart{}, pendingDevice{}, errors.New("the provider returned an incomplete device sign-in response")
	}
	if body.ExpiresIn <= 0 {
		body.ExpiresIn = 600
	}
	if body.Interval <= 0 {
		body.Interval = 5
	}
	expiresAt := s.now().Add(time.Duration(body.ExpiresIn) * time.Second)
	return DeviceStart{
			VerificationURI:         body.VerificationURI,
			VerificationURIComplete: body.VerificationURIComplete,
			UserCode:                body.UserCode,
			Interval:                body.Interval,
			ExpiresAt:               expiresAt,
		}, pendingDevice{
			deviceCode: body.DeviceCode,
			userCode:   body.UserCode,
			interval:   body.Interval,
			expiresAt:  expiresAt,
		}, nil
}

// PollDevice performs one upstream poll. Pending is a successful result, not
// an error, so browser polling stays quiet while the user signs in.
func (s *Service) PollDevice(ctx context.Context, providerID, flowID string) (DevicePoll, error) {
	provider, err := s.provider(providerID)
	if err != nil {
		return DevicePoll{}, err
	}
	if !supports(provider, AuthMethodDeviceCode) || provider.Device == nil {
		return DevicePoll{}, ErrUnsupportedAuth
	}

	s.mu.Lock()
	pending, ok := s.pending[flowID]
	if ok && (!pending.expiresAt.After(s.now()) || pending.providerID != provider.ID) {
		delete(s.pending, flowID)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return DevicePoll{}, ErrInvalidFlow
	}

	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	tokens, state, err := s.pollTokens(callCtx, provider, pending)
	if err != nil {
		if errors.Is(err, ErrDeviceExpired) || errors.Is(err, ErrDeviceDenied) {
			s.mu.Lock()
			delete(s.pending, flowID)
			s.mu.Unlock()
		}
		return DevicePoll{}, err
	}
	if state == "pending" {
		return DevicePoll{Status: "pending", Interval: pending.interval}, nil
	}

	now := s.now()
	tokens.ConnectedAt = now
	s.applyTokenIdentity(provider, &tokens)
	if err := s.storeTokens(provider, tokens); err != nil {
		return DevicePoll{}, err
	}
	if err := s.store.Clear(CredentialKey(provider.ID, AuthMethodAPIKey)); err != nil {
		return DevicePoll{}, err
	}
	s.mu.Lock()
	delete(s.pending, flowID)
	s.mu.Unlock()
	status, err := s.status(provider)
	if err != nil {
		return DevicePoll{}, err
	}
	return DevicePoll{Status: "connected", Provider: &status}, nil
}

func (s *Service) pollTokens(ctx context.Context, provider Provider, pending pendingDevice) (tokenCredential, string, error) {
	device := provider.Device
	if device.Protocol == DeviceProtocolOpenAI {
		body, _ := json.Marshal(map[string]string{
			"device_auth_id": pending.deviceAuthID,
			"user_code":      pending.userCode,
		})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, device.PollURL, strings.NewReader(string(body)))
		if err != nil {
			return tokenCredential{}, "", errors.New("could not create device sign-in poll")
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := s.client.Do(request)
		if err != nil {
			return tokenCredential{}, "", errors.New("the provider did not answer")
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
			return tokenCredential{}, "pending", nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return tokenCredential{}, "", fmt.Errorf("device sign-in poll failed with status %d", response.StatusCode)
		}
		var code struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err := decodeJSON(response.Body, &code); err != nil || code.AuthorizationCode == "" || code.CodeVerifier == "" {
			return tokenCredential{}, "", errors.New("the provider returned an invalid device sign-in result")
		}
		return s.exchangeOpenAICode(ctx, provider, code.AuthorizationCode, code.CodeVerifier)
	}

	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {pending.deviceCode},
		"client_id":   {device.ClientID},
	}
	pollURL := device.PollURL
	if pollURL == "" {
		pollURL = device.TokenURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, pollURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenCredential{}, "", errors.New("could not create device sign-in poll")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-grok-client-surface", "ui")
	response, err := s.client.Do(request)
	if err != nil {
		return tokenCredential{}, "", errors.New("the provider did not answer")
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		tokens, decodeErr := decodeTokenResponse(response.Body, s.now())
		return tokens, "connected", decodeErr
	}
	var oauthError struct {
		Error string `json:"error"`
	}
	_ = decodeJSON(response.Body, &oauthError)
	switch oauthError.Error {
	case "authorization_pending", "slow_down":
		return tokenCredential{}, "pending", nil
	case "access_denied":
		return tokenCredential{}, "", ErrDeviceDenied
	case "expired_token":
		return tokenCredential{}, "", ErrDeviceExpired
	default:
		return tokenCredential{}, "", fmt.Errorf("device sign-in poll failed with status %d", response.StatusCode)
	}
}

func (s *Service) exchangeOpenAICode(ctx context.Context, provider Provider, authorizationCode, verifier string) (tokenCredential, string, error) {
	device := provider.Device
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authorizationCode},
		"redirect_uri":  {device.RedirectURI},
		"client_id":     {device.ClientID},
		"code_verifier": {verifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, device.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenCredential{}, "", errors.New("could not create token exchange request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return tokenCredential{}, "", errors.New("the provider did not answer")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenCredential{}, "", fmt.Errorf("token exchange failed with status %d", response.StatusCode)
	}
	tokens, err := decodeTokenResponse(response.Body, s.now())
	if err != nil {
		return tokenCredential{}, "", err
	}
	if tokens.RefreshToken == "" {
		return tokenCredential{}, "", errors.New("the provider did not return a refresh token")
	}
	return tokens, "connected", nil
}

// TestConnection performs one cheap authenticated request with a strict
// ten-second overall timeout. Device credentials refresh when necessary and
// retry once after an authentication refusal.
func (s *Service) TestConnection(ctx context.Context, providerID string) (time.Time, error) {
	provider, err := s.provider(providerID)
	if err != nil {
		return time.Time{}, err
	}
	testCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	method, key, secret, tokens, err := s.activeCredential(provider)
	if err != nil {
		return time.Time{}, err
	}
	if method == AuthMethodDeviceCode && !tokens.ExpiresAt.IsZero() && !tokens.ExpiresAt.After(s.now().Add(time.Minute)) {
		tokens, err = s.refresh(testCtx, provider, tokens)
		if err != nil {
			return time.Time{}, err
		}
		secret = tokens.AccessToken
	}

	statusCode, err := s.probe(testCtx, provider, method, secret, tokens)
	if err != nil {
		return time.Time{}, err
	}
	if statusCode == http.StatusUnauthorized && method == AuthMethodDeviceCode && tokens.RefreshToken != "" {
		tokens, err = s.refresh(testCtx, provider, tokens)
		if err != nil {
			return time.Time{}, err
		}
		statusCode, err = s.probe(testCtx, provider, method, tokens.AccessToken, tokens)
		if err != nil {
			return time.Time{}, err
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return time.Time{}, fmt.Errorf("provider test failed with status %d", statusCode)
	}

	testedAt := s.now()
	if err := s.store.MarkVerified(key, testedAt); err != nil {
		return time.Time{}, err
	}
	return testedAt, nil
}

func (s *Service) activeCredential(provider Provider) (AuthMethod, string, string, tokenCredential, error) {
	deviceKey := CredentialKey(provider.ID, AuthMethodDeviceCode)
	if supports(provider, AuthMethodDeviceCode) {
		status, err := s.store.Status(deviceKey)
		if err != nil {
			return "", "", "", tokenCredential{}, err
		}
		if status.Configured {
			credential, err := s.store.Reveal(deviceKey)
			if err != nil {
				return "", "", "", tokenCredential{}, err
			}
			tokens, err := decodeTokens(credential.Secret.Reveal())
			if err != nil {
				return "", "", "", tokenCredential{}, err
			}
			return AuthMethodDeviceCode, deviceKey, tokens.AccessToken, tokens, nil
		}
	}

	apiKey := CredentialKey(provider.ID, AuthMethodAPIKey)
	status, err := s.store.Status(apiKey)
	if err != nil {
		return "", "", "", tokenCredential{}, err
	}
	if !status.Configured {
		return "", "", "", tokenCredential{}, ErrNotConnected
	}
	credential, err := s.store.Reveal(apiKey)
	if err != nil {
		return "", "", "", tokenCredential{}, err
	}
	return AuthMethodAPIKey, apiKey, credential.Secret.Reveal(), tokenCredential{}, nil
}

func (s *Service) probe(ctx context.Context, provider Provider, method AuthMethod, secret string, tokens tokenCredential) (int, error) {
	endpoint := provider.TestEndpoint
	headers := provider.APIKeyHeaders
	if method == AuthMethodDeviceCode {
		endpoint = provider.Device.TestEndpoint
		headers = provider.Device.TestHeaders
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, errors.New("could not create provider test request")
	}
	request.Header.Set("Accept", "application/json")
	if method == AuthMethodAPIKey && strings.EqualFold(provider.APIKeyHeader, "x-api-key") {
		request.Header.Set(provider.APIKeyHeader, secret)
	} else {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if method == AuthMethodDeviceCode {
		switch provider.Device.Protocol {
		case DeviceProtocolOpenAI:
			if tokens.AccountID != "" {
				request.Header.Set("ChatGPT-Account-ID", tokens.AccountID)
			}
		case DeviceProtocolRFC8628:
			if tokens.UserID != "" {
				request.Header.Set("x-userid", tokens.UserID)
			}
			if tokens.Email != "" {
				request.Header.Set("x-email", tokens.Email)
			}
		}
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, errors.New("the provider did not answer")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
	return response.StatusCode, nil
}

func (s *Service) refresh(ctx context.Context, provider Provider, current tokenCredential) (tokenCredential, error) {
	if provider.Device == nil || current.RefreshToken == "" {
		return tokenCredential{}, errors.New("this connection cannot be refreshed")
	}
	device := provider.Device
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if device.Protocol == DeviceProtocolOpenAI {
		encoded, _ := json.Marshal(map[string]string{
			"client_id":     device.ClientID,
			"grant_type":    "refresh_token",
			"refresh_token": current.RefreshToken,
		})
		body = strings.NewReader(string(encoded))
		contentType = "application/json"
	} else {
		form := url.Values{
			"client_id":     {device.ClientID},
			"grant_type":    {"refresh_token"},
			"refresh_token": {current.RefreshToken},
		}
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, device.TokenURL, body)
	if err != nil {
		return tokenCredential{}, errors.New("could not create token refresh request")
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return tokenCredential{}, errors.New("the provider did not answer")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tokenCredential{}, fmt.Errorf("token refresh failed with status %d", response.StatusCode)
	}
	updated, err := decodeTokenResponse(response.Body, s.now())
	if err != nil {
		return tokenCredential{}, err
	}
	if updated.RefreshToken == "" {
		updated.RefreshToken = current.RefreshToken
	}
	if updated.IDToken == "" {
		updated.IDToken = current.IDToken
	}
	updated.ConnectedAt = current.ConnectedAt
	updated.Label = current.Label
	updated.AccountID = current.AccountID
	updated.UserID = current.UserID
	updated.Email = current.Email
	s.applyTokenIdentity(provider, &updated)
	if err := s.storeTokens(provider, updated); err != nil {
		return tokenCredential{}, err
	}
	return updated, nil
}

func (s *Service) storeTokens(provider Provider, tokens tokenCredential) error {
	encoded, err := json.Marshal(tokens)
	if err != nil {
		return errors.New("could not encode provider credentials")
	}
	return s.store.Put(CredentialKey(provider.ID, AuthMethodDeviceCode), tokens.Label, string(encoded))
}

func (s *Service) applyTokenIdentity(provider Provider, tokens *tokenCredential) {
	claims := jwtClaims(tokens.IDToken)
	if claims.Email != "" {
		tokens.Email = claims.Email
		tokens.Label = claims.Email
	}
	if tokens.Label == "" {
		tokens.Label = provider.AccountLabel
	}
	if provider.ID == "openai" {
		tokens.AccountID = claims.OpenAIAuth.ChatGPTAccountID
	} else if provider.ID == "xai" {
		tokens.UserID = claims.Subject
	}
}

func decodeTokenResponse(reader io.Reader, now time.Time) (tokenCredential, error) {
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := decodeJSON(reader, &response); err != nil {
		return tokenCredential{}, errors.New("the provider returned an invalid token response")
	}
	if response.AccessToken == "" {
		return tokenCredential{}, errors.New("the provider did not return an access token")
	}
	tokens := tokenCredential{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		IDToken:      response.IDToken,
		TokenType:    response.TokenType,
	}
	if response.ExpiresIn > 0 {
		tokens.ExpiresAt = now.Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return tokens, nil
}

func decodeTokens(secret string) (tokenCredential, error) {
	var tokens tokenCredential
	if err := json.Unmarshal([]byte(secret), &tokens); err != nil || tokens.AccessToken == "" {
		return tokenCredential{}, errors.New("stored provider credentials are invalid")
	}
	return tokens, nil
}

type claimsPayload struct {
	Email      string `json:"email"`
	Subject    string `json:"sub"`
	OpenAIAuth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

func jwtClaims(token string) claimsPayload {
	var claims claimsPayload
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > maxResponseBody {
		return claims
	}
	_ = json.Unmarshal(payload, &claims)
	claims.Email = safeLabel(claims.Email)
	claims.Subject = safeHeader(claims.Subject)
	claims.OpenAIAuth.ChatGPTAccountID = safeHeader(claims.OpenAIAuth.ChatGPTAccountID)
	return claims
}

func safeLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 160 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func safeHeader(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxResponseBody))
	return decoder.Decode(target)
}

func parseInterval(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		if typed >= 1 && typed <= 60 {
			return int(typed)
		}
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil && parsed >= 1 && parsed <= 60 {
			return parsed
		}
	}
	return fallback
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (s *Service) provider(id string) (Provider, error) {
	id = strings.TrimSpace(id)
	for _, provider := range s.providers {
		if provider.ID == id {
			return cloneProvider(provider), nil
		}
	}
	return Provider{}, ErrUnknownProvider
}
