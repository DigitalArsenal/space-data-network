// @ts-check
const fs = require('fs')
const http = require('http')
const path = require('path')
const { spawn } = require('child_process')
const { app } = require('electron')
const portfinder = require('portfinder')
const toUri = require('multiaddr-to-uri')
const logger = require('../common/logger')

const DEFAULT_ADMIN_URL = 'http://127.0.0.1:5001'
const DESKTOP_ADMIN_START_PORT = 17950
const DEFAULT_START_TIMEOUT_MS = 120000

/**
 * @param {string} value
 * @returns {string}
 */
function normalizeLoopbackAdminUrl (value) {
  const url = new URL(String(value || '').trim())
  if (url.protocol !== 'http:') {
    throw new Error(`SDN desktop daemon URL must use http on loopback (got ${url.protocol})`)
  }
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)) {
    throw new Error(`SDN desktop daemon URL must be loopback-only (got ${url.hostname})`)
  }
  if (!url.port) {
    throw new Error('SDN desktop daemon URL must include an explicit port')
  }
  return url.origin
}

/**
 * @param {any} address
 * @returns {string}
 */
function httpUrlFromMultiaddr (address) {
  if (!address) throw new Error('missing Kubo HTTP address')
  const ma = address.toString().includes('/http') ? address : address.encapsulate('/http')
  return new URL(toUri(ma)).origin
}

/**
 * @returns {string}
 */
function desktopRuntimeRoot () {
  if (process.env.SDN_DESKTOP_RUNTIME_ROOT) {
    return path.resolve(process.env.SDN_DESKTOP_RUNTIME_ROOT)
  }
  if (app.isPackaged) {
    return path.join(process.resourcesPath, 'sdn-runtime')
  }
  return path.join(app.getAppPath(), 'build', 'sdn-runtime')
}

/**
 * @param {string} runtimeRoot
 * @returns {string}
 */
function desktopDaemonBinary (runtimeRoot = desktopRuntimeRoot()) {
  if (process.env.SDN_DESKTOP_DAEMON_BINARY) {
    return path.resolve(process.env.SDN_DESKTOP_DAEMON_BINARY)
  }
  return path.join(runtimeRoot, 'bin', process.platform === 'win32' ? 'spacedatanetwork.exe' : 'spacedatanetwork')
}

/**
 * @param {string} configPath
 * @returns {string|null}
 */
function adminUrlFromConfig (configPath) {
  let source = ''
  try {
    source = fs.readFileSync(configPath, 'utf8')
  } catch (_) {
    return null
  }
  const match = source.match(/^\s*listen_addr:\s*["']?([^\s"'#]+)["']?\s*(?:#.*)?$/m)
  if (!match) return null
  return normalizeLoopbackAdminUrl(`http://${match[1]}`)
}

/**
 * @param {string} configPath
 * @param {{ adminUrl: string, storagePath: string, ipfsApiUrl: string, ipfsGatewayUrl: string, runtimeRoot: string }} options
 */
function writeDesktopDaemonConfig (configPath, options) {
  const admin = new URL(normalizeLoopbackAdminUrl(options.adminUrl))
  const walletWasm = path.join(options.runtimeRoot, 'runtime', 'ui', 'wallet-wasm')
  const walletUI = path.join(options.runtimeRoot, 'runtime', 'ui', 'wallet-ui')
  const quote = value => JSON.stringify(String(value))
  const config = [
    'mode: full',
    'network:',
    '  listen:',
    '    - "/ip4/0.0.0.0/tcp/0"',
    '    - "/ip4/0.0.0.0/udp/0/quic-v1"',
    '    - "/ip4/0.0.0.0/udp/0/webrtc-direct"',
    'storage:',
    `  path: ${quote(options.storagePath)}`,
    'admin:',
    '  enabled: true',
    `  listen_addr: ${quote(`${admin.hostname}:${admin.port}`)}`,
    '  require_auth: true',
    '  dev_auto_admin: false',
    '  tls_mode: disabled',
    `  ipfs_api_url: ${quote(options.ipfsApiUrl)}`,
    `  ipfs_gateway_url: ${quote(options.ipfsGatewayUrl)}`,
    'wallet_wasm:',
    `  assets_dir: ${quote(walletWasm)}`,
    `  ui_assets_dir: ${quote(walletUI)}`,
    'status:',
    '  allowed_origins:',
    `    - ${quote(options.adminUrl)}`,
    'update:',
    '  enabled: false',
    ''
  ].join('\n')

  fs.mkdirSync(path.dirname(configPath), { recursive: true, mode: 0o700 })
  fs.mkdirSync(options.storagePath, { recursive: true, mode: 0o700 })
  fs.writeFileSync(configPath, config, { mode: 0o600 })
}

/**
 * @param {string} adminUrl
 * @param {number} [timeoutMs]
 * @returns {Promise<any|null>}
 */
function probeDaemon (adminUrl, timeoutMs = 1000) {
  const endpoint = new URL('/api/node/info', normalizeLoopbackAdminUrl(adminUrl))
  return new Promise(resolve => {
    let settled = false
    const finish = value => {
      if (settled) return
      settled = true
      resolve(value)
    }
    const request = http.get(endpoint, { timeout: timeoutMs }, response => {
      const chunks = []
      response.on('data', chunk => chunks.push(chunk))
      response.on('end', () => {
        if (response.statusCode !== 200) return finish(null)
        try {
          const info = JSON.parse(Buffer.concat(chunks).toString('utf8'))
          finish(info && typeof info.peer_id === 'string' && info.peer_id ? info : null)
        } catch (_) {
          finish(null)
        }
      })
    })
    request.on('timeout', () => {
      request.destroy()
      finish(null)
    })
    request.on('error', () => finish(null))
  })
}

/**
 * @param {string} adminUrl
 * @param {import('child_process').ChildProcess} child
 * @param {number} timeoutMs
 * @returns {Promise<any>}
 */
async function waitForDaemon (adminUrl, child, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const info = await probeDaemon(adminUrl)
    if (info) return info
    if (child.exitCode !== null) {
      throw new Error(`SDN daemon exited during startup with code ${child.exitCode}`)
    }
    await new Promise(resolve => setTimeout(resolve, 250))
  }
  throw new Error(`SDN daemon did not become ready at ${adminUrl} within ${timeoutMs}ms`)
}

/**
 * @param {import('child_process').ChildProcess} child
 * @returns {Promise<void>}
 */
async function stopManagedProcess (child) {
  if (child.exitCode !== null) return
  const closed = new Promise(resolve => child.once('close', resolve))
  child.kill('SIGTERM')
  const graceful = await Promise.race([
    closed.then(() => true),
    new Promise(resolve => setTimeout(() => resolve(false), 15000))
  ])
  if (!graceful && child.exitCode === null) {
    child.kill('SIGKILL')
    await closed
  }
}

/**
 * Start the packaged SDN daemon or adopt an already-running loopback daemon.
 * Kubo remains managed by the existing desktop lifecycle and is supplied to
 * the SDN node as its content store.
 *
 * @param {{ ipfsd: any }} options
 * @returns {Promise<{adminUrl: string, peerId: string, managed: boolean, stop: () => Promise<void>}>>}
 */
async function startSdnDaemon ({ ipfsd }) {
  const runtimeRoot = desktopRuntimeRoot()
  const explicitAdminUrl = process.env.SDN_DESKTOP_DAEMON_URL
  const explicitConfigPath = process.env.SDN_DESKTOP_DAEMON_CONFIG
  const preferredAdminUrl = normalizeLoopbackAdminUrl(explicitAdminUrl || DEFAULT_ADMIN_URL)

  const preferredInfo = await probeDaemon(preferredAdminUrl)
  if (preferredInfo) {
    logger.info(`[sdn-daemon] adopted ${preferredAdminUrl}`)
    return {
      adminUrl: preferredAdminUrl,
      peerId: preferredInfo.peer_id,
      managed: false,
      stop: async () => {}
    }
  }

  const configPath = explicitConfigPath
    ? path.resolve(explicitConfigPath)
    : path.join(app.getPath('userData'), 'sdn-daemon', 'config.yaml')
  const configuredAdminUrl = explicitConfigPath ? adminUrlFromConfig(configPath) : null
  const adminUrl = explicitAdminUrl
    ? preferredAdminUrl
    : configuredAdminUrl || normalizeLoopbackAdminUrl(`http://127.0.0.1:${await portfinder.getPortPromise({ port: DESKTOP_ADMIN_START_PORT, host: '127.0.0.1' })}`)

  if (adminUrl !== preferredAdminUrl) {
    const configuredInfo = await probeDaemon(adminUrl)
    if (configuredInfo) {
      logger.info(`[sdn-daemon] adopted ${adminUrl}`)
      return {
        adminUrl,
        peerId: configuredInfo.peer_id,
        managed: false,
        stop: async () => {}
      }
    }
  }

  const binary = desktopDaemonBinary(runtimeRoot)
  if (!fs.existsSync(binary)) {
    throw new Error(`packaged SDN daemon is missing at ${binary}; run npm run build`)
  }

  if (!explicitConfigPath) {
    writeDesktopDaemonConfig(configPath, {
      adminUrl,
      storagePath: path.join(app.getPath('userData'), 'sdn-daemon', 'data'),
      ipfsApiUrl: httpUrlFromMultiaddr(ipfsd.apiAddr),
      ipfsGatewayUrl: httpUrlFromMultiaddr(ipfsd.gatewayAddr),
      runtimeRoot
    })
  }

  const wasmedgeRoot = path.join(runtimeRoot, 'runtime', 'wasmedge')
  const wasmedgeLib = path.join(wasmedgeRoot, 'lib')
  const env = {
    ...process.env,
    SDN_STOREFRONT_DEV_PAYMENTS: '0',
    SDN_KUBO_BINARY: path.join(runtimeRoot, 'runtime', 'kubo', process.platform === 'win32' ? 'ipfs.exe' : 'ipfs'),
    WASMEDGE_DIR: wasmedgeRoot
  }
  // This companion uses the already-running Desktop Kubo through the explicit
  // admin URLs above. An ambient IPFS_PATH would incorrectly tell the SDN node
  // that the operator-run repository belongs to its own managed-Kubo lifecycle.
  delete env.IPFS_PATH
  if (process.platform === 'darwin') {
    env.DYLD_LIBRARY_PATH = [wasmedgeLib, process.env.DYLD_LIBRARY_PATH].filter(Boolean).join(':')
  } else if (process.platform !== 'win32') {
    env.LD_LIBRARY_PATH = [wasmedgeLib, process.env.LD_LIBRARY_PATH].filter(Boolean).join(':')
  }

  logger.info(`[sdn-daemon] starting ${binary} at ${adminUrl}`)
  const child = spawn(binary, ['--config', configPath, 'daemon'], {
    cwd: runtimeRoot,
    env,
    stdio: ['ignore', 'pipe', 'pipe']
  })
  child.stdout.on('data', chunk => logger.info(`[sdn-daemon] ${chunk.toString().trimEnd()}`))
  child.stderr.on('data', chunk => logger.error(`[sdn-daemon] ${chunk.toString().trimEnd()}`))

  const timeoutMs = Number(process.env.SDN_DESKTOP_DAEMON_START_TIMEOUT_MS) || DEFAULT_START_TIMEOUT_MS
  try {
    const info = await waitForDaemon(adminUrl, child, timeoutMs)
    return {
      adminUrl,
      peerId: info.peer_id,
      managed: true,
      stop: () => stopManagedProcess(child)
    }
  } catch (err) {
    await stopManagedProcess(child)
    throw err
  }
}

module.exports = startSdnDaemon
module.exports.adminUrlFromConfig = adminUrlFromConfig
module.exports.desktopDaemonBinary = desktopDaemonBinary
module.exports.desktopRuntimeRoot = desktopRuntimeRoot
module.exports.httpUrlFromMultiaddr = httpUrlFromMultiaddr
module.exports.normalizeLoopbackAdminUrl = normalizeLoopbackAdminUrl
module.exports.probeDaemon = probeDaemon
module.exports.writeDesktopDaemonConfig = writeDesktopDaemonConfig
