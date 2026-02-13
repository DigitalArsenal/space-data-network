package auth

import (
	"html/template"
	"net/http"
	"strings"
)

type loginPageData struct {
	WalletUIBase string // "/wallet-ui" for local, "https://unpkg.com/hd-wallet-ui@latest" for CDN
}

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// If already authenticated, redirect to admin
	if session, err := h.sessionFromRequest(r); err == nil && session != nil {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}

	data := loginPageData{
		WalletUIBase: "https://unpkg.com/hd-wallet-ui@latest",
	}
	if strings.TrimSpace(h.walletUIPath) != "" {
		data.WalletUIBase = "/wallet-ui"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	loginTemplate.Execute(w, data)
}

var loginTemplate = template.Must(template.New("login").Parse(loginPageHTML))

const loginPageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Space Data Network — Login</title>
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Oxygen, Ubuntu, sans-serif;
      background: #0d1117;
      color: #c9d1d9;
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      align-items: center;
      justify-content: center;
    }
    .login-container {
      max-width: 480px;
      width: 100%;
      padding: 2rem;
    }
    h1 {
      text-align: center;
      font-size: 1.5rem;
      font-weight: 300;
      margin-bottom: 0.5rem;
      color: #e6edf6;
    }
    .subtitle {
      text-align: center;
      color: #8b949e;
      margin-bottom: 2rem;
      font-size: 0.9rem;
    }
    #wallet-root {
      margin-bottom: 1.5rem;
    }
    .sign-in-btn {
      display: none;
      width: 100%;
      padding: 12px;
      border: none;
      border-radius: 8px;
      background: linear-gradient(135deg, #238636, #2ea043);
      color: #fff;
      font-size: 1rem;
      font-weight: 600;
      cursor: pointer;
      transition: opacity 0.2s;
    }
    .sign-in-btn:hover { opacity: 0.9; }
    .sign-in-btn:disabled { opacity: 0.5; cursor: not-allowed; }
    .sign-in-btn.visible { display: block; }
    #status {
      text-align: center;
      margin-top: 1rem;
      font-size: 0.9rem;
      color: #8b949e;
      min-height: 1.5em;
    }
    #status.error { color: #f85149; }
    #status.success { color: #3fb950; }
    .node-info {
      margin-top: 2rem;
      padding: 1rem;
      background: #161b22;
      border: 1px solid #30363d;
      border-radius: 8px;
      display: none;
    }
    .node-info.visible { display: block; }
    .node-info h3 {
      font-size: 0.85rem;
      color: #8b949e;
      margin-bottom: 0.75rem;
      text-transform: uppercase;
      letter-spacing: 0.05em;
    }
    .info-row {
      display: flex;
      justify-content: space-between;
      padding: 6px 0;
      border-bottom: 1px solid #21262d;
      font-size: 0.85rem;
    }
    .info-row:last-child { border-bottom: none; }
    .info-label { color: #8b949e; }
    .info-value {
      color: #c9d1d9;
      font-family: monospace;
      max-width: 240px;
      overflow: hidden;
      text-overflow: ellipsis;
      cursor: pointer;
    }
    .info-value:hover { color: #58a6ff; }
  </style>
</head>
<body>
  <div class="login-container">
    <h1>SPACE DATA NETWORK</h1>
    <p class="subtitle">Authenticate with your HD wallet to continue</p>

    <div id="wallet-root"></div>

    <button id="signInBtn" class="sign-in-btn" onclick="signInToServer()">
      Sign In to Server
    </button>

    <div id="status"></div>

    <div id="nodeInfo" class="node-info">
      <h3>Node Information</h3>
      <div id="nodeInfoContent"></div>
    </div>
  </div>

  <script type="module">
    const WALLET_UI_BASE = '{{.WalletUIBase}}';

    // =========================================================================
    // Dynamic imports from hd-wallet-ui
    // =========================================================================
    let createWalletUI, walletLib, hdWalletWasm;
    let walletUI = null;
    let walletState = null;

    async function loadModules() {
      try {
        const appMod = await import(WALLET_UI_BASE + '/src/app.js');
        createWalletUI = appMod.createWalletUI;
      } catch(e) {
        // Try alternate import path
        const appMod = await import(WALLET_UI_BASE);
        createWalletUI = appMod.createWalletUI;
      }
    }

    // =========================================================================
    // Initialize wallet UI
    // =========================================================================
    async function initWallet() {
      await loadModules();

      const root = document.getElementById('wallet-root');
      walletUI = await createWalletUI(root, { autoOpenWallet: true });

      // Watch for login state changes by polling
      setInterval(checkLoginState, 500);
    }

    function checkLoginState() {
      // The wallet-ui sets a login flag on its internal state.
      // We detect login by checking for the keys-modal having content.
      const keysModal = document.getElementById('keys-modal');
      const loginModal = document.getElementById('login-modal');
      const signInBtn = document.getElementById('signInBtn');

      if (keysModal && !loginModal?.classList.contains('active')) {
        // User has logged in if the keys modal exists and login is dismissed
        const walletTab = document.querySelector('[data-tab="wallet"]');
        if (walletTab || document.querySelector('.wallet-portfolio')) {
          signInBtn.classList.add('visible');
        }
      }
    }

    // =========================================================================
    // Challenge-response authentication
    // =========================================================================
    window.signInToServer = async function() {
      const status = document.getElementById('status');
      const btn = document.getElementById('signInBtn');
      btn.disabled = true;
      status.className = '';
      status.textContent = 'Authenticating...';

      try {
        // Import noble curves for Ed25519 signing
        const { ed25519 } = await import('https://esm.sh/@noble/curves@1.8.1/ed25519');

        // Get the HD root from wallet-ui's internal state
        // The wallet-ui module stores state in a closure, but we can access
        // the derived keys through the DOM or by re-deriving from stored wallet
        const { getSigningKey, buildSigningPath, WellKnownCoinType } = await import(WALLET_UI_BASE + '/src/lib.js');

        // Try to get stored wallet data to re-derive keys
        const { hasStoredWallet, retrieveWithPIN } = await import(WALLET_UI_BASE + '/src/lib.js');

        // We need the Ed25519 signing key at SDN coin type (9999)
        // The wallet-ui derives keys from hdRoot — we need to access it
        // Strategy: look for the xpub in localStorage or prompt re-derivation

        // Access the wallet module's internal hdRoot via the global hdkey
        // The wallet-ui sets window.Buffer and uses hdkey from hd-wallet-wasm
        const initHDWallet = (await import('hd-wallet-wasm')).default;
        const hdMod = await import('hd-wallet-wasm');

        // Check if we can find the seed in the wallet-ui's state
        // The createWalletUI function returns a limited API, but the state
        // is in module scope. We'll re-derive from PIN storage if available.

        // For now, prompt the user to enter their signing details
        // This will be replaced with proper wallet-ui state access

        // Derive the SDN signing key (coin type 9999, account 0)
        // hdRoot should be available from the wallet-ui's initialization
        const hdRoot = window.__hdRoot; // Set by wallet-ui after login

        if (!hdRoot) {
          throw new Error('Wallet not initialized. Please log in first using the wallet above.');
        }

        const sdnSigningPath = "m/44'/9999'/0'/0'/0'";
        const derived = hdRoot.derivePath(sdnSigningPath);
        const signingKeyBytes = derived.privateKey;

        // Get Ed25519 public key from the 32-byte seed
        const pubKey = ed25519.getPublicKey(signingKeyBytes);
        const pubKeyHex = bytesToHex(pubKey);

        // Derive xpub at account level m/44'/9999'/0'
        const accountPath = "m/44'/9999'/0'";
        const accountDerived = hdRoot.derivePath(accountPath);
        const xpub = accountDerived.toXpub ? accountDerived.toXpub() : pubKeyHex;

        // Step 1: Request challenge
        const challengeResp = await fetch('/api/auth/challenge', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            xpub: xpub,
            client_pubkey_hex: pubKeyHex,
            ts: Math.floor(Date.now() / 1000)
          })
        });
        const challengeData = await challengeResp.json();
        if (!challengeResp.ok) throw new Error(challengeData.message || 'Challenge request failed');

        // Step 2: Sign the challenge
        const challengeBytes = base64ToBytes(challengeData.challenge);
        const signature = ed25519.sign(challengeBytes, signingKeyBytes);
        const signatureHex = bytesToHex(signature);

        // Step 3: Verify signature with server
        const verifyResp = await fetch('/api/auth/verify', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            challenge_id: challengeData.challenge_id,
            xpub: xpub,
            client_pubkey_hex: pubKeyHex,
            challenge: challengeData.challenge,
            signature_hex: signatureHex
          })
        });
        const verifyData = await verifyResp.json();
        if (!verifyResp.ok) throw new Error(verifyData.message || 'Verification failed');

        status.className = 'success';
        status.textContent = 'Authenticated as ' + (verifyData.user.name || 'user') +
          ' (trust: ' + verifyData.user.trust_level + '). Redirecting...';

        setTimeout(() => { window.location.href = '/admin'; }, 1000);

      } catch(err) {
        status.className = 'error';
        status.textContent = err.message;
        btn.disabled = false;
      }
    };

    // =========================================================================
    // Fetch and display node info
    // =========================================================================
    async function loadNodeInfo() {
      try {
        const resp = await fetch('/api/node/info');
        if (!resp.ok) return;
        const info = await resp.json();

        const container = document.getElementById('nodeInfoContent');
        const rows = [
          ['Peer ID', info.peer_id],
          ['Mode', info.mode],
          ['Version', info.version],
        ];
        if (info.signing_pubkey_hex) rows.push(['Signing Key', info.signing_pubkey_hex]);
        if (info.encryption_pubkey_hex) rows.push(['Encryption Key', info.encryption_pubkey_hex]);
        if (info.signing_key_path) rows.push(['Signing Path', info.signing_key_path]);
        if (info.listen_addresses) {
          info.listen_addresses.forEach((addr, i) => {
            rows.push(['Listen ' + (i+1), addr]);
          });
        }

        container.innerHTML = rows.map(([label, value]) =>
          '<div class="info-row">' +
          '  <span class="info-label">' + label + '</span>' +
          '  <span class="info-value" title="Click to copy" onclick="copyText(this)">' + value + '</span>' +
          '</div>'
        ).join('');

        document.getElementById('nodeInfo').classList.add('visible');
      } catch(e) {
        console.warn('Failed to load node info:', e);
      }
    }

    window.copyText = function(el) {
      navigator.clipboard.writeText(el.textContent.trim()).then(() => {
        const orig = el.textContent;
        el.textContent = 'Copied!';
        setTimeout(() => { el.textContent = orig; }, 1000);
      });
    };

    // =========================================================================
    // Utilities
    // =========================================================================
    function bytesToHex(bytes) {
      return Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
    }

    function base64ToBytes(b64) {
      // Handle both standard and URL-safe base64, with or without padding
      const padded = b64.replace(/-/g, '+').replace(/_/g, '/');
      const binary = atob(padded);
      return new Uint8Array(binary.length).map((_, i) => binary.charCodeAt(i));
    }

    // =========================================================================
    // Boot
    // =========================================================================
    loadNodeInfo();
    initWallet().catch(err => {
      console.error('Failed to initialize wallet UI:', err);
      document.getElementById('status').textContent =
        'Failed to load wallet UI. Check console for details.';
      document.getElementById('status').className = 'error';
    });
  </script>
</body>
</html>`
