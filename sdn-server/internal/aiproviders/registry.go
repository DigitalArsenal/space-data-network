// Package aiproviders owns the node-local AI provider connection registry and
// the OAuth/device-code mechanics used by the admin dashboard.
package aiproviders

import "strings"

// AuthMethod is a credential shape supported by a provider.
type AuthMethod string

const (
	AuthMethodAPIKey     AuthMethod = "api_key"
	AuthMethodDeviceCode AuthMethod = "device_code"
)

// DeviceProtocol identifies the wire protocol behind a device-code flow.
type DeviceProtocol string

const (
	DeviceProtocolOpenAI  DeviceProtocol = "openai"
	DeviceProtocolRFC8628 DeviceProtocol = "rfc8628"
)

// DeviceConfig is deliberately excluded from API serialization. It contains
// upstream protocol details, while Provider exposes only product capabilities.
type DeviceConfig struct {
	Protocol        DeviceProtocol
	ClientID        string
	Scope           string
	StartURL        string
	PollURL         string
	TokenURL        string
	RedirectURI     string
	VerificationURL string
	TestEndpoint    string
	TestHeaders     map[string]string
}

// Provider is the public registry row plus private request configuration.
type Provider struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	AccountLabel  string            `json:"account_label"`
	AuthMethods   []AuthMethod      `json:"auth_methods"`
	TestEndpoint  string            `json:"test_endpoint"`
	APIKeyHeader  string            `json:"-"`
	APIKeyHeaders map[string]string `json:"-"`
	Device        *DeviceConfig     `json:"-"`
}

const (
	openAIClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	xAIClientID    = "b1a00492-073a-47ea-816f-4c329264a828"
)

var defaultProviders = []Provider{
	{
		ID:           "openai",
		Name:         "OpenAI / Codex",
		AccountLabel: "ChatGPT account",
		AuthMethods:  []AuthMethod{AuthMethodDeviceCode, AuthMethodAPIKey},
		TestEndpoint: "https://api.openai.com/v1/models",
		APIKeyHeader: "Authorization",
		Device: &DeviceConfig{
			Protocol:        DeviceProtocolOpenAI,
			ClientID:        openAIClientID,
			StartURL:        "https://auth.openai.com/api/accounts/deviceauth/usercode",
			PollURL:         "https://auth.openai.com/api/accounts/deviceauth/token",
			TokenURL:        "https://auth.openai.com/oauth/token",
			RedirectURI:     "https://auth.openai.com/deviceauth/callback",
			VerificationURL: "https://auth.openai.com/codex/device",
			TestEndpoint:    "https://chatgpt.com/backend-api/codex/models?client_version=0.0.0",
		},
	},
	{
		ID:           "anthropic",
		Name:         "Anthropic / Claude",
		AccountLabel: "Claude account",
		AuthMethods:  []AuthMethod{AuthMethodAPIKey},
		TestEndpoint: "https://api.anthropic.com/v1/models",
		APIKeyHeader: "x-api-key",
		APIKeyHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
		},
	},
	{
		ID:           "xai",
		Name:         "xAI / Grok",
		AccountLabel: "Grok account",
		AuthMethods:  []AuthMethod{AuthMethodDeviceCode, AuthMethodAPIKey},
		TestEndpoint: "https://api.x.ai/v1/models",
		APIKeyHeader: "Authorization",
		Device: &DeviceConfig{
			Protocol:     DeviceProtocolRFC8628,
			ClientID:     xAIClientID,
			Scope:        "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write workspaces:read workspaces:write",
			StartURL:     "https://auth.x.ai/oauth2/device/code",
			PollURL:      "https://auth.x.ai/oauth2/token",
			TokenURL:     "https://auth.x.ai/oauth2/token",
			TestEndpoint: "https://cli-chat-proxy.grok.com/v1/models",
			TestHeaders: map[string]string{
				"X-XAI-Token-Auth":      "xai-grok-cli",
				"x-grok-client-version": "0.0.0",
				"x-grok-client-mode":    "api",
			},
		},
	},
}

// Registry returns a defensive copy of the three provider definitions.
func Registry() []Provider {
	out := make([]Provider, len(defaultProviders))
	for i, provider := range defaultProviders {
		out[i] = cloneProvider(provider)
	}
	return out
}

func cloneProvider(provider Provider) Provider {
	provider.AuthMethods = append([]AuthMethod(nil), provider.AuthMethods...)
	provider.APIKeyHeaders = cloneHeaders(provider.APIKeyHeaders)
	if provider.Device != nil {
		device := *provider.Device
		device.TestHeaders = cloneHeaders(device.TestHeaders)
		provider.Device = &device
	}
	return provider
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		out[name] = value
	}
	return out
}

func supports(provider Provider, method AuthMethod) bool {
	for _, supported := range provider.AuthMethods {
		if supported == method {
			return true
		}
	}
	return false
}

// CredentialKey maps one provider/method pair to the existing credstore lane
// grammar. Dashes avoid broadening the validator solely for this feature.
func CredentialKey(providerID string, method AuthMethod) string {
	return "ai-" + strings.TrimSpace(providerID) + "-" + strings.ReplaceAll(string(method), "_", "-")
}
