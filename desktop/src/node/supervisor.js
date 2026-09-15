// @ts-check
//
// Runs the bundled node and reports what it is doing.
//
// One child process, one config file, one readiness poll. Everything the shell
// shows -- the status window, the tray, the failure dialog -- is driven from
// the state this emits.
const { EventEmitter } = require('node:events')
const { createWriteStream, existsSync, mkdirSync } = require('node:fs')
const http = require('node:http')
const { execFile, spawn } = require('node:child_process')
const { join } = require('node:path')

const logger = require('../common/logger')
const { PHASES, initialState, reduce } = require('./ready')
const { ensureNodeConfig, nodeHome } = require('./config')
const { nodeEnv, resolveBundle } = require('./paths')

const PROBE_INTERVAL_MS = 500
const STOP_GRACE_MS = 30_000
const CLI_TIMEOUT_MS = 120_000

class NodeSupervisor extends EventEmitter {
  /**
   * @param {object} opts
   * @param {string} opts.userDataPath
   * @param {string} opts.appPath
   * @param {string} [opts.resourcesPath]
   * @param {string} opts.logsPath
   */
  constructor ({ userDataPath, appPath, resourcesPath, logsPath }) {
    super()
    this.userDataPath = userDataPath
    this.appPath = appPath
    this.resourcesPath = resourcesPath
    this.logsPath = logsPath
    /** @type {import('node:child_process').ChildProcess|null} */
    this.child = null
    /** @type {import('./ready').ReadyState} */
    this.state = initialState(Date.now())
    /** @type {NodeJS.Timeout|null} */
    this.timer = null
    /** @type {{ root: string, binary: string, version: string }|null} */
    this.bundle = null
    this.configPath = ''
    this.home = ''
    this.adminUrl = ''
    this.stopping = false
  }

  /** The URL of the node's own dashboard, once it is known. */
  get dashboardUrl () { return this.adminUrl }

  /** @param {object} event */
  apply (event) {
    const next = reduce(this.state, { at: Date.now(), ...event })
    if (next === this.state) return
    const phaseChanged = next.phase !== this.state.phase
    this.state = next
    if (phaseChanged) {
      logger.info(`[node] ${next.phase}: ${next.detail}`)
    }
    this.emit('state', this.snapshot())
  }

  snapshot () {
    return {
      phase: this.state.phase,
      detail: this.state.detail,
      log: this.state.log,
      url: this.adminUrl,
      version: this.bundle?.version ?? '',
      logsPath: this.logsPath
    }
  }

  /**
   * Prepare the node's home, identity and configuration, then run it.
   */
  async start () {
    this.stopping = false
    this.apply({ type: 'restarting' })
    try {
      this.bundle = resolveBundle({ resourcesPath: this.resourcesPath, appPath: this.appPath })
      logger.info(`[node] bundle ${this.bundle.root} (version ${this.bundle.version || 'unknown'})`)
      const config = await ensureNodeConfig({ userDataPath: this.userDataPath })
      this.configPath = config.path
      this.home = config.home
      if (config.created) logger.info(`[node] wrote first-run config ${config.path}`)
      await this.ensureIdentity()
      this.adminUrl = await this.readAdminUrl()
      this.spawnDaemon()
      this.poll()
    } catch (err) {
      logger.error(/** @type {Error} */(err))
      this.apply({ type: 'log', line: String(/** @type {Error} */(err).message ?? err) })
      this.apply({ type: 'exit', code: null, signal: '' })
    }
  }

  /**
   * `init` writes the configuration the daemon reads and creates the node's
   * encrypted mnemonic. It is a no-op once the identity exists, but running it
   * every time would still cost a WasmEdge start-up, so the mnemonic file is
   * the gate.
   */
  async ensureIdentity () {
    if (existsSync(join(nodeHome(this.userDataPath), 'keys', 'mnemonic'))) return
    logger.info('[node] no identity yet; running init')
    this.apply({ type: 'log', line: 'creating this node identity' })
    await this.runCli(['init', '--config', this.configPath])
  }

  /** Ask the node where its dashboard is, rather than parsing its config. */
  async readAdminUrl () {
    const { stdout } = await this.runCli(['open', '--config', this.configPath])
    const url = stdout.trim().split('\n').pop()?.trim() ?? ''
    if (!/^https?:\/\//.test(url)) throw new Error(`could not read the node URL (got ${JSON.stringify(url)})`)
    return url
  }

  /**
   * @param {string[]} args
   * @returns {Promise<{ stdout: string, stderr: string }>}
   */
  runCli (args) {
    const bundle = this.bundle
    if (!bundle) return Promise.reject(new Error('no node bundle resolved'))
    return new Promise((resolve, reject) => {
      execFile(bundle.binary, args, {
        env: nodeEnv(bundle.root),
        timeout: CLI_TIMEOUT_MS,
        maxBuffer: 4 << 20
      }, (err, stdout, stderr) => {
        if (err) {
          reject(new Error(`${args[0]} failed: ${String(stderr || err.message).trim()}`))
          return
        }
        resolve({ stdout: String(stdout), stderr: String(stderr) })
      })
    })
  }

  spawnDaemon () {
    const bundle = this.bundle
    if (!bundle) throw new Error('no node bundle resolved')
    mkdirSync(this.logsPath, { recursive: true })
    const nodeLog = createWriteStream(join(this.logsPath, 'node.log'), { flags: 'a' })
    const child = spawn(bundle.binary, ['daemon', '--config', this.configPath], {
      env: nodeEnv(bundle.root),
      cwd: this.home,
      stdio: ['ignore', 'pipe', 'pipe']
    })
    this.child = child
    logger.info(`[node] spawned ${bundle.binary} daemon (pid ${child.pid})`)

    const onOutput = (/** @type {Buffer} */ chunk) => {
      const text = chunk.toString()
      nodeLog.write(text)
      for (const line of text.split('\n')) {
        const trimmed = line.trim()
        if (trimmed) this.apply({ type: 'log', line: trimmed })
      }
    }
    child.stdout?.on('data', onOutput)
    child.stderr?.on('data', onOutput)
    child.on('error', (err) => {
      this.apply({ type: 'log', line: err.message })
      this.apply({ type: 'exit', code: null, signal: '' })
    })
    child.on('exit', (code, signal) => {
      nodeLog.end()
      this.child = null
      this.stopPolling()
      this.apply({ type: 'exit', code, signal: signal ?? '' })
    })
  }

  poll () {
    this.stopPolling()
    const tick = () => {
      if (!this.child || this.stopping) return
      this.probe().then((event) => {
        this.apply(event)
        if (this.state.phase === PHASES.READY || this.state.phase === PHASES.FAILED) {
          this.stopPolling()
          return
        }
        this.timer = setTimeout(tick, PROBE_INTERVAL_MS)
      })
    }
    tick()
  }

  stopPolling () {
    if (this.timer) clearTimeout(this.timer)
    this.timer = null
  }

  /**
   * One readiness probe against the local node. This is the only network call
   * the shell makes, and it never leaves loopback.
   *
   * @returns {Promise<object>}
   */
  probe () {
    return new Promise((resolve) => {
      let url
      try {
        url = new URL('/api/v1/ready', this.adminUrl || 'http://127.0.0.1:5001/')
      } catch (err) {
        resolve({ type: 'probe', error: String(err) })
        return
      }
      const request = http.get({
        host: url.hostname,
        port: url.port,
        path: url.pathname,
        timeout: 4000,
        headers: { connection: 'close' }
      }, (response) => {
        let body = ''
        response.setEncoding('utf8')
        response.on('data', (chunk) => { if (body.length < 512) body += chunk })
        response.on('end', () => resolve({ type: 'probe', status: response.statusCode, body }))
      })
      request.on('timeout', () => request.destroy(new Error('readiness probe timed out')))
      request.on('error', (err) => resolve({ type: 'probe', error: err.message }))
    })
  }

  /**
   * SIGTERM, then SIGKILL after the grace period. The node closes its store on
   * SIGTERM; killing it outright is what leaves a store to rebuild.
   *
   * @param {number} [graceMs]
   */
  async stop (graceMs = STOP_GRACE_MS) {
    this.stopping = true
    this.stopPolling()
    const child = this.child
    this.apply({ type: 'stopping' })
    if (!child || child.exitCode !== null) return
    logger.info(`[node] stopping (pid ${child.pid})`)
    const exited = new Promise((resolve) => child.once('exit', resolve))
    child.kill('SIGTERM')
    const killed = await Promise.race([
      exited.then(() => true),
      new Promise((resolve) => setTimeout(() => resolve(false), graceMs))
    ])
    if (!killed) {
      logger.warn(`[node] did not stop within ${graceMs}ms; killing`)
      child.kill('SIGKILL')
      await exited
    }
    this.child = null
    logger.info('[node] stopped')
  }

  async restart () {
    await this.stop()
    await this.start()
  }
}

module.exports = { NodeSupervisor, PROBE_INTERVAL_MS, STOP_GRACE_MS }
