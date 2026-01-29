import React from 'react'
import { connect } from 'redux-bundler-react'

const SdnStatusBar = ({ ipfsConnected, sdnPeersCount, peersCount }) => {
  const isConnected = ipfsConnected
  const sdnCount = sdnPeersCount || 0
  const ipfsCount = Math.max(0, (peersCount || 0) - sdnCount)

  return (
    <div style={{
      height: '28px',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      padding: '0 16px',
      backgroundColor: 'var(--sdn-bg-secondary, #161b22)',
      borderBottom: '1px solid var(--sdn-border, #30363d)',
      fontSize: '12px',
      fontFamily: 'var(--font-sans, Inter, -apple-system, sans-serif)',
      WebkitAppRegion: 'drag'
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        <span style={{
          width: '8px',
          height: '8px',
          borderRadius: '50%',
          backgroundColor: isConnected ? 'var(--sdn-accent-green, #3fb950)' : 'var(--sdn-accent-red, #f85149)',
          boxShadow: isConnected ? '0 0 6px rgba(63, 185, 80, 0.5)' : '0 0 6px rgba(248, 81, 73, 0.5)',
          display: 'inline-block'
        }} />
        <span style={{ color: 'var(--sdn-text-primary, #e6edf3)', fontWeight: 500 }}>
          {isConnected ? 'Connected to Space Data Network' : 'Disconnected'}
        </span>
      </div>
      {isConnected && (
        <div style={{ color: 'var(--sdn-text-secondary, #8b949e)' }}>
          {sdnCount} SDN peer{sdnCount !== 1 ? 's' : ''} · {ipfsCount} IPFS peer{ipfsCount !== 1 ? 's' : ''}
        </div>
      )}
    </div>
  )
}

export default connect(
  'selectIpfsConnected',
  'selectSdnPeersCount',
  'selectPeersCount',
  SdnStatusBar
)
