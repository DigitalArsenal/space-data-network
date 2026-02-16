import React from 'react'
import './Graph.css'

const GraphControls = ({
  viewMode,
  onViewModeChange,
  autoRefresh,
  onAutoRefreshChange,
  onRefresh,
  loading
}) => {
  return (
    <div className='graph-controls'>
      <div className='graph-controls-group'>
        <button
          className={`graph-ctrl-btn ${viewMode === 'force' ? 'active' : ''}`}
          onClick={() => onViewModeChange('force')}
        >
          Force
        </button>
        <button
          className={`graph-ctrl-btn ${viewMode === 'tree' ? 'active' : ''}`}
          onClick={() => onViewModeChange('tree')}
        >
          Tree
        </button>
      </div>

      <div className='graph-controls-group'>
        <button
          className='graph-ctrl-btn'
          onClick={onRefresh}
          disabled={loading}
        >
          {loading ? 'Loading...' : 'Refresh'}
        </button>
        <label className='graph-ctrl-label'>
          <input
            type='checkbox'
            checked={autoRefresh}
            onChange={e => onAutoRefreshChange(e.target.checked)}
          />
          Auto (30s)
        </label>
      </div>

      <div className='graph-legend'>
        <span className='graph-legend-item'>
          <span className='graph-legend-dot' style={{ background: '#bc8cff' }} />
          Local
        </span>
        <span className='graph-legend-item'>
          <span className='graph-legend-dot' style={{ background: '#3fb950' }} />
          Trusted
        </span>
        <span className='graph-legend-item'>
          <span className='graph-legend-dot' style={{ background: '#58a6ff' }} />
          Standard
        </span>
        <span className='graph-legend-item'>
          <span className='graph-legend-dot' style={{ background: '#d29922' }} />
          Limited
        </span>
        <span className='graph-legend-item'>
          <span className='graph-legend-dot' style={{ background: '#8b949e' }} />
          Unknown
        </span>
      </div>
    </div>
  )
}

export default GraphControls
