import React from 'react'
import { connect } from 'redux-bundler-react'
import { withTranslation } from 'react-i18next'
import { humanSize } from '../lib/files.js'
import ContextSwitcher from '../components/context-switcher/ContextSwitcher.js'

/**
 * SDN Network Stats Dashboard - Phase 17.3
 *
 * Prominent SDN stats panel showing:
 * - Connected SDN peers
 * - Active PubSub topics
 * - Data volume
 * - Schema types
 * - EPM identity card
 *
 * With context switching between SDN and IPFS views.
 */

const StatCard = ({ value, label, accent }) => (
  <div className='sdn-stat-card'>
    <div className='sdn-stat-value' style={accent ? { color: accent } : undefined}>
      {value}
    </div>
    <div className='sdn-stat-label'>{label}</div>
  </div>
)

const EpmIdentityCard = ({ identity }) => {
  if (!identity) {
    return (
      <div className='sdn-identity-card'>
        <h3>EPM Identity</h3>
        <div style={{ color: 'var(--sdn-text-secondary)', fontSize: '13px' }}>
          No EPM identity configured. Set up your Entity Profile Message in Settings.
        </div>
      </div>
    )
  }

  return (
    <div className='sdn-identity-card'>
      <h3>EPM Identity</h3>
      <div className='sdn-identity-field'>
        <span className='sdn-identity-field-label'>Entity Name</span>
        <span className='sdn-identity-field-value'>{identity.entityName || 'Unknown'}</span>
      </div>
      <div className='sdn-identity-field'>
        <span className='sdn-identity-field-label'>Entity ID</span>
        <span className='sdn-identity-field-value'>{identity.entityId || 'N/A'}</span>
      </div>
      <div className='sdn-identity-field'>
        <span className='sdn-identity-field-label'>Node Type</span>
        <span className='sdn-identity-field-value'>{identity.nodeType || 'Full Node'}</span>
      </div>
    </div>
  )
}

const summaryLineStyle = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: '10px 16px',
  fontSize: '13px',
  color: 'var(--sdn-text-secondary)',
  background: 'var(--sdn-bg-secondary)',
  borderRadius: '8px',
  marginBottom: '12px',
  border: '1px solid var(--sdn-border)'
}

const SdnDashboard = ({
  sdnPeersCount,
  sdnActivePubsubCount,
  sdnDataVolume,
  sdnSchemaTypes,
  peersCount,
  repoSize,
  isSdnContext,
  isIpfsContext,
  doSetSdnContext
}) => {
  const humanDataVolume = humanSize(sdnDataVolume || 0)
  const humanRepoSize = humanSize(repoSize || 0)
  const ipfsOnlyPeers = Math.max(0, (peersCount || 0) - (sdnPeersCount || 0))

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: '12px' }}>
        <ContextSwitcher />
      </div>

      {/* SDN Section */}
      {isSdnContext && (
        <div className='sdn-panel'>
          <div className='sdn-panel-header'>
            <span className='sdn-badge sdn-badge-sdn' style={{ marginRight: '8px' }}>SDN</span>
            Space Data Network
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '12px', marginBottom: '16px' }}>
            <StatCard value={sdnPeersCount || 0} label='SDN Peers' accent='var(--sdn-accent)' />
            <StatCard value={sdnActivePubsubCount || 0} label='Active Topics' accent='var(--sdn-accent-green)' />
            <StatCard value={humanDataVolume} label='Data Volume' accent='var(--sdn-accent-purple)' />
            <a href='#/schemas' style={{ textDecoration: 'none', color: 'inherit' }}>
              <StatCard value={sdnSchemaTypes?.length || 0} label='Schema Types' accent='var(--sdn-accent-orange)' />
            </a>
          </div>

          <EpmIdentityCard identity={null} />
        </div>
      )}
      {!isSdnContext && (
        <div style={summaryLineStyle}>
          <div>
            <span className='sdn-badge sdn-badge-sdn' style={{ marginRight: '8px' }}>SDN</span>
            {sdnPeersCount || 0} peers | {sdnActivePubsubCount || 0} topics | {humanDataVolume} data
          </div>
          <button
            onClick={() => doSetSdnContext('sdn')}
            style={{ background: 'none', border: 'none', color: 'var(--sdn-accent)', cursor: 'pointer', fontSize: '12px', fontWeight: 600 }}
          >
            Expand
          </button>
        </div>
      )}

      {/* IPFS Section */}
      {isIpfsContext && (
        <div className='sdn-panel'>
          <div className='sdn-panel-header'>
            <span className='sdn-badge sdn-badge-ipfs' style={{ marginRight: '8px' }}>IPFS</span>
            IPFS Network Stats
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '12px' }}>
            <StatCard value={ipfsOnlyPeers} label='IPFS Peers' />
            <StatCard value={humanRepoSize} label='Repo Size' />
            <StatCard value={peersCount || 0} label='Total Peers' />
          </div>
        </div>
      )}
      {!isIpfsContext && (
        <div style={summaryLineStyle}>
          <div>
            <span className='sdn-badge sdn-badge-ipfs' style={{ marginRight: '8px' }}>IPFS</span>
            {ipfsOnlyPeers} peers | {humanRepoSize} repo
          </div>
          <button
            onClick={() => doSetSdnContext('ipfs')}
            style={{ background: 'none', border: 'none', color: 'var(--sdn-accent)', cursor: 'pointer', fontSize: '12px', fontWeight: 600 }}
          >
            Expand
          </button>
        </div>
      )}
    </div>
  )
}

export default connect(
  'selectSdnPeersCount',
  'selectSdnActivePubsubCount',
  'selectSdnDataVolume',
  'selectSdnSchemaTypes',
  'selectPeersCount',
  'selectRepoSize',
  'selectIsSdnContext',
  'selectIsIpfsContext',
  'doSetSdnContext',
  withTranslation('status')(SdnDashboard)
)
