import React, { useState, useCallback } from 'react'
import TrustBadge from '../trust-badge/TrustBadge.js'
import './IdentityCard.css'

const CopyField = ({ label, value }) => {
  const [copied, setCopied] = useState(false)

  const doCopy = useCallback(() => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }, [value])

  if (!value) return null

  return (
    <div className='identity-card-field'>
      <span className='identity-card-label'>{label}</span>
      <span className='identity-card-value'>{value}</span>
      <button className='identity-card-copy-btn' onClick={doCopy}>
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}

const TextField = ({ label, value }) => {
  if (!value) return null
  return (
    <div className='identity-card-field'>
      <span className='identity-card-label'>{label}</span>
      <span className='identity-card-value-text'>{value}</span>
    </div>
  )
}

const getInitials = (dn) => {
  if (!dn) return '?'
  const parts = dn.split(/\s+/)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return dn.slice(0, 2).toUpperCase()
}

const IdentityCard = ({
  epm,
  trustLevel,
  qrUrl,
  onDownloadVCard,
  onShowQR,
  isLocal = false,
  compact = false
}) => {
  const [showQR, setShowQR] = useState(false)

  if (!epm) return null

  const dn = epm.dn || 'Unknown Node'
  const signingKey = epm.keys?.find(k => k.key_type === 'signing')
  const encryptionKey = epm.keys?.find(k => k.key_type === 'encryption')
  const xpub = signingKey?.xpub
  const multiAddrs = epm.multiformat_address || []

  const handleShowQR = () => {
    if (onShowQR) onShowQR()
    setShowQR(true)
  }

  return (
    <div className='identity-card'>
      {/* Header */}
      <div className='identity-card-header'>
        <div className='identity-card-avatar'>
          {getInitials(dn)}
        </div>
        <div>
          <h3 className='identity-card-title'>{dn}</h3>
          {(epm.legal_name || epm.job_title) && (
            <p className='identity-card-subtitle'>
              {[epm.job_title, epm.legal_name].filter(Boolean).join(' - ')}
            </p>
          )}
          {trustLevel && <TrustBadge trustLevel={trustLevel} size='small' />}
        </div>
      </div>

      {/* Identity */}
      <div className='identity-card-section'>
        <div className='identity-card-section-title'>Identity</div>
        <CopyField label='Peer ID' value={epm.peer_id} />
        {xpub && <CopyField label='XPub' value={xpub} />}
      </div>

      {/* Cryptographic Keys */}
      {(signingKey || encryptionKey) && (
        <div className='identity-card-section'>
          <div className='identity-card-section-title'>Cryptographic Keys</div>
          {signingKey && (
            <CopyField
              label='Signing (Ed25519)'
              value={signingKey.public_key}
            />
          )}
          {encryptionKey && (
            <CopyField
              label='Encrypt (X25519)'
              value={encryptionKey.public_key}
            />
          )}
          {signingKey?.key_address && (
            <TextField label='Signing Path' value={signingKey.key_address} />
          )}
          {encryptionKey?.key_address && (
            <TextField label='Encrypt Path' value={encryptionKey.key_address} />
          )}
        </div>
      )}

      {/* Contact */}
      {!compact && (epm.email || epm.telephone) && (
        <div className='identity-card-section'>
          <div className='identity-card-section-title'>Contact</div>
          <TextField label='Email' value={epm.email} />
          <TextField label='Telephone' value={epm.telephone} />
        </div>
      )}

      {/* Network */}
      {!compact && multiAddrs.length > 0 && (
        <div className='identity-card-section'>
          <div className='identity-card-section-title'>Network</div>
          {multiAddrs.map((addr, i) => (
            <CopyField key={i} label={i === 0 ? 'IPNS' : ''} value={addr} />
          ))}
        </div>
      )}

      {/* Actions */}
      <div className='identity-card-actions'>
        {onDownloadVCard && (
          <button className='identity-card-btn' onClick={onDownloadVCard}>
            Download vCard
          </button>
        )}
        <button className='identity-card-btn' onClick={handleShowQR}>
          Show QR
        </button>
      </div>

      {/* QR Modal */}
      {showQR && qrUrl && (
        // eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events
        <div className='identity-card-qr-modal' onClick={() => setShowQR(false)}>
          {/* eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events */}
          <div className='identity-card-qr-content' onClick={e => e.stopPropagation()}>
            <img src={qrUrl} alt='EPM QR Code' />
            <p style={{ color: 'var(--sdn-text-secondary)', marginTop: 12, fontSize: 13 }}>
              Scan to import this node's vCard
            </p>
            <button
              className='identity-card-btn'
              onClick={() => setShowQR(false)}
              style={{ marginTop: 12 }}
            >
              Close
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

export default IdentityCard
