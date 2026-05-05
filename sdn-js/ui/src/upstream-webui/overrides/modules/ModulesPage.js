import React, { useEffect, useMemo, useState } from 'react'

import {
  emptyModuleRuntimeSnapshot,
  loadModuleRuntimeSnapshotFromServer,
  resolveSelectedModuleId,
  runModuleRuntimeAction,
  updateModuleRuntimeOption
} from '../../../../../src/ui/runtime/modules.js'

const SDK_README_URL =
  'https://github.com/DigitalArsenal/space-data-module-sdk/blob/main/README.md'
const MODULE_RUNTIME_DOC_URL =
  'https://github.com/DigitalArsenal/space-data-network/blob/main/docs/module-runtime-dashboard.md'

function ModulesPage() {
  const [snapshot, setSnapshot] = useState(emptyModuleRuntimeSnapshot())
  const [status, setStatus] = useState('loading')
  const [error, setError] = useState(null)
  const [selectedId, setSelectedId] = useState('')
  const [lastRefresh, setLastRefresh] = useState(null)
  const [moduleSearch, setModuleSearch] = useState('')

  const modules = snapshot.modules
  const filteredModules = useMemo(
    () => modules.filter((module) => filterModuleBySearch(module, moduleSearch)),
    [modules, moduleSearch]
  )
  const selectedModule = useMemo(() => {
    const nextSelectedId = resolveSelectedModuleId(selectedId, modules)
    return modules.find((module) => module.id === nextSelectedId) ?? null
  }, [modules, selectedId])

  const counts = useMemo(() => {
    return modules.reduce(
      (acc, module) => {
        acc.total += 1
        if (module.status === 'running') acc.running += 1
        if (module.status === 'error') acc.error += 1
        if (module.restartPending || module.status === 'updated') acc.updated += 1
        acc.memoryBytes += module.stats?.memoryBytes ?? 0
        return acc
      },
      { total: 0, running: 0, error: 0, updated: 0, memoryBytes: 0 }
    )
  }, [modules])

  async function refreshModules({ silent = false } = {}) {
    if (!silent) {
      setStatus('loading')
    }
    setError(null)
    try {
      const nextSnapshot =
        await loadModuleRuntimeSnapshotFromServer(runtimeBaseUrl())
      setSnapshot(nextSnapshot)
      setSelectedId((previousSelectedId) =>
        resolveSelectedModuleId(previousSelectedId, nextSnapshot.modules)
      )
      setLastRefresh(Date.now())
      setStatus('ready')
    } catch (err) {
      setStatus('error')
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    let cancelled = false

    async function load() {
      try {
        const nextSnapshot =
          await loadModuleRuntimeSnapshotFromServer(runtimeBaseUrl())
        if (!cancelled) {
          setSnapshot(nextSnapshot)
          setSelectedId((previousSelectedId) =>
            resolveSelectedModuleId(previousSelectedId, nextSnapshot.modules)
          )
          setLastRefresh(Date.now())
          setStatus('ready')
        }
      } catch (err) {
        if (!cancelled) {
          setStatus('error')
          setError(err instanceof Error ? err.message : String(err))
        }
      }
    }

    load()
    const interval = window.setInterval(load, 10000)
    return () => {
      cancelled = true
      window.clearInterval(interval)
    }
  }, [])

  return (
    <main className='sdn-modules-page w-100 ph3 ph4-l pv3' style={pageStyle}>
      <header className='mb3 flex flex-column flex-row-l justify-between-l items-start-l'>
        <div className='pr4-l'>
          <h1 className='f2 f1-l mv0'>Modules</h1>
          <p className='mt2 mb0 f4 lh-copy black-70'>
            {status === 'loading' && modules.length === 0
              ? 'Loading runtime snapshot...'
              : `${counts.running} running, ${counts.updated} updated, ${counts.error} errors`}
          </p>
        </div>
        <div className='mt3 mt0-l flex items-center'>
          <button
            type='button'
            className='button-reset ba b--black-20 bg-white br2 pv2 ph3 pointer hover-bg-near-white'
            onClick={() => refreshModules()}
          >
            Refresh
          </button>
        </div>
      </header>

      {error && (
        <section className='pa3 ba b--red bg-washed-red dark-red br2 mb3'>
          {error}
        </section>
      )}

      <section
        className='grid modules-summary-grid mb3'
        style={summaryGridStyle}
      >
        <SummaryMetric label='Loaded' value={counts.total} />
        <SummaryMetric label='Running' value={counts.running} />
        <SummaryMetric label='Updated' value={counts.updated} />
        <SummaryMetric label='WASM memory' value={formatBytes(counts.memoryBytes)} />
      </section>

      <section className='flex flex-column flex-row-l' style={contentLayoutStyle}>
        <section
          className='w-100 w-40-l ba b--black-10 br2 bg-white overflow-hidden mr0 mr3-l mb3 mb0-l'
          style={moduleListPanelStyle}
        >
          <div className='pa3 bb b--black-10' style={panelHeaderStyle}>
            <div className='flex items-center justify-between mb3'>
              <div className='flex items-center'>
                <h2 className='f4 mv0 mr2'>Modules</h2>
                <ModulesHelp />
              </div>
              <span className='f6 black-60'>
                {lastRefresh ? formatClock(lastRefresh) : ''}
              </span>
            </div>
            <input
              aria-label='Search modules'
              className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
              placeholder='Search modules'
              type='search'
              value={moduleSearch}
              onChange={(event) => setModuleSearch(event.target.value)}
            />
          </div>
          <div className='overflow-auto' style={moduleListBodyStyle}>
            <table className='collapse w-100' style={modulesTableStyle}>
              <colgroup>
                <col style={moduleNameColumnStyle} />
                <col style={moduleStatusColumnStyle} />
                <col style={moduleMemoryColumnStyle} />
              </colgroup>
              <thead style={stickyTableHeaderStyle}>
                <tr className='tl f6 ttu tracked black-60 bg-white'>
                  <th className='pa2 fw6' style={moduleNameCellStyle}>Module</th>
                  <th className='pa2 fw6' style={moduleStatusCellStyle}>Status</th>
                  <th className='pa2 fw6 tr' style={moduleMemoryCellStyle}>Memory</th>
                </tr>
              </thead>
              <tbody>
                {filteredModules.map((module) => (
                  <tr
                    key={module.id}
                    className={`${selectedModule?.id === module.id ? 'bg-lightest-blue' : 'bg-white'} pointer hover-bg-near-white`}
                    onClick={() => setSelectedId(module.id)}
                  >
                    <td className='pa2 bb b--black-05' style={moduleNameCellStyle}>
                      <div className='fw6 truncate' title={module.id}>
                        {module.manifest?.name || module.id}
                      </div>
                      <div className='f6 black-60 truncate'>
                        {module.version ||
                          module.manifest?.version ||
                          'unversioned'}
                      </div>
                    </td>
                    <td className='pa2 bb b--black-05' style={moduleStatusCellStyle}>
                      <StatusPill status={module.status} />
                    </td>
                    <td className='pa2 bb b--black-05 tr' style={moduleMemoryCellStyle}>
                      {formatBytes(module.stats?.memoryBytes ?? 0)}
                    </td>
                  </tr>
                ))}
                {filteredModules.length === 0 && (
                  <tr>
                    <td className='pa3 black-60' colSpan={3}>
                      {modules.length === 0
                        ? 'No runtime modules reported.'
                        : 'No modules match the current search.'}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>

        <section className='w-100 w-60-l' style={moduleDetailPanelStyle}>
          {selectedModule ? (
            <ModuleDetail module={selectedModule} onRefresh={refreshModules} />
          ) : (
            <EmptyDetail />
          )}
        </section>
      </section>
    </main>
  )
}

function ModulesHelp() {
  return (
    <details className='relative'>
      <summary
        aria-label='What are modules?'
        className='button-reset ba b--black-20 bg-white br-100 pointer flex items-center justify-center'
        style={helpButtonStyle}
        title='What are modules?'
      >
        ?
      </summary>
      <div className='absolute z-5 bg-white ba b--black-20 br2 pa3 shadow-4' style={helpPopoverStyle}>
        <p className='mt0 mb2 f6 lh-copy black-80'>
          Runtime modules are SDK-compliant WASM packages loaded by this SDN node.
        </p>
        <a className='db link blue mb2' href={SDK_README_URL} target='_blank' rel='noreferrer'>
          Module SDK README
        </a>
        <a className='db link blue' href={MODULE_RUNTIME_DOC_URL} target='_blank' rel='noreferrer'>
          Runtime dashboard docs
        </a>
      </div>
    </details>
  )
}

function SummaryMetric({ label, value }) {
  return (
    <section className='ba b--black-10 br2 bg-white pa3'>
      <div className='f6 ttu tracked black-60'>{label}</div>
      <div className='f3 fw6 mt2'>{value}</div>
    </section>
  )
}

function ModuleDetail({ module, onRefresh }) {
  const manifest = module.manifest
  return (
    <section className='ba b--black-10 br2 bg-white overflow-hidden' style={detailPanelStyle}>
      <header className='pa3 bb b--black-10 bg-white' style={detailPanelHeaderStyle}>
        <div className='flex flex-column flex-row-l justify-between-l items-start-l mb3'>
          <div className='pr3-l'>
            <h2 className='f3 mv0'>{manifest?.name || module.id}</h2>
            <div className='f6 black-60 mt2'>
              {module.id}
              {module.version ? ` @ ${module.version}` : ''}
            </div>
          </div>
          <div className='mt3 mt0-l flex flex-column items-start items-end-l' style={detailActionPanelStyle}>
            <StatusPill status={module.status} />
            <div className='mt2'>
              <LifecycleActionBar module={module} onRefresh={onRefresh} />
            </div>
          </div>
        </div>
        {module.statusMessage && (
          <div className='mb3 dark-red'>{module.statusMessage}</div>
        )}
      </header>

      <div className='pa3' style={detailPanelBodyStyle}>
        <DetailSection title='Stats'>
          <div className='grid' style={detailGridStyle}>
            <KeyValue label='Memory pages' value={formatNumber(module.stats?.memoryPages ?? 0)} />
            <KeyValue label='Memory bytes' value={formatBytes(module.stats?.memoryBytes ?? 0)} />
            <KeyValue label='Memory limit' value={formatBytes(module.stats?.maxMemoryBytes ?? 0)} />
            <KeyValue label='Host RSS' value={formatBytes(module.stats?.hostRssBytes ?? 0)} />
            <KeyValue label='Uptime' value={formatDuration(module.stats?.uptimeMs ?? 0)} />
            <KeyValue label='Invokes' value={formatNumber(module.stats?.invokeCount ?? 0)} />
            <KeyValue label='Errors' value={formatNumber(module.stats?.errorCount ?? 0)} />
            <KeyValue label='Avg latency' value={`${formatNumber(module.stats?.averageLatencyMs ?? 0)} ms`} />
            <KeyValue label='Timers' value={formatNumber(module.stats?.timerRunCount ?? 0)} />
          </div>
        </DetailSection>

        <DetailSection title='Configure'>
          <ConfigureModuleButton module={module} />
        </DetailSection>

        <DetailSection title='Options'>
          {module.options.length > 0 ? (
            <div className='flex flex-column'>
              {module.options.map((option) => (
                <ModuleOptionControl
                  key={option.key}
                  moduleId={module.id}
                  option={option}
                  onRefresh={onRefresh}
                />
              ))}
            </div>
          ) : (
            <div className='black-60'>No runtime options reported.</div>
          )}
        </DetailSection>

        <DetailSection title='Command history'>
          <CommandHistory history={module.commandHistory ?? []} />
        </DetailSection>

        <DetailSection title='Recent status'>
          <SimpleList
            empty='No status history reported.'
            items={(module.statusHistory ?? []).map((event, index) => ({
              key: `${event.status}-${event.at || index}`,
              primary: event.status,
              secondary: [
                event.message,
                event.at ? formatDateTime(event.at) : ''
              ]
                .filter(Boolean)
                .join(' | ')
            }))}
          />
        </DetailSection>

        <DetailSection title='Protocols'>
          <SimpleList
            empty='No protocols reported.'
            items={(manifest?.protocols ?? []).map((protocol) => ({
              key: protocol.protocolId,
              primary: protocol.wireId || protocol.protocolId,
              secondary: protocol.methodId
            }))}
          />
        </DetailSection>

        <DetailSection title='Capabilities'>
          <div className='flex flex-wrap'>
            {(manifest?.capabilities ?? []).map((capability) => (
              <span
                key={capability}
                className='dib br2 bg-near-white ba b--black-10 f6 pv1 ph2 mr2 mb2'
              >
                {capability}
              </span>
            ))}
            {(!manifest?.capabilities ||
              manifest.capabilities.length === 0) && (
              <span className='black-60'>No capabilities reported.</span>
            )}
          </div>
        </DetailSection>

        <DetailSection title='Links'>
          <div className='flex flex-wrap'>
            {module.links?.logsUrl && (
              <RuntimeLink href={module.links.logsUrl} label='Logs' />
            )}
            {module.links?.eventsUrl && (
              <RuntimeLink href={module.links.eventsUrl} label='Events' />
            )}
            {!module.links?.logsUrl && !module.links?.eventsUrl && (
              <span className='black-60'>No runtime links reported.</span>
            )}
          </div>
        </DetailSection>
      </div>
    </section>
  )
}

function LifecycleActionBar({ module, onRefresh }) {
  const actions = [...(module.actions ?? [])].sort((a, b) => {
    return lifecycleActionPriority(a.actionId) - lifecycleActionPriority(b.actionId)
  })
  if (actions.length === 0) {
    return <div className='black-60'>No lifecycle actions reported.</div>
  }
  return (
    <div className='flex items-center justify-end' style={lifecycleActionBarStyle}>
      {actions.map((action) => (
        <button
          key={action.actionId}
          type='button'
          className='button-reset ba br2 pv2 ph3 ml2 pointer disabled'
          disabled={!action.enabled}
          title={action.description || action.label}
          style={lifecycleActionButtonStyle(action)}
          onClick={async () => {
            await runModuleRuntimeAction(
              runtimeBaseUrl(),
              module.id,
              action.actionId
            )
            await onRefresh()
          }}
        >
          {action.label}
        </button>
      ))}
    </div>
  )
}

function lifecycleActionPriority(actionId) {
  const priority = {
    restart: 0,
    start: 1,
    load: 2,
    'reload-manifest': 3,
    pause: 4,
    stop: 5,
    unload: 6,
    'clear-error': 7
  }
  return priority[actionId] ?? 20
}

function lifecycleActionButtonStyle(action) {
  const disabled = !action.enabled
  const palette = lifecycleActionPalette(action.actionId)
  return {
    ...lifecycleActionButtonBaseStyle,
    borderColor: palette.border,
    backgroundColor: disabled ? '#f4f4f4' : palette.background,
    color: disabled ? '#777777' : palette.color,
    cursor: disabled ? 'not-allowed' : 'pointer',
    opacity: disabled ? 0.72 : 1
  }
}

function lifecycleActionPalette(actionId) {
  if (actionId === 'restart' || actionId === 'reload-manifest') {
    return { background: '#d9480f', border: '#b7390b', color: '#ffffff' }
  }
  if (actionId === 'start' || actionId === 'load') {
    return { background: '#0b6b70', border: '#064f54', color: '#ffffff' }
  }
  if (actionId === 'pause') {
    return { background: '#8a6d1d', border: '#6f5615', color: '#ffffff' }
  }
  if (actionId === 'stop' || actionId === 'unload') {
    return { background: '#c92a2a', border: '#9f1f1f', color: '#ffffff' }
  }
  if (actionId === 'clear-error') {
    return { background: '#2f80a7', border: '#236681', color: '#ffffff' }
  }
  return { background: '#ffffff', border: '#d0d0d0', color: '#111111' }
}

function ConfigureModuleButton({ module }) {
  return (
    <a
      className='dib button-reset ba br2 pv2 ph3 pointer link'
      href={moduleConfigureUrl(module)}
      rel='noreferrer'
      style={configureButtonStyle}
      target='_blank'
    >
      Configure module
    </a>
  )
}

function CommandHistory({ history }) {
  if (!history.length) {
    return <div className='black-60'>No commands recorded.</div>
  }
  return (
    <div className='ba b--black-10 br2 overflow-hidden'>
      {history.map((entry) => (
        <div key={entry.id} className='pa2 bb b--black-05'>
          <div className='flex flex-column flex-row-l justify-between-l'>
            <div className='fw6'>
              {entry.command}
              {entry.status ? ` | ${entry.status}` : ''}
            </div>
            <div className='f7 black-60 mt1 mt0-l'>{entry.at ? formatDateTime(entry.at) : ''}</div>
          </div>
          <div className='f6 black-70 mt1'>
            {[entry.methodId, entry.portId, entry.summary].filter(Boolean).join(' | ')}
          </div>
        </div>
      ))}
    </div>
  )
}

function ModuleOptionControl({ moduleId, option, onRefresh }) {
  const [value, setValue] = useState(option.value || '')
  const [status, setStatus] = useState('')

  useEffect(() => {
    setValue(option.value || '')
    setStatus('')
  }, [option.key, option.value])

  return (
    <label className='db mb3'>
      <span className='db f6 ttu tracked black-60 mb2'>{option.label}</span>
      <div className='flex'>
        <input
          className='input-reset ba b--black-20 br2 pa2 w-100'
          value={value}
          readOnly={option.readOnly}
          min={option.min}
          max={option.max}
          title={option.description || option.key}
          onChange={(event) => setValue(event.target.value)}
        />
        {!option.readOnly && (
          <button
            type='button'
            className='button-reset ba b--black-20 bg-white br2 pv2 ph3 ml2 pointer hover-bg-near-white'
            onClick={async () => {
              setStatus('saving')
              await updateModuleRuntimeOption(
                runtimeBaseUrl(),
                moduleId,
                option.key,
                value
              )
              setStatus('saved')
              await onRefresh()
            }}
          >
            Apply
          </button>
        )}
      </div>
      <span className='db f6 black-60 mt2'>
        {[
          option.units,
          option.persistence,
          option.restartRequired ? 'restart required' : '',
          status
        ]
          .filter(Boolean)
          .join(' | ')}
      </span>
    </label>
  )
}

function RuntimeLink({ href, label }) {
  return (
    <a
      className='dib br2 bg-near-white ba b--black-10 f6 pv1 ph2 mr2 mb2 link black'
      href={href}
    >
      {label}
    </a>
  )
}

function DetailSection({ title, children }) {
  return (
    <section className='mb4'>
      <h3 className='f5 mt0 mb3'>{title}</h3>
      {children}
    </section>
  )
}

function KeyValue({ label, value }) {
  return (
    <div className='pa2 ba b--black-10 br2'>
      <div className='f6 ttu tracked black-60'>{label}</div>
      <div className='fw6 mt2 truncate' title={String(value)}>
        {value}
      </div>
    </div>
  )
}

function SimpleList({ items, empty }) {
  if (!items.length) {
    return <div className='black-60'>{empty}</div>
  }
  return (
    <div className='ba b--black-10 br2 overflow-hidden'>
      {items.map((item) => (
        <div key={item.key} className='pa2 bb b--black-05'>
          <div className='fw6 truncate' title={item.primary}>
            {item.primary}
          </div>
          {item.secondary && (
            <div className='f6 black-60 mt1 truncate' title={item.secondary}>
              {item.secondary}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

function EmptyDetail() {
  return (
    <section className='ba b--black-10 br2 bg-white pa3 black-60'>
      Select a module.
    </section>
  )
}

function StatusPill({ status }) {
  const normalized = String(status || 'unknown').toLowerCase()
  const className =
    normalized === 'running'
      ? 'bg-washed-green dark-green b--green'
      : normalized === 'updated'
        ? 'bg-washed-yellow brown b--gold'
        : normalized === 'error'
          ? 'bg-washed-red dark-red b--red'
          : 'bg-near-white black-60 b--black-20'
  return (
    <span className={`dib br-pill ba ${className}`} style={statusPillStyle}>
      {normalized}
    </span>
  )
}

function filterModuleBySearch(module, search) {
  const query = String(search || '').trim().toLowerCase()
  if (!query) return true
  return [
    module.id,
    module.status,
    module.version,
    module.manifest?.name,
    module.manifest?.version
  ]
    .filter(Boolean)
    .some((value) => String(value).toLowerCase().includes(query))
}

function moduleConfigureUrl(module) {
  return `/modules/${encodeURIComponent(module.id)}/configure`
}

function runtimeBaseUrl() {
  const configured =
    typeof window !== 'undefined' ? window.__SDN_CONFIG__?.serverBaseUrl : ''
  return String(configured || window.location.origin || '').replace(/\/+$/, '')
}

function formatBytes(value) {
  const bytes = Number(value || 0)
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return '0 B'
  }
  const units = ['B', 'KB', 'MB', 'GB']
  let unit = 0
  let next = bytes
  while (next >= 1024 && unit < units.length - 1) {
    next /= 1024
    unit += 1
  }
  return `${next.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

function formatNumber(value) {
  return Number(value || 0).toLocaleString()
}

function formatDuration(ms) {
  const value = Number(ms || 0)
  if (!Number.isFinite(value) || value <= 0) {
    return '0s'
  }
  const seconds = Math.floor(value / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

function formatClock(timestamp) {
  return new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

function formatDateTime(timestamp) {
  return new Date(timestamp).toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

const pageStyle = {
  height: 'calc(100vh - 1.5rem)',
  minHeight: '42rem',
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column'
}

const contentLayoutStyle = {
  minHeight: 0,
  flex: '1 1 auto'
}

const moduleListPanelStyle = {
  minHeight: 0,
  display: 'flex',
  flexDirection: 'column'
}

const panelHeaderStyle = {
  flex: '0 0 auto'
}

const moduleListBodyStyle = {
  flex: '1 1 auto',
  minHeight: 0,
  overflowY: 'auto'
}

const modulesTableStyle = {
  tableLayout: 'fixed'
}

const moduleNameColumnStyle = {
  width: 'auto'
}

const moduleStatusColumnStyle = {
  width: '5.75rem'
}

const moduleMemoryColumnStyle = {
  width: '5.5rem'
}

const moduleNameCellStyle = {
  minWidth: 0
}

const moduleStatusCellStyle = {
  whiteSpace: 'nowrap'
}

const moduleMemoryCellStyle = {
  whiteSpace: 'nowrap'
}

const moduleDetailPanelStyle = {
  minHeight: 0,
  display: 'flex'
}

const detailPanelStyle = {
  height: '100%',
  minHeight: 0,
  display: 'flex',
  flexDirection: 'column'
}

const detailPanelHeaderStyle = {
  flex: '0 0 auto'
}

const detailActionPanelStyle = {
  maxWidth: '100%',
  overflowX: 'auto'
}

const detailPanelBodyStyle = {
  flex: '1 1 auto',
  minHeight: 0,
  overflowY: 'auto'
}

const stickyTableHeaderStyle = {
  position: 'sticky',
  top: 0,
  zIndex: 1
}

const summaryGridStyle = {
  display: 'grid',
  gap: '1rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(10rem, 1fr))',
  flex: '0 0 auto'
}

const detailGridStyle = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(9rem, 1fr))'
}

const helpButtonStyle = {
  width: '1.5rem',
  height: '1.5rem',
  lineHeight: 1
}

const helpPopoverStyle = {
  width: '18rem',
  left: 0,
  top: '2rem'
}

const lifecycleActionBarStyle = {
  flexWrap: 'nowrap',
  maxWidth: '100%',
  overflowX: 'auto',
  paddingBottom: '0.125rem'
}

const lifecycleActionButtonBaseStyle = {
  flex: '0 0 auto',
  fontWeight: 600,
  whiteSpace: 'nowrap'
}

const configureButtonStyle = {
  borderColor: '#0b6b70',
  backgroundColor: '#0b6b70',
  color: '#ffffff',
  fontWeight: 600
}

const statusPillStyle = {
  fontSize: '0.7rem',
  lineHeight: 1,
  padding: '0.18rem 0.45rem',
  whiteSpace: 'nowrap'
}

export default ModulesPage
