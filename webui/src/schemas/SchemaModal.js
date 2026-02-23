import React, { useState, useEffect, useCallback } from 'react'
import { SCHEMA_CATEGORIES, generateCode, generateJsonSchema, generateFbs, generateTS, generateGo, generatePython, generateRust, downloadContent } from './schema-data.js'
import FieldExplorer from './FieldExplorer.js'
import CodeView from './CodeView.js'

const FORMATS = [
  { key: 'json-schema', label: 'JSON Schema' },
  { key: 'flatbuffers', label: 'FlatBuffers' },
  { key: 'typescript', label: 'TypeScript' },
  { key: 'go', label: 'Go' },
  { key: 'python', label: 'Python' },
  { key: 'rust', label: 'Rust' }
]

const DOWNLOAD_MAP = {
  'json-schema': { ext: 'schema.json', mime: 'application/json', gen: (s) => JSON.stringify(generateJsonSchema(s), null, 2) },
  flatbuffers: { ext: 'fbs', mime: 'text/plain', gen: generateFbs },
  typescript: { ext: 'ts', mime: 'text/typescript', gen: generateTS },
  go: { ext: 'go', mime: 'text/plain', gen: generateGo },
  python: { ext: 'py', mime: 'text/python', gen: generatePython },
  rust: { ext: 'rs', mime: 'text/plain', gen: generateRust }
}

const SchemaModal = ({ schema, onClose }) => {
  const [format, setFormat] = useState('json-schema')

  useEffect(() => {
    const handleKey = (e) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [onClose])

  const handleOverlayClick = useCallback((e) => {
    if (e.target === e.currentTarget) onClose()
  }, [onClose])

  const handleDownload = useCallback(() => {
    const dl = DOWNLOAD_MAP[format]
    if (!dl) return
    const content = dl.gen(schema)
    downloadContent(content, `${schema.name}.${dl.ext}`, dl.mime)
  }, [format, schema])

  const cat = SCHEMA_CATEGORIES[schema.category]
  const code = format !== 'json-schema' ? generateCode(schema, format) : ''

  return (
    <div className='schema-modal-overlay' onClick={handleOverlayClick} role='presentation'>
      <div className='schema-modal'>
        <div className='schema-modal-header'>
          <div>
            <div className='schema-modal-title'>{schema.name}</div>
            <div className='schema-modal-subtitle'>{schema.fullName}</div>
            <div className='schema-modal-meta'>
              <span className={`schema-category-badge ${schema.category}`}>
                {cat ? cat.label : schema.category}
              </span>
              <span className='schema-modal-version'>v{schema.version}</span>
            </div>
          </div>
          <button type='button' className='schema-modal-close' onClick={onClose} aria-label='Close'>&times;</button>
        </div>
        <p className='schema-modal-desc'>{schema.description}</p>
        <div className='schema-tabs'>
          {FORMATS.map(f => (
            <button
              key={f.key}
              type='button'
              className={`schema-tab${format === f.key ? ' active' : ''}`}
              onClick={() => setFormat(f.key)}
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className='schema-modal-body'>
          {format === 'json-schema'
            ? <FieldExplorer schema={schema} />
            : <CodeView code={code} format={format} onDownload={handleDownload} />
          }
        </div>
      </div>
    </div>
  )
}

export default SchemaModal
