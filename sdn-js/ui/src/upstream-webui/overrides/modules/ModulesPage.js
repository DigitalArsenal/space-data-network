import React, { useEffect, useMemo, useState } from 'react'

import {
  emptyModuleRuntimeSnapshot,
  loadModuleRuntimeSnapshotFromServer,
  runModuleRuntimeAction,
  updateModuleRuntimeOption
} from '../../../../../src/ui/runtime/modules.js'

function ModulesPage() {
  const [snapshot, setSnapshot] = useState(emptyModuleRuntimeSnapshot())
  const [status, setStatus] = useState('loading')
  const [error, setError] = useState(null)
  const [selectedId, setSelectedId] = useState('')
  const [lastRefresh, setLastRefresh] = useState(null)

  const modules = snapshot.modules
  const selectedModule = useMemo(() => {
    return (
      modules.find((module) => module.id === selectedId) ?? modules[0] ?? null
    )
  }, [modules, selectedId])

  const counts = useMemo(() => {
    return modules.reduce(
      (acc, module) => {
        acc.total += 1
        if (module.status === 'running') acc.running += 1
        if (module.status === 'error') acc.error += 1
        acc.memoryBytes += module.stats?.memoryBytes ?? 0
        return acc
      },
      { total: 0, running: 0, error: 0, memoryBytes: 0 }
    )
  }, [modules])

  async function refreshModules() {
    setStatus('loading')
    setError(null)
    try {
      const nextSnapshot =
        await loadModuleRuntimeSnapshotFromServer(runtimeBaseUrl())
      setSnapshot(nextSnapshot)
      setLastRefresh(Date.now())
      setStatus('ready')
      if (!selectedId && nextSnapshot.modules[0]) {
        setSelectedId(nextSnapshot.modules[0].id)
      }
    } catch (err) {
      setStatus('error')
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    let cancelled = false

    async function load() {
      setStatus('loading')
      setError(null)
      try {
        const nextSnapshot =
          await loadModuleRuntimeSnapshotFromServer(runtimeBaseUrl())
        if (!cancelled) {
          setSnapshot(nextSnapshot)
          setLastRefresh(Date.now())
          setStatus('ready')
          if (!selectedId && nextSnapshot.modules[0]) {
            setSelectedId(nextSnapshot.modules[0].id)
          }
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
    <main className='sdn-modules-page w-100 ph3 ph4-l pv3'>
      <header className='mb3 flex flex-column flex-row-l justify-between-l items-start-l'>
        <div className='pr4-l'>
          <h1 className='f2 f1-l mv0'>Modules</h1>
          <p className='mt2 mb0 f4 lh-copy black-70'>
            {status === 'loading' && modules.length === 0
              ? 'Loading runtime snapshot...'
              : `${counts.running} running of ${counts.total} loaded`}
          </p>
        </div>
        <div className='mt3 mt0-l flex items-center'>
          <button
            type='button'
            className='button-reset ba b--black-20 bg-white br2 pv2 ph3 pointer hover-bg-near-white'
            onClick={refreshModules}
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
        <SummaryMetric label='Errors' value={counts.error} />
        <SummaryMetric
          label='WASM memory'
          value={formatBytes(counts.memoryBytes)}
        />
      </section>

      <section className='flex flex-column flex-row-l'>
        <div className='w-100 w-40-l pr0 pr3-l mb3 mb0-l'>
          <section className='ba b--black-10 br2 bg-white overflow-hidden'>
            <div className='pa3 bb b--black-10 flex items-center justify-between'>
              <h2 className='f4 mv0'>Runtime modules</h2>
              <span className='f6 black-60'>
                {lastRefresh ? formatClock(lastRefresh) : ''}
              </span>
            </div>
            <div className='overflow-auto'>
              <table className='collapse w-100'>
                <thead>
                  <tr className='tl f6 ttu tracked black-60'>
                    <th className='pa2 fw6'>Module</th>
                    <th className='pa2 fw6'>Status</th>
                    <th className='pa2 fw6 tr'>Memory</th>
                  </tr>
                </thead>
                <tbody>
                  {modules.map((module) => (
                    <tr
                      key={module.id}
                      className={`${selectedModule?.id === module.id ? 'bg-lightest-blue' : 'bg-white'} pointer hover-bg-near-white`}
                      onClick={() => setSelectedId(module.id)}
                    >
                      <td className='pa2 bb b--black-05'>
                        <div className='fw6 truncate' title={module.id}>
                          {module.manifest?.name || module.id}
                        </div>
                        <div className='f6 black-60 truncate'>
                          {module.version ||
                            module.manifest?.version ||
                            'unversioned'}
                        </div>
                      </td>
                      <td className='pa2 bb b--black-05'>
                        <StatusPill status={module.status} />
                      </td>
                      <td className='pa2 bb b--black-05 tr'>
                        {formatBytes(module.stats?.memoryBytes ?? 0)}
                      </td>
                    </tr>
                  ))}
                  {modules.length === 0 && (
                    <tr>
                      <td className='pa3 black-60' colSpan={3}>
                        No runtime modules reported.
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </section>
        </div>

        <div className='w-100 w-60-l'>
          {selectedModule ? (
            <ModuleDetail module={selectedModule} onRefresh={refreshModules} />
          ) : (
            <EmptyDetail />
          )}
        </div>
      </section>
    </main>
  )
}

function SummaryMetric({ label, value }) {
  return (
    <section className='ba b--black-10 br2 bg-white pa3'>
      <div className='f6 ttu tracked black-60'>{label}</div>
      <div className='f3 fw6 mt1'>{value}</div>
    </section>
  )
}

function ModuleDetail({ module, onRefresh }) {
  const manifest = module.manifest
  return (
    <section className='ba b--black-10 br2 bg-white'>
      <header className='pa3 bb b--black-10'>
        <div className='flex flex-column flex-row-l justify-between-l items-start-l'>
          <div className='pr3-l'>
            <h2 className='f3 mv0'>{manifest?.name || module.id}</h2>
            <div className='f6 black-60 mt1'>
              {module.id}
              {module.version ? ` @ ${module.version}` : ''}
            </div>
          </div>
          <div className='mt2 mt0-l'>
            <StatusPill status={module.status} />
          </div>
        </div>
        {module.statusMessage && (
          <div className='mt2 dark-red'>{module.statusMessage}</div>
        )}
      </header>

      <div className='pa3'>
        <DetailSection title='Stats'>
          <div className='grid' style={detailGridStyle}>
            <KeyValue
              label='Memory pages'
              value={formatNumber(module.stats?.memoryPages ?? 0)}
            />
            <KeyValue
              label='Memory bytes'
              value={formatBytes(module.stats?.memoryBytes ?? 0)}
            />
            <KeyValue
              label='Memory limit'
              value={formatBytes(module.stats?.maxMemoryBytes ?? 0)}
            />
            <KeyValue
              label='Host RSS'
              value={formatBytes(module.stats?.hostRssBytes ?? 0)}
            />
            <KeyValue
              label='Uptime'
              value={formatDuration(module.stats?.uptimeMs ?? 0)}
            />
            <KeyValue
              label='Invokes'
              value={formatNumber(module.stats?.invokeCount ?? 0)}
            />
            <KeyValue
              label='Errors'
              value={formatNumber(module.stats?.errorCount ?? 0)}
            />
            <KeyValue
              label='Avg latency'
              value={`${formatNumber(module.stats?.averageLatencyMs ?? 0)} ms`}
            />
            <KeyValue
              label='Timers'
              value={formatNumber(module.stats?.timerRunCount ?? 0)}
            />
          </div>
        </DetailSection>

        <DetailSection title='Lifecycle'>
          <div className='flex flex-wrap'>
            {(module.actions ?? []).map((action) => (
              <button
                key={action.actionId}
                type='button'
                className='button-reset ba b--black-20 bg-white br2 pv2 ph3 mr2 mb2 pointer hover-bg-near-white disabled'
                disabled={!action.enabled}
                title={action.description || action.label}
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
            {(!module.actions || module.actions.length === 0) && (
              <span className='black-60'>No lifecycle actions reported.</span>
            )}
          </div>
        </DetailSection>

        <DetailSection title='Methods'>
          <MethodList methods={manifest?.methods ?? []} />
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
      </div>
    </section>
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
      <span className='db f6 ttu tracked black-60 mb1'>{option.label}</span>
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
      <span className='db f6 black-60 mt1'>
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
      <h3 className='f5 mv0 mb2'>{title}</h3>
      {children}
    </section>
  )
}

function KeyValue({ label, value }) {
  return (
    <div className='pa2 ba b--black-10 br2'>
      <div className='f6 ttu tracked black-60'>{label}</div>
      <div className='fw6 mt1 truncate' title={String(value)}>
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
            <div className='f6 black-60 truncate' title={item.secondary}>
              {item.secondary}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}

function MethodList({ methods }) {
  if (!methods.length) {
    return <div className='black-60'>No methods reported.</div>
  }
  return (
    <div className='ba b--black-10 br2 overflow-hidden'>
      {methods.map((method) => (
        <div key={method.methodId} className='pa2 bb b--black-05'>
          <div className='flex flex-column flex-row-l justify-between-l'>
            <div className='pr3-l'>
              <div
                className='fw6 truncate'
                title={method.displayName || method.methodId}
              >
                {method.displayName || method.methodId}
              </div>
              {method.description && (
                <div
                  className='f6 black-60 truncate'
                  title={method.description}
                >
                  {method.description}
                </div>
              )}
            </div>
            <div className='f6 black-60 mt1 mt0-l tr-l'>
              {[
                method.drainPolicy,
                method.maxBatch ? `batch ${method.maxBatch}` : ''
              ]
                .filter(Boolean)
                .join(' | ')}
            </div>
          </div>
          <PortGroup label='Inputs' ports={method.inputPorts ?? []} />
          <PortGroup label='Outputs' ports={method.outputPorts ?? []} />
        </div>
      ))}
    </div>
  )
}

function PortGroup({ label, ports }) {
  if (!ports.length) {
    return null
  }
  return (
    <div className='mt2'>
      <div className='f6 ttu tracked black-60 mb1'>{label}</div>
      <div className='grid' style={portGridStyle}>
        {ports.map((port) => (
          <div
            key={port.portId}
            className='pa2 br2 bg-near-white ba b--black-10'
          >
            <div
              className='fw6 f6 truncate'
              title={port.displayName || port.portId}
            >
              {port.displayName || port.portId}
            </div>
            <div className='f7 black-60 mt1'>
              {[
                port.required ? 'required' : '',
                port.minStreams || port.maxStreams
                  ? `${port.minStreams || 0}-${port.maxStreams || '*'}`
                  : ''
              ]
                .filter(Boolean)
                .join(' | ')}
            </div>
            <TypeSetList sets={port.acceptedTypeSets ?? []} />
          </div>
        ))}
      </div>
    </div>
  )
}

function TypeSetList({ sets }) {
  if (!sets.length) {
    return null
  }
  return (
    <div className='mt2'>
      {sets.map((set, index) => (
        <div key={set.setId || index} className='f7 black-70 mb1'>
          <div className='truncate' title={formatTypeSet(set)}>
            {formatTypeSet(set)}
          </div>
          {set.allowedWireFormats?.length > 0 && (
            <div
              className='black-50 truncate'
              title={set.allowedWireFormats.join(', ')}
            >
              {set.allowedWireFormats.join(', ')}
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
      : normalized === 'error'
        ? 'bg-washed-red dark-red b--red'
        : 'bg-near-white black-60 b--black-20'
  return (
    <span className={`dib br-pill ba f6 pv1 ph2 ${className}`}>
      {normalized}
    </span>
  )
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

const summaryGridStyle = {
  display: 'grid',
  gap: '1rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(10rem, 1fr))'
}

const detailGridStyle = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(9rem, 1fr))'
}

const portGridStyle = {
  display: 'grid',
  gap: '0.5rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(12rem, 1fr))'
}

function formatTypeSet(set) {
  const types = (set.allowedTypes ?? [])
    .map((typeRef) => {
      return [typeRef.schemaName, typeRef.rootType].filter(Boolean).join(':')
    })
    .filter(Boolean)
  if (types.length > 0) {
    return types.join(', ')
  }
  return set.setId || 'untyped'
}

export default ModulesPage
