import React, { useCallback } from 'react'

const FORMAT_LABELS = {
  flatbuffers: 'FlatBuffers (.fbs)',
  typescript: 'TypeScript',
  go: 'Go',
  python: 'Python',
  rust: 'Rust'
}

const CodeView = ({ code, format, onDownload }) => {
  const copyCode = useCallback(() => {
    navigator.clipboard.writeText(code)
  }, [code])

  return (
    <div className='code-view'>
      <div className='code-view-header'>
        <span className='code-view-label'>{FORMAT_LABELS[format] || format}</span>
        <div className='code-view-actions'>
          <button type='button' className='code-view-btn' onClick={copyCode}>Copy</button>
          {onDownload && <button type='button' className='code-view-btn' onClick={onDownload}>Download</button>}
        </div>
      </div>
      <pre><code>{code}</code></pre>
    </div>
  )
}

export default CodeView
