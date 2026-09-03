package aiproviders

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
)

func newAIStore(t *testing.T) *credstore.Store {
	t.Helper()
	store, err := credstore.NewStore(t.TempDir(), "ai-provider-test-root")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestAIRegistryListsExactlyThreeProvidersAndTheirAuthMethods(t *testing.T) {
	providers := Registry()
	if len(providers) != 3 {
		t.Fatalf("len(Registry()) = %d, want 3", len(providers))
	}
	want := map[string][]AuthMethod{
		"openai":    {AuthMethodDeviceCode, AuthMethodAPIKey},
		"anthropic": {AuthMethodAPIKey},
		"xai":       {AuthMethodDeviceCode, AuthMethodAPIKey},
	}
	for _, provider := range providers {
		methods, ok := want[provider.ID]
		if !ok {
			t.Fatalf("unexpected provider %q", provider.ID)
		}
		if strings.Join(authStrings(provider.AuthMethods), ",") != strings.Join(authStrings(methods), ",") {
			t.Errorf("%s auth methods = %v, want %v", provider.ID, provider.AuthMethods, methods)
		}
		delete(want, provider.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing providers: %v", want)
	}
}

func authStrings(methods []AuthMethod) []string {
	out := make([]string, len(methods))
	for i, method := range methods {
		out[i] = string(method)
	}
	return out
}

func TestAIAPIKeyConnectStoresSecretAndStatusNeverRevealsIt(t *testing.T) {
	store := newAIStore(t)
	service := NewService(store)
	const key = "sk-plaintext-canary-never-return"

	status, err := service.ConnectAPIKey("anthropic", key)
	if err != nil {
		t.Fatalf("ConnectAPIKey: %v", err)
	}
	if !status.Connected || status.Connection == nil || status.Connection.Method != AuthMethodAPIKey {
		t.Fatalf("status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), key) {
		t.Fatal("provider status revealed the API key")
	}
	stored, err := store.Reveal(CredentialKey("anthropic", AuthMethodAPIKey))
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if stored.Secret.Reveal() != key {
		t.Fatal("API key was not stored")
	}
}

func TestAIDeviceFlowCompletesAndStoresTokens(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device/start":
			var request map[string]string
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["client_id"] != "test-client" {
				t.Errorf("client_id = %q", request["client_id"])
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"device_auth_id": "device-id",
				"user_code":      "ABCD-1234",
				"interval":       "1",
			})
		case "/device/poll":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_code": "authorization-code",
				"code_verifier":      "verifier",
				"code_challenge":     "challenge",
			})
		case "/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "authorization-code" {
				t.Errorf("token form = %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token-canary",
				"refresh_token": "refresh-token-canary",
				"id_token":      testJWT(t, map[string]any{"email": "operator@example.com", "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account-1"}}),
				"expires_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	provider := Provider{
		ID: "openai", Name: "OpenAI / Codex", AccountLabel: "ChatGPT account",
		AuthMethods:  []AuthMethod{AuthMethodDeviceCode, AuthMethodAPIKey},
		TestEndpoint: upstream.URL + "/models", APIKeyHeader: "Authorization",
		Device: &DeviceConfig{
			Protocol: DeviceProtocolOpenAI, ClientID: "test-client",
			StartURL: upstream.URL + "/device/start", PollURL: upstream.URL + "/device/poll",
			TokenURL:    upstream.URL + "/oauth/token",
			RedirectURI: upstream.URL + "/callback", VerificationURL: upstream.URL + "/verify",
			TestEndpoint: upstream.URL + "/models",
		},
	}
	store := newAIStore(t)
	service := NewService(store, WithProviders([]Provider{provider}), WithHTTPClient(upstream.Client()))

	start, err := service.StartDevice(context.Background(), "openai")
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if start.FlowID == "" || start.UserCode != "ABCD-1234" || start.VerificationURI != upstream.URL+"/verify" {
		t.Fatalf("start = %#v", start)
	}

	result, err := service.PollDevice(context.Background(), "openai", start.FlowID)
	if err != nil {
		t.Fatalf("PollDevice: %v", err)
	}
	if result.Status != "connected" || result.Provider == nil || result.Provider.Connection.Label != "operator@example.com" {
		t.Fatalf("poll result = %#v", result)
	}

	credential, err := store.Reveal(CredentialKey("openai", AuthMethodDeviceCode))
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	secret := credential.Secret.Reveal()
	for _, token := range []string{"access-token-canary", "refresh-token-canary"} {
		if !strings.Contains(secret, token) {
			t.Fatalf("stored device credential is missing %s", token)
		}
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "token-canary") {
		t.Fatal("device poll response revealed stored tokens")
	}
}

func TestAIDisconnectClearsStoredCredentials(t *testing.T) {
	store := newAIStore(t)
	service := NewService(store)
	if _, err := service.ConnectAPIKey("xai", "xai-key"); err != nil {
		t.Fatalf("ConnectAPIKey: %v", err)
	}
	status, err := service.Disconnect("xai")
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if status.Connected {
		t.Fatalf("status after disconnect = %#v", status)
	}
	stored, err := store.Status(CredentialKey("xai", AuthMethodAPIKey))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if stored.Configured {
		t.Fatal("credential remains after disconnect")
	}
}

func TestAIRefreshesDeviceTokenBeforeTest(t *testing.T) {
	var refreshed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			refreshed = true
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-access", "refresh_token": "fresh-refresh", "expires_in": 3600})
		case "/models":
			if r.Header.Get("Authorization") != "Bearer fresh-access" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := Provider{
		ID: "xai", Name: "xAI / Grok", AccountLabel: "Grok account",
		AuthMethods:  []AuthMethod{AuthMethodDeviceCode, AuthMethodAPIKey},
		TestEndpoint: server.URL + "/models", APIKeyHeader: "Authorization",
		Device: &DeviceConfig{Protocol: DeviceProtocolRFC8628, ClientID: "client", TokenURL: server.URL + "/token", TestEndpoint: server.URL + "/models"},
	}
	store := newAIStore(t)
	service := NewService(store, WithProviders([]Provider{provider}), WithHTTPClient(server.Client()))
	tokens := tokenCredential{AccessToken: "old-access", RefreshToken: "old-refresh", ConnectedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute), Label: "operator@example.com"}
	if err := service.storeTokens(provider, tokens); err != nil {
		t.Fatalf("storeTokens: %v", err)
	}
	if _, err := service.TestConnection(context.Background(), "xai"); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if !refreshed {
		t.Fatal("expired device credential was not refreshed")
	}
	stored, err := store.Reveal(CredentialKey("xai", AuthMethodDeviceCode))
	if err != nil || !strings.Contains(stored.Secret.Reveal(), "fresh-access") {
		t.Fatalf("refreshed tokens were not stored: %v", err)
	}
}

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("Marshal JWT claims: %v", err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(encoded) + ".signature"
}
