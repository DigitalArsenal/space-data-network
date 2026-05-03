import React, { useEffect, useMemo, useState } from 'react'

import {
  emptyModuleRuntimeSnapshot,
  loadModuleRuntimeSnapshotFromServer,
  resolveSelectedModuleId,
  runModuleRuntimeAction,
  saveModuleRuntimeInputValues,
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

  const modules = snapshot.modules
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

  useEffect(lockModulesBodyScroll, [])

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

      <section className='sdn-modules-content flex flex-column flex-row-l' style={contentLayoutStyle}>
        <section
          className='sdn-modules-list-panel w-100 w-40-l ba b--black-10 br2 bg-white overflow-hidden'
          style={moduleListPanelStyle}
        >
          <div className='pa3 bb b--black-10 flex items-center justify-between' style={panelHeaderStyle}>
            <div className='flex items-center'>
              <h2 className='f4 mv0 mr2'>Modules</h2>
              <ModulesHelp />
            </div>
            <span className='f6 black-60'>
              {lastRefresh ? formatClock(lastRefresh) : ''}
            </span>
          </div>
          <div className='overflow-auto' style={moduleListBodyStyle}>
            <table className='collapse w-100'>
              <thead style={stickyTableHeaderStyle}>
                <tr className='tl f6 ttu tracked black-60 bg-white'>
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

        <section className='sdn-modules-detail-panel w-100 w-60-l' style={moduleDetailPanelStyle}>
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

function lockModulesBodyScroll() {
  if (
    typeof document === 'undefined' ||
    !document.body ||
    !document.documentElement
  ) {
    return undefined
  }

  const previousBodyOverflow = document.body.style.overflow
  const previousDocumentOverflow = document.documentElement.style.overflow
  const previousBodyOverscroll = document.body.style.overscrollBehavior
  const previousDocumentOverscroll = document.documentElement.style.overscrollBehavior

  document.body.style.overflow = 'hidden'
  document.documentElement.style.overflow = 'hidden'
  document.body.style.overscrollBehavior = 'none'
  document.documentElement.style.overscrollBehavior = 'none'

  return () => {
    document.body.style.overflow = previousBodyOverflow
    document.documentElement.style.overflow = previousDocumentOverflow
    document.body.style.overscrollBehavior = previousBodyOverscroll
    document.documentElement.style.overscrollBehavior =
      previousDocumentOverscroll
  }
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
          <div className='mt2 mt0-l'>
            <StatusPill status={module.status} />
          </div>
        </div>
        {module.statusMessage && (
          <div className='mb3 dark-red'>{module.statusMessage}</div>
        )}
        <LifecycleActionBar module={module} onRefresh={onRefresh} />
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

        <DetailSection title='Methods'>
          <MethodList module={module} methods={manifest?.methods ?? []} onRefresh={onRefresh} />
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
    <div className='flex flex-wrap items-center'>
      {actions.map((action) => (
        <button
          key={action.actionId}
          type='button'
          className='button-reset ba br2 pv2 ph3 mr2 mb2 pointer disabled'
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
    borderColor: palette.border,
    backgroundColor: disabled ? '#f4f4f4' : palette.background,
    color: disabled ? '#777777' : palette.color,
    cursor: disabled ? 'not-allowed' : 'pointer',
    opacity: disabled ? 0.72 : 1
  }
}

function lifecycleActionPalette(actionId) {
  if (actionId === 'restart' || actionId === 'reload-manifest') {
    return { background: '#fff7ed', border: '#f97316', color: '#9a3412' }
  }
  if (actionId === 'start' || actionId === 'load') {
    return { background: '#ecfdf5', border: '#10b981', color: '#065f46' }
  }
  if (actionId === 'pause') {
    return { background: '#fefce8', border: '#eab308', color: '#713f12' }
  }
  if (actionId === 'stop' || actionId === 'unload') {
    return { background: '#fef2f2', border: '#ef4444', color: '#991b1b' }
  }
  if (actionId === 'clear-error') {
    return { background: '#eff6ff', border: '#3b82f6', color: '#1d4ed8' }
  }
  return { background: '#ffffff', border: '#d0d0d0', color: '#111111' }
}

function MethodList({ module, methods, onRefresh }) {
  if (!methods.length) {
    return <div className='black-60'>No methods reported.</div>
  }
  return (
    <div>
      {methods.map((method) => (
        <details key={method.methodId} className='ba b--black-10 br2 bg-white mb3' open>
          <summary className='pa3 pointer bg-near-white bb b--black-10'>
            <div className='flex flex-column flex-row-l justify-between-l'>
              <div className='pr3-l'>
                <span className='dib f7 ttu tracked br2 bg-black-80 white pv1 ph2 mr2'>
                  METHOD
                </span>
                <span className='fw6'>{method.displayName || method.methodId}</span>
                {method.description && (
                  <div className='f6 black-60 mt2' title={method.description}>
                    {method.description}
                  </div>
                )}
              </div>
              <div className='f6 black-60 mt2 mt0-l tr-l'>
                {[
                  method.drainPolicy,
                  method.maxBatch ? `batch ${method.maxBatch}` : ''
                ]
                  .filter(Boolean)
                  .join(' | ')}
              </div>
            </div>
          </summary>
          <div className='pa3'>
            <div className='grid' style={operationGridStyle}>
              <div>
                <h4 className='f5 mt0 mb3'>Inputs</h4>
                <MethodInputForm module={module} method={method} onRefresh={onRefresh} />
              </div>
              <div>
                <h4 className='f5 mt0 mb3'>Outputs</h4>
                <PortSchemaList ports={method.outputPorts ?? []} empty='No output ports reported.' />
              </div>
            </div>
          </div>
        </details>
      ))}
    </div>
  )
}

function MethodInputForm({ module, method, onRefresh }) {
  const ports = method.inputPorts ?? []
  const [drafts, setDrafts] = useState(() => buildInitialInputDrafts(module, method))
  const [status, setStatus] = useState('')

  useEffect(() => {
    setDrafts(buildInitialInputDrafts(module, method))
    setStatus('')
  }, [module.id, module.inputValues, method.methodId])

  if (!ports.length) {
    return <div className='black-60'>No input ports reported.</div>
  }

  const values = ports.map((port) => drafts[port.portId]).filter(Boolean)
  const canSave = values.some((value) => String(value.value || '').trim() !== '')

  return (
    <form
      onSubmit={async (event) => {
        event.preventDefault()
        setStatus('saving')
        await saveModuleRuntimeInputValues(runtimeBaseUrl(), module.id, values)
        setStatus('saved')
        await onRefresh()
      }}
    >
      {ports.map((port) => {
        const draft = drafts[port.portId] ?? buildInputDraft(module, method, port)
        const wireFormats = acceptedWireFormats(port)
        return (
          <section key={port.portId} className='ba b--black-10 br2 mb3 overflow-hidden'>
            <header className='pa2 bg-near-white bb b--black-10'>
              <div className='fw6'>{port.displayName || port.portId}</div>
              <div className='f7 black-60 mt1'>
                {[port.required ? 'required' : 'optional', cardinalityLabel(port)]
                  .filter(Boolean)
                  .join(' | ')}
              </div>
            </header>
            <div className='pa3'>
              <PortSchemaList ports={[port]} empty='' compact />
              <div className='grid mt3' style={formGridStyle}>
                <label className='db'>
                  <span className='db f7 ttu tracked black-60 mb1'>Wire format</span>
                  <select
                    className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
                    value={draft.wireFormat}
                    onChange={(event) =>
                      updateDraft(setDrafts, port.portId, {
                        wireFormat: event.target.value,
                        encoding: defaultEncodingForWireFormat(event.target.value)
                      })
                    }
                  >
                    {wireFormats.map((format) => (
                      <option key={format} value={format}>
                        {format}
                      </option>
                    ))}
                  </select>
                </label>
                <label className='db'>
                  <span className='db f7 ttu tracked black-60 mb1'>Encoding</span>
                  <select
                    className='input-reset ba b--black-20 br2 pa2 w-100 bg-white'
                    value={draft.encoding}
                    onChange={(event) =>
                      updateDraft(setDrafts, port.portId, {
                        encoding: event.target.value
                      })
                    }
                  >
                    <option value='json'>json</option>
                    <option value='text'>text</option>
                    <option value='base64'>base64</option>
                    <option value='hex'>hex</option>
                  </select>
                </label>
              </div>
              <label className='db mt3'>
                <span className='db f7 ttu tracked black-60 mb1'>Value</span>
                <textarea
                  className='input-reset ba b--black-20 br2 pa2 w-100 code'
                  rows={6}
                  value={draft.value}
                  onChange={(event) =>
                    updateDraft(setDrafts, port.portId, {
                      value: event.target.value
                    })
                  }
                  placeholder={defaultValuePlaceholder(draft)}
                />
              </label>
            </div>
          </section>
        )
      })}
      <div className='flex items-center'>
        <button
          type='submit'
          className='button-reset ba b--blue bg-white blue br2 pv2 ph3 pointer hover-bg-near-white'
          disabled={!canSave || status === 'saving'}
        >
          Save inputs
        </button>
        <span className='ml3 f6 black-60'>
          {status === 'saving'
            ? 'Saving...'
            : status === 'saved'
              ? 'Saved. Restart to apply.'
              : module.restartPending
                ? 'Restart pending.'
                : ''}
        </span>
      </div>
    </form>
  )
}

function PortSchemaList({ ports, empty, compact = false }) {
  if (!ports.length) {
    return empty ? <div className='black-60'>{empty}</div> : null
  }
  return (
    <div className='flex flex-column'>
      {ports.map((port) => (
        <section key={port.portId} className={compact ? 'mb2' : 'ba b--black-10 br2 mb3 overflow-hidden'}>
          {!compact && (
            <header className='pa2 bg-near-white bb b--black-10'>
              <div className='fw6'>{port.displayName || port.portId}</div>
              <div className='f7 black-60 mt1'>
                {[port.required ? 'required' : 'optional', cardinalityLabel(port)]
                  .filter(Boolean)
                  .join(' | ')}
              </div>
            </header>
          )}
          <div className={compact ? '' : 'pa3'}>
            {port.description && <div className='f6 black-70 mb2'>{port.description}</div>}
            {(port.acceptedTypeSets ?? []).length > 0 ? (
              (port.acceptedTypeSets ?? []).map((set, index) => (
                <div key={set.setId || index} className='mb2'>
                  <div className='flex flex-wrap'>
                    {(set.allowedWireFormats ?? []).map((format) => (
                      <span key={format} className='dib br2 bg-lightest-blue blue ba b--blue f7 pv1 ph2 mr2 mb2'>
                        {format}
                      </span>
                    ))}
                    {set.setId && (
                      <span className='dib br2 bg-near-white ba b--black-10 f7 pv1 ph2 mr2 mb2'>
                        {set.setId}
                      </span>
                    )}
                  </div>
                  {(set.allowedTypes ?? []).map((typeRef, typeIndex) => (
                    <div key={`${typeRef.schemaName || ''}-${typeRef.rootType || ''}-${typeIndex}`} className='pa2 bg-near-white br2 mb2'>
                      <SchemaKeyValue label='Schema' value={typeRef.schemaName} />
                      <SchemaKeyValue label='Root type' value={typeRef.rootType} />
                      <SchemaKeyValue label='File id' value={typeRef.fileIdentifier} />
                      <SchemaKeyValue label='Version' value={typeRef.schemaVersion} />
                    </div>
                  ))}
                  {set.description && <div className='f7 black-60'>{set.description}</div>}
                </div>
              ))
            ) : (
              <div className='black-60'>No schema metadata reported.</div>
            )}
          </div>
        </section>
      ))}
    </div>
  )
}

function SchemaKeyValue({ label, value }) {
  if (!value) {
    return null
  }
  return (
    <div className='flex justify-between f7 mb1'>
      <span className='black-60 mr2'>{label}</span>
      <span className='code tr truncate' title={value}>{value}</span>
    </div>
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

function buildInitialInputDrafts(module, method) {
  return (method.inputPorts ?? []).reduce((acc, port) => {
    acc[port.portId] = buildInputDraft(module, method, port)
    return acc
  }, {})
}

function buildInputDraft(module, method, port) {
  const saved = (module.inputValues ?? []).find(
    (value) => value.methodId === method.methodId && value.portId === port.portId
  )
  const typeRef = firstAcceptedType(port)
  const wireFormat = saved?.wireFormat || acceptedWireFormats(port)[0] || 'JSON'
  return {
    methodId: method.methodId,
    portId: port.portId,
    wireFormat,
    encoding: saved?.encoding || defaultEncodingForWireFormat(wireFormat),
    schemaName: saved?.schemaName || typeRef?.schemaName || '',
    fileIdentifier: saved?.fileIdentifier || typeRef?.fileIdentifier || '',
    schemaVersion: saved?.schemaVersion || typeRef?.schemaVersion || '',
    rootType: saved?.rootType || typeRef?.rootType || '',
    value: saved?.value || defaultInputValueForWireFormat(wireFormat)
  }
}

function updateDraft(setDrafts, portId, patch) {
  setDrafts((previous) => ({
    ...previous,
    [portId]: {
      ...previous[portId],
      ...patch
    }
  }))
}

function acceptedWireFormats(port) {
  const formats = (port.acceptedTypeSets ?? []).flatMap(
    (set) => set.allowedWireFormats ?? []
  )
  return formats.length > 0 ? Array.from(new Set(formats)) : ['JSON']
}

function firstAcceptedType(port) {
  for (const set of port.acceptedTypeSets ?? []) {
    const typeRef = set.allowedTypes?.[0]
    if (typeRef) return typeRef
  }
  return null
}

function defaultEncodingForWireFormat(wireFormat) {
  return String(wireFormat || '').toUpperCase().includes('JSON') ? 'json' : 'text'
}

function defaultInputValueForWireFormat(wireFormat) {
  return String(wireFormat || '').toUpperCase().includes('JSON') ? '{}' : ''
}

function defaultValuePlaceholder(draft) {
  if (draft.encoding === 'json') {
    return '{ "field": "value" }'
  }
  if (draft.encoding === 'base64') {
    return 'base64 payload'
  }
  if (draft.encoding === 'hex') {
    return '00ff'
  }
  return 'value'
}

function cardinalityLabel(port) {
  if (!port.minStreams && !port.maxStreams) {
    return ''
  }
  return `${port.minStreams || 0}-${port.maxStreams || '*'} streams`
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

const pageStyle = {
  width: '100%',
  height: 'calc(100vh - 75px)',
  maxHeight: 'calc(100vh - 75px)',
  minHeight: 0,
  boxSizing: 'border-box',
  overflow: 'hidden',
  overflowX: 'hidden',
  display: 'flex',
  flexDirection: 'column'
}

const contentLayoutStyle = {
  width: '100%',
  minHeight: 0,
  flex: '1 1 auto',
  overflow: 'hidden',
  gap: '1rem'
}

const moduleListPanelStyle = {
  width: '100%',
  minWidth: 0,
  minHeight: 0,
  display: 'flex',
  flexDirection: 'column',
  flex: '0 0 min(32rem, 40%)'
}

const panelHeaderStyle = {
  flex: '0 0 auto'
}

const moduleListBodyStyle = {
  flex: '1 1 auto',
  minHeight: 0,
  overflowX: 'hidden',
  overflowY: 'auto'
}

const moduleDetailPanelStyle = {
  width: '100%',
  minWidth: 0,
  minHeight: 0,
  display: 'flex',
  flex: '1 1 0'
}

const detailPanelStyle = {
  width: '100%',
  height: '100%',
  minHeight: 0,
  boxSizing: 'border-box',
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column'
}

const detailPanelHeaderStyle = {
  flex: '0 0 auto'
}

const detailPanelBodyStyle = {
  flex: '1 1 auto',
  minHeight: 0,
  overflowX: 'hidden',
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

const operationGridStyle = {
  display: 'grid',
  gap: '1rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(16rem, 1fr))'
}

const formGridStyle = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(10rem, 1fr))'
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

export default ModulesPage
