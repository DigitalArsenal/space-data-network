import React, { useState, useEffect, useCallback } from 'react'
import { Helmet } from 'react-helmet'
import { connect } from 'redux-bundler-react'
import './PluginsPage.css'

/**
 * Fetches the plugin manifest from the server.
 * Each plugin can declare a `ui` object with:
 *   - ui.url:   path to an HTML page served by the plugin (rendered in iframe)
 *   - ui.title: display name shown in the card
 *   - ui.description: short description
 *   - ui.icon:  emoji or single character for the card icon
 *   - ui.color: CSS background color for the icon badge
 */
async function fetchPluginManifest (apiUrl) {
  try {
    const res = await fetch(`${apiUrl}/api/v1/plugins/manifest`)
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
    <div className='plugin-card' onClick={() => onSelect(plugin)} role='button' tabIndex={0}>
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

const PluginsPage = ({ ipfsApiUrl }) => {
  const [plugins, setPlugins] = useState(null)
  const [selected, setSelected] = useState(null)

  const apiUrl = ipfsApiUrl || ''

  useEffect(() => {
    fetchPluginManifest(apiUrl).then(setPlugins)
  }, [apiUrl])

  const handleSelect = useCallback((plugin) => {
    if (plugin.ui?.url) {
      setSelected(plugin)
    }
  }, [])

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
        <h1>Plugins</h1>
        <p className='plugins-header-sub'>
          Installed WASI plugins running on this node
        </p>
      </div>

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
  'selectIpfsApiUrl',
  PluginsPage
)
