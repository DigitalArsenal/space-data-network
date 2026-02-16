import React, { useEffect, useRef } from 'react'
import { connect } from 'redux-bundler-react'
import ForceGraph from './ForceGraph.js'
import TreeGraph from './TreeGraph.js'
import GraphControls from './GraphControls.js'

const GraphTab = ({
  graphSnapshot,
  graphLoading,
  graphViewMode,
  graphAutoRefresh,
  doFetchGraph,
  doSetGraphView,
  doSetGraphAutoRefresh
}) => {
  const intervalRef = useRef(null)

  useEffect(() => {
    if (!graphSnapshot) doFetchGraph()
  }, [graphSnapshot, doFetchGraph])

  useEffect(() => {
    if (graphAutoRefresh) {
      intervalRef.current = setInterval(doFetchGraph, 30000)
    }
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current)
    }
  }, [graphAutoRefresh, doFetchGraph])

  const handleNodeClick = (node) => {
    // For now just log; PeerDetailModal integration can be added later
    console.log('Graph node clicked:', node)
  }

  return (
    <div>
      <GraphControls
        viewMode={graphViewMode}
        onViewModeChange={doSetGraphView}
        autoRefresh={graphAutoRefresh}
        onAutoRefreshChange={doSetGraphAutoRefresh}
        onRefresh={doFetchGraph}
        loading={graphLoading}
      />

      {graphLoading && !graphSnapshot && (
        <div style={{ color: 'var(--sdn-text-secondary)', padding: 20 }}>
          Loading peer graph...
        </div>
      )}

      {graphSnapshot && graphViewMode === 'force' && (
        <ForceGraph
          snapshot={graphSnapshot}
          onNodeClick={handleNodeClick}
        />
      )}

      {graphSnapshot && graphViewMode === 'tree' && (
        <TreeGraph
          snapshot={graphSnapshot}
          onNodeClick={handleNodeClick}
        />
      )}

      {!graphLoading && !graphSnapshot && (
        <div style={{ color: 'var(--sdn-text-secondary)', padding: 20, textAlign: 'center' }}>
          No graph data available. Click Refresh to load.
        </div>
      )}
    </div>
  )
}

export default connect(
  'selectGraphSnapshot',
  'selectGraphLoading',
  'selectGraphViewMode',
  'selectGraphAutoRefresh',
  'doFetchGraph',
  'doSetGraphView',
  'doSetGraphAutoRefresh',
  GraphTab
)
