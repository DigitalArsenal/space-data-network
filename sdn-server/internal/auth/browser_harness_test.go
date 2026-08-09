package auth

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/gateway"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// A REAL auth wall, on a real port, for a REAL browser to drive cross-origin.
//
// The acceptance for this work is a browser fact, not a Go fact: an anonymous
// page must READ a 401 (and say "sign in" rather than "the node is down"), and
// a signed-in page must get its credential write ACCEPTED and be able to read
// the answer. Neither can be shown with httptest recorders, because the thing
// under test is what a browser does with the headers.
//
// So this is a harness, not a test: it is inert unless SDN_AUTH_E2E_ADDR is
// set, and then it serves the genuine `RequireAuth` wall, the genuine
// `/api/auth/challenge` handler and the genuine signed-request verifier in
// front of a credential-shaped stub for SDN_AUTH_E2E_SECONDS.
//
// What it deliberately DOES mirror from cmd/spacedatanetwork/main.go, because
// leaving it out would make the browser evidence meaningless:
//   - anonymous routes (GET /providers, POST /aggregate, POST /api/auth/*)
//     carry the public-surface CORS headers and answer OPTIONS with 204;
//   - gated routes (PUT/DELETE /credentials/{id}) get no such treatment and
//     reach the wall, which is exactly where the decoration under test lives.
//
// What it does NOT do is store a credential. The module's store round trip
// belongs to the (blocked) cellular lane and needs its signed runtime; what is
// proven here is authority — that the write is authorised, accepted and
// readable.
func TestBrowserHarness(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("SDN_AUTH_E2E_ADDR"))
	if addr == "" {
		t.Skip("harness: set SDN_AUTH_E2E_ADDR to serve it (browser E2E only)")
	}
	seconds, _ := strconv.Atoi(os.Getenv("SDN_AUTH_E2E_SECONDS"))
	if seconds <= 0 {
		seconds = 180
	}

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer func() { _ = userStore.Close() }()

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	defer func() { _ = closer() }()
	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	// One enrolled operator with a BOUND signing key — the only kind of
	// identity signed-request admission will ever accept.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const operatorXPub = "xpub-e2e-operator"
	if err := userStore.AddUser(operatorXPub, "E2E Operator", peers.Standard, hex.EncodeToString(pub)); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	// A real X25519 slot so the page can run the real sealToKeySlot path.
	slotKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("X25519: %v", err)
	}

	h := NewHandler(userStore, sessions, time.Hour, "", "")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// The anonymous half of the cellular mount.
	mux.HandleFunc("/api/v1/cellular/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"providers": []map[string]any{{
				"id":                   "opencellid",
				"name":                 "OpenCellID",
				"credentialRequired":   true,
				"credentialConfigured": false,
				"credentialLane":       "cell_opencellid",
			}},
			"credentialLanes": []string{"cell_opencellid"},
			"keySlot": map[string]any{
				"slotId":    "provider-wrapping",
				"algorithm": "X25519",
				"publicKey": base64.StdEncoding.EncodeToString(slotKey.PublicKey().Bytes()),
			},
		})
	})

	// The gated half: the wall, then a credential-shaped answer.
	credential := h.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		session := SessionFromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{
			"credentialConfigured": r.Method == http.MethodPut,
			// The audit line the task asks about: FINGERPRINT ONLY, never the
			// raw xpub, and never anything derived from the sealed body.
			"operatorFingerprint": XPubFingerprint(session.XPub),
			"signedRequest":       session.SignedRequest,
		})
	})
	mux.HandleFunc("/api/v1/cellular/credentials/", credential)

	// Everything the daemon's adminSecurityMiddleware does that matters here:
	// public routes get CORS + a 204 preflight, gated routes get neither.
	anonymous := func(method, path string) bool {
		if method == http.MethodOptions {
			// The live gateway admits the PREFLIGHT for a mounted flow route
			// even where the write itself is gated — measured on host-01:
			// OPTIONS /api/v1/cellular/credentials/opencellid -> 204 + ACAO
			// while PUT on the same path -> 401. Modelling that exactly is what
			// makes this harness's browser evidence transferable to the node;
			// a harness that failed the preflight would prove nothing about the
			// refusal, because the refusal would never be requested.
			return anonymousRoute(http.MethodGet, path) ||
				anonymousRoute(http.MethodPost, path) ||
				strings.HasPrefix(path, "/api/v1/cellular/credentials/")
		}
		return anonymousRoute(method, path)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if anonymous(r.Method, r.URL.Path) {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" {
				origin = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
			gateway.AddVaryOrigin(w.Header())
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})

	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.ListenAndServe() }()
	defer func() { _ = server.Close() }()

	// The page needs the operator's key to sign with. Writing it to a file the
	// browser side reads keeps it out of the process table and out of this
	// test's output; it is a throwaway key that exists for one run.
	identity := map[string]string{
		"xpub":                operatorXPub,
		"signingPrivateKey":   hex.EncodeToString(priv.Seed()),
		"signingPublicKey":    hex.EncodeToString(pub),
		"expectedFingerprint": XPubFingerprint(operatorXPub),
	}
	blob, _ := json.MarshalIndent(identity, "", "  ")
	out := strings.TrimSpace(os.Getenv("SDN_AUTH_E2E_IDENTITY_FILE"))
	if out == "" {
		out = filepath.Join(os.TempDir(), "sdn-auth-e2e-identity.json")
	}
	if err := os.WriteFile(out, blob, 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
	fmt.Printf("HARNESS READY addr=%s identity=%s fingerprint=%s\n", addr, out, identity["expectedFingerprint"])

	time.Sleep(time.Duration(seconds) * time.Second)
}

// anonymousRoute mirrors the daemon's anonymous classification for the routes
// this harness serves: the auth admit points, and the cellular READS. The
// credential writes are absent on purpose — that is what makes them reach the
// wall.
func anonymousRoute(method, path string) bool {
	switch {
	case method == http.MethodPost && (path == "/api/auth/challenge" || path == "/api/auth/verify"):
		return true
	case method == http.MethodGet && path == "/api/auth/status":
		return true
	case method == http.MethodGet && path == "/api/v1/cellular/providers":
		return true
	case method == http.MethodPost && path == "/api/v1/cellular/aggregate":
		return true
	}
	return false
}
