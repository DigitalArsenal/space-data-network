package gateway

import (
	"net/http"
	"testing"
)

func staticList(method, path string) bool {
	return method == http.MethodGet && path == "/api/v1/static"
}

func discoveryRoutes() []RouteDecl {
	return []RouteDecl{
		{Method: "GET", Path: JoinMountPath("/api/v1/peers/", ""), Anonymous: true},
		{Method: "GET", Path: JoinMountPath("/api/v1/peers/", "{peerId}"), Anonymous: true},
		{Method: "GET", Path: JoinMountPath("/api/v1/standards", ""), Anonymous: true},
		{Method: "POST", Path: JoinMountPath("/api/v1/data/", "query"), Anonymous: false},
	}
}

func TestJoinMountPath(t *testing.T) {
	// Must agree with internal/api docs.go joinMountPath by construction.
	cases := []struct{ mount, route, want string }{
		{"/api/v1/peers/", "", "/api/v1/peers"},
		{"/api/v1/peers/", "{peerId}", "/api/v1/peers/{peerId}"},
		{"/api/v1/standards", "", "/api/v1/standards"},
		{"/api/v1/data/", "omm/bulk", "/api/v1/data/omm/bulk"},
		{"", "", "/"},
	}
	for _, c := range cases {
		if got := JoinMountPath(c.mount, c.route); got != c.want {
			t.Fatalf("JoinMountPath(%q, %q) = %q, want %q", c.mount, c.route, got, c.want)
		}
	}
}

func TestAnonymousPolicyMountedRoutes(t *testing.T) {
	policy := NewAnonymousPolicy(staticList, discoveryRoutes(), nil, nil)

	// Declared anonymous flow routes are admitted, concrete or template form.
	for _, path := range []string{
		"/api/v1/peers",
		"/api/v1/peers/",
		"/api/v1/peers/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3",
		"/api/v1/peers/{peerId}", // the OpenAPI generator stamps template paths
		"/api/v1/standards",
	} {
		if !policy.Anonymous("GET", path) {
			t.Fatalf("GET %s should be anonymous", path)
		}
	}

	// HEAD rides the GET decision; the static list still works.
	if !policy.Anonymous("HEAD", "/api/v1/peers") {
		t.Fatalf("HEAD peers should be anonymous")
	}
	if !policy.Anonymous("GET", "/api/v1/static") {
		t.Fatalf("static allowlist entry must remain anonymous")
	}

	// Non-anonymous declarations and unknown paths stay gated.
	if policy.Anonymous("POST", "/api/v1/data/query") {
		t.Fatalf("anonymous:false route must stay gated")
	}
	if policy.Anonymous("POST", "/api/v1/peers") {
		t.Fatalf("non-GET on a GET-only route must stay gated")
	}
	if policy.Anonymous("GET", "/api/v1/peers/x/pnm") {
		t.Fatalf("deeper unclaimed path must stay gated (G.3 not landed)")
	}
	if policy.Anonymous("GET", "/api/v1/other") {
		t.Fatalf("unrelated path must stay gated")
	}
}

func TestAnonymousPolicyConfigVetoAndExtend(t *testing.T) {
	// Deny vetoes a flow-declared route AND a static entry.
	policy := NewAnonymousPolicy(staticList, discoveryRoutes(),
		nil, []string{"/api/v1/peers/", "/api/v1/static"})
	if policy.Anonymous("GET", "/api/v1/peers/16Uiu2X") {
		t.Fatalf("gateway.anonymous.deny prefix must veto the peers subtree")
	}
	if policy.Anonymous("GET", "/api/v1/static") {
		t.Fatalf("gateway.anonymous.deny must veto static entries too")
	}
	if !policy.Anonymous("GET", "/api/v1/standards") {
		t.Fatalf("deny must not leak onto other routes")
	}

	// Allow extends read access without a flow declaration.
	policy = NewAnonymousPolicy(nil, nil, []string{"/api/v1/extra", "/api/v1/tree/"}, nil)
	if !policy.Anonymous("GET", "/api/v1/extra") {
		t.Fatalf("gateway.anonymous.allow exact entry")
	}
	if !policy.Anonymous("GET", "/api/v1/tree/deep/path") {
		t.Fatalf("gateway.anonymous.allow prefix entry")
	}
	if policy.Anonymous("POST", "/api/v1/extra") {
		t.Fatalf("allow entries are read-only (GET/HEAD)")
	}
}

func TestTemplateMatch(t *testing.T) {
	cases := []struct {
		template, path string
		want           bool
	}{
		{"/api/v1/peers/{peerId}", "/api/v1/peers/16Uiu2X", true},
		{"/api/v1/peers/{peerId}", "/api/v1/peers/16Uiu2X/", true},
		{"/api/v1/peers/{peerId}", "/api/v1/peers", false},
		{"/api/v1/peers/{peerId}", "/api/v1/peers/a/b", false},
		{"/api/v1/peers", "/api/v1/peers/", true},
		{"/api/v1/peers", "/api/v1/peersX", false},
	}
	for _, c := range cases {
		if got := templateMatch(c.template, c.path); got != c.want {
			t.Fatalf("templateMatch(%q, %q) = %v, want %v", c.template, c.path, got, c.want)
		}
	}
}
