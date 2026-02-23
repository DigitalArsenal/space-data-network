import React, { useState, useCallback } from 'react'

const FieldExplorer = ({ schema }) => {
  const [expanded, setExpanded] = useState({})

  const toggleField = useCallback((idx) => {
    setExpanded(prev => ({ ...prev, [idx]: !prev[idx] }))
  }, [])

  const expandAll = useCallback(() => {
    const all = {}
    schema.fields.forEach((_, i) => { all[i] = true })
    setExpanded(all)
  }, [schema])

  const collapseAll = useCallback(() => {
    setExpanded({})
  }, [])

  const copyFieldPath = useCallback((path) => {
    navigator.clipboard.writeText(path)
  }, [])

  return (
    <div>
      <div className='field-explorer-controls'>
        <button type='button' className='field-explorer-btn' onClick={expandAll}>Expand All</button>
        <button type='button' className='field-explorer-btn' onClick={collapseAll}>Collapse All</button>
      </div>
      {schema.fields.map((f, i) => (
        <div key={f.name} className={`field-item${expanded[i] ? ' expanded' : ''}`}>
          <div className='field-header' onClick={() => toggleField(i)} role='button' tabIndex={0} onKeyDown={e => { if (e.key === 'Enter') toggleField(i) }}>
            <svg className='field-toggle' viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='2'>
              <path d='m9 18 6-6-6-6' />
            </svg>
            <span className='field-name'>{f.name}</span>
            <span className='field-type'>{f.type}</span>
            {f.required && <span className='field-required-badge'>required</span>}
            <button
              type='button'
              className='field-path-btn'
              onClick={e => { e.stopPropagation(); copyFieldPath(`${schema.name}.${f.name}`) }}
              title='Copy field path'
            >
              {schema.name}.{f.name}
            </button>
          </div>
          <div className='field-details'>
            <div className='field-detail-row'>
              <span className='field-detail-label'>Description</span>
              <span className='field-detail-value'>{f.desc}</span>
            </div>
            <div className='field-detail-row'>
              <span className='field-detail-label'>x-flatbuffer-type</span>
              <span className='field-detail-value'>{f.fbType}</span>
            </div>
            <div className='field-detail-row'>
              <span className='field-detail-label'>x-flatbuffer-field-id</span>
              <span className='field-detail-value'>{f.fbId}</span>
            </div>
            {f.default && (
              <div className='field-detail-row'>
                <span className='field-detail-label'>x-flatbuffer-default</span>
                <span className='field-detail-value'>{f.default}</span>
              </div>
            )}
            {f.enum && (
              <div className='field-detail-row'>
                <span className='field-detail-label'>x-flatbuffer-enum</span>
                <span className='field-detail-value'>{f.enum}</span>
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}

export default FieldExplorer
