const { test, expect } = require('@playwright/test')

const { PHASES, initialState, reduce, readyReason } = require('../../src/node/ready')

test.describe('the node start-up state machine', () => {
  test('stays in starting while the admin listener refuses connections', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'probe', at: 500, error: 'connect ECONNREFUSED' })
    state = reduce(state, { type: 'probe', at: 1000, error: 'connect ECONNREFUSED' })

    expect(state.phase).toBe(PHASES.STARTING)
    expect(state.probes).toBe(2)
    expect(state.listening).toBe(false)
  })

  test('reports hydrating with the reason readiness gave', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'probe', at: 900, status: 503, body: 'not ready: engine still opening\n' })

    expect(state.phase).toBe(PHASES.HYDRATING)
    expect(state.detail).toBe('engine still opening')
    expect(state.listening).toBe(true)
  })

  test('becomes ready on a 200', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'probe', at: 900, status: 503, body: 'not ready: store opening' })
    state = reduce(state, { type: 'probe', at: 1400, status: 200, body: 'ready\n' })

    expect(state.phase).toBe(PHASES.READY)
    expect(state.changedAt).toBe(1400)
  })

  test('treats a listener that stops answering as busy, not as a failure', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'probe', at: 900, status: 503, body: 'not ready: store opening' })
    state = reduce(state, { type: 'probe', at: 1400, error: 'socket hang up' })

    expect(state.phase).toBe(PHASES.HYDRATING)
  })

  test('fails when the listener never opens inside the timeout', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'probe', at: 4000, error: 'ECONNREFUSED' }, { listenTimeoutMs: 5000 })
    expect(state.phase).toBe(PHASES.STARTING)

    state = reduce(state, { type: 'probe', at: 5000, error: 'ECONNREFUSED' }, { listenTimeoutMs: 5000 })
    expect(state.phase).toBe(PHASES.FAILED)
    expect(state.detail).toContain('5s')
  })

  test('fails when the child exits, and says how', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'exit', at: 300, code: 1, signal: '' })

    expect(state.phase).toBe(PHASES.FAILED)
    expect(state.detail).toContain('exit code 1')
  })

  test('does not turn a requested stop into a failure', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'stopping', at: 100 })
    state = reduce(state, { type: 'exit', at: 200, code: null, signal: 'SIGTERM' })

    expect(state.phase).toBe(PHASES.STOPPED)
  })

  test('keeps the last log lines for the failure screen', () => {
    let state = initialState(0)
    for (let i = 0; i < 40; i += 1) state = reduce(state, { type: 'log', line: `line ${i}` })
    state = reduce(state, { type: 'exit', at: 10, code: 2, signal: '' })

    expect(state.log).toHaveLength(30)
    expect(state.log[29]).toBe('line 39')
    expect(state.phase).toBe(PHASES.FAILED)
  })

  test('starts clean on a restart but keeps what the last run said', () => {
    let state = initialState(0)
    state = reduce(state, { type: 'log', line: 'boom' })
    state = reduce(state, { type: 'exit', at: 10, code: 1, signal: '' })
    state = reduce(state, { type: 'restarting', at: 20 })

    expect(state.phase).toBe(PHASES.STARTING)
    expect(state.startedAt).toBe(20)
    expect(state.log).toEqual(['boom'])
  })

  test('reads the failing component out of a readiness body', () => {
    expect(readyReason('not ready: libp2p host is down\n')).toBe('libp2p host is down')
    expect(readyReason('ready\n')).toBe('')
    expect(readyReason(undefined)).toBe('')
  })
})
