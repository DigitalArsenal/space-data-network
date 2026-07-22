package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// U1.2: with an external (embedded SpaceAware) login UI, the auth handler
// must leave the exact "/login" mux pattern unregistered — so the "/"
// frontend surface serves the product login — and keep the legacy
// wallet-gated page reachable at /login/legacy for wallet creation /
// first-admin bootstrap. Default behavior (no external UI) is unchanged.

func matchedPattern(t *testing.T, mux *http.ServeMux, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	_, pattern := mux.Handler(req)
	return pattern
}

func TestRegisterRoutes_DefaultLoginPageOwnsLogin(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {}) // frontend surface stand-in
	h.RegisterRoutes(mux)

	if got := matchedPattern(t, mux, "/login"); got != "/login" {
		t.Fatalf("default: /login matched pattern %q, want %q", got, "/login")
	}
	if got := matchedPattern(t, mux, "/login/legacy"); got == "/login/legacy" {
		t.Fatalf("default: /login/legacy must not be a distinct route, matched %q", got)
	}
}

func TestRegisterRoutes_ExternalLoginUIYieldsLoginToFrontendSurface(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	h.SetExternalLoginUI(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {}) // frontend surface stand-in
	h.RegisterRoutes(mux)

	// /login falls through to the frontend surface ("/" pattern), which in
	// the real binary serves the embedded SpaceAware login (spaceaware_ui.go).
	if got := matchedPattern(t, mux, "/login"); got != "/" {
		t.Fatalf("external UI: /login matched pattern %q, want frontend surface %q", got, "/")
	}
	// The legacy wallet-gated page stays reachable for wallet creation.
	if got := matchedPattern(t, mux, "/login/legacy"); got != "/login/legacy" {
		t.Fatalf("external UI: /login/legacy matched pattern %q, want %q", got, "/login/legacy")
	}
	// Auth API routes are unaffected.
	if got := matchedPattern(t, mux, "/api/auth/challenge"); got != "/api/auth/challenge" {
		t.Fatalf("external UI: /api/auth/challenge matched pattern %q", got)
	}
}

// The shipped conjunction server explicitly selects the no-product-login
// policy by calling SetExternalLoginUI(false). That explicit production choice
// must not fall back to the legacy generic wallet page; only callers that do
// not configure a product policy retain the backwards-compatible default.
func TestRegisterRoutes_ExplicitNoExternalLoginDisablesLegacyLoginPage(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	h.SetExternalLoginUI(false)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) {})
	h.RegisterRoutes(mux)

	for _, path := range []string{"/login", "/login/legacy"} {
		if got := matchedPattern(t, mux, path); got != "/" {
			t.Errorf("explicit no-external-login policy: %s matched %q, want frontend fallback %q", path, got, "/")
		}
	}
	if got := matchedPattern(t, mux, "/api/auth/challenge"); got != "/api/auth/challenge" {
		t.Fatalf("auth API route changed under no-login policy: matched %q", got)
	}
}
