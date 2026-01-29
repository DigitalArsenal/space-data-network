import React from 'react'
import { connect } from 'redux-bundler-react'
import { withTranslation } from 'react-i18next'
import { humanSize } from '../lib/files.js'

/**
 * SDN Network Stats Dashboard - Phase 17.3
 *
 * Prominent SDN stats panel showing:
 * - Connected SDN peers
 * - Active PubSub topics
 * - Data volume
 * - Schema types
 * - EPM identity card
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

const SdnDashboard = ({
  sdnPeersCount,
  sdnActivePubsubCount,
  sdnDataVolume,
  sdnSchemaTypes,
  peersCount,
  repoSize
}) => {
  const humanDataVolume = humanSize(sdnDataVolume || 0)
  const humanRepoSize = humanSize(repoSize || 0)
  const ipfsOnlyPeers = Math.max(0, (peersCount || 0) - (sdnPeersCount || 0))

  return (
    <div>
      {/* SDN Network Stats - Prominent */}
      <div className='sdn-panel'>
        <div className='sdn-panel-header'>
          <span className='sdn-badge sdn-badge-sdn' style={{ marginRight: '8px' }}>SDN</span>
          Space Data Network Stats
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '12px', marginBottom: '16px' }}>
          <StatCard
            value={sdnPeersCount || 0}
            label='SDN Peers'
            accent='var(--sdn-accent)'
          />
          <StatCard
            value={sdnActivePubsubCount || 0}
            label='Active Topics'
            accent='var(--sdn-accent-green)'
          />
          <StatCard
            value={humanDataVolume}
            label='Data Volume'
            accent='var(--sdn-accent-purple)'
          />
          <StatCard
            value={sdnSchemaTypes?.length || 0}
            label='Schema Types'
            accent='var(--sdn-accent-orange)'
          />
        </div>

        {sdnSchemaTypes && sdnSchemaTypes.length > 0 && (
          <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap', marginBottom: '16px' }}>
            {sdnSchemaTypes.map(type => (
              <span key={type} className='sdn-badge sdn-badge-sdn'>{type}</span>
            ))}
          </div>
        )}

        <EpmIdentityCard identity={null} />
      </div>

      {/* IPFS Stats - Secondary */}
      <div className='sdn-panel' style={{ opacity: 0.85 }}>
        <div className='sdn-panel-header'>
          <span className='sdn-badge sdn-badge-ipfs' style={{ marginRight: '8px' }}>IPFS</span>
          IPFS Network Stats
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))', gap: '12px' }}>
          <StatCard
            value={ipfsOnlyPeers}
            label='IPFS Peers'
          />
          <StatCard
            value={humanRepoSize}
            label='Repo Size'
          />
          <StatCard
            value={peersCount || 0}
            label='Total Peers'
          />
        </div>
      </div>
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
  withTranslation('status')(SdnDashboard)
)
