import React from 'react'

const SdnLogo = ({ width = 40, className = '' }) => (
  <svg viewBox='0 0 100 100' width={width} className={className} style={{ color: '#58a6ff', transition: 'filter 0.2s ease' }}>
    <circle cx='50' cy='50' r='45' fill='none' stroke='currentColor' strokeWidth='4'/>
    <ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' strokeWidth='2'/>
    <ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' strokeWidth='2' transform='rotate(60 50 50)'/>
    <ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' strokeWidth='2' transform='rotate(120 50 50)'/>
    <circle cx='50' cy='50' r='8' fill='currentColor'/>
  </svg>
)

export default SdnLogo
