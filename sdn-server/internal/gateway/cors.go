package gateway

import (
	"net/http"
	"strings"
)

// A refusal the browser cannot read is a refusal the page cannot act on.
//
// WHY THIS LIVES HERE
// -------------------
// CORS is applied in exactly one place today — adminSecurityMiddleware in
// cmd/spacedatanetwork/main.go — and only for requests this package's
// AnonymousPolicy classifies as public. That is right for the SUCCESS path and
// wrong for the REFUSAL path, because a gated route is BY DEFINITION not
// public: the 401 it produces reaches a cross-origin browser with no
// Access-Control-Allow-Origin at all, so the browser discards the response and
// reports net::ERR_FAILED. The page receives a perfectly well-formed "you are
// not signed in" and can only say "the node could not be reached".
//
// Measured on host-01, 2026-08-09, Origin https://digitalarsenal.github.io:
//
//	OPTIONS /api/v1/cellular/credentials/opencellid -> 204 + access-control-allow-origin
//	PUT     /api/v1/cellular/credentials/opencellid -> 401 + NO access-control-allow-origin
//
// The preflight passes and the answer is unreadable.
//
// It lives in `gateway` rather than in `auth` because this package is already
// the declared single source for "one predicate serves the auth wall, CORS,
// CSRF and the OpenAPI stamp" (main.go:1430-1432, AnonymousPolicy). The auth
// wall calls it; when the header-writing half of main.go is next touched, its
// applyPublicAPICORSHeaders collapses onto this file instead of growing a
// second copy of the policy. HERMES ruling 2026-08-09.
//
// WHAT IT DELIBERATELY IS NOT
// ---------------------------
//   - It runs ONLY on a refusal. An admitted request keeps exactly the headers
//     the middleware gave it.
//   - It runs ONLY when the request carries an Origin. There is NO "*"
//     fallback — that makes it strictly narrower than the public-surface
//     helper it mirrors, which does fall back to "*". curl, the loopback
//     control lanes and same-origin page requests see byte-identical
//     responses.
//   - It never overwrites a decoration already present.
//   - It NEVER emits Access-Control-Allow-Credentials. That header is what
//     makes a COOKIE-BEARING cross-origin response readable; emitting it here
//     would be the actual widening, and it is the one thing this file must
//     never do. TestRefusalNeverAllowsCredentials pins it.
//
// The disclosed information is an HTTP status and a constant body. Which routes
// exist and which are gated is already public: /api/v1/openapi.json is served
// anonymously and stamps every route with x-sdn-anonymous from the SAME
// predicate the wall enforces.

// PublicSurfaceCORSHeaderNames are the header names the public-surface
// decoration (applyPublicAPICORSHeaders, cmd/spacedatanetwork/main.go) sets on
// an ANONYMOUS response.
//
// It is recorded here so the refusal set can be pinned as a strict SUBSET of
// it: a refusal must never announce something the public surface does not.
// The list cannot be imported — that function is in package main — so
// TestRefusalHeadersAreASubsetOfThePublicSurface compares against this
// declaration, and any future header added to main.go must be added here in
// the same change.
var PublicSurfaceCORSHeaderNames = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Vary",
}

// RefusalCORSHeaderNames is what a refusal carries: the MINIMUM a browser needs
// to read the status.
//
// Allow-Methods and Allow-Headers are preflight-RESPONSE headers; the browser
// ignores them on an actual response, and emitting them on a gated route would
// falsely advertise it as public surface. So they are not emitted. HERMES
// ruling 2026-08-09 §1(a).
var RefusalCORSHeaderNames = []string{
	"Access-Control-Allow-Origin",
	"Vary",
}

// DecorateRefusal makes a refusal readable to the browser origin that sent it,
// and does nothing at all otherwise.
//
// The guard order IS the contract:
//  1. nil request (defensive) -> nothing;
//  2. no Origin header -> nothing, with no "*" fallback;
//  3. already decorated -> nothing, because the route was public and the
//     middleware owns its headers.
//
// It reports whether it decorated, so callers can assert on it in tests.
func DecorateRefusal(w http.ResponseWriter, r *http.Request) bool {
	if w == nil || r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	header := w.Header()
	if strings.TrimSpace(header.Get("Access-Control-Allow-Origin")) != "" {
		return false
	}
	header.Set("Access-Control-Allow-Origin", origin)
	AddVaryOrigin(header)
	return true
}

// AddVaryOrigin appends Origin to Vary without destroying an existing value.
//
// http.Header.Set would REPLACE a Vary the handler chain already set (for
// example Accept-Encoding), turning a correctness fix into a cache-correctness
// bug. main.go's helper uses Set; that is a latent bug there and is
// deliberately not copied. A duplicate Origin entry is also avoided, since a
// second one is pure noise in every cache key.
func AddVaryOrigin(header http.Header) {
	for _, existing := range header.Values("Vary") {
		for _, field := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Origin") {
				return
			}
		}
	}
	header.Add("Vary", "Origin")
}
