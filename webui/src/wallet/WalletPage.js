import React, { useEffect, useMemo, useState } from 'react'
import { connect } from 'redux-bundler-react'
import Box from '../components/box/Box.js'

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

export const WalletPage = ({
  isAdminUser: isAdmin,
  authStatus,
  doFetchAuthStatus,
  doUpdateHash,
  embedded
}) => {
  const [walletUI, setWalletUI] = useState(() => window.__sdnWalletUI || null)

  const walletAssets = useMemo(() => {
    const jsFile = authStatus && authStatus.wallet_js_file ? String(authStatus.wallet_js_file) : ''
    const cssFile = authStatus && authStatus.wallet_css_file ? String(authStatus.wallet_css_file) : ''
    const configured = !!(authStatus && authStatus.wallet_ui_configured)
    return { jsFile, cssFile, configured }
  }, [authStatus])

  useEffect(() => {
    if (!isAdmin) {
      if (!embedded) doUpdateHash('/status')
      return
    }
    doFetchAuthStatus()
  }, [isAdmin, doFetchAuthStatus, doUpdateHash, embedded])

  useEffect(() => {
    window.__sdnWalletReady = (ui) => {
      window.__sdnWalletUI = ui
      setWalletUI(ui)
    }
    return () => { window.__sdnWalletReady = undefined }
  }, [])

  useEffect(() => {
    if (!walletAssets.jsFile && !walletAssets.cssFile) return
    ensureWalletAssets({ jsFile: walletAssets.jsFile, cssFile: walletAssets.cssFile })
  }, [walletAssets.jsFile, walletAssets.cssFile])

  useEffect(() => {
    // The current hd-wallet-ui build used by SDN injects its own modal container
    // into the document; there is nothing to "mount" into the React tree.
  }, [walletUI])

  if (!isAdmin) {
    if (embedded) return null
    return (
      <Box>
        <h1 className='f3 ma0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Wallet</h1>
        <p className='ma0' style={{ color: 'var(--sdn-text-secondary)' }}>
          Admin access is required.
        </p>
      </Box>
    )
  }

  const Wrapper = embedded ? React.Fragment : Box
  const wrapperProps = embedded ? {} : { className: 'pa3 pa4-l' }

  if (!walletAssets.configured) {
    return (
      <Wrapper {...wrapperProps}>
        <h1 className='f3 ma0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Wallet</h1>
        <p className='ma0' style={{ color: 'var(--sdn-text-secondary)' }}>
          Wallet UI is not configured on this node. Set <code>admin.wallet_ui_path</code> and restart.
        </p>
      </Wrapper>
    )
  }

  if (!walletAssets.jsFile) {
    return (
      <Wrapper {...wrapperProps}>
        <h1 className='f3 ma0 mb2' style={{ color: 'var(--sdn-text-primary)' }}>Wallet</h1>
        <p className='ma0' style={{ color: 'var(--sdn-text-secondary)' }}>
          Wallet UI assets were not detected in <code>admin.wallet_ui_path</code>.
        </p>
      </Wrapper>
    )
  }

  return (
    <Wrapper {...wrapperProps}>
      <div className='flex items-center justify-between mb3'>
        <div>
          <h1 className='f3 ma0' style={{ color: 'var(--sdn-text-primary)' }}>Wallet</h1>
          <div style={{ color: 'var(--sdn-text-secondary)', fontSize: 13, marginTop: 4 }}>
            HD wallet management UI embedded from the node.
          </div>
        </div>
        <div className='flex items-center' style={{ gap: 10 }}>
          <button
            className='pointer'
            disabled={!walletUI || typeof walletUI.openAccount !== 'function'}
            onClick={() => walletUI && walletUI.openAccount && walletUI.openAccount()}
            style={{
              padding: '9px 12px',
              borderRadius: 10,
              border: '1px solid rgba(88, 166, 255, 0.6)',
              background: 'var(--sdn-bg-tertiary)',
              color: 'var(--sdn-text-primary)',
              fontWeight: 800
            }}
          >
            Open Keys
          </button>
          <button
            className='pointer'
            disabled={!walletUI || typeof walletUI.openLogin !== 'function'}
            onClick={() => walletUI && walletUI.openLogin && walletUI.openLogin()}
            style={{
              padding: '9px 12px',
              borderRadius: 10,
              border: '1px solid rgba(88, 166, 255, 0.35)',
              background: 'transparent',
              color: 'var(--sdn-text-primary)',
              fontWeight: 800
            }}
          >
            Open Login
          </button>
        </div>
      </div>

      <div style={{ color: 'var(--sdn-text-secondary)', fontSize: 13 }}>
        Use the buttons above to open the wallet modals. This page is admin-only.
      </div>
    </Wrapper>
  )
}

export default connect(
  'selectIsAdminUser',
  'selectAuthStatus',
  'doFetchAuthStatus',
  'doUpdateHash',
  WalletPage
)
