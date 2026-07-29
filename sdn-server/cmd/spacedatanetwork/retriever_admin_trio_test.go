package main

// THE ADMIT POINT IS NOT A PROFILE FEATURE.
//
// Owner order, 2026-07-28: "the CLI must work against EVERY daemon, not just
// the full sidecar." The retriever daemon on host-02 (admin.require_auth:false,
// loopback listener 127.0.0.1:5003) served GET /api/apps and mounted the
// run-now route, but 404'd POST /api/auth/challenge — because the auth handler
// was constructed INSIDE `if cfg.Admin.RequireAuth`. Every Admin-gated CLI
// command signs in through the §19 root ceremony first, so `apps run` could not
// work there at all: the route was present, the DOOR was missing.
//
// These tests lock the ruling:
//
//   - Every daemon with an admin listener mounts the SAME trio — auth
//     challenge/verify, the $APPS feed, and apps run-now — regardless of
//     require_auth and regardless of the listener being loopback-only.
//   - require_auth:false widens the READ surface only. Operator-authority paths
//     (run-now among them) keep the Admin gate the authenticated profile
//     applies, and fail CLOSED (503) if the admit point is unavailable.
//   - The CLI's `apps run` drives that shape end to end.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// retrieverProfileMux builds the admin mux a require_auth:false daemon serves
// after the fix: the REAL auth handler (root identity registered, routes
// mounted), the anonymous $APPS feed, and the run-now route. The run-now leg is
// a sentinel so the test measures the WALL and the ADMIT POINT — the two things
// that were broken — not the plugin manager behind them.
type retrieverProfile struct {
	mux      *http.ServeMux
	auth     *auth.Handler
	sessions *auth.SessionStore
	rootKey  ed25519.PrivateKey
	ranNow   *bool
}

func retrieverProfileMux(t *testing.T) retrieverProfile {
	t.Helper()

	rootPub, rootPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	dir := t.TempDir()
	userStore, err := auth.NewUserStore(filepath.Join(dir, "auth.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })
	sessions, err := auth.NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := auth.NewHandler(userStore, sessions, time.Hour, "", "")
	h.SetNodeRootIdentity(&auth.RootIdentity{
		XPub:        "xpub-retriever-node-root",
		Name:        "Node Root",
		SigningKeys: []ed25519.PublicKey{rootPub},
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	mux.HandleFunc("/api/apps", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"generated_at":"2026-07-29T00:00:00Z","apps":[` +
			`{"id":"org.spacedatanetwork.celestrak","name":"CelesTrak","kind":"flow","status":"running",` +
			`"timers":[{"trigger_id":"gp","interval_hours":3}],` +
			`"sources":[{"source_id":"celestrak-gp","last_records":0,"last_inserted":0}]}]}`))
	})

	ranNow := false
	mux.HandleFunc("/api/v1/modules/runtime/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/run") {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ranNow = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","startedAt":"2026-07-29T00:00:00Z",` +
			`"finishedAt":"2026-07-29T00:04:00Z","outputSize":4096}`))
	})

	return retrieverProfile{mux: mux, auth: h, sessions: sessions, rootKey: rootPriv, ranNow: &ranNow}
}

// retrieverProfileAnonymous is the anonymous policy a retriever runs with: the
// $APPS feed and the admit point are public, nothing else is.
func retrieverProfileAnonymous(method, path string) bool {
	if method == http.MethodPost {
		return path == "/api/auth/challenge" || path == "/api/auth/verify"
	}
	return method == http.MethodGet && path == "/api/apps"
}

// TestRetrieverProfileMountsTheAdminTrio is the regression lock on the defect
// verified live on host-02: /api/auth/challenge 404 on a require_auth:false
// daemon. It must be reachable, it must mint an Admin session for the node's
// own root key, and run-now must sit behind that session — never open.
func TestRetrieverProfileMountsTheAdminTrio(t *testing.T) {
	t.Parallel()

	p := retrieverProfileMux(t)
	rootPriv, ranNow := p.rootKey, p.ranNow
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// require_auth == FALSE — the retriever profile, verbatim.
		serveAdminMuxRequest(w, r, p.mux, false, false, p.auth, retrieverProfileAnonymous)
	}))
	defer srv.Close()

	const runPath = "/api/v1/modules/runtime/org.spacedatanetwork.celestrak/schedules/gp/run"

	// 1. THE DOOR EXISTS. This is the exact call that returned 404 live.
	c := &adminClient{baseURL: srv.URL, http: srv.Client()}
	var ch map[string]any
	if err := c.postJSON(context.Background(), "/api/auth/challenge",
		map[string]any{"client_pubkey_hex": hex.EncodeToString(rootPriv.Public().(ed25519.PublicKey)),
			"ts": time.Now().Unix()}, &ch, ""); err != nil {
		t.Fatalf("a require_auth:false daemon must still serve the admit point: %v", err)
	}
	if ch["challenge"] == "" || ch["challenge"] == nil {
		t.Fatalf("challenge response carried no challenge: %+v", ch)
	}

	// 2. RUN-NOW IS NOT OPEN. Fixing the door must not open the house.
	anon := &adminClient{baseURL: srv.URL, http: srv.Client()}
	if err := anon.postJSON(context.Background(), runPath, nil, nil, ""); err == nil {
		t.Fatal("run-now answered an UNAUTHENTICATED caller on a require_auth:false daemon")
	}
	if *ranNow {
		t.Fatal("run-now EXECUTED for an unauthenticated caller")
	}

	// 3. THE REAL CEREMONY OPENS IT. Same wire exchange the CLI performs.
	signed := &adminClient{baseURL: srv.URL, http: srv.Client()}
	token, err := signInWire(context.Background(), signed, rootPriv)
	if err != nil {
		t.Fatalf("§19 root ceremony failed against the retriever profile: %v", err)
	}
	signed.token = token
	var run map[string]any
	if err := signed.postJSON(context.Background(), runPath, nil, &run, ""); err != nil {
		t.Fatalf("run-now refused the node's own root session: %v", err)
	}
	if !*ranNow {
		t.Fatal("run-now did not execute for the authenticated root session")
	}
	if run["status"] != "ok" {
		t.Fatalf("run-now payload = %+v", run)
	}
}

// TestRetrieverProfileFailsClosedWithoutAdmitPoint locks the other half of the
// ruling: if the auth handler could not be built at all, an Admin-only path
// refuses (503). "No auth configured" must never mean "anyone may drive this
// node's schedules".
func TestRetrieverProfileFailsClosedWithoutAdmitPoint(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	served := false
	mux.HandleFunc("/api/v1/modules/runtime/", func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/modules/runtime/app/schedules/gp/run", nil)
	serveAdminMuxRequest(rec, req, mux, false, false, nil, retrieverProfileAnonymous)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail closed)", rec.Code)
	}
	if served {
		t.Fatal("run-now executed with NO admit point at all")
	}
}

// TestRetrieverProfileKeepsAnonymousReadsOpen guards the blast radius: the
// tightening above must not close the anonymous $APPS feed a node board polls.
func TestRetrieverProfileKeepsAnonymousReadsOpen(t *testing.T) {
	t.Parallel()

	p := retrieverProfileMux(t)
	rec := httptest.NewRecorder()
	serveAdminMuxRequest(rec, httptest.NewRequest(http.MethodGet, "/api/apps", nil),
		p.mux, false, false, p.auth, retrieverProfileAnonymous)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/apps status = %d, want 200 anonymous", rec.Code)
	}
}

// TestAppsRunCommandSucceedsAgainstRetrieverProfileDaemon drives the ACTUAL
// cobra command — trigger discovery from the $APPS feed, then the run-now POST
// — against a daemon in the retriever profile. This is the end-to-end shape the
// owner asked for: `spacedatanetwork apps run <app-id>` on a loopback-admin,
// require_auth:false node.
func TestAppsRunCommandSucceedsAgainstRetrieverProfileDaemon(t *testing.T) {
	p := retrieverProfileMux(t)
	ranNow := p.ranNow
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAdminMuxRequest(w, r, p.mux, false, false, p.auth, retrieverProfileAnonymous)
	}))
	defer srv.Close()

	// The CLI resolves its target from the daemon's OWN config, exactly as it
	// does on the host: admin.listen_addr, TLS disabled.
	addr := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := "storage:\n  path: " + dir + "\nadmin:\n  enabled: true\n  listen_addr: " +
		addr + "\n  require_auth: false\n  tls_enabled: false\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	oldConfigPath := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = oldConfigPath })

	// Mint the session through the REAL admit point this daemon now serves,
	// then hand it to the command the way an operator's environment does. The
	// seed-reading half of newAdminClient is covered by the §19 CLI tests; what
	// is under test here is that the command completes against this profile.
	sessionToken, err := p.sessions.CreateSession(
		"xpub-retriever-node-root", peers.Admin, "127.0.0.1", "cli-test", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Setenv("SDN_SESSION_TOKEN", sessionToken)

	var out bytes.Buffer
	cmd := appsRunCmd
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, []string{"org.spacedatanetwork.celestrak"}); err != nil {
		t.Fatalf("apps run failed against a retriever-profile daemon: %v\n%s", err, out.String())
	}
	if !*ranNow {
		t.Fatal("apps run reported success but the run-now route never fired")
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("apps run output did not report the run: %s", out.String())
	}
}

// TestHDWalletWasmResolvesBesideTheBinary locks the second half of "the CLI
// works against every daemon": the §19 ceremony derives the node root key
// through the HD-wallet wasm, so a deploy directory that ships the binary and
// its wasm together must resolve without env vars or leftovers from another
// install being on the box.
func TestHDWalletWasmResolvesBesideTheBinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wasmDir := filepath.Join(dir, "wasm")
	if err := os.MkdirAll(wasmDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	beside := filepath.Join(wasmDir, "hd-wallet-wasi.wasm")
	if err := os.WriteFile(beside, []byte("\x00asm"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := resolveHDWalletWasmPathFromInputs("", "", bundle.Layout{},
		append([]string{beside}, "/nonexistent/hd-wallet-wasi.wasm"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != beside {
		t.Fatalf("resolved %q, want the copy beside the binary %q", got, beside)
	}

	// And the real candidate list must PREFER the executable-relative paths over
	// the hard-coded absolute ones belonging to other installs.
	candidates := defaultHDWalletWasmCandidates()
	if len(candidates) < 4 {
		t.Fatalf("candidate list is too short: %v", candidates)
	}
	for i, c := range candidates[:3] {
		if !filepath.IsAbs(c) {
			t.Fatalf("candidate %d (%q) is not executable-relative", i, c)
		}
	}
	if !strings.HasSuffix(candidates[0], filepath.Join("wasm", "hd-wallet-wasi.wasm")) {
		t.Fatalf("first candidate should be <exeDir>/wasm/hd-wallet-wasi.wasm, got %q", candidates[0])
	}
}

// TestLoopbackSelfGatedAdminPathsStayReachableWithoutSession protects the §19
// publish trigger from the tightening above. The publish route's gate is
// loopback-only, enforced in its own handler, and the flow that calls it holds
// the `http` capability and no credential BY DESIGN. Making run-now demand a
// session must not quietly put a session in front of that door too.
func TestLoopbackSelfGatedAdminPathsStayReachableWithoutSession(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/admin/dataset-updates/publish",
		"/api/v1/admin/update/shutdown",
	} {
		if !isAdminOnlyAPIPath(path) {
			t.Fatalf("%s is expected to be under the admin prefix", path)
		}
		if !isLoopbackSelfGatedAdminPath(path) {
			t.Fatalf("%s must be recognised as loopback-self-gated", path)
		}

		reached := false
		mux := http.NewServeMux()
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		// require_auth:false, NO auth handler at all — the retriever's shape.
		serveAdminMuxRequest(rec, req, mux, false, false, nil, retrieverProfileAnonymous)

		if !reached || rec.Code != http.StatusOK {
			t.Fatalf("%s was gated by the session wall (status %d); the publish trigger would break", path, rec.Code)
		}
	}
}

// TestRunNowIsNotLoopbackExempt is the companion guard: the carve-out above is
// a NAMED list, never a prefix rule, so run-now cannot slip through it.
func TestRunNowIsNotLoopbackExempt(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/modules/runtime/app/schedules/gp/run",
		"/api/v1/admin/peers",
		"/api/v1/admin/dataset-updates/publish/../../modules/runtime/x/schedules/y/run",
	} {
		if isLoopbackSelfGatedAdminPath(path) {
			t.Fatalf("%s must NOT be exempt from the session gate", path)
		}
	}
}

// TestAppsListFallsBackToAnonymousRead locks the $APPS feed's contract on the
// CLI side: the feed is anonymous, so `apps list` must still work when this
// process cannot sign in (no readable seed), and must SAY it read anonymously.
func TestAppsListFallsBackToAnonymousRead(t *testing.T) {
	p := retrieverProfileMux(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAdminMuxRequest(w, r, p.mux, false, false, p.auth, retrieverProfileAnonymous)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := "storage:\n  path: " + dir + "\nadmin:\n  enabled: true\n  listen_addr: " +
		strings.TrimPrefix(srv.URL, "http://") + "\n  require_auth: false\n  tls_enabled: false\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	oldConfigPath := configPath
	configPath = cfgPath
	t.Cleanup(func() { configPath = oldConfigPath })

	// No seed on disk and no session token: sign-in cannot succeed.
	t.Setenv("SDN_SESSION_TOKEN", "")

	var out, errOut bytes.Buffer
	cmd := appsListCmd
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("apps list must not require a session for an anonymous feed: %v", err)
	}
	if !strings.Contains(out.String(), "org.spacedatanetwork.celestrak") {
		t.Fatalf("apps list did not render the feed: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "anonymously") {
		t.Fatalf("apps list read anonymously without saying so: %q", errOut.String())
	}
}

// TestRequireAuthOffKeepsAdminClassifiedReadsOpen is the blast-radius lock. The
// first cut of this change gated every isAdminOnlyAPIPath prefix and closed
// GET /api/v1/data/records/{schema}/{cid} on a require_auth:false node — caught
// by the plugin-demo integration suite, not by unit tests. The read surface of
// an auth-disabled node must not narrow by one route.
func TestRequireAuthOffKeepsAdminClassifiedReadsOpen(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/api/v1/data/records/OMM.fbs/bafyrecordcid",
		"/api/peers/graph",
		"/api/v1/data/summary",
	} {
		if !isAdminOnlyAPIPath(path) {
			t.Fatalf("%s is expected to be admin-classified; the test would prove nothing", path)
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			served := false
			mux := http.NewServeMux()
			mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
				served = true
				w.WriteHeader(http.StatusOK)
			})

			rec := httptest.NewRecorder()
			serveAdminMuxRequest(rec, httptest.NewRequest(method, path, nil),
				mux, false, false, nil, retrieverProfileAnonymous)

			if !served || rec.Code != http.StatusOK {
				t.Fatalf("%s %s was closed on a require_auth:false node (status %d)", method, path, rec.Code)
			}
		}
	}
}

// TestStateChangingMethodsAreGatedOnAuthDisabledNodes is the other side of the
// same coin: the WRITE half of those very prefixes still costs a session.
func TestStateChangingMethodsAreGatedOnAuthDisabledNodes(t *testing.T) {
	t.Parallel()

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "PURGE",
	} {
		if !isStateChangingMethod(method) {
			t.Fatalf("%s must count as state-changing", method)
		}
		served := false
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/modules/runtime/", func(w http.ResponseWriter, _ *http.Request) {
			served = true
			w.WriteHeader(http.StatusOK)
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/modules/runtime/app/schedules/gp/run", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		serveAdminMuxRequest(rec, req, mux, false, false, nil, retrieverProfileAnonymous)
		if served {
			t.Fatalf("%s reached the run-now handler unauthenticated", method)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503 with no admit point", method, rec.Code)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if isStateChangingMethod(method) {
			t.Fatalf("%s must NOT count as state-changing", method)
		}
	}
}
