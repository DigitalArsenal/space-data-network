package accessgate

import "testing"

func TestDefaultPolicy(t *testing.T) {
	p, err := New(DefaultPublic, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path string
		public       bool
	}{
		{"GET", "/ipfs/bafy123", true},
		{"HEAD", "/ipfs/bafy123", true},
		{"OPTIONS", "/ipfs/bafy123", true},
		{"PUT", "/ipfs/bafy123", false},
		{"GET", "/", true},
		{"GET", "/admin/", false},
		{"POST", "/api/auth/challenge", true},
		{"GET", "/api/auth/challenge", false},
		{"GET", "/api/v1/cellular/tiles/1", true},
		{"POST", "/api/v1/cellular/aggregate", true},
		{"PUT", "/api/v1/cellular/credentials", false},
		{"GET", "/api/storefront/listings", true},
		{"GET", "/api/storefront/listingsX", false},
		{"GET", "/api/v1/stats", false},
		{"GET", "/api/v0/cat", false},
		{"GET", "/api/node/epm", false},
		{"GET", "/webui", false},
		{"GET", "/docs/", false},
	} {
		if got := p.Public(c.method, c.path); got != c.public {
			t.Errorf("%s %s: public=%v, want %v", c.method, c.path, got, c.public)
		}
	}
}

func TestConfiguredAdditionsAndRemovals(t *testing.T) {
	p, err := New(DefaultPublic, []string{"/api/v1/data/records/RFB/", "GET,POST /api/custom*"}, []string{"/ipfs/"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Public("GET", "/api/v1/data/records/RFB/bafy") {
		t.Error("configured addition not public")
	}
	if !p.Public("POST", "/api/customthing") || p.Public("DELETE", "/api/customthing") {
		t.Error("method list not honored")
	}
	if p.Public("GET", "/ipfs/bafy") {
		t.Error("configured removal did not win over the default")
	}
	p.Extra = func(method, path string) bool { return path == "/api/v1/data/omm/bulk" }
	if !p.Public("GET", "/api/v1/data/omm/bulk") {
		t.Error("extra predicate ignored")
	}
	if _, err := New(nil, []string{"no-slash"}, nil); err == nil {
		t.Error("accepted a path without a leading slash")
	}
}
