package auth

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// handleLoginPage serves a branded SDN login page that loads the wallet-ui
// as a module.  A header bar shows the SDN logo and a "Sign In" button;
// below it the page displays live node information fetched from /api/node/info.
//
// Clicking "Sign In" opens the wallet-ui login modal.  After the user
// authenticates with their HD wallet the injected window.__sdnOnLogin
// callback performs an Ed25519 challenge-response against /api/auth/*
// and redirects to /admin on success.
//
// If no local wallet-ui dist is available the handler returns a minimal
// fallback page.
func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Already authenticated → redirect to admin.
	if session, err := h.sessionFromRequest(r); err == nil && session != nil {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}

	walletUI := strings.TrimSpace(h.walletUIPath)
	if walletUI == "" {
		serveFallbackLogin(w)
		return
	}

	html := cachedLoginPage(walletUI)
	if html == "" {
		serveFallbackLogin(w)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(html))
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

// DiscoverWalletAssets scans the wallet-ui dist for asset filenames and caches them.
// Call this at startup to make WalletAssets() available immediately.
func DiscoverWalletAssets(walletUIPath string) {
	if walletUIPath == "" {
		return
	}
	cachedLoginPage(walletUIPath)
}

// WalletAssets returns the discovered wallet-ui JS and CSS filenames.
func WalletAssets() (jsFile, cssFile string) {
	return walletJSFile, walletCSSFile
}

// cachedLoginPage reads the wallet-ui dist/index.html once to discover asset
// filenames, then builds and caches a custom branded login page.
func cachedLoginPage(walletUIPath string) string {
	loginPageOnce.Do(func() {
		indexPath := filepath.Join(walletUIPath, "index.html")
		raw, err := os.ReadFile(indexPath)
		if err != nil {
			return
		}
		src := string(raw)

		// Extract hashed asset filenames from the dist HTML.
		jsMatch := reScriptSrc.FindStringSubmatch(src)
		cssMatch := reCSSHref.FindStringSubmatch(src)

		jsFile := ""
		cssFile := ""
		if len(jsMatch) > 1 {
			jsFile = jsMatch[1]
		}
		if len(cssMatch) > 1 {
			cssFile = cssMatch[1]
		}
		if jsFile == "" {
			return
		}

		walletJSFile = jsFile
		walletCSSFile = cssFile
		loginPageCache = buildLoginPage(jsFile, cssFile)
	})
	return loginPageCache
}

// buildLoginPage returns the full HTML for the SDN login page.
func buildLoginPage(jsFile, cssFile string) string {
	cssLink := ""
	if cssFile != "" {
		cssLink = `<link rel="stylesheet" crossorigin href="/wallet-ui/assets/` + cssFile + `">`
	}

	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Space Data Network — Login</title>
  ` + cssLink + `
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
  <style>
    *,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
    :root{
      --bg:#000;
      --text-primary:#F5F5F7;
      --text-secondary:rgba(255,255,255,0.8);
      --text-muted:rgba(134,134,139,1.0);
      --ui-bg:rgba(42,42,45,0.72);
      --ui-border:rgba(134,134,139,0.3);
      --ui-border-hover:rgba(134,134,139,0.5);
      --nav-bg:rgba(22,22,23,0.95);
      --font-sans:'Inter',-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;
      --font-mono:'JetBrains Mono','SF Mono','Fira Code',monospace;
      --radius:16px;
    }
    *,*::before,*::after{box-sizing:border-box}
    html,body{height:100%;margin:0;padding:0}
    body{
      font-family:var(--font-sans);
      background:var(--bg);color:var(--text-primary);
      display:flex;flex-direction:column;
      -webkit-font-smoothing:antialiased;
      -moz-osx-font-smoothing:grayscale;
    }

    /* ---- Header ---- */
    .sdn-header{
      position:sticky;top:0;z-index:900;
      display:flex;align-items:center;justify-content:space-between;
      padding:20px 48px;
      background:linear-gradient(180deg, rgba(22,22,23,0.98) 0%, rgba(22,22,23,0.92) 100%);
      backdrop-filter:blur(24px);-webkit-backdrop-filter:blur(24px);
      border-bottom:1px solid rgba(255,255,255,0.08);
      box-shadow:0 1px 12px rgba(0,0,0,0.3);
    }
    .sdn-logo{display:flex;align-items:center;gap:14px;color:var(--text-primary);font-weight:600;font-size:18px;letter-spacing:.06em;white-space:nowrap}
    .sdn-logo svg{width:36px;height:36px;flex-shrink:0;opacity:0.9}
    .sdn-sign-in{
      padding:10px 28px;border:none;border-radius:980px;cursor:pointer;
      font-family:var(--font-sans);font-size:15px;font-weight:600;
      background:var(--text-primary);color:var(--bg);
      transition:all .2s;letter-spacing:.02em;
      align-self:center;height:auto;line-height:1;
      flex-shrink:0;
    }
    .sdn-sign-in:hover{opacity:.85;transform:scale(1.02)}
    .sdn-sign-in:disabled{opacity:.3;cursor:default;transform:none}
    .sdn-header-right{display:flex;align-items:center;gap:16px}
    .sdn-trust-badge{
      display:none;align-items:center;gap:8px;
      font-size:13px;color:var(--text-muted);font-family:var(--font-mono);
    }
    .sdn-trust-badge .trust-level{
      padding:4px 12px;border-radius:980px;font-size:12px;font-weight:600;
      letter-spacing:.04em;text-transform:uppercase;
    }
    .sdn-trust-badge .trust-level.admin{background:rgba(52,211,153,.15);color:#6ee7b7;border:1px solid rgba(52,211,153,.3)}
    .sdn-trust-badge .trust-level.trusted{background:rgba(96,165,250,.15);color:#93bbfd;border:1px solid rgba(96,165,250,.3)}
    .sdn-trust-badge .trust-level.standard{background:rgba(251,191,36,.15);color:#fcd34d;border:1px solid rgba(251,191,36,.3)}
    .sdn-trust-badge .trust-level.limited{background:rgba(248,113,113,.15);color:#fca5a5;border:1px solid rgba(248,113,113,.3)}
    .sdn-trust-badge .trust-level.untrusted{background:rgba(134,134,139,.15);color:#a1a1a6;border:1px solid rgba(134,134,139,.3)}
    .sdn-trust-badge .trust-desc{color:var(--text-muted);font-family:var(--font-sans);font-size:12px}

    /* ---- Main ---- */
    .sdn-main{flex:1;display:flex;flex-direction:column;align-items:center;padding:60px 24px 80px}
    .sdn-hero{text-align:center;margin-bottom:48px}
    .sdn-hero h1{font-size:32px;font-weight:300;letter-spacing:.02em;margin-bottom:10px}
    .sdn-hero p{color:var(--text-muted);font-size:15px;line-height:1.7;max-width:520px;margin:0 auto}

    /* ---- Node info cards ---- */
    .sdn-cards{
      display:flex;flex-wrap:wrap;gap:16px;justify-content:center;
      width:100%;max-width:880px;
    }
    .sdn-card{
      background:var(--ui-bg);border:1px solid var(--ui-border);
      border-radius:var(--radius);padding:24px;width:100%;max-width:420px;
      backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);
    }
    .sdn-card h3{font-size:11px;text-transform:uppercase;letter-spacing:.1em;color:var(--text-muted);margin-bottom:14px;font-weight:600}
    .sdn-card .val{
      font-family:var(--font-mono);
      font-size:13px;word-break:break-all;line-height:1.7;color:var(--text-primary);
    }
    .sdn-card .val.accent{color:rgba(255,255,255,0.95)}
    .sdn-card .label{font-size:12px;color:var(--text-muted);margin-top:12px;margin-bottom:2px;font-weight:500}
    .sdn-card .chip{
      display:inline-block;padding:4px 10px;border-radius:8px;font-size:12px;font-weight:500;
      background:rgba(255,255,255,0.08);color:var(--text-secondary);margin:2px 4px 2px 0;
      border:1px solid rgba(255,255,255,0.06);
      font-family:var(--font-mono);
    }
    .sdn-placeholder{text-align:center;padding:48px 0;color:var(--text-muted);font-size:14px}

    /* ---- Setup banner ---- */
    .sdn-setup{
      max-width:640px;width:100%;margin:0 auto 32px;
      background:rgba(234,179,8,0.08);border:1px solid rgba(234,179,8,0.25);
      border-radius:var(--radius);padding:24px 28px;
    }
    .sdn-setup h2{font-size:15px;font-weight:600;color:#fbbf24;margin-bottom:8px;display:flex;align-items:center;gap:8px}
    .sdn-setup p{font-size:13px;color:var(--text-secondary);line-height:1.7;margin-bottom:16px}
    .sdn-setup code{
      display:block;background:rgba(255,255,255,0.06);border:1px solid rgba(255,255,255,0.08);
      border-radius:8px;padding:12px 16px;margin:12px 0 16px;font-family:var(--font-mono);
      font-size:12px;line-height:1.8;color:var(--text-secondary);white-space:pre;overflow-x:auto;
    }
    .sdn-setup .step{color:var(--text-muted);font-size:12px;margin-top:16px}

    /* ---- Auth toast ---- */
    .sdn-auth-status{
      display:none;position:fixed;bottom:24px;left:50%;transform:translateX(-50%);
      padding:12px 28px;background:rgba(30,30,35,.95);color:#F5F5F7;
      border:1px solid var(--ui-border);border-radius:14px;
      font-size:14px;font-weight:500;z-index:100000;
      backdrop-filter:blur(12px);text-align:center;max-width:90vw;
      box-shadow:0 8px 32px rgba(0,0,0,.6);
      font-family:var(--font-sans);
    }
    .sdn-auth-status.success{border-color:rgba(52,211,153,.5);color:#6ee7b7}
    .sdn-auth-status.error{border-color:rgba(248,113,113,.5);color:#fca5a5}
  </style>

  <script>
  // --- SDN Auth Hook ---
  // Runs BEFORE the deferred wallet-ui module script.
  window.__sdnAutoOpen = false;
  window.__sdnOpenAccountAfterLogin = false;

  window.__sdnOnLogin = async function(identity) {
    var statusEl = document.getElementById('sdn-auth-status');
    var show = function(msg, cls) {
      if (!statusEl) return;
      statusEl.className = 'sdn-auth-status ' + (cls || '');
      statusEl.textContent = msg;
      statusEl.style.display = 'block';
    };
    var hide = function() { if (statusEl) statusEl.style.display = 'none'; };

    var trustDescriptions = {
      admin: 'full access',
      trusted: 'elevated privileges',
      standard: 'basic access',
      limited: 'read-only',
      untrusted: 'no access'
    };

    function showTrustBadge(trustName, desc) {
      var badge = document.getElementById('sdn-trust-badge');
      if (!badge) return;
      badge.innerHTML = '<span class="trust-level ' + trustName + '">' + trustName + '</span>' +
        '<span class="trust-desc">(' + desc + ')</span>';
      badge.style.display = 'flex';
    }

    function updateBannerIdentity(xpub) {
      var banner = document.getElementById('sdn-setup-banner');
      if (!banner) return;
      var codes = banner.querySelectorAll('code');
      for (var i = 0; i < codes.length; i++) {
        if (xpub && codes[i].textContent.indexOf('YOUR_XPUB_HERE') !== -1) {
          codes[i].textContent = codes[i].textContent.replace('YOUR_XPUB_HERE', xpub);
        }
      }
    }

    try {
      var pubKeyHex = Array.from(identity.signingPublicKey)
        .map(function(b){return b.toString(16).padStart(2,'0')}).join('');
      var xpub = identity.xpub;

      show('Requesting challenge\u2026');

      var challengeResp = await fetch('/api/auth/challenge', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ xpub: xpub, client_pubkey_hex: pubKeyHex, ts: Math.floor(Date.now()/1000) })
      });
      var challengeData = await challengeResp.json();
      if (!challengeResp.ok) throw new Error(challengeData.message || 'Challenge failed');

      show('Signing challenge\u2026');

      var b64 = challengeData.challenge;
      while (b64.length % 4 !== 0) b64 += '=';
      var binary = atob(b64);
      var challengeBytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) challengeBytes[i] = binary.charCodeAt(i);

      var signature = await identity.sign(challengeBytes);
      var sigHex = Array.from(signature)
        .map(function(b){return b.toString(16).padStart(2,'0')}).join('');

      show('Verifying\u2026');

      var verifyResp = await fetch('/api/auth/verify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          challenge_id: challengeData.challenge_id,
          xpub: xpub, client_pubkey_hex: pubKeyHex,
          challenge: challengeData.challenge, signature_hex: sigHex
        })
      });
      var verifyData = await verifyResp.json();

      if (!verifyResp.ok) {
        // Auth failed — user not in config. Show "unregistered" badge, update banner.
        updateBannerIdentity(xpub);
        showTrustBadge('untrusted', 'not in server config');
        hide();
        var btn = document.getElementById('sdn-sign-in');
        if (btn) { btn.textContent = 'Sign In'; btn.disabled = false; }
        return;
      }

      var trustName = (verifyData.user.trust_level || 'unknown').toLowerCase();
      var trustDesc = trustDescriptions[trustName] || '';

      // Show trust badge in header
      showTrustBadge(trustName, trustDesc);
      hide();

      if (trustName === 'admin') {
        // Admin — redirect to admin panel
        show('Redirecting to admin panel\u2026', 'success');
        setTimeout(function(){ window.location.href = '/admin/'; }, 600);
      } else {
        // Non-admin — stay on page, show their level
        var btn = document.getElementById('sdn-sign-in');
        if (btn) { btn.textContent = verifyData.user.name || 'Signed In'; btn.disabled = true; }
      }

    } catch (err) {
      // Network or unexpected error — show badge, not a toast
      showTrustBadge('untrusted', err.message);
      hide();
      if (typeof xpub !== 'undefined' && xpub) updateBannerIdentity(xpub);
    }
  };

  window.__sdnWalletReady = function(ui) {
    var btn = document.getElementById('sdn-sign-in');
    if (btn) {
      btn.disabled = false;
      btn.addEventListener('click', function(){ ui.openLogin(); });
    }
  };
  </script>
</head>
<body>

  <header class="sdn-header">
    <div class="sdn-logo">
      <svg viewBox="0 0 100 100" fill="none" stroke="currentColor" stroke-width="4">
        <circle cx="50" cy="50" r="45"/>
        <ellipse cx="50" cy="50" rx="45" ry="18" stroke-width="2"/>
        <ellipse cx="50" cy="50" rx="45" ry="18" stroke-width="2" transform="rotate(60 50 50)"/>
        <ellipse cx="50" cy="50" rx="45" ry="18" stroke-width="2" transform="rotate(120 50 50)"/>
        <circle cx="50" cy="50" r="8" fill="currentColor" stroke="none"/>
      </svg>
      <span>SPACE DATA NETWORK</span>
    </div>
    <div class="sdn-header-right">
      <div id="sdn-trust-badge" class="sdn-trust-badge"></div>
      <button id="sdn-sign-in" class="sdn-sign-in" disabled>Sign In</button>
    </div>
  </header>

  <main class="sdn-main">
    <div id="sdn-setup-banner"></div>

    <section class="sdn-hero">
      <h1>Node Dashboard</h1>
      <p>Sign in with your HD Wallet to access the admin panel.<br>
         Authentication uses Ed25519 challenge-response &mdash; your keys never leave your browser.</p>
    </section>

    <section class="sdn-cards" id="sdn-node-info">
      <div class="sdn-placeholder">Loading node information&hellip;</div>
    </section>
  </main>

  <div id="sdn-auth-status" class="sdn-auth-status"></div>

  <script type="module" crossorigin src="/wallet-ui/assets/` + jsFile + `"></script>

  <script>
  (function(){
    // Check if admin is configured — show setup banner if not
    fetch('/api/auth/status').then(function(r){return r.json()}).then(function(s){
      if (s.admin_configured) return;
      var banner = document.getElementById('sdn-setup-banner');
      if (!banner) return;
      var cfgPath = s.config_path || 'config.yaml';
      banner.innerHTML =
        '<div class="sdn-setup">' +
          '<h2>\u26a0 Admin Setup Required</h2>' +
          '<p>No administrator account is configured. Add your SDN <strong>extended public key (xpub)</strong> to:</p>' +
          '<code>' + esc(cfgPath) + '</code>' +
          '<p style="margin-top:12px">Add the following block:</p>' +
          '<code>users:\n  - xpub: "YOUR_XPUB_HERE"\n    trust_level: "admin"\n    name: "Operator"</code>' +
          '<p>Click <strong>Sign In</strong> to open your wallet \u2014 your xpub will be auto-filled above.</p>' +
          '<p class="step">After editing, restart the server:</p>' +
          '<code>sudo systemctl restart spacedatanetwork</code>' +
        '</div>';
    }).catch(function(){});

    fetch('/api/node/info').then(function(r){return r.json()}).then(function(info){
      var el = document.getElementById('sdn-node-info');
      if (!el) return;

      var addrs = (info.listen_addresses||[]).map(function(a){
        return '<span class="chip">' + esc(a) + '</span>';
      }).join(' ');

      var cards = '';

      // Identity card
      cards += '<div class="sdn-card">';
      cards += '<h3>Node Identity</h3>';
      cards += '<div class="val accent">' + esc(info.peer_id) + '</div>';
      cards += '<div class="label">Mode</div><div class="val">' + esc(info.mode) + '</div>';
      cards += '<div class="label">Version</div><div class="val">' + esc(info.version) + '</div>';
      if (addrs) { cards += '<div class="label">Listen Addresses</div><div style="margin-top:4px">' + addrs + '</div>'; }
      cards += '</div>';

      // Crypto card
      if (info.signing_pubkey_hex || info.encryption_pubkey_hex) {
        cards += '<div class="sdn-card">';
        cards += '<h3>Cryptographic Keys</h3>';
        if (info.signing_pubkey_hex) {
          cards += '<div class="label">Signing (Ed25519)</div>';
          cards += '<div class="val">' + esc(info.signing_pubkey_hex) + '</div>';
          if (info.signing_key_path) cards += '<div class="val" style="color:var(--text-muted);font-size:12px">' + esc(info.signing_key_path) + '</div>';
        }
        if (info.encryption_pubkey_hex) {
          cards += '<div class="label" style="margin-top:12px">Encryption (X25519)</div>';
          cards += '<div class="val">' + esc(info.encryption_pubkey_hex) + '</div>';
          if (info.encryption_key_path) cards += '<div class="val" style="color:var(--text-muted);font-size:12px">' + esc(info.encryption_key_path) + '</div>';
        }
        cards += '</div>';
      }

      // Blockchain addresses card
      var a = info.addresses;
      if (a && (a.bitcoin || a.ethereum || a.solana)) {
        cards += '<div class="sdn-card">';
        cards += '<h3>Blockchain Addresses</h3>';
        if (a.bitcoin) {
          cards += '<div class="label">Bitcoin (P2WPKH)</div>';
          cards += '<div class="val accent">' + esc(a.bitcoin.address) + '</div>';
          cards += '<div class="val" style="color:var(--text-muted);font-size:12px">' + esc(a.bitcoin.path) + '</div>';
        }
        if (a.ethereum) {
          cards += '<div class="label" style="margin-top:12px">Ethereum</div>';
          cards += '<div class="val accent">' + esc(a.ethereum.address) + '</div>';
          cards += '<div class="val" style="color:var(--text-muted);font-size:12px">' + esc(a.ethereum.path) + '</div>';
        }
        if (a.solana) {
          cards += '<div class="label" style="margin-top:12px">Solana</div>';
          cards += '<div class="val accent">' + esc(a.solana.address) + '</div>';
          cards += '<div class="val" style="color:var(--text-muted);font-size:12px">' + esc(a.solana.path) + '</div>';
        }
        cards += '</div>';
      }

      el.innerHTML = cards;
    }).catch(function(){
      var el = document.getElementById('sdn-node-info');
      if (el) el.innerHTML = '<div class="sdn-placeholder">Unable to load node information.</div>';
    });

    function esc(s) {
      if (!s) return '';
      var d = document.createElement('div');
      d.textContent = s;
      return d.innerHTML;
    }
  })();
  </script>

</body>
</html>`
}

// ---------------------------------------------------------------------------
// Fallback login page (no local wallet-ui dist)
// ---------------------------------------------------------------------------

func serveFallbackLogin(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(fallbackLoginHTML))
}

const fallbackLoginHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Space Data Network — Login</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      background: #0d1117; color: #c9d1d9;
      min-height: 100vh; display: flex; align-items: center; justify-content: center;
    }
    .container { max-width: 480px; width: 100%; padding: 2rem; text-align: center; }
    h1 { font-size: 1.5rem; font-weight: 300; color: #e6edf6; margin-bottom: 1rem; }
    p { color: #8b949e; margin-bottom: 1.5rem; line-height: 1.6; }
    a { color: #58a6ff; text-decoration: none; }
    a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <div class="container">
    <h1>SPACE DATA NETWORK</h1>
    <p>
      Authentication requires the HD Wallet UI.<br>
      The server administrator needs to configure <code>wallet_ui_path</code> in the
      server config to point to the wallet-ui dist directory.
    </p>
    <p>
      <a href="https://github.com/DigitalArsenal/hd-wallet-wasm" target="_blank">
        Get HD Wallet UI &rarr;
      </a>
    </p>
  </div>
</body>
</html>`
