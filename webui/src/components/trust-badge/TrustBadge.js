import React from 'react'

const TRUST_COLORS = {
  admin: 'var(--sdn-accent-purple)',
  trusted: 'var(--sdn-accent-green)',
  standard: 'var(--sdn-accent)',
  limited: 'var(--sdn-accent-orange)',
  untrusted: 'var(--sdn-accent-red)'
}

const TrustBadge = ({ level, onClick, size = 'normal' }) => {
  const l = String(level || '').toLowerCase()
  if (!l) return null
  const color = TRUST_COLORS[l] || 'var(--sdn-accent-red)'
  const small = size === 'small'

  const style = {
    display: 'inline-flex',
    alignItems: 'center',
    padding: small ? '1px 7px' : '2px 8px',
    borderRadius: 999,
    border: `1px solid ${color}`,
    color,
    fontSize: small ? 10 : 11,
    fontWeight: 800,
    letterSpacing: '0.03em',
    textTransform: 'uppercase',
    background: 'rgba(0,0,0,0.12)',
    cursor: onClick ? 'pointer' : 'default'
  }

  if (onClick) {
    return (
      <button
        onClick={onClick}
        style={{ ...style, fontFamily: 'inherit' }}
        title={`Trust level: ${l}`}
      >
        {l}
      </button>
    )
  }

  return (
    <span style={style} title={`Trust level: ${l}`}>
      {l}
    </span>
  )
}

export default TrustBadge
