package auth

import (
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/tlsmgr"
)

// handleLoginPage serves the wallet-gated SDN landing page used by /login,
// /admin, and /webui. The page presents a centered SDN shell, shows a brief
// network summary plus live detected-node count, and only opens the wallet UI
// when the user clicks "Login".
//
// After wallet authentication, window.__sdnOnLogin performs the Ed25519
// challenge-response against /api/auth/* and redirects only when the signed-in
// user has permission for the requested protected surface.
//
// If no local wallet-ui dist is available the handler returns a fallback page.
func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if session, err := h.sessionFromRequest(r); err == nil && session != nil {
		session = h.maybeRefreshSessionCookie(w, r, session)
		if target := authorizedPostLoginPath(session.TrustLevel, r.URL.Query().Get("next")); target != "" && !loginPageRequiresWalletAccount(r) {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if session.TrustLevel >= peers.Admin && !loginPageRequiresWalletAccount(r) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}
		if loginPageRequiresWalletAccount(r) {
			if target := authorizedPostLoginPath(session.TrustLevel, r.URL.Query().Get("next")); target != "" && !strings.HasPrefix(target, "/admin/") {
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
		}
	}

	tlsStatus := tlsmgr.Status{}
	if h.tlsManager != nil {
		tlsStatus = h.tlsManager.Status()
	}

	walletUI := strings.TrimSpace(h.walletUIPath)
	if walletUI == "" {
		serveFallbackLogin(w, r, tlsStatus)
		return
	}

	if cachedLoginPage(walletUI) == "" || walletJSFile == "" || walletCSSFile == "" {
		serveFallbackLogin(w, r, tlsStatus)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(buildLoginPageWithTLSStatus("/wallet-ui/"+walletJSFile, "/wallet-ui/"+walletCSSFile, loginPageHost(r), tlsStatus)))
}

func loginPageRequiresWalletAccount(r *http.Request) bool {
	return strings.TrimSpace(r.URL.Query().Get("unauthorized")) == "1"
}

func authorizedPostLoginPath(trust peers.TrustLevel, next string) string {
	target := sanitizePostLoginPath(next)
	if target == "" {
		return ""
	}

	switch {
	case target == "/admin/" || strings.HasPrefix(target, "/admin/"):
		if trust >= peers.Admin {
			return target
		}
	case target == "/webui/" || strings.HasPrefix(target, "/webui/"):
		if trust >= peers.Standard {
			return target
		}
	case target == "/":
		return target
	}

	return ""
}

// ---------------------------------------------------------------------------
// Login page builder
// ---------------------------------------------------------------------------

var (
	loginPageOnce  sync.Once
	loginPageCache string

	walletJSFile  string
	walletCSSFile string

	reScriptSrc = regexp.MustCompile(`src="\.\/assets\/(main-[^"]+\.js)"`)
	reCSSHref   = regexp.MustCompile(`href="\.\/assets\/(main-[^"]+\.css)"`)
)

const loginPageSummaryHTML = `A decentralized peer-to-peer network for standardized space data exchange, built on <a class="sdn-summary-link" href="https://ipfs.tech/" target="_blank" rel="noreferrer">IPFS</a> and <a class="sdn-summary-link" href="https://libp2p.io/" target="_blank" rel="noreferrer">libp2p</a> for real-time collaboration without a central server.`

// WalletUIStaticRoot resolves the filesystem root that should be mounted at
// /wallet-ui/. When a config points at wallet-ui/dist, this prefers the package
// root so the daemon can serve both packaged assets and any bundled dist files.
func WalletUIStaticRoot(walletUIPath string) string {
	if root := resolveWalletUIPackageRoot(walletUIPath); root != "" {
		return root
	}
	return strings.TrimSpace(walletUIPath)
}

// DiscoverWalletAssets discovers any legacy wallet-ui dist assets and caches the
// rendered login page when a local bundled wallet-ui build is available.
func DiscoverWalletAssets(walletUIPath string) {
	if walletUIPath == "" {
		return
	}
	cachedLoginPage(walletUIPath)
}

// WalletAssets returns relative legacy wallet-ui asset paths for admin fallbacks.
func WalletAssets() (jsFile, cssFile string) {
	return walletJSFile, walletCSSFile
}

// cachedLoginPage discovers a local wallet-ui package once, caches the login
// page, and records any dist asset paths needed by legacy admin fallbacks.
func cachedLoginPage(walletUIPath string) string {
	loginPageOnce.Do(func() {
		staticRoot := WalletUIStaticRoot(walletUIPath)
		if staticRoot == "" {
			return
		}

		if jsPath, cssPath := discoverLegacyWalletAssets(staticRoot, walletUIPath); jsPath != "" {
			walletJSFile = jsPath
			walletCSSFile = cssPath
		}

		if jsPath, cssPath := walletJSFile, walletCSSFile; jsPath != "" && cssPath != "" {
			loginPageCache = buildLoginPage("/wallet-ui/"+jsPath, "/wallet-ui/"+cssPath)
			return
		}
	})
	return loginPageCache
}

func resolveWalletUIPackageRoot(walletUIPath string) string {
	trimmed := strings.TrimSpace(walletUIPath)
	if trimmed == "" {
		return ""
	}

	for _, candidate := range uniqueNonEmptyPaths(trimmed, filepath.Dir(trimmed)) {
		if fileExists(filepath.Join(candidate, "src", "app.js")) && fileExists(filepath.Join(candidate, "styles", "widget.css")) {
			return candidate
		}
	}

	return ""
}

func resolveWalletUIDistRoot(walletUIPath string) string {
	trimmed := strings.TrimSpace(walletUIPath)
	if trimmed == "" {
		return ""
	}

	candidates := uniqueNonEmptyPaths(
		trimmed,
		filepath.Join(trimmed, "dist"),
		filepath.Join(filepath.Dir(trimmed), "dist"),
	)

	for _, candidate := range candidates {
		if fileExists(filepath.Join(candidate, "index.html")) && dirExists(filepath.Join(candidate, "assets")) {
			return candidate
		}
	}

	return ""
}

func discoverLegacyWalletAssets(staticRoot, walletUIPath string) (jsPath, cssPath string) {
	distRoot := resolveWalletUIDistRoot(walletUIPath)
	if distRoot == "" || staticRoot == "" {
		return "", ""
	}

	raw, err := os.ReadFile(filepath.Join(distRoot, "index.html"))
	if err != nil {
		return "", ""
	}
	src := string(raw)

	jsMatch := reScriptSrc.FindStringSubmatch(src)
	cssMatch := reCSSHref.FindStringSubmatch(src)

	if len(jsMatch) > 1 {
		jsPath = relativeAssetPath(staticRoot, filepath.Join(distRoot, "assets", jsMatch[1]))
	}
	if len(cssMatch) > 1 {
		cssPath = relativeAssetPath(staticRoot, filepath.Join(distRoot, "assets", cssMatch[1]))
	}

	return jsPath, cssPath
}

func relativeAssetPath(staticRoot, assetPath string) string {
	if staticRoot == "" || assetPath == "" {
		return ""
	}
	rel, err := filepath.Rel(staticRoot, assetPath)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func uniqueNonEmptyPaths(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

// buildLoginPage returns the full HTML for the SDN login page using bundled
// wallet-ui dist artifacts that auto-initialize and call window.__sdnWalletReady.
func buildLoginPage(moduleURL, cssURL string) string {
	return buildWalletLoginPage(moduleURL, cssURL, true, "", tlsmgr.Status{})
}

func buildLoginPageWithTLSStatus(moduleURL, cssURL, host string, status tlsmgr.Status) string {
	return buildWalletLoginPage(moduleURL, cssURL, true, host, status)
}

func buildWalletLoginPage(moduleURL, cssURL string, bundledAutoInit bool, host string, tlsStatus tlsmgr.Status) string {
	showLocalTLSInfo := shouldRenderLocalTLSInfo(host)
	walletLoader := `
  window.__sdnEnsureWalletUI = async function() {
    if (window.__sdnWalletUI) return window.__sdnWalletUI;
    if (window.__sdnWalletInitPromise) return window.__sdnWalletInitPromise;
    window.__sdnWalletInitPromise = (async function() {
      await window.__sdnEnsureWalletUIStyles();
      var ready = window.__sdnAwaitWalletReady();
      await import('` + moduleURL + `');
      return await ready;
    })().catch(function(err) {
      window.__sdnWalletInitPromise = null;
      throw err;
    });
    return window.__sdnWalletInitPromise;
  };`
	if !bundledAutoInit {
		walletLoader = `
  window.__sdnEnsureWalletUI = async function() {
    if (window.__sdnWalletUI) return window.__sdnWalletUI;
    if (window.__sdnWalletInitPromise) return window.__sdnWalletInitPromise;
    window.__sdnWalletInitPromise = (async function() {
      await window.__sdnEnsureWalletUIStyles();
      var walletModule = await import('` + moduleURL + `');
      if (!walletModule || typeof walletModule.createWalletUI !== 'function') {
        throw new Error('Wallet UI module is unavailable.');
      }
      var ui = await walletModule.createWalletUI(document.body, {
        onLogin: window.__sdnOnLogin,
        openAccountAfterLogin: false
      });
      window.__sdnWalletReady(ui);
      return ui;
    })().catch(function(err) {
      window.__sdnWalletInitPromise = null;
      throw err;
    });
    return window.__sdnWalletInitPromise;
  };`
	}

	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Space Data Network — Login</title>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@400;500;700&family=IBM+Plex+Mono:wght@400;500&display=swap" rel="stylesheet">
  <style>
    *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
    :root{
      --bg:#04131f;
      --panel:rgba(11,58,83,0.38);
      --panel-border:rgba(101,194,203,0.42);
      --panel-border-strong:rgba(55,128,133,0.55);
      --text:#f4fbfd;
      --muted:rgba(221,239,242,0.78);
      --cyan:#65c2cb;
      --teal:#378085;
      --danger:#ff7a9f;
      --font-sans:'Space Grotesk','Avenir Next','Helvetica Neue',sans-serif;
      --font-mono:'IBM Plex Mono','SFMono-Regular','Consolas',monospace;
    }
    html,body{height:100%}
    body{
      min-height:100vh;
      overflow-x:hidden;
      overflow-y:auto;
      padding:24px;
      color:var(--text);
      font-family:var(--font-sans);
      background:
        radial-gradient(circle at 50% 16%, rgba(101,194,203,0.24), transparent 24%),
        radial-gradient(circle at 50% 84%, rgba(55,128,133,0.18), transparent 28%),
        linear-gradient(180deg, #0a1a28 0%, #0b2233 48%, #04131f 100%);
      -webkit-font-smoothing:antialiased;
      -moz-osx-font-smoothing:grayscale;
    }
    body::before{
      content:"";
      position:fixed;
      inset:0;
      pointer-events:none;
      opacity:0.18;
      background-image:
        linear-gradient(rgba(101,194,203,0.18) 1px, transparent 1px),
        linear-gradient(90deg, rgba(101,194,203,0.18) 1px, transparent 1px);
      background-size:42px 42px;
      mask-image:radial-gradient(circle at center, black 26%, transparent 78%);
      -webkit-mask-image:radial-gradient(circle at center, black 26%, transparent 78%);
    }
    body::after{
      content:"";
      position:fixed;
      inset:18px;
      pointer-events:none;
      border:1px solid rgba(101,194,203,0.1);
      border-radius:32px;
      box-shadow:0 0 0 1px rgba(101,194,203,0.03) inset;
    }
    .sdn-stage{
      min-height:calc(100vh - 48px);
      width:100%;
      display:flex;
      align-items:center;
      justify-content:center;
    }
    .sdn-shell{
      position:relative;
      width:min(440px,100%);
      padding:56px 36px 32px;
      border-radius:28px;
      background:linear-gradient(180deg, rgba(12,33,49,0.96), rgba(7,22,34,0.92));
      border:1px solid var(--panel-border);
      box-shadow:
        0 24px 80px rgba(0,0,0,0.55),
        inset 0 1px 0 rgba(255,255,255,0.04),
        0 0 44px rgba(101,194,203,0.1);
      backdrop-filter:blur(18px);
      -webkit-backdrop-filter:blur(18px);
      text-align:center;
      isolation:isolate;
    }
    .sdn-shell::before{
      content:"";
      position:absolute;
      inset:-1px;
      border-radius:28px;
      padding:1px;
      background:linear-gradient(135deg, rgba(101,194,203,0.75), rgba(55,128,133,0.42), rgba(101,194,203,0.12));
      -webkit-mask:
        linear-gradient(#000 0 0) content-box,
        linear-gradient(#000 0 0);
      -webkit-mask-composite:xor;
      mask-composite:exclude;
      opacity:0.72;
      pointer-events:none;
    }
    .sdn-title{
      font-family:var(--font-mono);
      font-size:clamp(1.7rem,4vw,2.4rem);
      line-height:1.1;
      letter-spacing:0.24em;
      text-transform:uppercase;
      color:var(--text);
      text-wrap:balance;
      text-shadow:0 0 24px rgba(101,194,203,0.16);
    }
    .sdn-summary{
      margin:18px auto 0;
      max-width:320px;
      color:var(--muted);
      font-size:0.98rem;
      line-height:1.65;
    }
    .sdn-summary-link{
      color:#9be6ec;
      text-decoration:none;
      border-bottom:1px solid rgba(155,230,236,0.34);
      transition:border-color .18s ease, color .18s ease;
    }
    .sdn-summary-link:hover{
      color:#dffbfe;
      border-bottom-color:rgba(223,251,254,0.72);
    }
    .sdn-techs{
      margin-top:20px;
      padding-top:18px;
      border-top:1px solid rgba(101,194,203,0.14);
    }
    .sdn-tech-label{
      color:var(--muted);
      font-family:var(--font-mono);
      font-size:0.72rem;
      letter-spacing:0.14em;
      text-transform:uppercase;
    }
    .sdn-tech-list{
      list-style:none;
      margin-top:12px;
      display:flex;
      flex-wrap:wrap;
      justify-content:center;
      gap:10px;
    }
    .sdn-tech-link{
      display:inline-flex;
      align-items:center;
      justify-content:center;
      min-height:34px;
      padding:0 14px;
      border-radius:999px;
      border:1px solid rgba(101,194,203,0.18);
      background:rgba(101,194,203,0.08);
      color:var(--text);
      font-size:0.82rem;
      text-decoration:none;
      transition:transform .18s ease, border-color .18s ease, background .18s ease, color .18s ease;
    }
    .sdn-tech-link:hover{
      transform:translateY(-1px);
      border-color:rgba(101,194,203,0.34);
      background:rgba(101,194,203,0.14);
      color:#dffbfe;
    }
    .sdn-tls{
      margin-top:18px;
      display:flex;
      justify-content:center;
    }
    .sdn-tls-trigger{
      color:#9be6ec;
      text-decoration:none;
      font-size:0.84rem;
      letter-spacing:0.05em;
      border-bottom:1px solid rgba(155,230,236,0.34);
      transition:border-color .18s ease, color .18s ease;
    }
    .sdn-tls-trigger:hover{
      color:#dffbfe;
      border-bottom-color:rgba(223,251,254,0.72);
    }
    .sdn-modal[hidden]{
      display:none;
    }
    .sdn-modal{
      position:fixed;
      inset:0;
      z-index:40;
      display:flex;
      align-items:center;
      justify-content:center;
      padding:20px;
      background:rgba(1,10,18,0.72);
      backdrop-filter:blur(14px);
      -webkit-backdrop-filter:blur(14px);
    }
    .sdn-modal-card{
      width:min(560px,100%);
      max-height:min(80vh,720px);
      overflow:auto;
      padding:24px 22px 20px;
      border-radius:22px;
      border:1px solid rgba(101,194,203,0.18);
      background:linear-gradient(180deg, rgba(12,33,49,0.98), rgba(7,22,34,0.96));
      box-shadow:0 24px 80px rgba(0,0,0,0.48);
      text-align:left;
    }
    .sdn-modal-head{
      display:flex;
      align-items:flex-start;
      justify-content:space-between;
      gap:16px;
    }
    .sdn-modal-title{
      font-size:1.02rem;
      color:var(--text);
    }
    .sdn-modal-copy{
      margin-top:6px;
      color:var(--muted);
      font-size:0.84rem;
      line-height:1.55;
    }
    .sdn-modal-close{
      border:0;
      background:transparent;
      color:var(--muted);
      cursor:pointer;
      font-family:var(--font-mono);
      font-size:0.78rem;
      letter-spacing:0.1em;
      text-transform:uppercase;
    }
    .sdn-modal-close:hover{
      color:var(--text);
    }
    .sdn-tls-panel{
      margin-top:18px;
      padding:18px;
      border-radius:18px;
      border:1px solid rgba(101,194,203,0.16);
      background:rgba(3,19,31,0.42);
      text-align:left;
    }
    .sdn-tls-label{
      color:var(--muted);
      font-family:var(--font-mono);
      font-size:0.7rem;
      letter-spacing:0.14em;
      text-transform:uppercase;
    }
    .sdn-tls-title{
      margin-top:8px;
      font-size:0.98rem;
      color:var(--text);
    }
    .sdn-tls-grid{
      margin-top:12px;
      display:grid;
      grid-template-columns:minmax(0,110px) minmax(0,1fr);
      gap:8px 12px;
      font-size:0.8rem;
      color:var(--muted);
    }
    .sdn-tls-grid dt{
      font-family:var(--font-mono);
      text-transform:uppercase;
      letter-spacing:0.08em;
    }
    .sdn-tls-grid dd{
      min-width:0;
      overflow-wrap:anywhere;
    }
    .sdn-tls-grid code{
      color:#dffbfe;
      font-family:var(--font-mono);
      font-size:0.76rem;
    }
    .sdn-tls-download{
      margin-top:14px;
      display:inline-flex;
      color:#9be6ec;
      text-decoration:none;
      border-bottom:1px solid rgba(155,230,236,0.34);
    }
    .sdn-tls-download:hover{
      color:#dffbfe;
      border-bottom-color:rgba(223,251,254,0.72);
    }
    .sdn-metric{
      margin-top:18px;
      padding-top:18px;
      border-top:1px solid rgba(101,194,203,0.14);
      font-family:var(--font-mono);
      display:flex;
      align-items:center;
      justify-content:center;
      gap:10px;
      color:var(--muted);
      font-size:0.78rem;
      letter-spacing:0.1em;
      text-transform:uppercase;
    }
    .sdn-metric-value{
      color:var(--text);
      font-size:0.92rem;
      letter-spacing:0.14em;
    }
    .sdn-login{
      width:100%;
      margin-top:26px;
      border:0;
      border-radius:999px;
      padding:15px 22px;
      cursor:pointer;
      font-family:var(--font-mono);
      font-size:0.95rem;
      font-weight:500;
      letter-spacing:0.22em;
      text-transform:uppercase;
      color:#041018;
      background:linear-gradient(135deg, var(--cyan), var(--teal));
      box-shadow:0 14px 30px rgba(109,220,255,0.18);
      transition:transform .18s ease, box-shadow .18s ease, opacity .18s ease;
    }
    .sdn-login:hover:not(:disabled){
      transform:translateY(-1px);
      box-shadow:0 18px 34px rgba(109,220,255,0.24);
    }
    .sdn-login:disabled{
      opacity:0.62;
      cursor:default;
      transform:none;
      box-shadow:none;
    }
    .sdn-home{
      margin-top:18px;
      display:inline-flex;
      align-items:center;
      justify-content:center;
      color:#9be6ec;
      text-decoration:none;
      font-size:0.84rem;
      letter-spacing:0.08em;
      text-transform:uppercase;
      border-bottom:1px solid rgba(155,230,236,0.34);
      transition:border-color .18s ease, color .18s ease;
    }
    .sdn-home:hover{
      color:#dffbfe;
      border-bottom-color:rgba(223,251,254,0.72);
    }
    .sdn-auth-status{
      min-height:18px;
      margin-top:16px;
      font-family:var(--font-mono);
      font-size:11px;
      letter-spacing:0.12em;
      text-transform:uppercase;
      color:var(--muted);
      opacity:0;
      transform:translateY(6px);
      transition:opacity .2s ease, transform .2s ease;
    }
    .sdn-auth-status[data-visible="true"]{
      opacity:1;
      transform:none;
    }
    .sdn-auth-status.success{color:var(--teal)}
    .sdn-auth-status.error{color:var(--danger)}
    @media (max-width: 640px){
      body{padding:16px}
      .sdn-stage{min-height:calc(100vh - 32px)}
      .sdn-shell{padding:46px 24px 24px}
      .sdn-title{letter-spacing:0.16em}
      .sdn-summary{font-size:0.92rem}
      .sdn-metric{flex-direction:column;gap:6px}
    }
  </style>

  <script>
  window.__sdnAutoOpen = false;
  window.__sdnOpenAccountAfterLogin = false;
  window.__sdnWalletUI = null;
  window.__sdnWalletInitPromise = null;
  window.__sdnWalletWaiters = [];
  window.__sdnWalletQuery = new URLSearchParams(window.location.search);
  window.__sdnResolveNextPath = function() {
    var raw = window.__sdnWalletQuery.get('next') || '';
    if (!raw || raw.charAt(0) !== '/' || raw.indexOf('//') === 0) return '';
    if (raw === '/admin') return '/admin/';
    if (raw === '/webui') return '/webui/';
    if (raw === '/' || raw.indexOf('/admin/') === 0 || raw.indexOf('/webui/') === 0) return raw;
    return '';
  };
  window.__sdnSetStatus = function(message, cls) {
    var statusEl = document.getElementById('sdn-auth-status');
    if (!statusEl) return;
    if (!message) {
      statusEl.textContent = '';
      statusEl.className = 'sdn-auth-status';
      statusEl.setAttribute('data-visible', 'false');
      return;
    }
    statusEl.className = 'sdn-auth-status' + (cls ? ' ' + cls : '');
    statusEl.textContent = message;
    statusEl.setAttribute('data-visible', 'true');
  };
  window.__sdnShouldDisablePasskeys = function() {
    var host = window.location.hostname || '';
    if (!host) return true;
    if (host === 'localhost' || host.slice(-10) === '.localhost') return false;
    if (/^\d{1,3}(?:\.\d{1,3}){3}$/.test(host)) return true;
    if (host.indexOf(':') !== -1) return true;
    return false;
  };
  window.__sdnNormalizeWalletLoginUI = function() {
    if (!window.__sdnShouldDisablePasskeys()) return;
    var storedTab = document.getElementById('stored-tab');
    var storedMethod = document.getElementById('stored-method');
    var seedTab = document.querySelector('.method-tab[data-method="seed"]');
    var seedMethod = document.getElementById('seed-method');
    ['password', 'seed'].forEach(function(target) {
      var pinBtn = document.querySelector('.remember-method-btn[data-target="' + target + '"][data-method="pin"]');
      var passkeyBtn = document.querySelector('.remember-method-btn[data-target="' + target + '"][data-method="passkey"]');
      var passkeyInfo = document.getElementById('passkey-info-' + target);
      if (pinBtn && !pinBtn.classList.contains('active')) {
        pinBtn.click();
      }
      if (passkeyBtn) {
        passkeyBtn.style.display = 'none';
      }
      if (passkeyInfo) {
        passkeyInfo.style.display = 'none';
      }
    });
    var storedPasskeySection = document.getElementById('stored-passkey-section');
    if (storedPasskeySection) {
      storedPasskeySection.style.display = 'none';
    }
    var unlockPasskeyButton = document.getElementById('unlock-with-passkey');
    if (unlockPasskeyButton) {
      unlockPasskeyButton.style.display = 'none';
    }
    if (storedTab) {
      var storedTabWasActive = storedTab.classList.contains('active');
      storedTab.style.display = 'none';
      storedTab.classList.remove('active');
      if (storedTabWasActive && storedMethod) {
        storedMethod.classList.remove('active');
        if (seedTab) seedTab.classList.add('active');
        if (seedMethod) seedMethod.classList.add('active');
      }
    }
  };
  window.__sdnOpenWalletAccount = function() {
    if (window.__sdnWalletUI && typeof window.__sdnWalletUI.openAccount === 'function') {
      window.__sdnWalletUI.openAccount();
    }
  };
  window.__sdnInitializeTLSInfoModal = function() {
    var trigger = document.getElementById('sdn-tls-trigger');
    var modal = document.getElementById('sdn-tls-modal');
    if (!trigger || !modal) return;
    var closeButton = document.getElementById('sdn-tls-close');
    var setOpen = function(open) {
      modal.hidden = !open;
      modal.setAttribute('aria-hidden', open ? 'false' : 'true');
    };
    trigger.addEventListener('click', function(event) {
      event.preventDefault();
      setOpen(true);
    });
    if (closeButton) {
      closeButton.addEventListener('click', function() {
        setOpen(false);
      });
    }
    modal.addEventListener('click', function(event) {
      if (event.target === modal) {
        setOpen(false);
      }
    });
    document.addEventListener('keydown', function(event) {
      if (event.key === 'Escape' && !modal.hidden) {
        setOpen(false);
      }
    });
  };
  window.__sdnRefreshNodeCount = async function() {
    var countEl = document.getElementById('sdn-node-count');
    if (!countEl) return;
    try {
      var response = await fetch('/api/relay/status', {
        headers: { 'Accept': 'application/json' }
      });
      if (!response.ok) return;
      var payload = await response.json();
      if (typeof payload.connections === 'number') {
        countEl.textContent = String(payload.connections);
      }
    } catch (err) {}
  };
  window.__sdnWalletThemeCSS = [
    '#hd-wallet-ui-container{',
    '  --black:#04131f;',
    '  --white:#f4fbfd;',
    '  --white-90:rgba(244,251,253,0.9);',
    '  --white-80:rgba(244,251,253,0.8);',
    '  --white-70:rgba(221,239,242,0.78);',
    '  --white-60:rgba(221,239,242,0.62);',
    '  --white-50:rgba(221,239,242,0.5);',
    '  --white-40:rgba(221,239,242,0.4);',
    '  --white-30:rgba(221,239,242,0.3);',
    '  --white-25:rgba(221,239,242,0.25);',
    '  --white-20:rgba(221,239,242,0.2);',
    '  --white-15:rgba(221,239,242,0.15);',
    '  --white-10:rgba(221,239,242,0.1);',
    '  --white-08:rgba(221,239,242,0.08);',
    '  --white-05:rgba(221,239,242,0.05);',
    '  --white-03:rgba(221,239,242,0.03);',
    '  --muted:rgba(173,212,221,0.82);',
    '  --glass-bg:rgba(11,58,83,0.42);',
    '  --glass-hover:rgba(16,75,106,0.52);',
    '  --glass-border:rgba(101,194,203,0.22);',
    '  --glass-blur:blur(20px);',
    '  --weapon:#65c2cb;',
    '  --galaxy:#2a6f97;',
    '  --success:#71d8ce;',
    '  --monster:#ff7a9f;',
    '  --font-sans:"Space Grotesk","Avenir Next","Helvetica Neue",sans-serif;',
    '  --font-mono:"IBM Plex Mono","SFMono-Regular","Consolas",monospace;',
    '}',
    '#hd-wallet-ui-container .blurred-background{',
    '  background:',
    '    radial-gradient(ellipse at 20% 50%, rgba(101,194,203,0.22) 0%, transparent 50%),',
    '    radial-gradient(ellipse at 80% 20%, rgba(42,111,151,0.18) 0%, transparent 50%),',
    '    radial-gradient(ellipse at 50% 80%, rgba(113,216,206,0.12) 0%, transparent 50%),',
    '    linear-gradient(to bottom, rgba(4,19,31,0.18) 0%, rgba(4,19,31,0.56) 50%, rgba(4,19,31,0.92) 100%);',
    '}'
  ].join('\n');
  window.__sdnEnsureWalletUIStyles = function() {
    if (!document.getElementById('sdn-wallet-theme')) {
      var theme = document.createElement('style');
      theme.id = 'sdn-wallet-theme';
      theme.textContent = window.__sdnWalletThemeCSS;
      document.head.appendChild(theme);
    }
    if (document.getElementById('sdn-wallet-ui-css')) {
      return Promise.resolve();
    }
    return new Promise(function(resolve, reject) {
      var link = document.createElement('link');
      link.id = 'sdn-wallet-ui-css';
      link.rel = 'stylesheet';
      link.href = '` + cssURL + `';
      link.onload = function() { resolve(); };
      link.onerror = function() { reject(new Error('Failed to load wallet styles.')); };
      document.head.appendChild(link);
    });
  };
  window.__sdnAwaitWalletReady = function() {
    if (window.__sdnWalletUI) return Promise.resolve(window.__sdnWalletUI);
    return new Promise(function(resolve, reject) {
      var done = false;
      var timeout = setTimeout(function() {
        if (done) return;
        done = true;
        reject(new Error('Wallet UI did not finish loading.'));
      }, 20000);
      window.__sdnWalletWaiters.push(function(ui) {
        if (done) return;
        done = true;
        clearTimeout(timeout);
        resolve(ui);
      });
    });
  };
` + walletLoader + `
  window.__sdnOpenLogin = async function() {
    var button = document.getElementById('sdn-sign-in');
    try {
      if (button) {
        button.disabled = true;
        button.textContent = 'Loading';
      }
      window.__sdnSetStatus('Loading wallet...');
      var ui = await window.__sdnEnsureWalletUI();
      if (button) {
        button.disabled = false;
        button.textContent = 'Login';
      }
      window.__sdnNormalizeWalletLoginUI();
      window.__sdnSetStatus('', '');
      ui.openLogin();
    } catch (err) {
      if (button) {
        button.disabled = false;
        button.textContent = 'Login';
      }
      window.__sdnSetStatus((err && err.message) || 'Failed to load wallet interface.', 'error');
    }
  };
  window.__sdnInitializeLoginButton = function() {
    var button = document.getElementById('sdn-sign-in');
    if (!button) return;
    button.disabled = false;
    button.textContent = 'Login';
    button.addEventListener('click', window.__sdnOpenLogin);
  };

  window.__sdnOnLogin = async function(identity) {
    var button = document.getElementById('sdn-sign-in');
    var nextPath = window.__sdnResolveNextPath();

    function resetButton() {
      if (!button) return;
      button.disabled = false;
      button.textContent = 'Login';
    }

    try {
      var pubKeyHex = Array.from(identity.signingPublicKey)
        .map(function(b){return b.toString(16).padStart(2,'0')}).join('');

      window.__sdnSetStatus('Requesting challenge...');
      if (button) {
        button.disabled = true;
        button.textContent = 'Authorizing';
      }

      var challengeResp = await fetch('/api/auth/challenge', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ client_pubkey_hex: pubKeyHex, ts: Math.floor(Date.now()/1000) })
      });
      var challengeData = await challengeResp.json();
      if (!challengeResp.ok) throw new Error(challengeData.message || 'Challenge failed');

      window.__sdnSetStatus('Signing challenge...');

      var b64 = challengeData.challenge;
      while (b64.length % 4 !== 0) b64 += '=';
      var binary = atob(b64);
      var challengeBytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) challengeBytes[i] = binary.charCodeAt(i);

      var signature = await identity.sign(challengeBytes);
      var sigHex = Array.from(signature)
        .map(function(b){return b.toString(16).padStart(2,'0')}).join('');

      window.__sdnSetStatus('Verifying session...');

      var verifyResp = await fetch('/api/auth/verify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({
          challenge_id: challengeData.challenge_id,
          client_pubkey_hex: pubKeyHex,
          challenge: challengeData.challenge, signature_hex: sigHex
        })
      });
      var verifyData = await verifyResp.json();

      if (!verifyResp.ok) {
        resetButton();
        window.__sdnSetStatus('Wallet unlocked. Access not granted.', 'success');
        window.__sdnOpenWalletAccount();
        return;
      }

      var trustName = (verifyData.user.trust_level || 'unknown').toLowerCase();

      if ((nextPath.indexOf('/admin/') === 0 && trustName === 'admin') ||
          (nextPath.indexOf('/webui/') === 0 && ['standard', 'trusted', 'admin'].indexOf(trustName) !== -1) ||
          nextPath === '/') {
        window.__sdnSetStatus('Redirecting...', 'success');
        setTimeout(function(){ window.location.href = nextPath; }, 600);
        return;
      }
      if (trustName === 'admin') {
        window.__sdnSetStatus('Redirecting...', 'success');
        setTimeout(function(){ window.location.href = '/admin/'; }, 600);
      } else {
        resetButton();
        window.__sdnSetStatus('Wallet unlocked.', 'success');
        window.__sdnOpenWalletAccount();
      }

    } catch (err) {
      resetButton();
      window.__sdnSetStatus((err && err.message) || 'Authentication failed.', 'error');
    }
  };

  window.__sdnWalletReady = function(ui) {
    window.__sdnWalletUI = ui;
    window.__sdnNormalizeWalletLoginUI();
    if (Array.isArray(window.__sdnWalletWaiters) && window.__sdnWalletWaiters.length > 0) {
      var waiters = window.__sdnWalletWaiters.slice();
      window.__sdnWalletWaiters = [];
      waiters.forEach(function(resolve) { resolve(ui); });
    }
    window.__sdnSetStatus('', '');
  };
  </script>
</head>
<body>
  <div class="sdn-stage">
    <main class="sdn-shell" aria-label="Space Data Network login">
      <h1 class="sdn-title">SPACE DATA NETWORK</h1>
      <p class="sdn-summary">` + loginPageSummaryHTML + `</p>
      <div class="sdn-metric">
        <span>Nodes detected</span>
        <span id="sdn-node-count" class="sdn-metric-value">--</span>
      </div>
      <div class="sdn-techs" aria-label="Built on">
        <div class="sdn-tech-label">Built On</div>
        <ul class="sdn-tech-list">
          <li><a class="sdn-tech-link" href="https://digitalarsenal.github.io/flatbuffers/" target="_blank" rel="noreferrer">FlatBuffers</a></li>
          <li><a class="sdn-tech-link" href="https://digitalarsenal.github.io/flatsql/" target="_blank" rel="noreferrer">FlatSQL</a></li>
          <li><a class="sdn-tech-link" href="https://spacedatastandards.org/" target="_blank" rel="noreferrer">Space Data Standards</a></li>
        </ul>
      </div>
      ` + buildTLSStatusMarkup(tlsStatus, showLocalTLSInfo) + `
      <button id="sdn-sign-in" class="sdn-login" type="button" disabled>Login</button>
      <a class="sdn-home" href="https://spacedatanet.org/" target="_blank" rel="noreferrer">Homepage</a>
      <div id="sdn-auth-status" class="sdn-auth-status" data-visible="false" aria-live="polite"></div>
    </main>
  </div>

  <script>
    window.__sdnInitializeLoginButton();
    window.__sdnInitializeTLSInfoModal();
    window.__sdnRefreshNodeCount();
  </script>

</body>
</html>`
}

// ---------------------------------------------------------------------------
// Fallback login page (no local wallet-ui dist)
// ---------------------------------------------------------------------------

func serveFallbackLogin(w http.ResponseWriter, r *http.Request, status tlsmgr.Status) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(buildFallbackLoginPage(loginPageHost(r), status)))
}

const (
	fallbackWalletUIModuleURL = "https://unpkg.com/hd-wallet-ui@2.0.6/src/app.js?module"
	fallbackWalletUICSSURL    = "https://unpkg.com/hd-wallet-ui@2.0.6/styles/widget.css"
)

func buildFallbackLoginPage(host string, status tlsmgr.Status) string {
	return buildWalletLoginPage(fallbackWalletUIModuleURL, fallbackWalletUICSSURL, false, host, status)
}

func buildTLSStatusMarkup(status tlsmgr.Status, showLocalTLSInfo bool) string {
	if !showLocalTLSInfo || strings.TrimSpace(status.Mode) == "" {
		return ""
	}

	var download string
	if strings.TrimSpace(status.BootstrapCertURL) != "" {
		download = `<a class="sdn-tls-download" href="` + html.EscapeString(status.BootstrapCertURL) + `">Download bootstrap certificate</a>`
	}

	return `<div class="sdn-tls"><a id="sdn-tls-trigger" class="sdn-tls-trigger" href="#sdn-tls-modal" aria-haspopup="dialog" aria-controls="sdn-tls-modal">TLS identity</a></div>` +
		`<div id="sdn-tls-modal" class="sdn-modal" aria-hidden="true" hidden>` +
		`<div class="sdn-modal-card" role="dialog" aria-modal="true" aria-labelledby="sdn-tls-modal-title">` +
		`<div class="sdn-modal-head">` +
		`<div>` +
		`<div id="sdn-tls-modal-title" class="sdn-modal-title">Local TLS certificate</div>` +
		`<p class="sdn-modal-copy">This loopback node is using a local certificate so you can verify and optionally trust it during development.</p>` +
		`</div>` +
		`<button id="sdn-tls-close" class="sdn-modal-close" type="button">Close</button>` +
		`</div>` +
		`<section class="sdn-tls-panel" aria-label="TLS identity">` +
		`<div class="sdn-tls-label">TLS identity</div>` +
		`<div class="sdn-tls-title">` + html.EscapeString(loginPageCertificateLabel(status)) + `</div>` +
		`<dl class="sdn-tls-grid">` +
		`<dt>Mode</dt><dd>` + html.EscapeString(status.Mode) + `</dd>` +
		`<dt>Fingerprint</dt><dd><code>` + html.EscapeString(status.FingerprintSHA256) + `</code></dd>` +
		`<dt>Peer ID</dt><dd><code>` + html.EscapeString(status.PeerID) + `</code></dd>` +
		`<dt>Encryption</dt><dd><code>` + html.EscapeString(status.EncryptionPublicKey) + `</code></dd>` +
		`<dt>Proof</dt><dd>` + html.EscapeString(status.ProofStatus) + `</dd>` +
		`</dl>` +
		download +
		`</section>` +
		`</div>` +
		`</div>`
}

func loginPageHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Host)
}

func shouldRenderLocalTLSInfo(host string) bool {
	hostname := strings.TrimSpace(host)
	if hostname == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = parsedHost
	}
	hostname = strings.Trim(hostname, "[]")
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func loginPageCertificateLabel(status tlsmgr.Status) string {
	switch status.ActiveCertificateType {
	case "bootstrap":
		return "Bootstrap self-signed certificate"
	case "static":
		return "Static operator certificate"
	case "managed":
		return "Managed public certificate"
	default:
		return "TLS certificate"
	}
}
