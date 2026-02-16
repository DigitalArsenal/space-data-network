import React, { useRef, useEffect, useCallback } from 'react'
import * as d3 from 'd3'
import './Graph.css'

const TRUST_COLORS = {
  admin: '#da3633',
  trusted: '#3fb950',
  standard: '#58a6ff',
  limited: '#d29922',
  untrusted: '#8b949e',
  '': '#8b949e'
}

const TreeGraph = ({ snapshot, onNodeClick, width = 800, height = 500 }) => {
  const svgRef = useRef(null)

  const draw = useCallback(() => {
    if (!snapshot || !svgRef.current) return

    const svg = d3.select(svgRef.current)
    svg.selectAll('*').remove()

    const localId = snapshot.local_peer_id
    const nodeMap = {}
    ;(snapshot.nodes || []).forEach(n => { nodeMap[n.peer_id] = n })

    // Build a hierarchy: local node → direct peers → relay peers
    const directPeers = new Set()
    const relayPeers = new Set()

    ;(snapshot.edges || []).forEach(e => {
      if (e.source_peer_id === localId) {
        if (e.connection_type === 'Relay') {
          relayPeers.add(e.target_peer_id)
        } else {
          directPeers.add(e.target_peer_id)
        }
      }
    })

    // Build tree data
    const root = {
      id: localId,
      data: nodeMap[localId] || { peer_id: localId, dn: 'Local Node' },
      children: []
    }

    // Add direct peers as L1
    for (const pid of directPeers) {
      const child = {
        id: pid,
        data: nodeMap[pid] || { peer_id: pid },
        children: []
      }
      root.children.push(child)
    }

    // Add relay peers as L2 under a "Relay" group
    if (relayPeers.size > 0) {
      const relayGroup = {
        id: '__relay__',
        data: { peer_id: '__relay__', dn: 'Relay Peers' },
        children: []
      }
      for (const pid of relayPeers) {
        relayGroup.children.push({
          id: pid,
          data: nodeMap[pid] || { peer_id: pid },
          children: []
        })
      }
      root.children.push(relayGroup)
    }

    // Add unconnected known peers
    const connectedPeers = new Set([localId, ...directPeers, ...relayPeers])
    const offlinePeers = (snapshot.nodes || []).filter(n =>
      !connectedPeers.has(n.peer_id) && n.peer_id !== localId
    )
    if (offlinePeers.length > 0) {
      const offlineGroup = {
        id: '__offline__',
        data: { peer_id: '__offline__', dn: 'Offline Peers' },
        children: offlinePeers.map(n => ({
          id: n.peer_id,
          data: n,
          children: []
        }))
      }
      root.children.push(offlineGroup)
    }

    // Compute tree layout
    const margin = { top: 30, right: 40, bottom: 30, left: 80 }
    const treeWidth = width - margin.left - margin.right
    const treeHeight = height - margin.top - margin.bottom

    const hierarchy = d3.hierarchy(root)
    const treeLayout = d3.tree().size([treeHeight, treeWidth])
    treeLayout(hierarchy)

    // Zoom container
    const g = svg.append('g')
      .attr('transform', `translate(${margin.left},${margin.top})`)

    const zoomG = svg.append('g')
    const zoom = d3.zoom()
      .scaleExtent([0.3, 4])
      .on('zoom', () => {
        g.attr('transform', d3.event.transform)
      })
    svg.call(zoom)
    svg.call(zoom.transform, d3.zoomIdentity.translate(margin.left, margin.top))
    zoomG.remove()

    // Links
    g.selectAll('.tree-link')
      .data(hierarchy.links())
      .enter()
      .append('path')
      .attr('class', 'tree-link')
      .attr('fill', 'none')
      .attr('stroke', 'rgba(88, 166, 255, 0.3)')
      .attr('stroke-width', 1.5)
      .attr('d', d3.linkHorizontal()
        .x(d => d.y)
        .y(d => d.x)
      )

    // Nodes
    const node = g.selectAll('.tree-node')
      .data(hierarchy.descendants())
      .enter()
      .append('g')
      .attr('class', 'tree-node')
      .attr('transform', d => `translate(${d.y},${d.x})`)

    node.append('circle')
      .attr('r', d => d.depth === 0 ? 10 : 7)
      .attr('fill', d => {
        if (d.depth === 0) return '#bc8cff'
        if (d.data.id.startsWith('__')) return '#444'
        return TRUST_COLORS[d.data.data?.trust_level || '']
      })
      .attr('stroke', d => d.depth === 0 ? '#fff' : 'rgba(255,255,255,0.2)')
      .attr('stroke-width', d => d.depth === 0 ? 2 : 1)
      .attr('opacity', d => {
        if (d.data.id.startsWith('__')) return 0.6
        return d.data.data?.is_online || d.depth === 0 ? 1 : 0.5
      })

    node.append('text')
      .attr('class', 'graph-label')
      .attr('dy', '0.35em')
      .attr('x', d => d.children ? -14 : 14)
      .attr('text-anchor', d => d.children ? 'end' : 'start')
      .text(d => {
        const dn = d.data.data?.dn
        const id = d.data.id
        if (id.startsWith('__')) return dn || id
        return dn || (id ? id.slice(0, 12) + '...' : '')
      })

    node.filter(d => !d.data.id.startsWith('__'))
      .on('click', d => {
        if (onNodeClick && d.data.data) onNodeClick(d.data.data)
      })
      .style('cursor', 'pointer')

    node.append('title')
      .text(d => {
        const data = d.data.data
        if (!data) return ''
        return `${data.dn || data.peer_id}\n${data.trust_level || ''}\n${data.is_online ? 'Online' : 'Offline'}`
      })
  }, [snapshot, width, height, onNodeClick])

  useEffect(() => {
    draw()
  }, [draw])

  return (
    <div className='graph-container'>
      <svg ref={svgRef} width={width} height={height} className='graph-svg' />
    </div>
  )
}

export default TreeGraph
