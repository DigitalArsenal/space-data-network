import React, { useEffect, useRef, useState } from 'react'
import { createUiRuntimeAdapter } from '../../../../src/ui/runtime/server-adapter.js'

function IdentityPage() {
  const runtimeRef = useRef(null)
  const [status, setStatus] = useState('loading')
  const [nodeInfo, setNodeInfo] = useState({
    displayName: 'Loading node identity...',
    peerId: null,
    transport: 'helia',
    descriptorUrl: null,
  })
  const [snapshot, setSnapshot] = useState({ query: '', nodes: [], users: [] })
  const [error, setError] = useState(null)

  if (!runtimeRef.current) {
    runtimeRef.current = createUiRuntimeAdapter({
      config: window.__SDN_CONFIG__ ?? null,
      listDirectoryRecords: resolveLocalDirectoryRecords,
    })
  }

  useEffect(() => {
    let cancelled = false

    async function loadIdentity() {
      setStatus('loading')
      setError(null)

      try {
        const connected = await runtimeRef.current.connect()
        const nextNodeInfo = connected.nodeContext
        const query = nextNodeInfo.peerId || nextNodeInfo.displayName || ''
        const directorySnapshot = await runtimeRef.current.directory.search(query)

        if (!cancelled) {
          setNodeInfo(nextNodeInfo)
          setSnapshot(directorySnapshot)
          setStatus('ready')
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setStatus('error')
        }
      }
    }

    loadIdentity()

    return () => {
      cancelled = true
    }
  }, [])

  const nodeMatch = snapshot.nodes[0] ?? null
  const userMatch = snapshot.users[0] ?? null

  return (
    <main className='measure-wide center ph3 ph4-l pv3'>
      <header className='mb3'>
        <h1 className='f2 f1-l mv0'>Identity</h1>
        <p className='mt2 mb0 f4 lh-copy black-70'>
          Node identity, EPM evidence, and directory matches from the {runtimeRef.current.mode} runtime adapter.
        </p>
      </header>

      <section className='pa3 ba b--black-10 br2 bg-white mb3'>
        <h2 className='f4 mt0 mb3'>Node profile</h2>
        <dl className='mv0'>
          <div className='mb2'>
            <dt className='f6 ttu tracked black-60'>Display name</dt>
            <dd className='mv1 ml0'>{nodeInfo.displayName}</dd>
          </div>
          <div className='mb2'>
            <dt className='f6 ttu tracked black-60'>Peer ID</dt>
            <dd className='mv1 ml0'>{nodeInfo.peerId ?? 'Unavailable'}</dd>
          </div>
          <div className='mb2'>
            <dt className='f6 ttu tracked black-60'>Transport</dt>
            <dd className='mv1 ml0'>{nodeInfo.transport}</dd>
          </div>
          <div>
            <dt className='f6 ttu tracked black-60'>Descriptor URL</dt>
            <dd className='mv1 ml0 break-word'>{nodeInfo.descriptorUrl ?? 'Unavailable'}</dd>
          </div>
        </dl>
        <div className='f6 black-60 mt3'>
          {status === 'loading' ? 'Loading node identity...' : 'Node identity loaded from the shared runtime adapter.'}
        </div>
        {error && <div className='mt2 dark-red'>{error}</div>}
      </section>

      <section className='pa3 ba b--black-10 br2 bg-white mb3'>
        <h2 className='f4 mt0 mb3'>Matched directory node</h2>
        {nodeMatch ? (
          <dl className='mv0'>
            <div className='mb2'>
              <dt className='f6 ttu tracked black-60'>Directory name</dt>
              <dd className='mv1 ml0'>{nodeMatch.dn ?? nodeMatch.legal_name ?? 'Unknown node'}</dd>
            </div>
            <div className='mb2'>
              <dt className='f6 ttu tracked black-60'>Bitcoin address</dt>
              <dd className='mv1 ml0'>{nodeMatch.bitcoin_address ?? 'Unavailable'}</dd>
            </div>
            <div className='mb2'>
              <dt className='f6 ttu tracked black-60'>EPM CID</dt>
              <dd className='mv1 ml0 break-word'>{nodeMatch.epm_cid ?? 'Unavailable'}</dd>
            </div>
            <div>
              <dt className='f6 ttu tracked black-60'>Source</dt>
              <dd className='mv1 ml0'>{nodeMatch.source ?? 'Unknown'}</dd>
            </div>
          </dl>
        ) : (
          <p className='mv0 black-60'>No directory node match was found for this peer.</p>
        )}
      </section>

      <section className='pa3 ba b--black-10 br2 bg-white'>
        <h2 className='f4 mt0 mb3'>Matched directory user</h2>
        {userMatch ? (
          <dl className='mv0'>
            <div className='mb2'>
              <dt className='f6 ttu tracked black-60'>Directory name</dt>
              <dd className='mv1 ml0'>{userMatch.dn ?? 'Unknown user'}</dd>
            </div>
            <div className='mb2'>
              <dt className='f6 ttu tracked black-60'>Legal name</dt>
              <dd className='mv1 ml0'>{userMatch.legal_name ?? 'Unavailable'}</dd>
            </div>
            <div>
              <dt className='f6 ttu tracked black-60'>Peer ID</dt>
              <dd className='mv1 ml0'>{userMatch.peer_id || 'Unavailable'}</dd>
            </div>
          </dl>
        ) : (
          <p className='mv0 black-60'>No directory user match was found for this identity.</p>
        )}
      </section>
    </main>
  )
}

function resolveLocalDirectoryRecords() {
  const source = window.__SDN_DIRECTORY__
  if (!source) {
    return Promise.resolve([])
  }
  if (Array.isArray(source.records)) {
    return Promise.resolve(source.records)
  }
  if (typeof source.listDirectoryRecords === 'function') {
    return Promise.resolve(source.listDirectoryRecords())
  }
  return Promise.resolve([])
}

export default IdentityPage
