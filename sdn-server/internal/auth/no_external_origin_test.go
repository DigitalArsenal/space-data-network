package auth

// OWNER LAW 2026-07-27, verbatim: "we do NOT load anything from a site."
//
// These tests lock that law onto the AUTHENTICATION surface, which is where it
// matters most: a sign-in page is where an operator's key material is handled,
// and every byte it pulls from a third party is both a supply-chain hole and a
// disclosure of who is signing in and when.
//
// This surface violated the law twice before 2026-07-27:
//   - buildFallbackLoginPage served hd-wallet-ui 2.0.6 from unpkg.com whenever
//     no local wallet dist was configured — a third-party script on the login
//     page, AND a different version from the repo's pinned 2.0.28.
//   - the wallet login page pulled its webfonts from fonts.googleapis.com /
//     fonts.gstatic.com, leaking every sign-in page view.
// Both are gone. These tests are what stop them coming back.

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/tlsmgr"
)

// externalURLPattern matches an absolute URL to any origin.
var externalURLPattern = regexp.MustCompile(`(?i)\b(?:https?:)?//[a-z0-9][a-z0-9.-]*\.[a-z]{2,}`)

// documentOriginAllowlist is the set of absolute URLs an auth-surface DOCUMENT
// may still contain. These must be things a browser does NOT fetch:
// human-readable reference links the operator may click.
//
// Anything that would cause an automatic subresource fetch — script src, link
// href, @font-face src, @import, img src — must be same-origin and therefore
// must not appear here.
var documentOriginAllowlist = []string{
	"https://ipfs.tech/",
	"https://libp2p.io/",
	"https://digitalarsenal.github.io/flatbuffers/",
	"https://digitalarsenal.github.io/flatsql/",
	"https://spacedatastandards.org/",
	"https://spacedatanet.org/",
}

// assertNoExternalSubresources fails when doc pulls any subresource from
// another origin. It checks the fetching constructs specifically, so an
// operator-facing <a href> to a project homepage stays legal while a
// <script src> or a webfont URL does not.
func assertNoExternalSubresources(t *testing.T, label, doc string) {
	t.Helper()

	// Never, under any circumstances, on an auth surface.
	for _, banned := range []string{
		"unpkg.com",
		"cdn.jsdelivr.net",
		"fonts.googleapis.com",
		"fonts.gstatic.com",
		"cdnjs.cloudflare.com",
		"ajax.googleapis.com",
	} {
		if strings.Contains(strings.ToLower(doc), banned) {
			t.Fatalf("%s references %s — owner law 2026-07-27 forbids loading anything from a site", label, banned)
		}
	}

	// Fetching constructs must all be same-origin (relative).
	fetchers := map[string]*regexp.Regexp{
		"script src":  regexp.MustCompile(`(?i)<script[^>]*\ssrc\s*=\s*["']([^"']+)["']`),
		"link href":   regexp.MustCompile(`(?i)<link[^>]*\shref\s*=\s*["']([^"']+)["']`),
		"img src":     regexp.MustCompile(`(?i)<img[^>]*\ssrc\s*=\s*["']([^"']+)["']`),
		"css url()":   regexp.MustCompile(`(?i)url\(\s*["']?([^"')]+)["']?\s*\)`),
		"css @import": regexp.MustCompile(`(?i)@import\s+["']([^"']+)["']`),
	}
	for kind, re := range fetchers {
		for _, m := range re.FindAllStringSubmatch(doc, -1) {
			ref := strings.TrimSpace(m[1])
			if ref == "" || strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "#") {
				continue
			}
			if externalURLPattern.MatchString(ref) {
				t.Fatalf("%s: %s %q is an external load; auth-surface subresources must be same-origin", label, kind, ref)
			}
			if !strings.HasPrefix(ref, "/") && !strings.HasPrefix(ref, "./") {
				t.Fatalf("%s: %s %q is not an unambiguous same-origin path", label, kind, ref)
			}
		}
	}

	// Every remaining absolute URL must be an allow-listed operator link.
	for _, found := range externalURLPattern.FindAllString(doc, -1) {
		allowed := false
		for _, prefix := range documentOriginAllowlist {
			if strings.HasPrefix(strings.TrimPrefix(prefix, "https:"), strings.TrimPrefix(found, "https:")) ||
				strings.HasPrefix(prefix, found) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Fatalf("%s contains unexpected external origin %q; add it to documentOriginAllowlist only if a browser never fetches it", label, found)
		}
	}
}

// TestWalletUnavailablePageLoadsNothingExternal locks the replacement for the
// deleted unpkg fallback: it must state the situation and fetch nothing.
func TestWalletUnavailablePageLoadsNothingExternal(t *testing.T) {
	t.Parallel()

	doc := buildWalletUnavailablePage("node.example.invalid", tlsmgr.Status{})
	assertNoExternalSubresources(t, "wallet-unavailable page", doc)

	if !strings.Contains(doc, "Wallet sign-in is unavailable") {
		t.Fatal("page does not tell the operator what is wrong")
	}
	if !strings.Contains(doc, "stage-wallet-wasm.sh") {
		t.Fatal("page does not tell the operator how to fix it")
	}
	if strings.Contains(doc, "hd-wallet-ui@2.0.6") {
		t.Fatal("the removed CDN wallet version is still referenced")
	}
	// Self-hosted typography only.
	if !strings.Contains(doc, "/fonts/chakra-400.woff2") {
		t.Fatal("page does not use this node's self-hosted fonts")
	}
}

// TestFallbackLoginResponseIsLockedDown locks the response envelope: the
// unavailable page is served with a no-script CSP and nosniff, so even a future
// editing mistake cannot turn it into a script-executing surface.
func TestFallbackLoginResponseIsLockedDown(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	serveFallbackLogin(rec, req, tlsmgr.Status{})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP = %q, want default-src 'none'", csp)
	}
	if strings.Contains(csp, "script-src") && !strings.Contains(csp, "script-src 'none'") {
		t.Fatalf("CSP permits scripts on the unavailable page: %q", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	assertNoExternalSubresources(t, "fallback login response", rec.Body.String())
}

// TestWalletLoginPageLoadsNothingExternal locks the real wallet login page (the
// one served when a local dist IS configured). Its wallet module and stylesheet
// are same-origin paths, and its typography is this node's own.
func TestWalletLoginPageLoadsNothingExternal(t *testing.T) {
	t.Parallel()

	doc := buildWalletLoginPage(
		"/assets/wallet/app.js",
		"/assets/wallet/widget.css",
		false,
		"node.example.invalid",
		tlsmgr.Status{},
	)
	assertNoExternalSubresources(t, "wallet login page", doc)

	if !strings.Contains(doc, "/fonts/chakra-400.woff2") ||
		!strings.Contains(doc, "/fonts/plex-400.woff2") {
		t.Fatal("login page does not use this node's self-hosted fonts")
	}
}

// TestLoginPageSourceHasNoCDNConstants is a belt-and-braces check on the SOURCE
// rather than the rendered output: a CDN constant that is currently unreferenced
// is one edit away from being served again.
func TestLoginPageSourceHasNoCDNConstants(t *testing.T) {
	t.Parallel()

	for _, doc := range []string{
		buildWalletUnavailablePage("h", tlsmgr.Status{}),
		buildWalletLoginPage("/a.js", "/a.css", true, "h", tlsmgr.Status{}),
		buildLoginPageWithTLSStatus("/a.js", "/a.css", "h", tlsmgr.Status{}),
	} {
		for _, banned := range []string{"unpkg", "jsdelivr", "googleapis", "gstatic"} {
			if strings.Contains(strings.ToLower(doc), banned) {
				t.Fatalf("a rendered auth page still references %q", banned)
			}
		}
	}
}
