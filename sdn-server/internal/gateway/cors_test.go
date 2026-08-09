package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const galleryOrigin = "https://digitalarsenal.github.io"

func TestDecorateRefusalEchoesTheRequestOrigin(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/cellular/credentials/opencellid", nil)
	req.Header.Set("Origin", galleryOrigin)
	rec := httptest.NewRecorder()

	if !DecorateRefusal(rec, req) {
		t.Fatal("DecorateRefusal did nothing for a request that carries an Origin")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != galleryOrigin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q — a browser discards a response it cannot read", got, galleryOrigin)
	}
	if got := strings.Join(rec.Header().Values("Vary"), ", "); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want it to contain Origin", got)
	}
}

// No Origin, no decoration, and explicitly NO "*" fallback: this must be
// strictly narrower than the public-surface helper it mirrors.
func TestDecorateRefusalDoesNothingWithoutAnOrigin(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/cellular/credentials/opencellid", nil)
	rec := httptest.NewRecorder()

	if DecorateRefusal(rec, req) {
		t.Fatal("DecorateRefusal decorated a request with no Origin")
	}
	for _, name := range PublicSurfaceCORSHeaderNames {
		if got := rec.Header().Get(name); got != "" {
			t.Fatalf("%s = %q with no Origin on the request, want empty", name, got)
		}
	}
}

func TestDecorateRefusalNeverWidensToWildcard(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{"", "   "} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/anything", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		DecorateRefusal(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "*" {
			t.Fatalf("refusal decoration fell back to \"*\" for origin %q; it must emit nothing instead", origin)
		}
	}
}

// The one widening this file must never do.
func TestRefusalNeverAllowsCredentials(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/cellular/credentials/opencellid", nil)
	req.Header.Set("Origin", galleryOrigin)
	rec := httptest.NewRecorder()
	DecorateRefusal(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q; emitting it would make cookie-bearing cross-origin responses readable, which is the actual widening", got)
	}
	for _, name := range RefusalCORSHeaderNames {
		if strings.EqualFold(name, "Access-Control-Allow-Credentials") {
			t.Fatal("Access-Control-Allow-Credentials must never appear in the refusal header set")
		}
	}
}

// A refusal must never announce something the public surface does not.
func TestRefusalHeadersAreASubsetOfThePublicSurface(t *testing.T) {
	t.Parallel()

	public := map[string]bool{}
	for _, name := range PublicSurfaceCORSHeaderNames {
		public[strings.ToLower(name)] = true
	}
	for _, name := range RefusalCORSHeaderNames {
		if !public[strings.ToLower(name)] {
			t.Fatalf("refusal header %q is not part of the public-surface set; a gated route must not advertise more than an anonymous one", name)
		}
	}
	if len(RefusalCORSHeaderNames) >= len(PublicSurfaceCORSHeaderNames) {
		t.Fatalf("refusal set (%d) is no longer a STRICT subset of the public set (%d)", len(RefusalCORSHeaderNames), len(PublicSurfaceCORSHeaderNames))
	}
}

func TestDecorateRefusalDoesNotOverwriteAnExistingDecoration(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPut, "/api/v1/data/omm/bulk", nil)
	req.Header.Set("Origin", galleryOrigin)
	rec := httptest.NewRecorder()
	rec.Header().Set("Access-Control-Allow-Origin", "https://sdn.spaceaware.io")

	if DecorateRefusal(rec, req) {
		t.Fatal("DecorateRefusal overwrote a decoration the middleware already applied")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://sdn.spaceaware.io" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the middleware's value preserved", got)
	}
}

func TestAddVaryOriginAppendsAndDeduplicates(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("Vary", "Accept-Encoding")
	AddVaryOrigin(header)
	AddVaryOrigin(header)

	joined := strings.Join(header.Values("Vary"), ", ")
	if !strings.Contains(joined, "Accept-Encoding") {
		t.Fatalf("Vary = %q, want the pre-existing Accept-Encoding kept — Set would have destroyed it", joined)
	}
	if count := strings.Count(strings.ToLower(joined), "origin"); count != 1 {
		t.Fatalf("Vary = %q, want exactly one Origin entry, got %d", joined, count)
	}
}
