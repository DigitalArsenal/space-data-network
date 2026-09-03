package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/aiproviders"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/credstore"
)

func newAIProvidersHandlerForTest(t *testing.T) (*AIProvidersHandler, *credstore.Store) {
	t.Helper()
	store := newCredStore(t)
	return &AIProvidersHandler{service: aiproviders.NewService(store)}, store
}

func TestAIProviderRoutesRejectUnauthenticated(t *testing.T) {
	store := newCredStore(t)
	authHandler := auth.NewHandler(nil, nil, time.Hour, "", "")
	handler := NewAIProvidersHandler(store, authHandler, true)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/ai/providers", ""},
		{http.MethodPost, "/api/v1/ai/providers/openai/connect", `{"api_key":"secret"}`},
		{http.MethodPost, "/api/v1/ai/providers/openai/connect/device/start", `{}`},
		{http.MethodPost, "/api/v1/ai/providers/openai/connect/device/poll", `{"flow_id":"flow"}`},
		{http.MethodPost, "/api/v1/ai/providers/openai/test", `{}`},
		{http.MethodPost, "/api/v1/ai/providers/openai/disconnect", `{}`},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", request.method, request.path, recorder.Code)
		}
	}
}

func TestAIProviderRoutesFailClosedWhenAuthDisabled(t *testing.T) {
	store := newCredStore(t)
	handler := NewAIProvidersHandler(store, auth.NewHandler(nil, nil, time.Hour, "", ""), false)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ai/providers", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestAIProviderAPIKeyConnectStatusNeverRevealsKeyAndDisconnects(t *testing.T) {
	handler, store := newAIProvidersHandlerForTest(t)
	const key = "sk-api-response-canary"

	connectRequest := httptest.NewRequest(http.MethodPost, "/api/v1/ai/providers/openai/connect", strings.NewReader(`{"api_key":"`+key+`"}`))
	connectRequest.SetPathValue("id", "openai")
	connectResponse := httptest.NewRecorder()
	handler.handleConnect(connectResponse, connectRequest)
	if connectResponse.Code != http.StatusOK {
		t.Fatalf("connect status = %d: %s", connectResponse.Code, connectResponse.Body.String())
	}
	if strings.Contains(connectResponse.Body.String(), key) {
		t.Fatal("connect response revealed the API key")
	}

	credential, err := store.Reveal(aiproviders.CredentialKey("openai", aiproviders.AuthMethodAPIKey))
	if err != nil || credential.Secret.Reveal() != key {
		t.Fatalf("API key was not stored: %v", err)
	}

	listResponse := httptest.NewRecorder()
	handler.handleProviders(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/ai/providers", nil))
	if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), key) {
		t.Fatalf("unsafe provider status: %s", listResponse.Body.String())
	}
	var listed struct {
		Providers []aiproviders.ProviderStatus `json:"providers"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Providers) != 3 || !listed.Providers[0].Connected {
		t.Fatalf("provider list = %#v", listed.Providers)
	}

	disconnectRequest := httptest.NewRequest(http.MethodPost, "/api/v1/ai/providers/openai/disconnect", nil)
	disconnectRequest.SetPathValue("id", "openai")
	disconnectResponse := httptest.NewRecorder()
	handler.handleDisconnect(disconnectResponse, disconnectRequest)
	if disconnectResponse.Code != http.StatusOK || strings.Contains(disconnectResponse.Body.String(), key) {
		t.Fatalf("disconnect response = %d %s", disconnectResponse.Code, disconnectResponse.Body.String())
	}
	status, err := store.Status(aiproviders.CredentialKey("openai", aiproviders.AuthMethodAPIKey))
	if err != nil || status.Configured {
		t.Fatalf("credential remains after disconnect: %#v, %v", status, err)
	}
}

func TestAIDeviceStartIsAbsentForAnthropic(t *testing.T) {
	handler, _ := newAIProvidersHandlerForTest(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ai/providers/anthropic/connect/device/start", nil)
	request.SetPathValue("id", "anthropic")
	recorder := httptest.NewRecorder()
	handler.handleDeviceStart(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
}
