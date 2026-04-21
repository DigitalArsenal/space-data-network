package auth

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestRequireAuth_RedirectsBrowserForbiddenToWalletLogin(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	token, err := sessions.CreateSession(
		"xpub-standard-user",
		peers.Standard,
		"127.0.0.1",
		"test-agent",
		time.Hour,
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	h := NewHandler(nil, sessions, time.Hour, "", "")
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()

	h.RequireAuth(peers.Admin, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); location != "/login?next=%2Fadmin%2F&unauthorized=1" {
		t.Fatalf("Location = %q, want %q", location, "/login?next=%2Fadmin%2F&unauthorized=1")
	}
}

func TestRequireAuth_RedirectsBrowserUnauthenticatedWebUIToWalletLogin(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(nil, sessions, time.Hour, "", "")
	req := httptest.NewRequest(http.MethodGet, "/webui/", nil)
	rec := httptest.NewRecorder()

	h.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); location != "/login?next=%2Fwebui%2F" {
		t.Fatalf("Location = %q, want %q", location, "/login?next=%2Fwebui%2F")
	}
}

func TestRequireAuth_RotatesNearExpirySessionAndSetsReplacementCookie(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	token, err := sessions.CreateSession(
		"xpub-standard-user",
		peers.Standard,
		"127.0.0.1",
		"test-agent",
		time.Hour,
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := sdb.Exec(
		"UPDATE sessions SET expires_at = ? WHERE token = ?",
		time.Now().Add(20*time.Minute).Unix(),
		hashToken(token),
	); err != nil {
		t.Fatalf("UPDATE sessions expiry: %v", err)
	}

	h := NewHandler(nil, sessions, time.Hour, "", "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()

	h.RequireAuth(peers.Standard, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	var replacement *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "sdn_wallet_session" {
			replacement = cookie
			break
		}
	}
	if replacement == nil {
		t.Fatalf("expected replacement session cookie")
	}
	if replacement.Value == token {
		t.Fatalf("expected rotated session token")
	}
	if _, err := sessions.ValidateSession(token); err == nil {
		t.Fatalf("expected original session to be revoked after rotation")
	}
	if rotated, err := sessions.ValidateSession(replacement.Value); err != nil {
		t.Fatalf("ValidateSession(replacement): %v", err)
	} else if rotated.XPub != "xpub-standard-user" {
		t.Fatalf("rotated session xpub = %q, want %q", rotated.XPub, "xpub-standard-user")
	}
}
