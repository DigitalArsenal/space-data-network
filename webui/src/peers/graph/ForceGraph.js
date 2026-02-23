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

const NODE_SIZE = {
  local: 12,
  connected: 8,
  offline: 6
}

const ForceGraph = ({ snapshot, onNodeClick, width = 800, height = 500 }) => {
  const svgRef = useRef(null)
  const simRef = useRef(null)

  const draw = useCallback(() => {
    if (!snapshot || !svgRef.current) return

    const svg = d3.select(svgRef.current)
    svg.selectAll('*').remove()

    const nodes = (snapshot.nodes || []).map(n => ({
      ...n,
      id: n.peer_id,
      isLocal: n.peer_id === snapshot.local_peer_id
    }))

    const nodeMap = {}
    nodes.forEach(n => { nodeMap[n.id] = n })

    const links = (snapshot.edges || []).filter(e =>
      nodeMap[e.source_peer_id] && nodeMap[e.target_peer_id]
    ).map(e => ({
      ...e,
      source: e.source_peer_id,
      target: e.target_peer_id
    }))

    // Zoom container
    const g = svg.append('g')

    const zoom = d3.zoom()
      .scaleExtent([0.2, 5])
      .on('zoom', () => {
        g.attr('transform', d3.event.transform)
      })
    svg.call(zoom)

    // Simulation
    const sim = d3.forceSimulation(nodes)
      .force('link', d3.forceLink(links).id(d => d.id).distance(80))
      .force('charge', d3.forceManyBody().strength(-200))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .force('collision', d3.forceCollide().radius(20))

    simRef.current = sim

    // Links
    const link = g.append('g')
      .selectAll('line')
      .data(links)
      .enter()
      .append('line')
      .attr('class', 'graph-link')
      .attr('stroke', d => d.connection_type === 'Relay' ? '#d29922' : 'rgba(88, 166, 255, 0.3)')
      .attr('stroke-width', d => d.connection_type === 'Relay' ? 1 : 1.5)
      .attr('stroke-dasharray', d => d.connection_type === 'Relay' ? '4,4' : null)

    // Nodes
    const node = g.append('g')
      .selectAll('g')
      .data(nodes)
      .enter()
      .append('g')
      .attr('class', 'graph-node')
      .call(d3.drag()
        .on('start', d => {
          if (!d3.event.active) sim.alphaTarget(0.3).restart()
          d.fx = d.x
          d.fy = d.y
        })
        .on('drag', d => {
          d.fx = d3.event.x
          d.fy = d3.event.y
        })
        .on('end', d => {
          if (!d3.event.active) sim.alphaTarget(0)
          d.fx = null
          d.fy = null
        })
      )

    node.append('circle')
      .attr('r', d => d.isLocal ? NODE_SIZE.local : d.is_online ? NODE_SIZE.connected : NODE_SIZE.offline)
      .attr('fill', d => d.isLocal ? '#bc8cff' : TRUST_COLORS[d.trust_level || ''])
      .attr('stroke', d => d.isLocal ? '#fff' : 'rgba(255,255,255,0.2)')
      .attr('stroke-width', d => d.isLocal ? 2 : 1)
      .attr('opacity', d => d.is_online || d.isLocal ? 1 : 0.5)

    node.append('text')
      .attr('class', 'graph-label')
      .attr('dy', d => (d.isLocal ? NODE_SIZE.local : NODE_SIZE.connected) + 14)
      .attr('text-anchor', 'middle')
      .text(d => d.dn || (d.id ? d.id.slice(0, 8) + '...' : ''))

    node.on('click', d => {
      if (onNodeClick) onNodeClick(d)
    })

    node.append('title')
      .text(d => `${d.dn || d.id}\n${d.trust_level || ''}\n${d.is_online ? 'Online' : 'Offline'}`)

    sim.on('tick', () => {
      link
        .attr('x1', d => d.source.x)
        .attr('y1', d => d.source.y)
        .attr('x2', d => d.target.x)
        .attr('y2', d => d.target.y)

      node.attr('transform', d => `translate(${d.x},${d.y})`)
    })
  }, [snapshot, width, height, onNodeClick])

  useEffect(() => {
    draw()
    return () => {
      if (simRef.current) simRef.current.stop()
    }
  }, [draw])

  return (
    <div className='graph-container'>
      <svg ref={svgRef} width={width} height={height} className='graph-svg' />
    </div>
  )
}

export default ForceGraph
