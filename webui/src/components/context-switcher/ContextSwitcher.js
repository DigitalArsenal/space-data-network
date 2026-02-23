import React from 'react'
import { connect } from 'redux-bundler-react'

const pillStyle = {
  display: 'inline-flex',
  borderRadius: '20px',
  border: '1px solid var(--sdn-border)',
  overflow: 'hidden',
  fontSize: '12px',
  fontWeight: 600,
  letterSpacing: '0.03em'
}

const segmentStyle = (active) => ({
  padding: '5px 14px',
  cursor: 'pointer',
  background: active ? 'var(--sdn-accent, #58a6ff)' : 'transparent',
  color: active ? '#fff' : 'var(--sdn-text-secondary)',
  border: 'none',
  outline: 'none',
  transition: 'background 0.15s ease, color 0.15s ease',
  userSelect: 'none'
})

const ContextSwitcher = ({ sdnActiveContext, doSetSdnContext }) => (
  <div style={pillStyle}>
    <button
      style={segmentStyle(sdnActiveContext === 'sdn')}
      onClick={() => doSetSdnContext('sdn')}
    >
      SDN
    </button>
    <button
      style={segmentStyle(sdnActiveContext === 'ipfs')}
      onClick={() => doSetSdnContext('ipfs')}
    >
      IPFS
    </button>
  </div>
)

export default connect(
  'selectSdnActiveContext',
  'doSetSdnContext',
  ContextSwitcher
)
