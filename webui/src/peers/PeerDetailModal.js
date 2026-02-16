import React, { useState, useCallback } from 'react'
import TrustBadge from '../components/trust-badge/TrustBadge.js'
import './PeerDetailModal.css'

const truncate = (str, len = 12) => {
  if (!str || str.length <= len * 2 + 3) return str || ''
  return str.slice(0, len) + '...' + str.slice(-len)
}

const CopyValue = ({ value, truncated }) => {
  const [copied, setCopied] = useState(false)

  const doCopy = useCallback(() => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }, [value])

  if (!value) return null

  return (
    <span className='pdm-copyable' role='button' tabIndex={0} onClick={doCopy} onKeyDown={e => e.key === 'Enter' && doCopy()} title={value}>
      <span className='pdm-mono'>{truncated || truncate(value)}</span>
      <span className='pdm-copy-hint'>{copied ? 'Copied!' : 'Copy'}</span>
    </span>
  )
}

const formatBytes = (bytes) => {
  if (!bytes || bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${units[i]}`
}

const formatTime = (dateStr) => {
  if (!dateStr) return 'Never'
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return 'Never'
  return d.toLocaleString()
}

const getInitials = (name) => {
  if (!name) return '?'
  const parts = name.split(/\s+/)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}

const PeerDetailModal = ({ peer, isOnline, onClose, onDownloadVCard }) => {
  if (!peer) return null

  const peerId = String(peer.id)

  return (
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events
    <div className='pdm-overlay' onClick={onClose}>
      {/* eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events */}
      <div className='pdm-modal' onClick={e => e.stopPropagation()}>
        {/* Header */}
        <div className='pdm-header'>
          <div className='pdm-header-left'>
            <div className='pdm-avatar'>
              {getInitials(peer.name)}
            </div>
            <div>
              <h3 className='pdm-title'>{peer.name || truncate(peerId, 10)}</h3>
              {peer.organization && <p className='pdm-subtitle'>{peer.organization}</p>}
              <div className='pdm-badges'>
                <TrustBadge level={peer.trust_level} size='small' />
                <span className={`pdm-status ${isOnline ? 'pdm-online' : 'pdm-offline'}`}>
                  {isOnline ? 'Online' : 'Offline'}
                </span>
              </div>
            </div>
          </div>
          <button className='pdm-close' onClick={onClose}>&times;</button>
        </div>

        {/* Identity */}
        <div className='pdm-section'>
          <div className='pdm-section-title'>Identity</div>
          <div className='pdm-field'>
            <span className='pdm-label'>Peer ID</span>
            <CopyValue value={peerId} />
          </div>
          {peer.groups && peer.groups.length > 0 && (
            <div className='pdm-field'>
              <span className='pdm-label'>Groups</span>
              <span className='pdm-value'>{peer.groups.join(', ')}</span>
            </div>
          )}
        </div>

        {/* Connection Stats */}
        <div className='pdm-section'>
          <div className='pdm-section-title'>Connection Statistics</div>
          <div className='pdm-stats-grid'>
            <div className='pdm-stat'>
              <span className='pdm-stat-label'>Connections</span>
              <span className='pdm-stat-value'>{peer.connection_count || 0}</span>
            </div>
            <div className='pdm-stat'>
              <span className='pdm-stat-label'>Messages In</span>
              <span className='pdm-stat-value'>{peer.messages_received || 0}</span>
            </div>
            <div className='pdm-stat'>
              <span className='pdm-stat-label'>Messages Out</span>
              <span className='pdm-stat-value'>{peer.messages_sent || 0}</span>
            </div>
            <div className='pdm-stat'>
              <span className='pdm-stat-label'>Data In</span>
              <span className='pdm-stat-value'>{formatBytes(peer.bytes_received)}</span>
            </div>
            <div className='pdm-stat'>
              <span className='pdm-stat-label'>Data Out</span>
              <span className='pdm-stat-value'>{formatBytes(peer.bytes_sent)}</span>
            </div>
            <div className='pdm-stat'>
              <span className='pdm-stat-label'>Last Seen</span>
              <span className='pdm-stat-value'>{formatTime(peer.last_seen)}</span>
            </div>
          </div>
        </div>

        {/* Addresses */}
        {peer.addrs && peer.addrs.length > 0 && (
          <div className='pdm-section'>
            <div className='pdm-section-title'>Addresses</div>
            {peer.addrs.map((addr, i) => (
              <div key={i} className='pdm-field'>
                <CopyValue value={addr} truncated={truncate(addr, 20)} />
              </div>
            ))}
          </div>
        )}

        {/* Notes */}
        {peer.notes && (
          <div className='pdm-section'>
            <div className='pdm-section-title'>Notes</div>
            <div className='pdm-notes'>{peer.notes}</div>
          </div>
        )}

        {/* Actions */}
        <div className='pdm-actions'>
          {onDownloadVCard && (
            <button className='pdm-btn' onClick={onDownloadVCard}>
              Download vCard
            </button>
          )}
          <button className='pdm-btn' onClick={() => navigator.clipboard.writeText(peerId)}>
            Copy Peer ID
          </button>
        </div>
      </div>
    </div>
  )
}

export default PeerDetailModal
