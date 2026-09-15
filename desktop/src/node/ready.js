// @ts-check
//
// The start-up state machine, as a pure reducer.
//
// A node that is coming up looks like three different failures if you only
// watch the socket: connection refused while it binds, 503 while it opens the
// store, and a dead child if it refuses to start at all. This turns that into
// one phase the window and the tray can both render, and it does it without
// Electron, timers or sockets so it can be tested directly.
//
//   starting   the child is alive, the admin listener is not answering yet
//   hydrating  the listener answers, readiness reports a component still coming up
//   ready      GET /api/v1/ready returned 200
//   failed     the child exited, or start-up ran out of time
//   stopped    we asked it to stop

const PHASES = Object.freeze({
  STARTING: 'starting',
  HYDRATING: 'hydrating',
  READY: 'ready',
  FAILED: 'failed',
  STOPPED: 'stopped'
})

// A node with a cold store can spend minutes replaying records before it
// answers 200, and writing to it while it does that breaks hydration, so
// "hydrating" is a patient state: only the pre-listener phase is bounded.
const DEFAULT_LISTEN_TIMEOUT_MS = 120_000

/**
 * @typedef {object} ReadyState
 * @property {string} phase
 * @property {string} detail        one human-readable line about the phase
 * @property {number} startedAt     ms timestamp of the spawn
 * @property {number} changedAt     ms timestamp of the last phase change
 * @property {number} probes        readiness probes made so far
 * @property {boolean} listening    the admin listener has answered at least once
 * @property {string[]} log         last log lines, shown on failure
 */

/**
 * @param {number} now
 * @returns {ReadyState}
 */
function initialState (now = 0) {
  return {
    phase: PHASES.STARTING,
    detail: 'starting the node',
    startedAt: now,
    changedAt: now,
    probes: 0,
    listening: false,
    log: []
  }
}

/**
 * How many log lines a failure carries. Enough to show the reason, few enough
 * to fit a dialog.
 */
const LOG_TAIL = 30

/**
 * @param {ReadyState} state
 * @param {object} event
 * @param {string} event.type  'probe' | 'exit' | 'log' | 'stopping' | 'restarting'
 * @param {number} [event.at]
 * @param {number} [event.status]     HTTP status for a 'probe'
 * @param {string} [event.body]       response body for a 'probe'
 * @param {string} [event.error]      transport error for a 'probe'
 * @param {number|null} [event.code]  exit code for an 'exit'
 * @param {string} [event.signal]     exit signal for an 'exit'
 * @param {string} [event.line]       one output line for a 'log'
 * @param {object} [options]
 * @param {number} [options.listenTimeoutMs]
 * @returns {ReadyState}
 */
function reduce (state, event, options = {}) {
  const at = event.at ?? state.changedAt
  const listenTimeoutMs = options.listenTimeoutMs ?? DEFAULT_LISTEN_TIMEOUT_MS

  switch (event.type) {
    case 'log': {
      if (!event.line) return state
      const log = [...state.log, event.line].slice(-LOG_TAIL)
      return { ...state, log }
    }
    case 'stopping':
      return transition(state, PHASES.STOPPED, 'stopping the node', at)
    case 'restarting':
      return { ...initialState(at), log: state.log }
    case 'exit': {
      if (state.phase === PHASES.STOPPED) return state
      const how = event.signal ? `signal ${event.signal}` : `exit code ${event.code ?? 'unknown'}`
      return transition(state, PHASES.FAILED, `the node stopped (${how})`, at)
    }
    case 'probe': {
      if (state.phase === PHASES.STOPPED || state.phase === PHASES.FAILED) return state
      const probed = { ...state, probes: state.probes + 1 }
      if (event.status === 200) {
        return transition({ ...probed, listening: true }, PHASES.READY, 'the node is ready', at)
      }
      if (typeof event.status === 'number') {
        // Any answer at all means the listener is up; 503 carries the reason.
        const detail = readyReason(event.body) || `readiness answered ${event.status}`
        return transition({ ...probed, listening: true }, PHASES.HYDRATING, detail, at)
      }
      if (probed.listening) {
        // It answered before and does not now: it is busy, not gone. The child
        // exiting is what turns this into a failure.
        return { ...probed, phase: PHASES.HYDRATING, detail: 'waiting for the node to answer again' }
      }
      if (at - probed.startedAt >= listenTimeoutMs) {
        return transition(probed, PHASES.FAILED,
          `the node did not open its admin listener within ${Math.round(listenTimeoutMs / 1000)}s`, at)
      }
      return { ...probed, detail: 'starting the node' }
    }
    default:
      return state
  }
}

/**
 * Pull the component name out of `not ready: <reason>`.
 *
 * @param {string|undefined} body
 */
function readyReason (body) {
  if (!body) return ''
  const line = body.split('\n')[0].trim()
  const match = /^not ready:\s*(.+)$/i.exec(line)
  return match ? match[1] : ''
}

/**
 * @param {ReadyState} state
 * @param {string} phase
 * @param {string} detail
 * @param {number} at
 * @returns {ReadyState}
 */
function transition (state, phase, detail, at) {
  if (state.phase === phase && state.detail === detail) return state
  return { ...state, phase, detail, changedAt: state.phase === phase ? state.changedAt : at }
}

module.exports = { PHASES, DEFAULT_LISTEN_TIMEOUT_MS, LOG_TAIL, initialState, reduce, readyReason }
