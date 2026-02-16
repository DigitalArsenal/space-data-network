import React, { useState, useEffect, useCallback } from 'react'
import { Helmet } from 'react-helmet'
import { connect } from 'redux-bundler-react'
import './PluginsPage.css'

const bytesToHex = (bytes) => Array.from(bytes || [])
  .map(b => b.toString(16).padStart(2, '0'))
  .join('')

/**
 * Fetches the plugin manifest from the server.
 * Each plugin can declare a `ui` object with:
 *   - ui.url:   path to an HTML page served by the plugin (rendered in iframe)
 *   - ui.title: display name shown in the card
 *   - ui.description: short description
 *   - ui.icon:  emoji or single character for the card icon
 *   - ui.color: CSS background color for the icon badge
 */
async function fetchPluginManifest () {
  try {
    const res = await fetch(`${window.location.origin}/api/v1/plugins/manifest`)
    if (!res.ok) return []
    const data = await res.json()
    return Array.isArray(data) ? data : (data.plugins || [])
  } catch {
    return []
  }
}

const PluginCard = ({ plugin, onSelect }) => {
  const ui = plugin.ui || {}
  const status = plugin.status || 'running'
  return (
    <div
      className='plugin-card'
      onClick={() => onSelect(plugin)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect(plugin)
        }
      }}
      role='button'
      tabIndex={0}
    >
      <div className='plugin-card-header'>
        <div
          className='plugin-card-icon'
          style={{ background: ui.color || '#e0f2fe', color: ui.textColor || '#0369a1' }}
        >
          {ui.icon || plugin.id?.charAt(0)?.toUpperCase() || '?'}
        </div>
        <div>
          <span className='plugin-card-title'>{ui.title || plugin.id}</span>
          {plugin.version && <span className='plugin-card-version'>v{plugin.version}</span>}
        </div>
      </div>
      <div className='plugin-card-description'>
        {ui.description || plugin.description || 'No description available.'}
      </div>
      <div className='plugin-card-status'>
        <span className={`plugin-status-dot ${status}`} />
        {status === 'running' ? 'Running' : status === 'error' ? 'Error' : 'Stopped'}
        {ui.url && <span style={{ marginLeft: 'auto', color: 'var(--color-aqua)', fontSize: '0.8rem' }}>Open UI &rarr;</span>}
      </div>
    </div>
  )
}

const PluginDetail = ({ plugin, apiUrl, onBack }) => {
  const ui = plugin.ui || {}
  const uiUrl = ui.url
    ? (ui.url.startsWith('http') ? ui.url : `${apiUrl}${ui.url}`)
    : null

  return (
    <div className='plugin-detail'>
      <button className='plugin-detail-back' onClick={onBack}>
        &larr; Back to plugins
      </button>
      <div className='plugin-card-header' style={{ marginBottom: 16 }}>
        <div
          className='plugin-card-icon'
          style={{ background: ui.color || '#e0f2fe', color: ui.textColor || '#0369a1' }}
        >
          {ui.icon || plugin.id?.charAt(0)?.toUpperCase() || '?'}
        </div>
        <div>
          <span className='plugin-card-title' style={{ fontSize: '1.25rem' }}>
            {ui.title || plugin.id}
          </span>
          {plugin.version && <span className='plugin-card-version'>v{plugin.version}</span>}
        </div>
      </div>
      {uiUrl
        ? (
          <iframe
            className='plugin-ui-frame'
            src={uiUrl}
            title={ui.title || plugin.id}
            sandbox='allow-scripts allow-same-origin allow-forms allow-popups'
          />
          )
        : (
          <div className='plugins-empty'>
            <h3>No UI available</h3>
            <p>This plugin does not provide a web interface.</p>
          </div>
          )}
    </div>
  )
}

const PluginsPage = ({ ipfsApiAddress, isAdminUser, walletIdentity }) => {
  const [plugins, setPlugins] = useState(null)
  const [selected, setSelected] = useState(null)
  const [showUpload, setShowUpload] = useState(false)
  const [uploadFile, setUploadFile] = useState(null)
  const [uploadMeta, setUploadMeta] = useState({ id: '', version: '1.0.0' })
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState(null)
  const [uploadSuccess, setUploadSuccess] = useState(null)

  const apiUrl = ipfsApiAddress || ''
  const canUpload = isAdminUser && walletIdentity && typeof walletIdentity.sign === 'function'

  useEffect(() => {
    fetchPluginManifest().then(setPlugins)
  }, [])

  const handleSelect = useCallback((plugin) => {
    if (plugin.ui?.url) {
      setSelected(plugin)
    }
  }, [])

  const handleUpload = useCallback(async () => {
    if (!uploadFile || !walletIdentity || !uploadMeta.id) return
    setUploading(true)
    setUploadError(null)
    setUploadSuccess(null)
    try {
      const buffer = await uploadFile.arrayBuffer()
      const hashBuffer = await crypto.subtle.digest('SHA-256', buffer)
      const hashBytes = new Uint8Array(hashBuffer)
      const signature = await walletIdentity.sign(hashBytes)
      const sigHex = bytesToHex(signature)

      const form = new FormData()
      form.append('bundle', uploadFile)
      form.append('metadata', JSON.stringify({ id: uploadMeta.id.trim(), version: uploadMeta.version.trim() }))
      form.append('signature_hex', sigHex)

      const res = await fetch('/api/v1/plugins/upload', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'X-Requested-With': 'XMLHttpRequest' },
        body: form
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.message || 'Upload failed')

      setUploadSuccess(`Plugin "${data.plugin_id}" v${data.version} uploaded (${data.bundle_sha256.slice(0, 12)}...)`)
      setUploadFile(null)
      setUploadMeta({ id: '', version: '1.0.0' })
      fetchPluginManifest().then(setPlugins)
    } catch (err) {
      setUploadError(err.message || 'Upload failed')
    } finally {
      setUploading(false)
    }
  }, [uploadFile, uploadMeta, walletIdentity])

  if (selected) {
    return (
      <div className='plugins-page' data-id='PluginsPage'>
        <Helmet>
          <title>{selected.ui?.title || selected.id} | SDN</title>
        </Helmet>
        <PluginDetail plugin={selected} apiUrl={apiUrl} onBack={() => setSelected(null)} />
      </div>
    )
  }

  return (
    <div className='plugins-page' data-id='PluginsPage'>
      <Helmet>
        <title>Plugins | SDN</title>
      </Helmet>
      <div className='plugins-header'>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <h1>Plugins</h1>
          {canUpload && (
            <button className='plugin-upload-btn' onClick={() => setShowUpload(!showUpload)}>
              {showUpload ? 'Cancel' : 'Upload Plugin'}
            </button>
          )}
        </div>
        <p className='plugins-header-sub'>
          Installed WASI plugins running on this node
        </p>
      </div>

      {showUpload && canUpload && (
        <div className='plugin-upload-panel'>
          <h3>Upload WASM Plugin</h3>
          <p>The plugin binary will be signed with your wallet key before upload.</p>
          <input
            type='file'
            accept='.wasm'
            onChange={e => setUploadFile(e.target.files[0] || null)}
          />
          <div className='plugin-upload-fields'>
            <input
              type='text'
              placeholder='Plugin ID (e.g. my-plugin)'
              value={uploadMeta.id}
              onChange={e => setUploadMeta({ ...uploadMeta, id: e.target.value })}
            />
            <input
              type='text'
              placeholder='Version (e.g. 1.0.0)'
              value={uploadMeta.version}
              onChange={e => setUploadMeta({ ...uploadMeta, version: e.target.value })}
            />
          </div>
          <button
            className='plugin-upload-submit'
            disabled={!uploadFile || !uploadMeta.id.trim() || uploading}
            onClick={handleUpload}
          >
            {uploading ? 'Signing & Uploading...' : 'Sign & Upload'}
          </button>
          {uploadError && <div className='plugin-upload-error'>{uploadError}</div>}
          {uploadSuccess && <div className='plugin-upload-success'>{uploadSuccess}</div>}
        </div>
      )}

      {isAdminUser && !walletIdentity && (
        <div className='plugin-upload-hint'>
          To upload plugins, sign out and sign back in with your wallet — the signing key is only available during the login session (not after a page refresh).
        </div>
      )}

      {plugins === null
        ? (
          <div className='plugins-loading'>Loading plugins...</div>
          )
        : plugins.length === 0
          ? (
            <div className='plugins-empty'>
              <h3>No plugins installed</h3>
              <p>
                Plugins extend the SDN server with custom functionality.
                See the <a href='https://digitalarsenal.github.io/sdn-plugin-template/' target='_blank' rel='noopener noreferrer'>Plugin SDK docs</a> to build your own.
              </p>
            </div>
            )
          : (
            <div className='plugins-grid'>
              {plugins.map(plugin => (
                <PluginCard
                  key={plugin.id}
                  plugin={plugin}
                  onSelect={handleSelect}
                />
              ))}
            </div>
            )}
    </div>
  )
}

export default connect(
  'selectIpfsApiAddress',
  'selectIsAdminUser',
  'selectWalletIdentity',
  PluginsPage
)
