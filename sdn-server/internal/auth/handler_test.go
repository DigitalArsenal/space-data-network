package auth

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestAuth_ChallengeVerify_SucceedsWithBoundKey(t *testing.T) {
	t.Parallel()

	// Generate an Ed25519 keypair for auth.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{
			XPub:             "xpub-test-admin",
			SigningPubKeyHex: pubHex,
			TrustLevel:       "admin",
			Name:             "Test Admin",
		},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(userStore, sessions, 24*time.Hour, "", "")

	// Step 1: request challenge
	chReqBody, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-test-admin",
		"client_pubkey_hex": pubHex,
		"ts":                time.Now().Unix(),
	})
	chReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(chReqBody))
	chReq.RemoteAddr = "127.0.0.1:12345"
	chRec := httptest.NewRecorder()
	h.handleChallenge(chRec, chReq)

	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge status: got %d want %d: %s", chRec.Code, http.StatusOK, chRec.Body.String())
	}

	var chResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(chRec.Body.Bytes(), &chResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}
	if chResp.ChallengeID == "" || chResp.Challenge == "" {
		t.Fatalf("challenge response missing fields: %#v", chResp)
	}
	challengeBytes, err := base64.RawStdEncoding.DecodeString(chResp.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	// Step 2: sign and verify
	sig := ed25519.Sign(priv, challengeBytes)
	verReqBody, _ := json.Marshal(map[string]any{
		"challenge_id":      chResp.ChallengeID,
		"xpub":              "xpub-test-admin",
		"client_pubkey_hex": pubHex,
		"challenge":         chResp.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	})
	verReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(verReqBody))
	verReq.RemoteAddr = "127.0.0.1:12345"
	verRec := httptest.NewRecorder()
	h.handleVerify(verRec, verReq)

	if verRec.Code != http.StatusOK {
		t.Fatalf("verify status: got %d want %d: %s", verRec.Code, http.StatusOK, verRec.Body.String())
	}
	if cookie := verRec.Header().Get("Set-Cookie"); cookie == "" {
		t.Fatalf("expected Set-Cookie to be set")
	}

	var verResp struct {
		User struct {
			XPub       string           `json:"xpub"`
			TrustLevel peers.TrustLevel `json:"trust_level"`
		} `json:"user"`
	}
	if err := json.Unmarshal(verRec.Body.Bytes(), &verResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}
	if verResp.User.XPub != "xpub-test-admin" {
		t.Fatalf("unexpected xpub: %q", verResp.User.XPub)
	}
	if verResp.User.TrustLevel < peers.Admin {
		t.Fatalf("unexpected trust level: %v", verResp.User.TrustLevel)
	}
}

func TestAuth_ChallengeVerify_FailsWithMismatchedKey(t *testing.T) {
	t.Parallel()

	// Configured key for the user.
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	// Attacker uses a different keypair.
	attPub, attPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey(attacker): %v", err)
	}
	attPubHex := hex.EncodeToString(attPub)

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{
			XPub:             "xpub-test-user",
			SigningPubKeyHex: pubHex,
			TrustLevel:       "standard",
			Name:             "Test User",
		},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(userStore, sessions, 24*time.Hour, "", "")

	// Request a challenge, but with the wrong pubkey.
	chReqBody, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-test-user",
		"client_pubkey_hex": attPubHex,
		"ts":                time.Now().Unix(),
	})
	chReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(chReqBody))
	chReq.RemoteAddr = "127.0.0.1:12345"
	chRec := httptest.NewRecorder()
	h.handleChallenge(chRec, chReq)

	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge status: got %d want %d: %s", chRec.Code, http.StatusOK, chRec.Body.String())
	}

	var chResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(chRec.Body.Bytes(), &chResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}

	challengeBytes, err := base64.RawStdEncoding.DecodeString(chResp.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	sig := ed25519.Sign(attPriv, challengeBytes)

	verReqBody, _ := json.Marshal(map[string]any{
		"challenge_id":      chResp.ChallengeID,
		"xpub":              "xpub-test-user",
		"client_pubkey_hex": attPubHex,
		"challenge":         chResp.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	})
	verReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(verReqBody))
	verReq.RemoteAddr = "127.0.0.1:12345"
	verRec := httptest.NewRecorder()
	h.handleVerify(verRec, verReq)

	if verRec.Code != http.StatusForbidden {
		t.Fatalf("verify status: got %d want %d: %s", verRec.Code, http.StatusForbidden, verRec.Body.String())
	}
}

func TestAuth_TOFU_BindsSigningKeyOnFirstLogin(t *testing.T) {
	t.Parallel()

	// Client keypair — will be bound via TOFU.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), []config.UserEntry{
		{
			XPub:       "xpub-tofu-admin",
			TrustLevel: "admin",
			Name:       "TOFU Admin",
			// No SigningPubKeyHex — will be bound on first login.
		},
	})
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	defer userStore.Close()

	// HasAdmin should return true even without a signing key.
	if !userStore.HasAdmin() {
		t.Fatalf("HasAdmin() should return true for config admin without signing key")
	}

	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	h := NewHandler(userStore, sessions, 24*time.Hour, "", "")

	// Step 1: challenge with no pre-bound signing key → TOFU mode.
	chReqBody, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-tofu-admin",
		"client_pubkey_hex": pubHex,
		"ts":                time.Now().Unix(),
	})
	chReq := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(chReqBody))
	chReq.RemoteAddr = "127.0.0.1:12345"
	chRec := httptest.NewRecorder()
	h.handleChallenge(chRec, chReq)

	if chRec.Code != http.StatusOK {
		t.Fatalf("challenge status: got %d want %d: %s", chRec.Code, http.StatusOK, chRec.Body.String())
	}

	var chResp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(chRec.Body.Bytes(), &chResp); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}

	challengeBytes, err := base64.RawStdEncoding.DecodeString(chResp.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	// Step 2: sign and verify — should succeed and bind the signing key.
	sig := ed25519.Sign(priv, challengeBytes)
	verReqBody, _ := json.Marshal(map[string]any{
		"challenge_id":      chResp.ChallengeID,
		"xpub":              "xpub-tofu-admin",
		"client_pubkey_hex": pubHex,
		"challenge":         chResp.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	})
	verReq := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(verReqBody))
	verReq.RemoteAddr = "127.0.0.1:12345"
	verRec := httptest.NewRecorder()
	h.handleVerify(verRec, verReq)

	if verRec.Code != http.StatusOK {
		t.Fatalf("verify status: got %d want %d: %s", verRec.Code, http.StatusOK, verRec.Body.String())
	}

	// Verify the signing key was bound in the store.
	user, err := userStore.GetUser("xpub-tofu-admin")
	if err != nil || user == nil {
		t.Fatalf("GetUser after TOFU: %v", err)
	}
	if user.SigningPubKeyHex != pubHex {
		t.Fatalf("signing key not bound: got %q want %q", user.SigningPubKeyHex, pubHex)
	}

	// Step 3: a different key should now be rejected (key is bound).
	attPub, attPriv, _ := ed25519.GenerateKey(nil)
	attPubHex := hex.EncodeToString(attPub)

	ch2Body, _ := json.Marshal(map[string]any{
		"xpub":              "xpub-tofu-admin",
		"client_pubkey_hex": attPubHex,
		"ts":                time.Now().Unix(),
	})
	ch2Req := httptest.NewRequest(http.MethodPost, "/api/auth/challenge", bytes.NewReader(ch2Body))
	ch2Req.RemoteAddr = "127.0.0.1:12345"
	ch2Rec := httptest.NewRecorder()
	h.handleChallenge(ch2Rec, ch2Req)

	var ch2Resp struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	json.Unmarshal(ch2Rec.Body.Bytes(), &ch2Resp)
	ch2Bytes, _ := base64.RawStdEncoding.DecodeString(ch2Resp.Challenge)
	attSig := ed25519.Sign(attPriv, ch2Bytes)

	ver2Body, _ := json.Marshal(map[string]any{
		"challenge_id":      ch2Resp.ChallengeID,
		"xpub":              "xpub-tofu-admin",
		"client_pubkey_hex": attPubHex,
		"challenge":         ch2Resp.Challenge,
		"signature_hex":     hex.EncodeToString(attSig),
	})
	ver2Req := httptest.NewRequest(http.MethodPost, "/api/auth/verify", bytes.NewReader(ver2Body))
	ver2Req.RemoteAddr = "127.0.0.1:12345"
	ver2Rec := httptest.NewRecorder()
	h.handleVerify(ver2Rec, ver2Req)

	if ver2Rec.Code != http.StatusForbidden {
		t.Fatalf("attacker verify status: got %d want %d (key should be bound now)", ver2Rec.Code, http.StatusForbidden)
	}
}

func TestLoginPage_UsesCDNWalletUIFallbackWhenNoLocalDistIsConfigured(t *testing.T) {
	t.Parallel()

	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()

	h.handleLoginPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login status: got %d want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "https://unpkg.com/hd-wallet-ui@2.0.2/src/app.js?module") {
		t.Fatalf("login page missing CDN wallet-ui module: %s", body)
	}
	if !strings.Contains(body, "https://unpkg.com/hd-wallet-ui@2.0.2/styles/widget.css") {
		t.Fatalf("login page missing CDN wallet-ui stylesheet: %s", body)
	}
	if !strings.Contains(body, "createWalletUI") {
		t.Fatalf("login page missing wallet initialization hook: %s", body)
	}
}

func TestLoginPage_BuildersExposeWalletAccountSurfaceForUnauthorizedUsers(t *testing.T) {
	t.Parallel()

	pages := []struct {
		name string
		html string
	}{
		{
			name: "hosted wallet dist page",
			html: buildLoginPage("/wallet-ui/dist/assets/wallet.js", "/wallet-ui/dist/assets/wallet.css"),
		},
		{
			name: "fallback CDN page",
			html: buildFallbackLoginPage(),
		},
	}

	for _, page := range pages {
		page := page
		t.Run(page.name, func(t *testing.T) {
			if !strings.Contains(page.html, ">Login<") {
				t.Fatalf("page missing explicit login button: %s", page.html)
			}
			if !strings.Contains(page.html, "window.__sdnOpenWalletAccount") {
				t.Fatalf("page missing wallet account modal hook: %s", page.html)
			}
			if !strings.Contains(page.html, "window.__sdnResolveNextPath") {
				t.Fatalf("page missing shared post-login target helper: %s", page.html)
			}
			if !strings.Contains(page.html, "SPACE DATA NETWORK") {
				t.Fatalf("page missing SDN title: %s", page.html)
			}
			if !strings.Contains(page.html, `href="https://ipfs.tech/"`) {
				t.Fatalf("page missing IPFS summary link: %s", page.html)
			}
			if !strings.Contains(page.html, `href="https://libp2p.io/"`) {
				t.Fatalf("page missing libp2p summary link: %s", page.html)
			}
			if !strings.Contains(page.html, "Nodes detected") {
				t.Fatalf("page missing detected node metric: %s", page.html)
			}
			if !strings.Contains(page.html, "window.__sdnRefreshNodeCount") {
				t.Fatalf("page missing node-count refresh hook: %s", page.html)
			}
			if !strings.Contains(page.html, "digitalarsenal.github.io/flatbuffers") {
				t.Fatalf("page missing FlatBuffers technology link: %s", page.html)
			}
			if !strings.Contains(page.html, "digitalarsenal.github.io/flatsql") {
				t.Fatalf("page missing FlatSQL technology link: %s", page.html)
			}
			if !strings.Contains(page.html, "spacedatastandards.org") {
				t.Fatalf("page missing Space Data Standards technology link: %s", page.html)
			}
			if !strings.Contains(page.html, `href="https://spacedatanet.org/"`) {
				t.Fatalf("page missing SDN homepage link: %s", page.html)
			}
			if !strings.Contains(page.html, "window.__sdnEnsureWalletUI") {
				t.Fatalf("page missing lazy wallet loader hook: %s", page.html)
			}
			if strings.Contains(page.html, `/wallet-ui/src/app.js`) {
				t.Fatalf("page should not reference the source wallet module on initial render: %s", page.html)
			}
			if strings.Contains(page.html, "Open Wallet") {
				t.Fatalf("page should not expose a separate wallet button: %s", page.html)
			}
			if strings.Contains(page.html, "Protected Surfaces") {
				t.Fatalf("page should not render the protected surfaces card: %s", page.html)
			}
			if strings.Contains(page.html, "window.__sdnWalletQuery.get('unauthorized') === '1'") {
				t.Fatalf("page should not auto-open the wallet modal by default: %s", page.html)
			}
		})
	}
}

func TestLoginPage_BuildersWrapShellInCenteredStage(t *testing.T) {
	t.Parallel()

	html := buildLoginPage("/wallet-ui/dist/assets/wallet.js", "/wallet-ui/dist/assets/wallet.css")

	if !strings.Contains(html, ".sdn-stage{") {
		t.Fatalf("login page missing dedicated stage styles: %s", html)
	}
	if !strings.Contains(html, "display:flex") {
		t.Fatalf("login page missing flex stage layout: %s", html)
	}
	if !strings.Contains(html, "align-items:center") {
		t.Fatalf("login page missing vertical centering: %s", html)
	}
	if !strings.Contains(html, "justify-content:center") {
		t.Fatalf("login page missing horizontal centering: %s", html)
	}
	if !strings.Contains(html, `<div class="sdn-stage">`) {
		t.Fatalf("login page missing stage wrapper: %s", html)
	}
	if !strings.Contains(html, `<main class="sdn-shell" aria-label="Space Data Network login">`) {
		t.Fatalf("login page missing SDN shell main element: %s", html)
	}
}

func TestLoginPage_BuildersDisablePasskeyStorageOnLocalIPHosts(t *testing.T) {
	t.Parallel()

	html := buildLoginPage("/wallet-ui/dist/assets/wallet.js", "/wallet-ui/dist/assets/wallet.css")

	if !strings.Contains(html, "window.__sdnShouldDisablePasskeys") {
		t.Fatalf("login page missing local passkey guard helper: %s", html)
	}
	if !strings.Contains(html, "window.location.hostname") {
		t.Fatalf("login page missing hostname-based passkey guard: %s", html)
	}
	if !strings.Contains(html, ".remember-method-btn[data-target=\"") {
		t.Fatalf("login page missing embedded wallet remember-method pin selector hook: %s", html)
	}
	if !strings.Contains(html, "unlock-with-passkey") {
		t.Fatalf("login page missing stored passkey suppression hook: %s", html)
	}
	if !strings.Contains(html, "window.__sdnNormalizeWalletLoginUI") {
		t.Fatalf("login page missing wallet login normalization hook: %s", html)
	}
}

func TestCachedLoginPage_UsesBundledWalletDistArtifactsInsteadOfSourceModules(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("MkdirAll src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "styles"), 0o755); err != nil {
		t.Fatalf("MkdirAll styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist", "assets"), 0o755); err != nil {
		t.Fatalf("MkdirAll assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "app.js"), []byte("export function createWalletUI(){}"), 0o644); err != nil {
		t.Fatalf("WriteFile src/app.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "styles", "widget.css"), []byte("#hd-wallet-ui-container{}"), 0o644); err != nil {
		t.Fatalf("WriteFile styles/widget.css: %v", err)
	}
	indexHTML := `<!doctype html><html><head><link rel="stylesheet" crossorigin href="./assets/main-test.css"></head><body><script type="module" crossorigin src="./assets/main-test.js"></script></body></html>`
	if err := os.WriteFile(filepath.Join(dir, "dist", "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("WriteFile dist/index.html: %v", err)
	}

	prevOnce := loginPageOnce
	prevCache := loginPageCache
	prevJS := walletJSFile
	prevCSS := walletCSSFile
	loginPageOnce = sync.Once{}
	loginPageCache = ""
	walletJSFile = ""
	walletCSSFile = ""
	defer func() {
		loginPageOnce = prevOnce
		loginPageCache = prevCache
		walletJSFile = prevJS
		walletCSSFile = prevCSS
	}()

	html := cachedLoginPage(dir)
	if !strings.Contains(html, "/wallet-ui/dist/assets/main-test.js") {
		t.Fatalf("cached login page missing bundled wallet JS path: %s", html)
	}
	if !strings.Contains(html, "/wallet-ui/dist/assets/main-test.css") {
		t.Fatalf("cached login page missing bundled wallet CSS path: %s", html)
	}
	if strings.Contains(html, "/wallet-ui/src/app.js") {
		t.Fatalf("cached login page should not reference source wallet module: %s", html)
	}
	if strings.Contains(html, "/wallet-ui/styles/widget.css") {
		t.Fatalf("cached login page should not reference source wallet stylesheet: %s", html)
	}
}

func TestLoginPage_AllowsUnauthorizedSessionsToReachWalletSurface(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/login?unauthorized=1", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()

	h.handleLoginPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if location := rec.Header().Get("Location"); location != "" {
		t.Fatalf("unexpected redirect to %q", location)
	}
}

func TestLoginPage_RedirectsAuthenticatedStandardUsersToRequestedWebUI(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/login?next=/webui/", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()

	h.handleLoginPage(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); location != "/webui/" {
		t.Fatalf("Location = %q, want %q", location, "/webui/")
	}
}
