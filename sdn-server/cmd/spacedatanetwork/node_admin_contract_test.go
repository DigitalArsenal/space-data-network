package main

// Tests for graph task nst-node-admin-contract: the node-side gating and asset
// surface the dashboard's sign-in / This-Node editor consumes.
//
// This file is deliberately separate from main_test.go, which the repository's
// mnemonic guard forbids re-staging.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// TestNodeEPMPathPolicySplitsReadsFromWrites locks the top-level wall's half of
// the /api/node/epm contract: every read stays on the anonymous surface, and
// every non-read method is raised to Admin trust rather than the Standard
// default serveAdminMuxRequest would otherwise apply.
func TestNodeEPMPathPolicySplitsReadsFromWrites(t *testing.T) {
	t.Parallel()

	readPaths := []string{
		"/api/node/epm",
		"/api/node/epm/json",
		"/api/node/epm/vcard",
		"/api/node/epm/qr",
	}
	for _, path := range readPaths {
		path := path
		t.Run("public read "+path, func(t *testing.T) {
			t.Parallel()
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				if !isPublicAPIRequest(method, path) {
					t.Fatalf("%s %s is not on the anonymous read surface", method, path)
				}
			}
		})
	}

	t.Run("write is admin-only", func(t *testing.T) {
		t.Parallel()
		if !isAdminOnlyAPIPath("/api/node/epm") {
			t.Fatal("PUT /api/node/epm does not require admin trust at the top-level wall")
		}
		for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			if isPublicAPIRequest(method, "/api/node/epm") {
				t.Fatalf("%s /api/node/epm is treated as anonymous", method)
			}
		}
	})
}

// TestGateNodeEPMWriteRequiresAdminSession locks the mount-level, method
// granular half: reads pass through unauthenticated, writes need an Admin
// session, and a below-Admin session is refused.
func TestGateNodeEPMWriteRequiresAdminSession(t *testing.T) {
	t.Parallel()

	newGate := func(handler *auth.Handler, requireAuth bool) (http.HandlerFunc, *int) {
		calls := 0
		inner := func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
		}
		return gateNodeEPMWrite(inner, requireAuth, func() *auth.Handler { return handler }), &calls
	}

	t.Run("anonymous read passes", func(t *testing.T) {
		t.Parallel()
		handler, _ := newAdminSession(t, peers.Admin)
		gate, calls := newGate(handler, true)
		rec := httptest.NewRecorder()
		gate(rec, httptest.NewRequest(http.MethodGet, "/api/node/epm", nil))
		if rec.Code != http.StatusOK || *calls != 1 {
			t.Fatalf("anonymous GET status = %d, inner calls = %d; want 200, 1", rec.Code, *calls)
		}
	})

	t.Run("anonymous write is refused", func(t *testing.T) {
		t.Parallel()
		handler, _ := newAdminSession(t, peers.Admin)
		gate, calls := newGate(handler, true)
		rec := httptest.NewRecorder()
		gate(rec, httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader(`{"dn":"x"}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous PUT status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if *calls != 0 {
			t.Fatalf("anonymous PUT reached the EPM handler (%d calls)", *calls)
		}
	})

	for _, trust := range []peers.TrustLevel{peers.Unknown, peers.Marginal, peers.Standard, peers.Trusted} {
		trust := trust
		t.Run("write below admin is refused/"+trust.String(), func(t *testing.T) {
			t.Parallel()
			handler, token := newAdminSession(t, trust)
			gate, calls := newGate(handler, true)
			req := httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader(`{"dn":"x"}`))
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
			rec := httptest.NewRecorder()
			gate(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s PUT status = %d, want %d", trust, rec.Code, http.StatusForbidden)
			}
			if *calls != 0 {
				t.Fatalf("%s PUT reached the EPM handler (%d calls)", trust, *calls)
			}
		})
	}

	t.Run("admin write passes", func(t *testing.T) {
		t.Parallel()
		handler, token := newAdminSession(t, peers.Admin)
		gate, calls := newGate(handler, true)
		req := httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader(`{"dn":"x"}`))
		req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
		rec := httptest.NewRecorder()
		gate(rec, req)
		if rec.Code != http.StatusOK || *calls != 1 {
			t.Fatalf("admin PUT status = %d, inner calls = %d; want 200, 1: %s", rec.Code, *calls, rec.Body.String())
		}
	})

	t.Run("missing auth handler fails closed", func(t *testing.T) {
		t.Parallel()
		gate, calls := newGate(nil, true)
		rec := httptest.NewRecorder()
		gate(rec, httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if *calls != 0 {
			t.Fatalf("write reached the EPM handler without an auth backend (%d calls)", *calls)
		}
	})

	t.Run("auth disabled keeps open-admin behavior", func(t *testing.T) {
		t.Parallel()
		gate, calls := newGate(nil, false)
		rec := httptest.NewRecorder()
		gate(rec, httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader(`{}`)))
		if rec.Code != http.StatusOK || *calls != 1 {
			t.Fatalf("status = %d, inner calls = %d; want 200, 1", rec.Code, *calls)
		}
	})
}

// TestGateNodeEPMWriteReportsJSONNotARedirect locks that the gate answers API
// status codes. The dashboard calls this with fetch(); a login-page redirect
// would surface as an opaque HTML body.
func TestGateNodeEPMWriteReportsJSONNotARedirect(t *testing.T) {
	t.Parallel()

	handler, _ := newAdminSession(t, peers.Admin)
	gate := gateNodeEPMWrite(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, true, func() *auth.Handler { return handler })

	rec := httptest.NewRecorder()
	gate(rec, httptest.NewRequest(http.MethodPut, "/api/node/epm", strings.NewReader(`{}`)))

	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("gate redirected to %q", got)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON (%q): %v", rec.Body.String(), err)
	}
	if body.Code != "unauthorized" {
		t.Fatalf("error code = %q, want %q", body.Code, "unauthorized")
	}
}

// TestSessionIntrospectionIsReachableAtEveryTrustTier locks the ruling that
// /api/auth/me and /api/auth/logout impose no trust FLOOR: an operator who
// validly signed in at unknown or marginal tier must still be able to see who
// they are and end their own session. Gating them at the wall's Standard
// default made "signed in, insufficient permissions" an unrenderable state and
// stranded a live credential.
func TestSessionIntrospectionIsReachableAtEveryTrustTier(t *testing.T) {
	t.Parallel()

	newMux := func(reached *int) *http.ServeMux {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
			*reached++
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
			*reached++
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/api/auth/attest", func(w http.ResponseWriter, r *http.Request) {
			*reached++
			w.WriteHeader(http.StatusOK)
		})
		return mux
	}

	type call struct {
		method string
		path   string
	}
	sessionCalls := []call{
		{http.MethodGet, "/api/auth/me"},
		{http.MethodPost, "/api/auth/logout"},
	}

	// Every tier, including the bottom of the scale, reaches both.
	for _, trust := range []peers.TrustLevel{
		peers.Never, peers.Unknown, peers.Marginal, peers.Standard, peers.Trusted, peers.Admin,
	} {
		trust := trust
		t.Run("admitted/"+trust.String(), func(t *testing.T) {
			t.Parallel()
			authHandler, token := newAdminSession(t, trust)
			reached := 0
			mux := newMux(&reached)
			for _, c := range sessionCalls {
				req := httptest.NewRequest(c.method, c.path, nil)
				req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
				rec := httptest.NewRecorder()
				serveAdminMuxRequest(rec, req, mux, true, false, authHandler, notPublicAPI)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s at %s status = %d, want %d: %s",
						c.method, c.path, trust, rec.Code, http.StatusOK, rec.Body.String())
				}
			}
			if reached != len(sessionCalls) {
				t.Fatalf("handler reached %d times, want %d", reached, len(sessionCalls))
			}
		})
	}

	// No session is still refused: these are NOT anonymous.
	t.Run("anonymous is still refused", func(t *testing.T) {
		t.Parallel()
		authHandler, _ := newAdminSession(t, peers.Admin)
		reached := 0
		mux := newMux(&reached)
		for _, c := range sessionCalls {
			rec := httptest.NewRecorder()
			serveAdminMuxRequest(rec, httptest.NewRequest(c.method, c.path, nil), mux, true, false, authHandler, notPublicAPI)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous %s %s status = %d, want %d", c.method, c.path, rec.Code, http.StatusUnauthorized)
			}
		}
		if reached != 0 {
			t.Fatalf("anonymous request reached the handler (%d times)", reached)
		}
	})

	// /api/auth/attest deliberately did NOT move: it names an arbitrary xpub,
	// so it keeps the wall's Standard default.
	t.Run("attest keeps the standard floor", func(t *testing.T) {
		t.Parallel()
		for _, trust := range []peers.TrustLevel{peers.Unknown, peers.Marginal} {
			authHandler, token := newAdminSession(t, trust)
			reached := 0
			mux := newMux(&reached)
			req := httptest.NewRequest(http.MethodPost, "/api/auth/attest", nil)
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
			rec := httptest.NewRecorder()
			serveAdminMuxRequest(rec, req, mux, true, false, authHandler, notPublicAPI)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("attest at %s status = %d, want %d", trust, rec.Code, http.StatusForbidden)
			}
			if reached != 0 {
				t.Fatalf("attest reached the handler below standard trust")
			}
		}
	})
}

// TestAnyTierAuthenticatedAPIPathIsExactMatchOnly locks that the relaxed floor
// is an exact-match allow-list. A prefix rule would hand look-alike paths the
// bottom of the trust scale.
func TestAnyTierAuthenticatedAPIPathIsExactMatchOnly(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/auth/me", "/api/auth/logout"} {
		if !isAnyTierAuthenticatedAPIPath(path) {
			t.Fatalf("%q should be reachable at any authenticated tier", path)
		}
	}
	for _, path := range []string{
		"/api/auth/me/", "/api/auth/mex", "/api/auth/members", "/api/auth/logout/all",
		"/api/auth/attest", "/api/auth/users", "/api/auth/verify", "/api/node/epm", "/api/peers",
	} {
		if isAnyTierAuthenticatedAPIPath(path) {
			t.Fatalf("%q must not receive the any-tier floor", path)
		}
	}

	// The relaxed floor must never collide with the admin floor.
	for _, path := range []string{"/api/auth/me", "/api/auth/logout"} {
		if isAdminOnlyAPIPath(path) {
			t.Fatalf("%q is classified both admin-only and any-tier", path)
		}
	}

	// And it must never become anonymous — anonymous paths skip CSRF checks.
	for _, path := range []string{"/api/auth/me", "/api/auth/logout"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if isPublicAPIRequest(method, path) {
				t.Fatalf("%s %q must still require a session", method, path)
			}
		}
	}
}

// stageWalletWasm writes a minimal mirror of the hd-wallet-wasm dist tree and
// returns its root.
func stageWalletWasm(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"hd-wallet.js":                            "export default function(){}",
		"hd-wallet-wasi.wasm":                     "\x00asm\x01\x00\x00\x00",
		"runtime/index.mjs":                       "export default async function init(){}",
		"runtime/generated/aligned/generated.mjs": "export const SIZE = 1;",
		"runtime/index.d.ts":                      "declare const x: number;",
		"secret.txt":                              "not servable",
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", rel, err)
		}
	}
	return root
}

// TestWalletWasmHandlerServesTheStagedPackageTree locks that the sign-in wallet
// resolves SAME-ORIGIN with the package's own relative layout intact — the
// entry module, the loader it imports as ../hd-wallet.js, the nested generated
// modules, and the standalone WASI artifact — each with the media type a
// browser needs to execute it as a module.
func TestWalletWasmHandlerServesTheStagedPackageTree(t *testing.T) {
	t.Parallel()

	handler := makeWalletWasmHandler(stageWalletWasm(t))
	cases := map[string]string{
		"/wallet-wasm/runtime/index.mjs":                       "text/javascript; charset=utf-8",
		"/wallet-wasm/runtime/generated/aligned/generated.mjs": "text/javascript; charset=utf-8",
		"/wallet-wasm/hd-wallet.js":                            "text/javascript; charset=utf-8",
		"/wallet-wasm/hd-wallet-wasi.wasm":                     "application/wasm",
	}
	for path, wantType := range cases {
		path, wantType := path, wantType
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != wantType {
				t.Fatalf("Content-Type = %q, want %q", got, wantType)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if rec.Body.Len() == 0 {
				t.Fatal("empty body")
			}
		})
	}
}

// TestWalletWasmHandlerRefusesEverythingElse locks the surface shut: no
// traversal, no non-allow-listed extensions, no dot-files, no directory
// listings, no unbounded depth, and no methods beyond GET/HEAD.
func TestWalletWasmHandlerRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	root := stageWalletWasm(t)
	handler := makeWalletWasmHandler(root)

	notFound := []string{
		"/wallet-wasm/",
		"/wallet-wasm/runtime",
		"/wallet-wasm/runtime/",
		"/wallet-wasm/secret.txt",
		"/wallet-wasm/runtime/index.d.ts",
		"/wallet-wasm/../../etc/passwd",
		"/wallet-wasm/..%2f..%2fetc%2fpasswd",
		"/wallet-wasm/runtime/../../hd-wallet.js",
		"/wallet-wasm/.hidden.mjs",
		"/wallet-wasm/runtime/.hidden.mjs",
		"/wallet-wasm/a/b/c/d/e.mjs",
		"/wallet-wasm/missing.mjs",
	}
	for _, path := range notFound {
		path := path
		t.Run("404 "+path, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		method := method
		t.Run("405 "+method, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(method, "/wallet-wasm/runtime/index.mjs", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

// TestWalletWasmHandlerFailsOpenWhenUnstaged locks the fail-open posture an
// unconfigured or unstaged node keeps: 404, never a 500 and never a leak of the
// process working directory.
func TestWalletWasmHandlerFailsOpenWhenUnstaged(t *testing.T) {
	t.Parallel()

	for name, dir := range map[string]string{
		"unconfigured": "",
		"missing dir":  filepath.Join(t.TempDir(), "never-staged"),
	} {
		name, dir := name, dir
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			handler := makeWalletWasmHandler(dir)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wallet-wasm/runtime/index.mjs", nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}
}

// TestWalletWasmSurfaceIsNotBehindTheAPIWall locks that the wallet assets stay
// reachable to an anonymous browser: the sign-in page must be able to LOAD the
// wallet before it can possibly hold a session.
func TestWalletWasmSurfaceIsNotBehindTheAPIWall(t *testing.T) {
	t.Parallel()

	authHandler, _ := newAdminSession(t, peers.Admin)
	adminMux := http.NewServeMux()
	adminMux.Handle("/wallet-wasm/", makeWalletWasmHandler(stageWalletWasm(t)))

	// No session cookie: the wall must let this through anyway, because
	// /wallet-wasm/ is neither an /api/ nor an /orbpro-key-broker/ path.
	req := httptest.NewRequest(http.MethodGet, "/wallet-wasm/runtime/index.mjs", nil)
	rec := httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — the wallet must load before sign-in", rec.Code, http.StatusOK)
	}
}
