import React, { useEffect, useMemo, useRef, useState } from 'react'
import QRCode from 'qrcode'
import Button from '../../../../../../webui/src/components/button/button.tsx'
import { getSharedUiRuntimeAdapter } from '../../../../../src/ui/runtime/server-adapter.js'
import { createVCardQrPayload } from '../../../../../src/ui/runtime/identity-vcard.js'

const directorySortKeys = {
  type: 'type',
  name: 'name',
  peerId: 'peerId',
  bitcoin: 'bitcoin',
  epmCid: 'epmCid',
  source: 'source',
}

const directoryAliasDomains = {
  signing: 'signing.spacedatanetwork.org',
  encryption: 'encryption.spacedatanetwork.org',
  bitcoin: 'bitcoin.spacedatanetwork.org',
  ethereum: 'ethereum.spacedatanetwork.org',
  solana: 'solana.spacedatanetwork.org',
}

const directoryHelpText = {
  'Contact card': 'A readable contact-card view of the signed EPM and vCard fields stored in the local directory.',
  Name: 'Identity and role fields for this node or user. Blank values are shown so you can see which contact fields are not populated yet.',
  Contact: 'Direct contact fields from the vCard/EPM record, such as email and telephone.',
  Address: 'Postal address fields from the contact record. These can be blank for nodes or privacy-preserving identities.',
  Blockchain: 'Blockchain addresses bound to this identity. Bitcoin addresses link to a public balance explorer.',
  Network: 'Network identifiers used to find and verify this Space Data Network entity, including the libp2p peer ID and EPM CID.',
  Signature: 'The EPM signature metadata when present. The signature binds the contact fields to the publishing identity.',
  Keys: 'Public identity, signing, and encryption keys advertised by the EPM. Private keys are never stored or displayed here.',
  'Chain proofs': 'Per-chain signatures proving that the listed blockchain addresses belong to this EPM identity, which makes imported contacts harder to spoof.',
  'Network addresses': 'Advertised libp2p/IPNS addresses for reaching this node or resolving its published profile.',
  'Scan vCard': 'A QR code containing the portable vCard representation of this directory record for mobile import or sharing.',
  Download: 'Exports this directory record as a binary EPM or as a vCard contact file.',
  'EPM CID': 'The content identifier for the signed EPM payload used to verify and retrieve this directory profile.',
}

const directoryButtonCenterStyle = {
  alignItems: 'center',
  display: 'inline-flex',
  justifyContent: 'center',
  textAlign: 'center',
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
  const [selectedRecord, setSelectedRecord] = useState(null)
  const [downloadError, setDownloadError] = useState(null)
  const [qrCodeURL, setQRCodeURL] = useState('')
  const [qrError, setQRError] = useState(null)

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

  useEffect(() => {
    let cancelled = false

    async function renderDirectoryQR() {
      if (!selectedRecord) {
        setQRCodeURL('')
        setQRError(null)
        return
      }

      setQRError(null)
      try {
        const dataURL = await QRCode.toDataURL(directoryRecordQRVCard(selectedRecord), {
          errorCorrectionLevel: 'L',
          width: 512,
          margin: 6,
          color: {
            dark: '#000000',
            light: '#ffffff',
          },
        })
        if (!cancelled) {
          setQRCodeURL(dataURL)
        }
      } catch (err) {
        if (!cancelled) {
          setQRCodeURL('')
          setQRError(err instanceof Error ? err.message : String(err))
        }
      }
    }

    renderDirectoryQR()

    return () => {
      cancelled = true
    }
  }, [selectedRecord])

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

  async function handleDownloadEPM(record) {
    setDownloadError(null)
    try {
      const downloaded = await downloadPeerArtifact(record, 'epm')
      if (downloaded) {
        return
      }

      const embeddedBytes = directoryRecordEmbeddedEPMBytes(record)
      if (!embeddedBytes) {
        throw new Error('Binary EPM is not available for this directory record.')
      }
      downloadBlob(embeddedBytes, directoryRecordFilename(record, 'epm'), 'application/x-flatbuffers')
    } catch (err) {
      setDownloadError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleDownloadVCard(record) {
    setDownloadError(null)
    try {
      const downloaded = await downloadPeerArtifact(record, 'vcard')
      if (downloaded) {
        return
      }

      downloadText(directoryRecordVCard(record), directoryRecordFilename(record, 'vcf'), 'text/vcard')
    } catch (err) {
      setDownloadError(err instanceof Error ? err.message : String(err))
    }
  }

  function handleSelectRecord(record) {
    setDownloadError(null)
    setSelectedRecord(record)
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
            <Button minWidth={150} style={directoryButtonCenterStyle} onClick={() => importFileRef.current?.click()} buttonType='button'>
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
                  <th className='bb b--black-10 pa3'>Actions</th>
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
                      <Button minWidth={80} style={directoryButtonCenterStyle} onClick={() => handleSelectRecord(record)} buttonType='button'>
                        View
                      </Button>
                    </td>
                    <td className='bt b--black-10 pa3'>
                      <span className='dib br-pill bg-aqua white ttu tracked f7 ph2 pv1'>{record.type}</span>
                    </td>
                    <td className='bt b--black-10 pa3 fw6 charcoal'>{record.name}</td>
                    <td className='bt b--black-10 pa3 break-word monospace'>{record.peerId}</td>
                    <td className='bt b--black-10 pa3 break-word monospace'>{renderBitcoinAddress(record.bitcoinAddress)}</td>
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
              style={directoryButtonCenterStyle}
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
              style={directoryButtonCenterStyle}
              disabled={currentPage >= totalPages}
              onClick={() => setPage(currentPage + 1)}
              buttonType='button'
            >
              Next
            </Button>
          </div>
        </div>
      </section>

      {selectedRecord && (
        <DirectoryRecordModal
          record={selectedRecord}
          qrCodeURL={qrCodeURL}
          qrError={qrError}
          downloadError={downloadError}
          onClose={() => setSelectedRecord(null)}
          onDownloadEPM={() => handleDownloadEPM(selectedRecord)}
          onDownloadVCard={() => handleDownloadVCard(selectedRecord)}
        />
      )}
    </main>
  )
}

function directoryRecordRow(record, type) {
  const bitcoinAddress = record.bitcoin_address ?? ''
  const epmJSON = parseDirectoryEPMJSON(record.epm_json)
  return {
    type,
    name: record.dn ?? record.legal_name ?? epmJSON?.dn ?? epmJSON?.legal_name ?? 'Unknown',
    peerId: record.peer_id || '—',
    bitcoin: bitcoinAddress || '—',
    bitcoinAddress,
    epmCid: record.epm_cid ?? '—',
    source: record.source ?? 'Unknown',
    epmJSON,
  }
}

function renderBitcoinAddress(address) {
  const trimmed = String(address ?? '').trim()
  if (!trimmed) {
    return '—'
  }
  return (
    <a className='blue link underline-hover' href={bitcoinBalanceURL(trimmed)} target='_blank' rel='noreferrer'>
      {trimmed}
    </a>
  )
}

function bitcoinBalanceURL(address) {
  return `https://mempool.space/address/${encodeURIComponent(address)}`
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

function DirectoryRecordModal({
  record,
  qrCodeURL,
  qrError,
  downloadError,
  onClose,
  onDownloadEPM,
  onDownloadVCard,
}) {
  const epm = record.epmJSON ?? {}
  const profileImage = firstString(epm.photo_data_url, epm.photoDataURL, epm.photo)
  const sections = directoryContactSections(record, epm)
  const keys = Array.isArray(epm.keys) ? epm.keys : []
  const chainProofs = Array.isArray(epm.chain_proofs) ? epm.chain_proofs : []
  const multiaddrs = Array.isArray(epm.multiformat_address) ? epm.multiformat_address : []

  return (
    <div className='fixed absolute--fill z-999 flex items-start justify-center pa3 pa4-l' style={directoryModalOverlayStyle}>
      <div className='bg-white br3 shadow-5 w-100 overflow-hidden' style={directoryModalCardStyle} role='dialog' aria-modal='true' aria-label='Directory EPM detail'>
        <div className='pa4 bg-snow-muted bb b--black-10 flex flex-column flex-row-l justify-between-l'>
          <div className='flex items-start'>
            <div className='mr3 flex-shrink-0'>
              {profileImage ? (
                <img src={profileImage} alt='' className='br3 ba b--black-10 bg-white' style={directoryProfileImageStyle} />
              ) : (
                <div className='br3 bg-navy white flex items-center justify-center f2 fw6' style={directoryProfileImageStyle}>
                  {directoryInitials(record.name)}
                </div>
              )}
            </div>
            <div>
              <div className='mb2'>
                <span className='dib br-pill bg-aqua white ttu tracked f7 ph2 pv1'>{record.type}</span>
              </div>
              <h2 className='f2-l f3 mt0 mb2 charcoal'>{record.name}</h2>
              <div className='monospace f6 break-word black-70'>{record.peerId}</div>
            </div>
          </div>
          <button
            className='button-reset pointer bg-transparent bn f2 fw3 black-60 hover-teal self-end self-start-l mt3 mt0-l'
            onClick={onClose}
            type='button'
            aria-label='Close EPM detail'
          >
            ×
          </button>
        </div>

        <div className='pa4 overflow-auto' style={directoryModalBodyStyle}>
          <div className='flex flex-column flex-row-l'>
            <section className='flex-auto pr0 pr4-l'>
              <DirectorySectionHeading level='h3' title='Contact card' className='f4 mt0 mb3 charcoal' />
              <div className='directory-contact-card'>
                {sections.map((section) => (
                  <DirectoryContactSection key={section.title} section={section} />
                ))}
              </div>

              {keys.length > 0 && <DirectoryJSONSection title='Keys' items={keys} />}
              {chainProofs.length > 0 && <DirectoryJSONSection title='Chain proofs' items={chainProofs} />}

              {multiaddrs.length > 0 && (
                <div className='mt4'>
                  <DirectorySectionHeading level='h4' title='Network addresses' className='f5 mt0 mb2 charcoal' />
                  <div className='pa3 br2 bg-near-white ba b--black-10'>
                    {multiaddrs.map((addr) => (
                      <div key={addr} className='monospace f7 break-word mb2 black-70'>{addr}</div>
                    ))}
                  </div>
                </div>
              )}
            </section>

            <aside className='w-100 w-third-l mt4 mt0-l pl0 pl4-l bl-l b--black-10'>
              <div className='pa3 br3 bg-near-white ba b--black-10 tc'>
                <DirectorySectionHeading level='h3' title='Scan vCard' className='f4 mt0 mb3 charcoal justify-center' />
                {qrCodeURL ? (
                  <img src={qrCodeURL} alt='Directory vCard QR code' className='db center bg-white pa2 br2 ba b--black-10' width='360' height='360' style={{ maxWidth: '100%', height: 'auto' }} />
                ) : (
                  <div className='pa4 black-60'>{qrError ? `QR unavailable: ${qrError}` : 'Rendering QR code...'}</div>
                )}
                <p className='f6 lh-copy black-60 mb0'>
                  Encodes a phone-scannable contact vCard with SDN peer and EPM identifiers.
                </p>
              </div>

              <div className='mt3 pa3 br3 ba b--black-10'>
                <DirectorySectionHeading level='h3' title='Download' className='f4 mt0 mb3 charcoal' />
                <div className='flex flex-column'>
                  <Button minWidth={180} style={{ ...directoryButtonCenterStyle, width: '100%' }} onClick={onDownloadEPM} buttonType='button'>
                    Download binary EPM
                  </Button>
                  <div className='mt2'>
                    <Button minWidth={180} bg='bg-white' color='blue' fill='fill-blue' className='ba b--black-20' style={{ ...directoryButtonCenterStyle, width: '100%' }} onClick={onDownloadVCard} buttonType='button'>
                      Download .vcf
                    </Button>
                  </div>
                </div>
                {downloadError && <div className='mt3 dark-red f6 lh-copy'>{downloadError}</div>}
              </div>

              <div className='mt3 pa3 br3 bg-navy white'>
                <DirectorySectionHeading level='div' title='EPM CID' className='f7 ttu tracked moon-gray mb2' dark />
                <div className='directory-side-cid monospace f7' title={record.epmCid}>{record.epmCid}</div>
              </div>
            </aside>
          </div>
        </div>
      </div>
      <style>{directoryModalCSS}</style>
    </div>
  )
}

function DirectoryContactSection({ section }) {
  return (
    <section className='directory-contact-section br3 ba b--black-10 bg-white'>
      <DirectorySectionHeading
        level='div'
        title={section.title}
        className='directory-contact-section-title f7 fw6 ttu tracked teal bg-near-white bb b--black-10 ph3 pv2'
      />
      <div className='grid-directory-fields pa3'>
        {section.fields.map((field) => (
          <DirectoryField key={field.label} label={field.label} value={field.value} isMono={field.isMono} />
        ))}
      </div>
    </section>
  )
}

function DirectorySectionHeading({ level: Level = 'div', title, className = '', dark = false }) {
  return (
    <Level className={`directory-section-heading flex items-center ${className}`}>
      <span>{title}</span>
      <DirectoryHelpButton topic={title} dark={dark} />
    </Level>
  )
}

function DirectoryHelpButton({ topic, dark = false }) {
  const helpRef = useRef(null)
  const [open, setOpen] = useState(false)
  const help = directoryHelpText[topic]

  useEffect(() => {
    if (!open) {
      return undefined
    }

    function handlePointerDown(event) {
      if (helpRef.current && event.target && !helpRef.current.contains(event.target)) {
        setOpen(false)
      }
    }

    function handleKeyDown(event) {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }

    document.addEventListener('pointerdown', handlePointerDown, true)
    document.addEventListener('keydown', handleKeyDown)

    return () => {
      document.removeEventListener('pointerdown', handlePointerDown, true)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  if (!help) {
    return null
  }

  return (
    <span className='directory-help-wrap ml2' ref={helpRef}>
      <button
        className={`directory-help-button button-reset pointer br-100 fw6 ${dark ? 'directory-help-button-dark' : ''}`}
        type='button'
        aria-label={`Explain ${topic}`}
        aria-expanded={open}
        onClick={() => setOpen((nextOpen) => !nextOpen)}
      >
        ?
      </button>
      {open && (
        <span className='directory-help-popover tl normal'>
          {help}
        </span>
      )}
    </span>
  )
}

function DirectoryField({ label, value, isMono = false }) {
  const empty = !isPresent(value)
  const title = empty ? undefined : String(value)
  return (
    <div className='directory-field-card pa3 br2 ba b--black-10 bg-white-90'>
      <div className='f7 ttu tracked black-50 mb1'>{label}</div>
      <div
        className={`directory-field-value ${isMono ? 'monospace f7' : 'f6'} ${empty ? 'black-40' : 'charcoal'}`}
        title={title}
      >
        {empty ? '—' : renderDirectoryFieldValue(label, value)}
      </div>
    </div>
  )
}

function DirectoryJSONSection({ title, items }) {
  return (
    <div className='mt4'>
      <DirectorySectionHeading level='h4' title={title} className='f5 mt0 mb2 charcoal' />
      <div className='pa3 br2 bg-near-white ba b--black-10'>
        {items.map((item, index) => (
          <pre key={`${title}-${index}`} className='directory-json-pre ma0 mb3 f7 lh-copy overflow-auto charcoal'>
            {JSON.stringify(item, null, 2)}
          </pre>
        ))}
      </div>
    </div>
  )
}

function compareDirectoryValues(left, right) {
  return String(left ?? '').localeCompare(String(right ?? ''), undefined, {
    numeric: true,
    sensitivity: 'base',
  })
}

function directoryContactSections(record, epm) {
  return [
    {
      title: 'Name',
      fields: [
        { label: 'Display name', value: firstString(epm.dn, record.name) },
        { label: 'Legal name', value: epm.legal_name },
        { label: 'Given name', value: epm.given_name },
        { label: 'Family name', value: epm.family_name },
        { label: 'Entity type', value: firstString(epm.entity_type, record.type) },
        { label: 'Job title', value: epm.job_title },
        { label: 'Occupation', value: epm.occupation },
      ],
    },
    {
      title: 'Contact',
      fields: [
        { label: 'Email', value: epm.email },
        { label: 'Telephone', value: epm.telephone },
      ],
    },
    {
      title: 'Address',
      fields: directoryAddressFields(epm.address),
    },
    {
      title: 'Blockchain',
      fields: [
        { label: 'Bitcoin', value: firstString(epm.bitcoin_address, record.bitcoinAddress), isMono: true },
        { label: 'Ethereum', value: epm.ethereum_address, isMono: true },
        { label: 'Solana', value: epm.solana_address, isMono: true },
      ],
    },
    {
      title: 'Network',
      fields: [
        { label: 'Peer ID', value: record.peerId, isMono: true },
        { label: 'EPM CID', value: record.epmCid, isMono: true },
        { label: 'Source', value: record.source },
      ],
    },
    {
      title: 'Signature',
      fields: [
        { label: 'Signature', value: epm.signature, isMono: true },
        { label: 'Signature timestamp', value: formatDirectoryTimestamp(epm.signature_timestamp) },
      ],
    },
  ]
}

function directoryAddressFields(address) {
  const normalizedAddress = address && typeof address === 'object' ? address : {}
  return [
    { label: 'Street', value: normalizedAddress.street },
    { label: 'Locality', value: normalizedAddress.locality },
    { label: 'Region', value: normalizedAddress.region },
    { label: 'Postal code', value: normalizedAddress.postal_code },
    { label: 'Country', value: normalizedAddress.country },
    { label: 'PO box', value: normalizedAddress.po_box },
  ]
}

function renderDirectoryFieldValue(label, value) {
  if (label === 'Bitcoin') {
    return renderBitcoinAddress(value)
  }
  return String(value)
}

function parseDirectoryEPMJSON(value) {
  if (!value) {
    return null
  }
  if (typeof value === 'object') {
    return value
  }
  if (typeof value !== 'string') {
    return null
  }
  try {
    const parsed = JSON.parse(value)
    return parsed && typeof parsed === 'object' ? parsed : null
  } catch {
    return null
  }
}

async function downloadPeerArtifact(record, artifact) {
  if (!isUsablePeerID(record.peerId) || typeof fetch !== 'function') {
    return false
  }
  const suffix = artifact === 'vcard' ? '/vcard' : ''
  const response = await fetch(`/api/peers/${encodeURIComponent(record.peerId)}/epm${suffix}`, {
    credentials: 'include',
  })
  if (!response.ok) {
    return false
  }
  const blob = await response.blob()
  downloadBlob(blob, directoryRecordFilename(record, artifact === 'vcard' ? 'vcf' : 'epm'), artifact === 'vcard' ? 'text/vcard' : 'application/x-flatbuffers')
  return true
}

function directoryRecordEmbeddedEPMBytes(record) {
  const epm = record.epmJSON ?? {}
  const payload = firstString(epm.epm_base64, epm.epm_b64, epm.x_sdn_epm_b64, epm['X-SDN-EPM-B64'])
  if (!payload || typeof atob !== 'function') {
    return null
  }
  const binary = atob(payload)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i)
  }
  return bytes
}

function directoryRecordVCard(record) {
  return directoryRecordVCardWithOptions(record, { includeSignedPayload: true })
}

function directoryRecordQRVCard(record) {
  return createVCardQrPayload({
    id: firstString(record.id, record.peerId, record.name, 'directory-record'),
    kind: 'hosted',
    label: firstString(record.name, record.epmJSON?.dn, 'Space Data Network'),
    peerId: '',
    epmJson: record.epmJSON ?? {},
  })
}

function directoryRecordVCardWithOptions(record, { includeSignedPayload }) {
  const epm = record.epmJSON ?? {}
  const lines = [
    'BEGIN:VCARD',
    'VERSION:3.0',
    'PRODID;VALUE=TEXT:-//Apple Inc.//iPhone OS 15.1.1//EN',
    'N:;;;;',
    `FN:${escapeVCardValue(firstString(epm.dn, record.name))}`,
  ]
  addVCardLine(lines, 'ORG', epm.legal_name)
  addVCardLine(lines, 'EMAIL', epm.email)
  addVCardLine(lines, 'TEL', epm.telephone)
  addVCardLine(lines, 'TITLE', epm.job_title)
  addVCardLine(lines, 'UID', record.peerId)
  addVCardLine(lines, 'X-SDN-DIRECTORY-KIND', record.type)
  addVCardLine(lines, 'X-SDN-PEER-ID', record.peerId)
  addVCardLine(lines, 'X-SDN-BITCOIN-ADDRESS', firstString(epm.bitcoin_address, record.bitcoinAddress))
  addVCardLine(lines, 'X-SDN-EPM-CID', record.epmCid)
  if (includeSignedPayload) {
    addVCardLine(lines, 'X-SDN-EPM-SIGNATURE', epm.signature)
    addVCardLine(lines, 'X-SDN-EPM-SIGNATURE-TIMESTAMP', epm.signature_timestamp)
    addVCardLine(lines, 'PHOTO', epm.photo_data_url)
    addVCardLine(lines, 'X-SDN-EPM-B64', firstString(epm.epm_base64, epm.epm_b64, epm.x_sdn_epm_b64, epm['X-SDN-EPM-B64']))
  }
  lines.push(...directoryAppleIdentityLines(record, includeSignedPayload))
  lines.push('END:VCARD')
  return `${lines.join('\r\n')}\r\n`
}

function addVCardLine(lines, key, value) {
  if (!isPresent(value)) {
    return
  }
  lines.push(foldVCardLine(`${key}:${escapeVCardValue(value)}`))
}

function directoryAppleIdentityLines(record, includeSignedPayload) {
  const entries = directoryAppleIdentityEntries(record, includeSignedPayload)
  const lines = []

  for (const entry of entries) {
    if (entry.emailDomain && entry.emailType && isSafeEmailLocalPart(entry.value)) {
      lines.push(foldVCardLine(`EMAIL;type=INTERNET;type=${entry.emailType}:${entry.value}@${entry.emailDomain}`))
    }
  }

  entries.forEach((entry, index) => {
    const item = index + 1
    lines.push(foldVCardLine(`item${item}.X-ABLabel:${escapeVCardValue(entry.label)}`))
    lines.push(foldVCardLine(`item${item}.X-ABRELATEDNAMES:${escapeVCardValue(entry.value)}`))
  })

  return lines
}

function directoryAppleIdentityEntries(record, includeSignedPayload) {
  const epm = record.epmJSON ?? {}
  const entries = []
  const seen = new Set()

  function addEntry(label, value, emailType = '', emailDomain = '') {
    const trimmed = firstString(value)
    if (!trimmed) {
      return
    }
    const key = `${label}\n${trimmed}`
    if (seen.has(key)) {
      return
    }
    seen.add(key)
    entries.push({ label, value: trimmed, emailType, emailDomain })
  }

  const keys = Array.isArray(epm.keys) ? epm.keys : []
  for (const key of keys) {
    const publicKey = firstString(key?.public_key, key?.PUBLIC_KEY)
    if (!publicKey) {
      continue
    }
    const keyType = firstString(key?.key_type, key?.KEY_TYPE).toLowerCase()
    const addressType = firstString(key?.address_type, key?.ADDRESS_TYPE)
    const keyPath = firstString(key?.key_address, key?.KEY_ADDRESS)
    if (keyType === 'encryption' || addressType.toLowerCase() === 'x25519') {
      addEntry(joinVCardLabel('Public Key Encryption', addressType, keyPath), publicKey, 'encryption', directoryAliasDomains.encryption)
    } else {
      addEntry(joinVCardLabel('Public Key Signing', addressType, keyPath), publicKey, 'signing', directoryAliasDomains.signing)
    }
    addEntry(joinVCardLabel('Extended Public Key', addressType, keyPath), firstString(key?.xpub, key?.XPUB))
  }

  addEntry('Public Key Signing', epm.signing_pubkey_hex, 'signing', directoryAliasDomains.signing)
  addEntry('Public Key Encryption', epm.encryption_pubkey_hex, 'encryption', directoryAliasDomains.encryption)
  addChainAddressEntries(entries, seen, record, epm)

  if (includeSignedPayload) {
    addEntry('EPM Signature', epm.signature)
    addEntry('EPM Signature Timestamp', epm.signature_timestamp)
    addEntry('Binary EPM', firstString(epm.epm_base64, epm.epm_b64, epm.x_sdn_epm_b64, epm['X-SDN-EPM-B64']))
  }

  return entries
}

function addChainAddressEntries(entries, seen, record, epm) {
  const add = (chain, address, keyPath = '') => {
    const trimmed = firstString(address)
    if (!trimmed) {
      return
    }
    const label = joinVCardLabel(`${chainDisplayName(chain)} Address`, keyPath)
    const key = `${label}\n${trimmed}`
    if (seen.has(key)) {
      return
    }
    seen.add(key)
    entries.push({
      label,
      value: trimmed,
      emailType: chain,
      emailDomain: directoryAliasDomains[chain],
    })
  }

  add('bitcoin', firstString(epm.bitcoin_address, record.bitcoinAddress), epm.bitcoin_key_path)
  add('ethereum', epm.ethereum_address, epm.ethereum_key_path)
  add('solana', epm.solana_address, epm.solana_key_path)

  const proofs = Array.isArray(epm.chain_proofs) ? epm.chain_proofs : []
  for (const proof of proofs) {
    const chain = firstString(proof?.chain, proof?.CHAIN).toLowerCase()
    if (['bitcoin', 'ethereum', 'solana'].includes(chain)) {
      add(chain, firstString(proof?.address, proof?.ADDRESS), firstString(proof?.key_path, proof?.KEY_PATH))
    }
  }
}

function joinVCardLabel(...parts) {
  return parts.map((part) => firstString(part)).filter(Boolean).join(' ')
}

function chainDisplayName(chain) {
  if (chain === 'bitcoin') {
    return 'Bitcoin'
  }
  if (chain === 'ethereum') {
    return 'Ethereum'
  }
  if (chain === 'solana') {
    return 'Solana'
  }
  return chain
}

function isSafeEmailLocalPart(value) {
  return /^[A-Za-z0-9._+-]+$/.test(String(value ?? '').trim())
}

function foldVCardLine(line) {
  const value = String(line)
  if (value.length <= 74) {
    return value
  }
  const chunks = []
  for (let offset = 0; offset < value.length; offset += 74) {
    chunks.push(value.slice(offset, offset + 74))
  }
  return chunks.join('\r\n ')
}

function escapeVCardValue(value) {
  return String(value ?? '')
    .replace(/\\/g, '\\\\')
    .replace(/\n/g, '\\n')
    .replace(/,/g, '\\,')
    .replace(/;/g, '\\;')
}

function downloadText(text, filename, type) {
  downloadBlob(new Blob([text], { type }), filename, type)
}

function downloadBlob(data, filename, type) {
  const blob = data instanceof Blob ? data : new Blob([data], { type })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

function directoryRecordFilename(record, extension) {
  const safeName = String(firstString(record.name, record.peerId, 'directory-record'))
    .replace(/[^a-z0-9._-]+/gi, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80) || 'directory-record'
  return `${safeName}.${extension}`
}

function firstString(...values) {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) {
      return value.trim()
    }
    if (typeof value === 'number' && Number.isFinite(value)) {
      return String(value)
    }
  }
  return ''
}

function isPresent(value) {
  return value !== null && value !== undefined && String(value).trim() !== '' && String(value).trim() !== '—'
}

function isUsablePeerID(peerId) {
  return isPresent(peerId) && !String(peerId).includes('—')
}

function formatDirectoryTimestamp(value) {
  if (!value) {
    return ''
  }
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) {
    return String(value)
  }
  const millis = numeric > 1_000_000_000_000 ? numeric : numeric * 1000
  return new Date(millis).toLocaleString()
}

function directoryInitials(name) {
  const parts = String(name ?? '').trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) {
    return 'SD'
  }
  return parts.slice(0, 2).map((part) => part[0]?.toUpperCase()).join('')
}

const directoryModalOverlayStyle = {
  background: 'rgba(7, 58, 70, 0.42)',
  backdropFilter: 'blur(5px)',
  zIndex: 9999,
}

const directoryModalCardStyle = {
  maxWidth: '74rem',
  maxHeight: 'calc(100vh - 3rem)',
}

const directoryModalBodyStyle = {
  maxHeight: 'calc(100vh - 13rem)',
}

const directoryProfileImageStyle = {
  height: '6rem',
  objectFit: 'cover',
  width: '6rem',
}

const directoryModalCSS = `
  .grid-directory-fields {
    display: grid;
    gap: 0.75rem;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
  }

  .directory-contact-card {
    display: grid;
    gap: 1rem;
  }

  .directory-contact-section {
    overflow: visible;
  }

  .directory-section-heading {
    gap: 0.35rem;
    min-width: 0;
    position: relative;
  }

  .directory-help-wrap {
    display: inline-flex;
    line-height: 1;
    position: relative;
  }

  .directory-help-button {
    align-items: center;
    background: #edf7f9;
    border: 1px solid rgba(105, 196, 207, 0.7);
    color: #27737e;
    display: inline-flex;
    height: 1.35rem;
    justify-content: center;
    text-align: center;
    width: 1.35rem;
  }

  .directory-help-button-dark {
    background: rgba(255, 255, 255, 0.12);
    border-color: rgba(255, 255, 255, 0.45);
    color: #ffffff;
  }

  .directory-help-popover {
    background: #ffffff;
    border: 1px solid rgba(7, 58, 70, 0.2);
    border-radius: 0.5rem;
    box-shadow: 0 0.75rem 2rem rgba(7, 58, 70, 0.18);
    color: #2b303b;
    font-size: 0.82rem;
    font-weight: 400;
    letter-spacing: normal;
    line-height: 1.45;
    bottom: calc(100% + 0.35rem);
    left: calc(100% + 0.35rem);
    padding: 0.75rem;
    position: absolute;
    text-transform: none;
    width: min(18rem, 70vw);
    z-index: 10000;
  }

  .directory-field-card {
    min-width: 0;
  }

  .directory-field-value,
  .directory-field-value a {
    display: block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .directory-side-cid {
    display: block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .directory-json-pre {
    max-width: 100%;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
`

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
