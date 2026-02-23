import React, { useEffect, useMemo, useState } from 'react'
import { connect } from 'redux-bundler-react'
import SdnLogo from '../icons/SdnLogo.js'
import './LoginPage.css'

const WALLET_CSS_ID = 'sdn-wallet-ui-css'
const WALLET_JS_ID = 'sdn-wallet-ui-js'

const ensureWalletAssets = ({ jsFile, cssFile }) => {
  if (typeof document === 'undefined') return

  if (cssFile && !document.getElementById(WALLET_CSS_ID)) {
    const link = document.createElement('link')
    link.id = WALLET_CSS_ID
    link.rel = 'stylesheet'
    link.crossOrigin = 'anonymous'
    link.href = `/wallet-ui/assets/${cssFile}`
    document.head.appendChild(link)
  }

  if (jsFile && !document.getElementById(WALLET_JS_ID)) {
    const script = document.createElement('script')
    script.id = WALLET_JS_ID
    script.type = 'module'
    script.crossOrigin = 'anonymous'
    script.src = `/wallet-ui/assets/${jsFile}`
    document.body.appendChild(script)
  }
}

export const LoginPage = ({
  doFetchAuthStatus,
  doWalletLogin,
  authStatus,
  authError,
  authEnabled
}) => {
  const [walletUI, setWalletUI] = useState(null)

  const walletAssets = useMemo(() => {
    const jsFile = authStatus && authStatus.wallet_js_file ? String(authStatus.wallet_js_file) : ''
    const cssFile = authStatus && authStatus.wallet_css_file ? String(authStatus.wallet_css_file) : ''
    const configured = !!(authStatus && authStatus.wallet_ui_configured)
    return { jsFile, cssFile, configured }
  }, [authStatus])

  useEffect(() => {
    doFetchAuthStatus()
  }, [doFetchAuthStatus])

  useEffect(() => {
    // Define the hooks the wallet-ui module uses to integrate.
    window.__sdnWalletReady = (ui) => {
      window.__sdnWalletUI = ui
      setWalletUI(ui)
    }
    window.__sdnOnLogin = async (identity) => {
      await doWalletLogin(identity)
    }

    return () => {
      // Leave assets loaded, but remove active callbacks.
      window.__sdnWalletReady = undefined
      window.__sdnOnLogin = undefined
    }
  }, [doWalletLogin])

  useEffect(() => {
    if (!walletAssets.jsFile && !walletAssets.cssFile) return
    ensureWalletAssets({ jsFile: walletAssets.jsFile, cssFile: walletAssets.cssFile })
  }, [walletAssets.jsFile, walletAssets.cssFile])

  const canSignIn = authEnabled && walletAssets.configured && !!walletAssets.jsFile

  return (
    <div className='sdn-login'>
      <div className='sdn-login-card'>
        <div className='sdn-login-header'>
          <SdnLogo width={44} className='sdn-logo' />
          <div className='sdn-login-title'>
            <strong>SPACE DATA NETWORK</strong>
            <span>Node authentication</span>
          </div>
        </div>

        <div className='sdn-login-body'>
          <h1>Sign in</h1>
          <p>
            Authenticate with your HD wallet to access the dashboard and trust tools.
            Challenge-response uses Ed25519 signatures; your private keys never leave your browser.
          </p>

          {!authEnabled && (
            <div className='sdn-login-setup'>
              Authentication is disabled on this node (admin.require_auth=false). The UI is running in open mode.
            </div>
          )}

          {authEnabled && !walletAssets.configured && (
            <div className='sdn-login-setup'>
              Wallet UI is not configured on the node. Set <code>admin.wallet_ui_path</code> to a built <code>hd-wallet-ui</code> dist directory.
            </div>
          )}

          {authEnabled && walletAssets.configured && !walletAssets.jsFile && (
            <div className='sdn-login-setup'>
              Wallet UI assets were not detected. Ensure <code>admin.wallet_ui_path</code> points at a directory containing <code>index.html</code> and <code>assets/</code>.
            </div>
          )}

          {authEnabled && authStatus && authStatus.admin_configured === false && (
            <div className='sdn-login-setup'>
              No admin account is configured on this node. Add your wallet xpub to the config, then restart the daemon.
              <code>{'users:\n  - xpub: "YOUR_XPUB_HERE"\n    signing_pubkey_hex: "YOUR_SIGNING_PUBKEY_HEX_HERE"\n    trust_level: "admin"\n    name: "Operator"'}</code>
            </div>
          )}

          <div className='sdn-login-actions'>
            <button
              className='sdn-login-btn'
              disabled={!canSignIn || !walletUI || typeof walletUI.openLogin !== 'function'}
              onClick={() => walletUI && walletUI.openLogin && walletUI.openLogin()}
            >
              Sign In With Wallet
            </button>
            <div className='sdn-login-secondary'>
              {canSignIn
                ? (walletUI ? 'Wallet ready' : 'Loading wallet…')
                : 'Wallet UI unavailable'}
            </div>
          </div>

          {authError && <div className='sdn-login-error'>{authError}</div>}
        </div>
      </div>
    </div>
  )
}

export default connect(
  'selectAuthStatus',
  'selectAuthError',
  'selectAuthEnabled',
  'doFetchAuthStatus',
  'doWalletLogin',
  LoginPage
)
