import React from 'react'

const SdnLogo = ({ width = 40, className = '' }) => (
  <svg viewBox='0 0 100 100' width={width} className={className} style={{ color: '#58a6ff', transition: 'filter 0.2s ease' }} shapeRendering='geometricPrecision'>
    {/* Outer circle */}
    <circle cx='50' cy='50' r='45' fill='none' stroke='currentColor' strokeWidth='3'/>
    {/* Orbital ellipses */}
    <ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' strokeWidth='1.8'/>
    <ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' strokeWidth='1.8' transform='rotate(60 50 50)'/>
    <ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' strokeWidth='1.8' transform='rotate(120 50 50)'/>
    {/* Center node */}
    <circle cx='50' cy='50' r='7' fill='currentColor'/>
    {/* Orbit endpoint nodes - 0° */}
    <circle cx='5' cy='50' r='5' fill='currentColor'/>
    <circle cx='95' cy='50' r='5' fill='currentColor'/>
    {/* Orbit endpoint nodes - 60° */}
    <circle cx='27.5' cy='11.03' r='5' fill='currentColor'/>
    <circle cx='72.5' cy='88.97' r='5' fill='currentColor'/>
    {/* Orbit endpoint nodes - 120° */}
    <circle cx='72.5' cy='11.03' r='5' fill='currentColor'/>
    <circle cx='27.5' cy='88.97' r='5' fill='currentColor'/>
  </svg>
)

export default SdnLogo
