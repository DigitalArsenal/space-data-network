import React, { useEffect, useMemo, useRef, useState } from 'react'
import Button from '../../../../../../webui/src/components/button/button.tsx'
import { getSharedUiRuntimeAdapter } from '../../../../../src/ui/runtime/server-adapter.js'

const directorySortKeys = {
  type: 'type',
  name: 'name',
  peerId: 'peerId',
  bitcoin: 'bitcoin',
  epmCid: 'epmCid',
  source: 'source',
}

function DirectoryPage() {
  const runtimeRef = useRef(null)
  const importFileRef = useRef(null)
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('loading')
  const [snapshot, setSnapshot] = useState({ query: '', nodes: [], users: [] })
  const [error, setError] = useState(null)
  const [importStatus, setImportStatus] = useState('idle')
  const [importError, setImportError] = useState(null)
  const [sortKey, setSortKey] = useState(directorySortKeys.name)
  const [sortDirection, setSortDirection] = useState('asc')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(25)

  if (!runtimeRef.current) {
    runtimeRef.current = getSharedUiRuntimeAdapter()
  }

  useEffect(() => {
    let cancelled = false

    async function loadDirectory() {
      setStatus('loading')
      setError(null)

      try {
        const nextSnapshot = await runtimeRef.current.directory.search(query)
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

  useEffect(() => {
    setPage(1)
  }, [query, sortKey, sortDirection, pageSize])

  const directoryRecords = useMemo(() => {
    return [
      ...snapshot.nodes.map((record) => directoryRecordRow(record, 'node')),
      ...snapshot.users.map((record) => directoryRecordRow(record, 'user')),
    ]
  }, [snapshot])

  const sortedRecords = useMemo(() => {
    const direction = sortDirection === 'asc' ? 1 : -1
    return [...directoryRecords].sort((left, right) => {
      return compareDirectoryValues(left[sortKey], right[sortKey]) * direction
    })
  }, [directoryRecords, sortDirection, sortKey])

  const totalPages = Math.max(1, Math.ceil(sortedRecords.length / pageSize))
  const currentPage = Math.min(page, totalPages)
  const pageStart = (currentPage - 1) * pageSize
  const visibleRecords = sortedRecords.slice(pageStart, pageStart + pageSize)
  const pageEnd = pageStart + visibleRecords.length

  function handleSort(nextSortKey) {
    if (sortKey === nextSortKey) {
      setSortDirection(sortDirection === 'asc' ? 'desc' : 'asc')
      return
    }
    setSortKey(nextSortKey)
    setSortDirection('asc')
  }

  async function handleImportFile(event) {
    const file = event.target.files?.[0]
    if (!file) {
      return
    }

    setImportStatus('loading')
    setImportError(null)

    try {
      const text = await file.text()
      const request = directoryImportRequestFromText(text)
      const result = await runtimeRef.current.directory.importRecord(request)
      const nextSnapshot = await runtimeRef.current.directory.search(query)
      setSnapshot(nextSnapshot)
      setStatus('ready')
      setImportStatus(`Imported ${result.imported} directory record${result.imported === 1 ? '' : 's'}.`)
    } catch (err) {
      setImportError(err instanceof Error ? err.message : String(err))
      setImportStatus('error')
    } finally {
      event.target.value = ''
    }
  }

  return (
    <main className='sdn-directory-page w-100 ph3 ph4-l pv3'>
      <header className='mb3 flex flex-column flex-row-l justify-between-l items-start-l'>
        <div className='pr4-l'>
          <h1 className='f2 f1-l mv0'>Directory</h1>
          <p className='mt2 mb0 f4 lh-copy black-70'>
            Search trusted Space Data Network vCards and EPMs from the {runtimeRef.current.mode} runtime adapter.
          </p>
        </div>
        <div className='mt3 mt0-l f6 ttu tracked black-60'>
          {status === 'loading' ? 'Loading directory records...' : `${sortedRecords.length} records`}
        </div>
      </header>

      <section className='pa3 ba b--black-10 br2 bg-white mb3'>
        <div className='flex flex-column flex-row-l'>
          <div className='flex-auto pr0 pr4-l mb3 mb0-l'>
            <label className='db'>
              <span className='db f6 ttu tracked black-60 mb1'>Search</span>
              <input
                id='sdn-directory-search'
                name='sdn-directory-search'
                className='input-reset ba b--black-20 pa2 br2 w-100'
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder='Search peer id, name, address, or CID'
              />
            </label>
            <div className='f6 black-60 mt2'>
              Search spans local FlatSQL-backed node and user records.
            </div>
            {error && <div className='mt2 dark-red'>{error}</div>}
          </div>

          <div className='w-100 w-third-l pl0 pl4-l bl-l b--black-10 mb3 mb0-l'>
            <h2 className='f4 mt0 mb2'>Upload vCard / EPM</h2>
            <p className='mt0 mb3 f6 lh-copy black-60'>
              Import trusted vCard or EPM JSON records. The record type is read from the file.
            </p>
            <input
              id='sdn-directory-import-file'
              name='sdn-directory-import-file'
              ref={importFileRef}
              className='dn'
              type='file'
              accept='.vcf,.vcard,.json,application/json,text/vcard,text/x-vcard'
              onChange={handleImportFile}
            />
            <Button minWidth={150} onClick={() => importFileRef.current?.click()} buttonType='button'>
              Upload vCard / EPM
            </Button>
            {importStatus !== 'idle' && importStatus !== 'error' && (
              <div className='mt2 f6 black-60'>{importStatus === 'loading' ? 'Importing directory record...' : importStatus}</div>
            )}
            {importError && <div className='mt2 dark-red'>{importError}</div>}
          </div>
        </div>
      </section>

      <section className='ba b--black-10 br2 bg-white'>
        <div className='pa3 flex flex-column flex-row-l justify-between-l items-start-l items-center-l bb b--black-10'>
          <div>
            <h2 className='f4 mt0 mb1'>Directory records</h2>
            <p className='mt0 mb0 f6 black-60'>
              Sorted, filtered, FlatSQL-backed records from trusted vCards and EPMs.
            </p>
          </div>
          <div className='mt3 mt0-l flex items-center'>
            <label className='f6 black-60 mr3' htmlFor='sdn-directory-page-size'>
              Rows
            </label>
            <select
              id='sdn-directory-page-size'
              name='sdn-directory-page-size'
              className='input-reset ba b--black-20 pa2 br2 bg-white'
              value={pageSize}
              onChange={(event) => setPageSize(Number(event.target.value))}
            >
              <option value={10}>10</option>
              <option value={25}>25</option>
              <option value={50}>50</option>
            </select>
          </div>
        </div>
        {visibleRecords.length ? (
          <div className='overflow-auto'>
            <table className='collapse w-100 f6' style={{ minWidth: '64rem' }}>
              <thead>
                <tr className='tl bg-snow-muted'>
                  {sortableDirectoryHeader('Type', directorySortKeys.type, sortKey, sortDirection, handleSort)}
                  {sortableDirectoryHeader('Name', directorySortKeys.name, sortKey, sortDirection, handleSort)}
                  {sortableDirectoryHeader('Peer ID', directorySortKeys.peerId, sortKey, sortDirection, handleSort)}
                  {sortableDirectoryHeader('Bitcoin', directorySortKeys.bitcoin, sortKey, sortDirection, handleSort)}
                  {sortableDirectoryHeader('EPM CID', directorySortKeys.epmCid, sortKey, sortDirection, handleSort)}
                  {sortableDirectoryHeader('Source', directorySortKeys.source, sortKey, sortDirection, handleSort)}
                </tr>
              </thead>
              <tbody>
                {visibleRecords.map((record) => (
                  <tr key={`${record.type}-${record.peerId}-${record.epmCid}-${record.name}`}>
                    <td className='bt b--black-10 pa3'>
                      <span className='dib br-pill bg-aqua white ttu tracked f7 ph2 pv1'>{record.type}</span>
                    </td>
                    <td className='bt b--black-10 pa3 fw6 charcoal'>{record.name}</td>
                    <td className='bt b--black-10 pa3 break-word monospace'>{record.peerId}</td>
                    <td className='bt b--black-10 pa3 break-word monospace'>{record.bitcoin}</td>
                    <td className='bt b--black-10 pa3 break-word monospace'>{record.epmCid}</td>
                    <td className='bt b--black-10 pa3'>{record.source}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className='mv0 pa3 black-60'>No directory records matched this query.</p>
        )}
        <div className='pa3 flex flex-column flex-row-ns justify-between-ns items-start items-center-ns bt b--black-10'>
          <div className='f6 black-60 mb3 mb0-ns'>
            Showing {visibleRecords.length ? pageStart + 1 : 0}-{pageEnd} of {sortedRecords.length}
          </div>
          <div className='flex items-center'>
            <Button
              minWidth={95}
              bg='bg-white'
              color='blue'
              fill='fill-blue'
              className='ba b--black-20'
              disabled={currentPage <= 1}
              onClick={() => setPage(currentPage - 1)}
              buttonType='button'
            >
              Previous
            </Button>
            <span className='mh3 f6 black-60'>
              Page {currentPage} / {totalPages}
            </span>
            <Button
              minWidth={95}
              bg='bg-white'
              color='blue'
              fill='fill-blue'
              className='ba b--black-20'
              disabled={currentPage >= totalPages}
              onClick={() => setPage(currentPage + 1)}
              buttonType='button'
            >
              Next
            </Button>
          </div>
        </div>
      </section>
    </main>
  )
}

function directoryRecordRow(record, type) {
  return {
    type,
    name: record.dn ?? record.legal_name ?? 'Unknown',
    peerId: record.peer_id || '—',
    bitcoin: record.bitcoin_address ?? '—',
    epmCid: record.epm_cid ?? '—',
    source: record.source ?? 'Unknown',
  }
}

function sortableDirectoryHeader(label, key, sortKey, sortDirection, onSort) {
  const active = sortKey === key
  const indicator = active ? (sortDirection === 'asc' ? ' asc' : ' desc') : ''
  return (
    <th className='bb b--black-10 pa3' key={key}>
      <button
        className={`button-reset bg-transparent bn pa0 tl pointer ttu tracked f7 fw6 ${active ? 'teal' : 'blue'}`}
        onClick={() => onSort(key)}
        type='button'
      >
        {label}{indicator}
      </button>
    </th>
  )
}

function compareDirectoryValues(left, right) {
  return String(left ?? '').localeCompare(String(right ?? ''), undefined, {
    numeric: true,
    sensitivity: 'base',
  })
}

function directoryImportRequestFromText(text) {
  const trimmed = String(text ?? '').trim()
  if (!trimmed) {
    throw new Error('directory import file is empty')
  }
  if (/^BEGIN:VCARD/i.test(trimmed)) {
    return {
      source: 'manual-upload',
      vcard: trimmed,
    }
  }

  const parsed = JSON.parse(trimmed)
  if (parsed && typeof parsed === 'object') {
    if (parsed.epm_json || parsed.record || parsed.vcard) {
      return {
        source: 'manual-upload',
        ...parsed,
      }
    }
    return {
      source: 'manual-upload',
      epm_json: parsed,
    }
  }

  throw new Error('directory import file must contain vCard or EPM JSON')
}

export default DirectoryPage
