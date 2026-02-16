import React from 'react'
import Overlay from '../overlay/overlay.tsx'

const SignOutModal = ({ show, onCancel, onConfirm }) => (
  <Overlay show={show} onLeave={onCancel} hidden={false}>
    <div
      className='sdn-modal'
      style={{
        position: 'fixed',
        top: '50%',
        left: '50%',
        transform: 'translate(-50%, -50%)',
        maxWidth: '24em',
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
      <h2 style={{ margin: '0 0 8px', fontSize: 18, fontWeight: 700 }}>Sign Out</h2>
      <p style={{ margin: '0 0 24px', fontSize: 14, color: 'var(--sdn-text-secondary)', lineHeight: 1.5 }}>
        Are you sure you want to sign out of this node?
      </p>

      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
        <button
          onClick={onCancel}
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
          Cancel
        </button>
        <button
          onClick={onConfirm}
          style={{
            padding: '8px 16px',
            borderRadius: 8,
            border: '1px solid rgba(248, 81, 73, 0.6)',
            background: 'rgba(248, 81, 73, 0.15)',
            color: 'var(--sdn-accent-red)',
            fontWeight: 700,
            cursor: 'pointer',
            fontSize: 13
          }}
        >
          Sign Out
        </button>
      </div>
    </div>
  </Overlay>
)

export default SignOutModal
