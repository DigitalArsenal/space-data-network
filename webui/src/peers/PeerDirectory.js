import React, { useState, useMemo } from 'react'
import { connect } from 'redux-bundler-react'
import TrustBadge from '../components/trust-badge/TrustBadge.js'
import PeerDetailModal from './PeerDetailModal.js'
import './PeerDirectory.css'

const TRUST_ORDER = { admin: 0, trusted: 1, standard: 2, limited: 3, untrusted: 4 }

const truncateId = (id) => {
  if (!id || id.length <= 20) return id || ''
  return id.slice(0, 8) + '...' + id.slice(-8)
}

const getInitials = (name) => {
  if (!name) return '?'
  const parts = name.split(/\s+/)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}

const timeAgo = (dateStr) => {
  if (!dateStr) return 'Never'
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return 'Never'
  const secs = Math.floor((Date.now() - d.getTime()) / 1000)
  if (secs < 60) return 'Just now'
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}

const PeerRow = ({ peer, isOnline, onClick }) => (
  <tr className='peer-dir-row' onClick={() => onClick(peer)}>
    <td className='peer-dir-cell'>
      <div className='peer-dir-name-cell'>
        <div className='peer-dir-avatar-sm'>
          {getInitials(peer.name)}
        </div>
        <div>
          <div className='peer-dir-name'>{peer.name || truncateId(String(peer.id))}</div>
          {peer.organization && <div className='peer-dir-org'>{peer.organization}</div>}
        </div>
      </div>
    </td>
    <td className='peer-dir-cell'>
      <TrustBadge level={peer.trust_level} size='small' />
    </td>
    <td className='peer-dir-cell'>
      <span className={`peer-dir-status ${isOnline ? 'peer-dir-status-online' : 'peer-dir-status-offline'}`}>
        {isOnline ? 'Online' : 'Offline'}
      </span>
    </td>
    <td className='peer-dir-cell peer-dir-cell-secondary'>
      {timeAgo(peer.last_seen)}
    </td>
    <td className='peer-dir-cell peer-dir-cell-mono'>
      {truncateId(String(peer.id))}
    </td>
  </tr>
)

const PeerTile = ({ peer, isOnline, onClick }) => (
  <div className='peer-dir-tile' role='button' tabIndex={0} onClick={() => onClick(peer)} onKeyDown={e => e.key === 'Enter' && onClick(peer)}>
    <div className='peer-dir-tile-header'>
      <div className='peer-dir-avatar'>
        {getInitials(peer.name)}
      </div>
      <span className={`peer-dir-status-dot ${isOnline ? 'peer-dir-status-online' : 'peer-dir-status-offline'}`} />
    </div>
    <div className='peer-dir-tile-name'>{peer.name || truncateId(String(peer.id))}</div>
    {peer.organization && <div className='peer-dir-tile-org'>{peer.organization}</div>}
    <div className='peer-dir-tile-footer'>
      <TrustBadge level={peer.trust_level} size='small' />
      <span className='peer-dir-tile-seen'>{timeAgo(peer.last_seen)}</span>
    </div>
  </div>
)

const PeerDirectory = ({
  trustedPeers,
  sdnPeerIds,
  doFetchPeerEPMVCard,
  doDownloadPeerVCard
}) => {
  const [search, setSearch] = useState('')
  const [sortBy, setSortBy] = useState('name')
  const [filterTrust, setFilterTrust] = useState('all')
  const [viewMode, setViewMode] = useState('list')
  const [selectedPeer, setSelectedPeer] = useState(null)

  const onlineSet = useMemo(() => sdnPeerIds || new Set(), [sdnPeerIds])

  const filtered = useMemo(() => {
    let peers = Array.isArray(trustedPeers) ? [...trustedPeers] : []

    // Filter by search
    if (search) {
      const q = search.toLowerCase()
      peers = peers.filter(p =>
        (p.name && p.name.toLowerCase().includes(q)) ||
        (p.organization && p.organization.toLowerCase().includes(q)) ||
        String(p.id).toLowerCase().includes(q)
      )
    }

    // Filter by trust level
    if (filterTrust !== 'all') {
      peers = peers.filter(p => p.trust_level === filterTrust)
    }

    // Sort
    peers.sort((a, b) => {
      if (sortBy === 'name') {
        return (a.name || '').localeCompare(b.name || '') || String(a.id).localeCompare(String(b.id))
      }
      if (sortBy === 'trust') {
        return (TRUST_ORDER[a.trust_level] ?? 5) - (TRUST_ORDER[b.trust_level] ?? 5)
      }
      if (sortBy === 'last_seen') {
        const ta = a.last_seen ? new Date(a.last_seen).getTime() : 0
        const tb = b.last_seen ? new Date(b.last_seen).getTime() : 0
        return tb - ta
      }
      if (sortBy === 'online') {
        const ao = onlineSet.has(String(a.id)) ? 0 : 1
        const bo = onlineSet.has(String(b.id)) ? 0 : 1
        return ao - bo
      }
      return 0
    })

    return peers
  }, [trustedPeers, search, filterTrust, sortBy, onlineSet])

  const handlePeerClick = (peer) => {
    setSelectedPeer(peer)
    doFetchPeerEPMVCard(String(peer.id))
  }

  return (
    <div className='peer-dir'>
      {/* Toolbar */}
      <div className='peer-dir-toolbar'>
        <input
          className='peer-dir-search'
          type='text'
          placeholder='Search peers...'
          value={search}
          onChange={e => setSearch(e.target.value)}
        />
        <select
          className='peer-dir-select'
          value={filterTrust}
          onChange={e => setFilterTrust(e.target.value)}
        >
          <option value='all'>All Trust</option>
          <option value='admin'>Admin</option>
          <option value='trusted'>Trusted</option>
          <option value='standard'>Standard</option>
          <option value='limited'>Limited</option>
          <option value='untrusted'>Untrusted</option>
        </select>
        <select
          className='peer-dir-select'
          value={sortBy}
          onChange={e => setSortBy(e.target.value)}
        >
          <option value='name'>Sort: Name</option>
          <option value='trust'>Sort: Trust</option>
          <option value='last_seen'>Sort: Last Seen</option>
          <option value='online'>Sort: Online</option>
        </select>
        <div className='peer-dir-view-toggle'>
          <button
            className={`peer-dir-view-btn ${viewMode === 'list' ? 'active' : ''}`}
            onClick={() => setViewMode('list')}
            title='List view'
          >
            &#9776;
          </button>
          <button
            className={`peer-dir-view-btn ${viewMode === 'grid' ? 'active' : ''}`}
            onClick={() => setViewMode('grid')}
            title='Grid view'
          >
            &#9638;
          </button>
        </div>
      </div>

      {/* Count */}
      <div className='peer-dir-count'>
        {filtered.length} peer{filtered.length !== 1 ? 's' : ''}
        {search && ` matching "${search}"`}
      </div>

      {/* List View */}
      {viewMode === 'list' && (
        <div className='peer-dir-table-wrap'>
          <table className='peer-dir-table'>
            <thead>
              <tr>
                <th className='peer-dir-th'>Name</th>
                <th className='peer-dir-th'>Trust</th>
                <th className='peer-dir-th'>Status</th>
                <th className='peer-dir-th'>Last Seen</th>
                <th className='peer-dir-th'>Peer ID</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(p => (
                <PeerRow
                  key={String(p.id)}
                  peer={p}
                  isOnline={onlineSet.has(String(p.id))}
                  onClick={handlePeerClick}
                />
              ))}
            </tbody>
          </table>
          {filtered.length === 0 && (
            <div className='peer-dir-empty'>
              {search ? 'No peers match your search.' : 'No peers in the directory yet.'}
            </div>
          )}
        </div>
      )}

      {/* Grid View */}
      {viewMode === 'grid' && (
        <div className='peer-dir-grid'>
          {filtered.map(p => (
            <PeerTile
              key={String(p.id)}
              peer={p}
              isOnline={onlineSet.has(String(p.id))}
              onClick={handlePeerClick}
            />
          ))}
          {filtered.length === 0 && (
            <div className='peer-dir-empty'>
              {search ? 'No peers match your search.' : 'No peers in the directory yet.'}
            </div>
          )}
        </div>
      )}

      {/* Detail Modal */}
      {selectedPeer && (
        <PeerDetailModal
          peer={selectedPeer}
          isOnline={onlineSet.has(String(selectedPeer.id))}
          onClose={() => setSelectedPeer(null)}
          onDownloadVCard={() => doDownloadPeerVCard(String(selectedPeer.id))}
        />
      )}
    </div>
  )
}

export default connect(
  'selectTrustedPeers',
  'selectSdnPeerIds',
  'doFetchPeerEPMVCard',
  'doDownloadPeerVCard',
  PeerDirectory
)
