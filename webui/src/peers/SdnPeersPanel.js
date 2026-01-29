import React, { useEffect, useState, useMemo } from 'react'
import { connect } from 'redux-bundler-react'
import { withTranslation } from 'react-i18next'

/**
 * SDN Peers Panel - Phase 17.2: SDN vs IPFS Peer Separation
 *
 * Splits peers into two panels:
 * - SDN Peers (prominent, top) - peers supporting /spacedatanetwork/sds-exchange/1.0.0
 * - IPFS Peers (secondary, below) - standard IPFS peers
 */

const PeerRow = ({ peer, isSdn }) => {
  const peerId = peer.peerId || peer.peer?.toString() || 'unknown'
  const shortId = peerId.length > 16 ? `${peerId.slice(0, 8)}...${peerId.slice(-8)}` : peerId

  return (
    <tr style={{ borderBottom: '1px solid var(--sdn-border)' }}>
      <td style={{ padding: '8px 12px' }}>
        {isSdn && <span className='sdn-badge sdn-badge-sdn' style={{ marginRight: '6px' }}>SDN</span>}
        <span className='monospace' style={{ fontSize: '12px', color: 'var(--sdn-text-primary)' }} title={peerId}>
          {shortId}
        </span>
      </td>
      <td style={{ padding: '8px 12px', color: 'var(--sdn-text-secondary)', fontSize: '13px' }}>
        {peer.location || 'Unknown'}
      </td>
      <td style={{ padding: '8px 12px', color: 'var(--sdn-text-secondary)', fontSize: '13px' }}>
        {peer.latency != null ? `${peer.latency}ms` : '-'}
      </td>
      <td style={{ padding: '8px 12px', color: 'var(--sdn-text-muted)', fontSize: '12px' }}>
        {peer.connection || peer.address || '-'}
      </td>
    </tr>
  )
}

const PeerTable = ({ peers, isSdn, emptyMessage }) => {
  if (!peers || peers.length === 0) {
    return (
      <div style={{ padding: '20px', textAlign: 'center', color: 'var(--sdn-text-secondary)', fontSize: '14px' }}>
        {emptyMessage}
      </div>
    )
  }

  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ borderBottom: '2px solid var(--sdn-border)' }}>
            <th style={{ padding: '8px 12px', textAlign: 'left', fontSize: '11px', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--sdn-text-secondary)' }}>Peer ID</th>
            <th style={{ padding: '8px 12px', textAlign: 'left', fontSize: '11px', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--sdn-text-secondary)' }}>Location</th>
            <th style={{ padding: '8px 12px', textAlign: 'left', fontSize: '11px', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--sdn-text-secondary)' }}>Latency</th>
            <th style={{ padding: '8px 12px', textAlign: 'left', fontSize: '11px', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--sdn-text-secondary)' }}>Connection</th>
          </tr>
        </thead>
        <tbody>
          {peers.map((peer, i) => (
            <PeerRow key={peer.peerId || i} peer={peer} isSdn={isSdn} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

const SdnPeersPanel = ({ peerLocationsForSwarm, sdnPeerIds, sdnPeersCount, t }) => {
  const [resolvedPeers, setResolvedPeers] = useState([])

  useEffect(() => {
    if (peerLocationsForSwarm?.then) {
      peerLocationsForSwarm.then(setResolvedPeers)
    } else if (Array.isArray(peerLocationsForSwarm)) {
      setResolvedPeers(peerLocationsForSwarm)
    }
  }, [peerLocationsForSwarm])

  const { sdnPeers, ipfsPeers } = useMemo(() => {
    const sdn = []
    const ipfs = []
    for (const peer of resolvedPeers) {
      const peerId = peer.peerId || ''
      if (sdnPeerIds && sdnPeerIds.has(peerId)) {
        sdn.push(peer)
      } else {
        ipfs.push(peer)
      }
    }
    return { sdnPeers: sdn, ipfsPeers: ipfs }
  }, [resolvedPeers, sdnPeerIds])

  return (
    <div>
      {/* SDN Peers - Prominent */}
      <div className='sdn-panel'>
        <div className='sdn-panel-header'>
          <span className='sdn-badge sdn-badge-sdn' style={{ marginRight: '8px' }}>SDN</span>
          SDN Peers ({sdnPeers.length})
        </div>
        <PeerTable
          peers={sdnPeers}
          isSdn={true}
          emptyMessage='No SDN peers connected. SDN peers support the /spacedatanetwork/sds-exchange/1.0.0 protocol.'
        />
      </div>

      {/* IPFS Peers - Secondary */}
      <div className='sdn-panel' style={{ opacity: 0.85 }}>
        <div className='sdn-panel-header'>
          <span className='sdn-badge sdn-badge-ipfs' style={{ marginRight: '8px' }}>IPFS</span>
          IPFS Peers ({ipfsPeers.length})
        </div>
        <PeerTable
          peers={ipfsPeers}
          isSdn={false}
          emptyMessage='No IPFS peers connected.'
        />
      </div>
    </div>
  )
}

export default connect(
  'selectPeerLocationsForSwarm',
  'selectSdnPeerIds',
  'selectSdnPeersCount',
  withTranslation('peers')(SdnPeersPanel)
)
