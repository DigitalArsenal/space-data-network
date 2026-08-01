package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// Regression for ops-update-shutdown-401: on a require_auth node the wallet
// wall default-denied /api/v1/admin/update/shutdown before the handler's own
// designed gate (loopback RemoteAddr + one-time control token) could run, so
// an unattended fleet update reported success while the OLD binary kept
// running. The wall must wave through a self-gated admin path ONLY when the
// request demonstrably originates on loopback; everything remote stays behind
// the wallet wall.
func TestAdminWalletWallAdmitsLoopbackSelfGatedUpdateControl(t *testing.T) {
	authHandler, _ := newAdminSession(t, peers.Standard)
	adminMux := http.NewServeMux()
	reached := 0
	adminMux.HandleFunc("/api/v1/admin/update/shutdown", func(w http.ResponseWriter, r *http.Request) {
		reached++
		// Stand-in for the real control handler's own gate output.
		w.WriteHeader(http.StatusForbidden)
	})

	// Loopback origin: must REACH the handler (whose own token gate answers),
	// not be swallowed by the wallet wall's 401.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI)
	if reached != 1 {
		t.Fatalf("loopback self-gated request never reached its handler (status=%d)", rec.Code)
	}
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("loopback self-gated request still wallet-walled: %d", rec.Code)
	}

	// IPv6 loopback too — the helper daemon may dial ::1.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", nil)
	req.RemoteAddr = "[::1]:54321"
	rec = httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI)
	if reached != 2 {
		t.Fatalf("IPv6 loopback self-gated request never reached its handler (status=%d)", rec.Code)
	}

	// Remote origin: the wall must hold exactly as before.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/update/shutdown", nil)
	req.RemoteAddr = "192.0.2.9:44444"
	rec = httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI)
	if reached != 2 {
		t.Fatal("REMOTE request to the self-gated path reached the handler — the wall is open")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote self-gated request status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// A non-self-gated admin path from loopback must still be wallet-walled:
	// the carve-out is the two designed paths, not "loopback bypasses auth".
	adminMux.HandleFunc("/api/peers/protected", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("non-self-gated admin path bypassed the wall from loopback")
	})
	req = httptest.NewRequest(http.MethodPost, "/api/peers/protected", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec = httptest.NewRecorder()
	serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("loopback non-self-gated admin path status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
