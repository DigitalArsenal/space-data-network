import React, { useEffect, useRef, useState } from 'react'
import { createHeliaDirectoryAdapter } from '../../../../src/ui/runtime/helia-directory.js'
import { createServerDirectoryAdapter } from '../../../../src/ui/runtime/server-directory.js'

function createUiDirectoryAdapter() {
  const config = window.__SDN_CONFIG__ ?? {}
  const serverBaseUrl = typeof config.serverBaseUrl === 'string' && config.serverBaseUrl.trim()
    ? config.serverBaseUrl.trim()
    : null

  if (serverBaseUrl) {
    return createServerDirectoryAdapter({
      baseUrl: serverBaseUrl,
      fetch: window.fetch.bind(window),
    })
  }

  if (window.__SDN_DIRECTORY__?.listDirectoryRecords || Array.isArray(window.__SDN_DIRECTORY__?.records)) {
    return createHeliaDirectoryAdapter({
      listDirectoryRecords: async () => await normalizeLocalDirectoryRecords(window.__SDN_DIRECTORY__),
    })
  }

  return createServerDirectoryAdapter({
    baseUrl: window.location.origin,
    fetch: window.fetch.bind(window),
  })
}

function normalizeLocalDirectoryRecords(source) {
  if (!source) {
    return []
  }

  if (Array.isArray(source.records)) {
    return source.records
  }

  if (typeof source.listDirectoryRecords === 'function') {
    return source.listDirectoryRecords()
  }

  return []
}

function DirectoryPage() {
  const adapterRef = useRef(null)
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('loading')
  const [snapshot, setSnapshot] = useState({ query: '', nodes: [], users: [] })
  const [error, setError] = useState(null)

  if (!adapterRef.current) {
    adapterRef.current = createUiDirectoryAdapter()
  }

  useEffect(() => {
    let cancelled = false

    async function loadDirectory() {
      setStatus('loading')
      setError(null)

      try {
        const nextSnapshot = await adapterRef.current.search(query)
        if (!cancelled) {
          setSnapshot(nextSnapshot)
          setStatus('ready')
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setStatus('error')
        }
      }
    }

    loadDirectory()

    return () => {
      cancelled = true
    }
  }, [query])

  return (
    <main className='measure-wide center ph3 ph4-l pv3'>
      <header className='mb3'>
        <h1 className='f2 f1-l mv0'>Directory</h1>
        <p className='mt2 mb0 f4 lh-copy black-70'>
          Shared directory results from the {adapterRef.current.mode} runtime adapter.
        </p>
      </header>

      <section className='pa3 ba b--black-10 br2 bg-white mb3'>
        <label className='db mb2'>
          <span className='db f6 ttu tracked black-60 mb1'>Search</span>
          <input
            className='input-reset ba b--black-20 pa2 br2 w-100'
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder='Search peer id, name, address, or CID'
          />
        </label>
        <div className='f6 black-60'>
          {status === 'loading' ? 'Loading directory records...' : `${snapshot.nodes.length} nodes and ${snapshot.users.length} users`}
        </div>
        {error && <div className='mt2 dark-red'>{error}</div>}
      </section>

      <section className='pa3 ba b--black-10 br2 bg-white mb3'>
        <h2 className='f4 mt0 mb3'>Nodes</h2>
        {snapshot.nodes.length ? (
          <div className='overflow-auto'>
            <table className='collapse w-100 f6'>
              <thead>
                <tr className='black-60 tl'>
                  <th className='bb b--black-10 pb2 pr3'>Peer ID</th>
                  <th className='bb b--black-10 pb2 pr3'>Name</th>
                  <th className='bb b--black-10 pb2 pr3'>Bitcoin</th>
                  <th className='bb b--black-10 pb2 pr3'>EPM CID</th>
                </tr>
              </thead>
              <tbody>
                {snapshot.nodes.map((record) => (
                  <tr key={`node-${record.peer_id}-${record.epm_cid ?? record.bitcoin_address ?? record.dn ?? ''}`}>
                    <td className='bt b--black-10 pt2 pr3'>{record.peer_id}</td>
                    <td className='bt b--black-10 pt2 pr3'>{record.dn ?? record.legal_name ?? 'Unknown node'}</td>
                    <td className='bt b--black-10 pt2 pr3'>{record.bitcoin_address ?? '—'}</td>
                    <td className='bt b--black-10 pt2 pr3'>{record.epm_cid ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className='mv0 black-60'>No node records matched this query.</p>
        )}
      </section>

      <section className='pa3 ba b--black-10 br2 bg-white'>
        <h2 className='f4 mt0 mb3'>Users</h2>
        {snapshot.users.length ? (
          <div className='overflow-auto'>
            <table className='collapse w-100 f6'>
              <thead>
                <tr className='black-60 tl'>
                  <th className='bb b--black-10 pb2 pr3'>Peer ID</th>
                  <th className='bb b--black-10 pb2 pr3'>Directory name</th>
                  <th className='bb b--black-10 pb2 pr3'>Legal name</th>
                </tr>
              </thead>
              <tbody>
                {snapshot.users.map((record) => (
                  <tr key={`user-${record.peer_id || record.dn || record.legal_name || 'unknown'}`}>
                    <td className='bt b--black-10 pt2 pr3'>{record.peer_id || '—'}</td>
                    <td className='bt b--black-10 pt2 pr3'>{record.dn ?? '—'}</td>
                    <td className='bt b--black-10 pt2 pr3'>{record.legal_name ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className='mv0 black-60'>No user records matched this query.</p>
        )}
      </section>
    </main>
  )
}

export default DirectoryPage
