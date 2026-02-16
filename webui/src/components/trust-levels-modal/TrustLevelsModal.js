import React from 'react'
import Overlay from '../overlay/overlay.tsx'
import TrustBadge from '../trust-badge/TrustBadge.js'

const LEVELS = [
  {
    id: 'admin',
    description: 'Full control. Can manage all peers, users, groups, and node settings.'
  },
  {
    id: 'trusted',
    description: 'Verified peer with full data exchange privileges and replication rights.'
  },
  {
    id: 'standard',
    description: 'Default level for known peers. Normal data access and exchange.'
  },
  {
    id: 'limited',
    description: 'Restricted access. Reduced privileges for partially verified peers.'
  },
  {
    id: 'untrusted',
    description: 'No special privileges. Connection only, no data exchange.'
  }
]

const TrustLevelsModal = ({ show, onClose }) => (
  <Overlay show={show} onLeave={onClose} hidden={false}>
    <div
      className='sdn-modal'
      style={{
        position: 'fixed',
        top: '50%',
        left: '50%',
        transform: 'translate(-50%, -50%)',
        maxWidth: '32em',
        width: '90vw',
        borderRadius: 12,
        border: '1px solid var(--sdn-border)',
        background: 'var(--sdn-bg-secondary)',
        color: 'var(--sdn-text-primary)',
        padding: '24px',
        fontFamily: 'system-ui, -apple-system, sans-serif',
        boxShadow: '0 16px 48px rgba(0,0,0,0.4)'
      }}
    >
      <h2 style={{ margin: '0 0 8px', fontSize: 18, fontWeight: 700 }}>Trust Levels</h2>
      <p style={{ margin: '0 0 20px', fontSize: 13, color: 'var(--sdn-text-secondary)', lineHeight: 1.5 }}>
        Space Data Network uses a <strong style={{ color: 'var(--sdn-text-primary)' }}>PGP-inspired web of trust</strong> model.
        Each peer and user is assigned a trust level that determines their privileges on the network.
      </p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 20 }}>
        {LEVELS.map(l => (
          <div key={l.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
            <div style={{ flexShrink: 0, width: 90, paddingTop: 1 }}>
              <TrustBadge level={l.id} />
            </div>
            <div style={{ fontSize: 13, color: 'var(--sdn-text-secondary)', lineHeight: 1.45 }}>
              {l.description}
            </div>
          </div>
        ))}
      </div>

      <div style={{
        padding: '14px 16px',
        borderRadius: 8,
        background: 'var(--sdn-bg-tertiary)',
        border: '1px solid var(--sdn-border)',
        marginBottom: 20
      }}>
        <h3 style={{ margin: '0 0 6px', fontSize: 13, fontWeight: 700, color: 'var(--sdn-text-primary)' }}>
          How Webs of Trust Work
        </h3>
        <p style={{ margin: 0, fontSize: 12, color: 'var(--sdn-text-secondary)', lineHeight: 1.5 }}>
          Trust is established directly between peers (like PGP key signing) rather than through a central
          authority. When you trust a peer, you vouch for their identity. Trust can be transitive: if Alice
          trusts Bob, and Bob trusts Carol, Alice may extend limited trust to Carol. This creates a
          decentralized web where trust flows through verified connections.
        </p>
      </div>

      <div style={{ fontSize: 12, color: 'var(--sdn-text-secondary)', marginBottom: 20, lineHeight: 1.6 }}>
        <strong style={{ color: 'var(--sdn-text-primary)' }}>Learn more:</strong>
        <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
          <li>
            <a
              href='https://en.wikipedia.org/wiki/Web_of_trust'
              target='_blank'
              rel='noopener noreferrer'
              style={{ color: 'var(--sdn-accent)' }}
            >
              PGP Web of Trust — Wikipedia
            </a>
          </li>
          <li>
            <a
              href='https://www.gnupg.org/gph/en/manual/x547.html'
              target='_blank'
              rel='noopener noreferrer'
              style={{ color: 'var(--sdn-accent)' }}
            >
              GnuPG Trust Models
            </a>
          </li>
          <li>
            <a
              href='https://datatracker.ietf.org/doc/html/rfc4880#section-5.2.3.13'
              target='_blank'
              rel='noopener noreferrer'
              style={{ color: 'var(--sdn-accent)' }}
            >
              RFC 4880 — Trust Signature Subpacket
            </a>
          </li>
        </ul>
      </div>

      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <button
          onClick={onClose}
          style={{
            padding: '8px 16px',
            borderRadius: 8,
            border: '1px solid var(--sdn-border)',
            background: 'var(--sdn-bg-tertiary)',
            color: 'var(--sdn-text-primary)',
            fontWeight: 600,
            cursor: 'pointer',
            fontSize: 13
          }}
        >
          Close
        </button>
      </div>
    </div>
  </Overlay>
)

export default TrustLevelsModal
