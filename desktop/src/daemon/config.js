const { join } = require('path')
const fs = require('fs-extra')
const { multiaddr } = require('multiaddr')
const http = require('http')
const portfinder = require('portfinder')
const { shell } = require('electron')
const store = require('../common/store')
const logger = require('../common/logger')
const dialogs = require('./dialogs')

const CUSTOM_SCHEME_ORIGIN_SUFFIX = '://-'
const SDN_CUSTOM_SCHEME_ORIGINS = Object.freeze(['sdn', 'webui'].map(scheme => `${scheme}${CUSTOM_SCHEME_ORIGIN_SUFFIX}`))
const STALE_RANDOM_GATEWAY_WEBUI_ORIGIN = 'http://webui.ipfs.io.ipns.localhost:0'
const DEFAULT_API_ADDR = '/ip4/127.0.0.1/tcp/5001'
const DEFAULT_GATEWAY_ADDR = '/ip4/127.0.0.1/tcp/8080'
const DESKTOP_API_CORS_METHODS = Object.freeze(['PUT', 'POST'])

/**
 * Get repository configuration file path.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {string} config file path
 */
function getConfigFilePath (ipfsd) {
  return join(ipfsd.path, 'config')
}

/**
 * Get repository api file path.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {string} api file path
 */
function getApiFilePath (ipfsd) {
  return join(ipfsd.path, 'api')
}

/**
 * Checks if the repository configuration file exists.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {boolean} true if config file exists
 */
function configExists (ipfsd) {
  return fs.pathExistsSync(getConfigFilePath(ipfsd))
}

/**
 * Checks if the repository api file exists.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {boolean} true if config file exists
 */
function apiFileExists (ipfsd) {
  return fs.pathExistsSync(getApiFilePath(ipfsd))
}

/**
 * Removes the repository api file.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {void}
 */
function removeApiFile (ipfsd) {
  fs.removeSync(getApiFilePath(ipfsd))
}

/**
 * Reads the repository configuration file.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {any} the configuration
 */
function readConfigFile (ipfsd) {
  return fs.readJsonSync(getConfigFilePath(ipfsd))
}

/**
 * Writes the repository configuration file.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @param {Object<string, any>} config
 */
function writeConfigFile (ipfsd, config) {
  fs.writeJsonSync(getConfigFilePath(ipfsd), config, { spaces: 2 })
}

/**
 * Set default minimum and maximum of connections to maintain
 * by default. This must only be called for repositories created
 * by IPFS Desktop. Existing ones shall remain intact.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 */
function applyDefaults (ipfsd) {
  const config = readConfigFile(ipfsd)

  // Ensure strict CORS checking
  // See: https://github.com/ipfs/js-ipfsd-ctl/issues/333
  config.API = { HTTPHeaders: {} }

  config.Swarm = config.Swarm ?? {}
  config.Swarm.DisableNatPortMap = false // uPnP
  config.Swarm.ConnMgr = config.Swarm.ConnMgr ?? {}

  config.Discovery = config.Discovery ?? {}
  config.Discovery.MDNS = config.Discovery.MDNS ?? {}
  config.Discovery.MDNS.Enabled = true

  config.AutoTLS = config.AutoTLS ?? {}
  config.AutoTLS.Enabled = true

  writeConfigFile(ipfsd, config)
}

/**
 * Parses multiaddr from the configuration.
 *
 * @param {string} addr
 * @returns {import('multiaddr').Multiaddr}
 */
function parseMultiaddr (addr) {
  return addr.includes('/http')
    ? multiaddr(addr)
    : multiaddr(addr).encapsulate('/http')
}

/**
 * Get local HTTP port.
 *
 * @param {array|string} addrs
 * @returns {number} the port
 */
function getHttpPort (addrs) {
  let httpUrl = null

  if (Array.isArray(addrs)) {
    httpUrl = addrs.find(v => v.includes('127.0.0.1'))
  } else {
    httpUrl = addrs
  }

  const gw = parseMultiaddr(httpUrl)
  return gw.nodeAddress().port
}

/**
 * Get gateway port from configuration.
 *
 * @param {any} config
 * @returns {number}
 */
const getGatewayPort = (config) => getHttpPort(config.Addresses.Gateway)

function normalizeSingleLocalRandomPortAddress (addrs, fallbackAddr) {
  if (!Array.isArray(addrs) || addrs.length !== 1) {
    return addrs
  }

  const ma = parseMultiaddr(addrs[0])
  const { address, port } = ma.nodeAddress()

  return address === '127.0.0.1' && port === 0
    ? fallbackAddr
    : addrs
}

function normalizeDesktopRandomPorts (config) {
  const apiAddr = normalizeSingleLocalRandomPortAddress(config.Addresses.API, DEFAULT_API_ADDR)
  const gatewayAddr = normalizeSingleLocalRandomPortAddress(config.Addresses.Gateway, DEFAULT_GATEWAY_ADDR)
  const changed = apiAddr !== config.Addresses.API || gatewayAddr !== config.Addresses.Gateway

  if (changed) {
    config.Addresses.API = apiAddr
    config.Addresses.Gateway = gatewayAddr
  }

  return changed
}

function normalizeCorsOrigins (origins) {
  if (Array.isArray(origins)) {
    return origins
  }

  return typeof origins === 'string'
    ? [origins]
    : []
}

function ensureCorsOriginsForConfig (config, origins) {
  const api = config.API || {}
  const httpHeaders = api.HTTPHeaders || {}
  const existingOrigins = normalizeCorsOrigins(httpHeaders['Access-Control-Allow-Origin'])
  const nextOrigins = existingOrigins.filter(origin => {
    return !SDN_CUSTOM_SCHEME_ORIGINS.includes(origin) &&
      origin !== STALE_RANDOM_GATEWAY_WEBUI_ORIGIN
  })

  for (const origin of origins.filter(Boolean)) {
    if (!nextOrigins.includes(origin)) {
      nextOrigins.push(origin)
    }
  }

  const changed = nextOrigins.length !== existingOrigins.length ||
    nextOrigins.some((origin, index) => origin !== existingOrigins[index])

  if (changed) {
    httpHeaders['Access-Control-Allow-Origin'] = nextOrigins
    api.HTTPHeaders = httpHeaders
    config.API = api
  }

  return changed
}

function ensureCorsMethodsForConfig (config, methods = DESKTOP_API_CORS_METHODS) {
  const api = config.API || {}
  const httpHeaders = api.HTTPHeaders || {}
  const existingMethods = normalizeCorsOrigins(httpHeaders['Access-Control-Allow-Methods'])
  const nextMethods = existingMethods.slice()

  for (const method of methods.filter(Boolean)) {
    if (!nextMethods.includes(method)) {
      nextMethods.push(method)
    }
  }

  const changed = nextMethods.length !== existingMethods.length ||
    nextMethods.some((method, index) => method !== existingMethods[index])

  if (changed) {
    httpHeaders['Access-Control-Allow-Methods'] = nextMethods
    api.HTTPHeaders = httpHeaders
    config.API = api
  }

  return changed
}

/**
 * Keep local desktop RPC access on an upstream-compatible HTTP origin.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @param {string} desktopWebOrigin
 */
function configureDesktopCors (ipfsd, desktopWebOrigin) {
  const config = readConfigFile(ipfsd)
  const originsChanged = ensureCorsOriginsForConfig(config, [
    desktopWebOrigin,
    'https://webui.ipfs.io',
    `http://webui.ipfs.io.ipns.localhost:${getGatewayPort(config)}`
  ])
  const methodsChanged = ensureCorsMethodsForConfig(config)

  if (originsChanged || methodsChanged) {
    writeConfigFile(ipfsd, config)
  }
}

/**
 * Apply one-time updates to the config of IPFS node. This is the place
 * where we execute fixes and performance tweaks for existing users.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 */
function migrateConfig (ipfsd) {
  // Bump revision number when new migration rule is added
  const REVISION = 8
  const REVISION_KEY = 'daemonConfigRevision'
  const CURRENT_REVISION = store.get(REVISION_KEY, 0)

  // Migration is applied only once per revision
  if (CURRENT_REVISION >= REVISION) return

  // Read config
  let config = null
  let changed = false
  try {
    config = readConfigFile(ipfsd)
  } catch (err) {
    // This is a best effort check, dont blow up here, that should happen else where.
    logger.error(`[daemon] migrateConfig: error reading config file: ${err.message || err}`)
    return
  }

  if (CURRENT_REVISION < 1) {
    // Cleanup https://github.com/ipfs-shipyard/ipfs-desktop/issues/1631
    if (config.Discovery && config.Discovery.MDNS && config.Discovery.MDNS.enabled) {
      config.Discovery.MDNS.Enabled = config.Discovery.MDNS.Enabled || true
      delete config.Discovery.MDNS.enabled
      changed = true
    }
  }

  if (CURRENT_REVISION < 8) {
    changed = normalizeDesktopRandomPorts(config) || changed
  }

  if (CURRENT_REVISION < 3) {
    changed = ensureCorsOriginsForConfig(config, [
      'https://webui.ipfs.io',
      `http://webui.ipfs.io.ipns.localhost:${getGatewayPort(config)}`
    ]) || changed
  }

  if (CURRENT_REVISION < 4) {
    if (config.Swarm && config.Swarm.ConnMgr) {
      // lower ConnMgr https://github.com/ipfs/ipfs-desktop/issues/2039
      const { GracePeriod, LowWater, HighWater } = config.Swarm.ConnMgr
      if (GracePeriod === '300s') {
        config.Swarm.ConnMgr.GracePeriod = '1m'
        changed = true
      }
      if (LowWater > 20) {
        config.Swarm.ConnMgr.LowWater = 20
        changed = true
      }
      if (HighWater > 40) {
        config.Swarm.ConnMgr.HighWater = 40
        changed = true
      }
    }
  }

  if (CURRENT_REVISION < 5) {
    if (config.Swarm && config.Swarm.ConnMgr) {
      const { GracePeriod, LowWater, HighWater } = config.Swarm.ConnMgr
      // Only touch config if user runs old defaults hardcoded in ipfs-desktop
      if (GracePeriod === '1m' && LowWater === 20 && HighWater === 40) {
        config.Swarm.ConnMgr = {} // remove overrides, use defaults from Kubo https://github.com/ipfs/kubo/pull/9483
        changed = true
      }
    }
  }

  if (CURRENT_REVISION < 6) {
    // Enable AutoTLS if there is no explicit user preference
    if (config.AutoTLS === undefined) {
      config.AutoTLS = {}
      changed = true
    }
    if (config.AutoTLS.Enabled === undefined) {
      config.AutoTLS.Enabled = true
      changed = true
    }
  }

  if (changed) {
    try {
      writeConfigFile(ipfsd, config)
      store.safeSet(REVISION_KEY, REVISION)
    } catch (err) {
      logger.error(`[daemon] migrateConfig: error writing config file: ${err.message || err}`)
      return
    }
  }
  store.safeSet(REVISION_KEY, REVISION)
}

/**
 * Checks if the given address is a daemon address.
 *
 * @param {{ family: 4 | 6, address: string, port: number }} addr
 * @returns {Promise<boolean>}
 */
async function checkIfAddrIsDaemon (addr) {
  const options = {
    timeout: 3000, // 3s is plenty for localhost request
    method: 'POST',
    host: addr.address,
    port: addr.port,
    path: '/api/v0/refs?arg=/ipfs/QmUNLLsPACCz1vLxQVkXqqLX5R1X345qqfHbsf67hvA3Nn'
  }

  return new Promise(resolve => {
    const req = http.request(options, function (r) {
      resolve(r.statusCode === 200)
    })

    req.on('error', () => {
      resolve(false)
    })

    req.end()
  })
}

/**
 * Find free close to port.
 *
 * @param {number} port
 * @returns {Promise<number>}
 */
const findFreePort = async (port) => {
  port = Math.max(port, 1024)
  return portfinder.getPortPromise({ port })
}

/**
 * Check if all the ports in the array are available.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @param {string[]} addrs
 * @returns {Promise<boolean>}
 */
async function checkPortsArray (ipfsd, addrs) {
  addrs = addrs.filter(Boolean)

  for (const addr of addrs) {
    const ma = parseMultiaddr(addr)
    const port = ma.nodeAddress().port

    if (port === 0) {
      continue
    }

    const isDaemon = await checkIfAddrIsDaemon(ma.nodeAddress())

    if (isDaemon) {
      continue
    }

    const freePort = await findFreePort(port)

    if (port !== freePort) {
      const openConfig = dialogs.multipleBusyPortsDialog()
      if (openConfig) {
        shell.openPath(getConfigFilePath(ipfsd))
      }

      return false
    }
  }

  return true
}

/**
 * Check if ports are available and handle it. Returns
 * true if ports are cleared for IPFS to start.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {Promise<boolean>}
 */
async function checkPorts (ipfsd) {
  const config = readConfigFile(ipfsd)

  const apiIsArr = Array.isArray(config.Addresses.API)
  const gatewayIsArr = Array.isArray(config.Addresses.Gateway)

  if (apiIsArr || gatewayIsArr) {
    logger.info('[daemon] custom configuration with array of API or Gateway addrs')
    return checkPortsArray(ipfsd, [].concat(config.Addresses.API, config.Addresses.Gateway))
  }

  const configApiMa = parseMultiaddr(config.Addresses.API)
  const configGatewayMa = parseMultiaddr(config.Addresses.Gateway)

  const isApiMaDaemon = await checkIfAddrIsDaemon(configApiMa.nodeAddress())
  const isGatewayMaDaemon = await checkIfAddrIsDaemon(configGatewayMa.nodeAddress())

  if (isApiMaDaemon && isGatewayMaDaemon) {
    logger.info('[daemon] ports busy by a daemon')
    return true
  }

  const apiPort = configApiMa.nodeAddress().port
  const gatewayPort = configGatewayMa.nodeAddress().port

  const freeGatewayPort = await findFreePort(gatewayPort)
  let freeApiPort = await findFreePort(apiPort)

  // ensure the picked ports are different
  while (freeApiPort === freeGatewayPort) {
    freeApiPort = await findFreePort(freeApiPort + 1)
  }

  const busyApiPort = apiPort !== freeApiPort
  const busyGatewayPort = gatewayPort !== freeGatewayPort

  if (!busyApiPort && !busyGatewayPort) {
    return true
  }

  // two "0" in config mean "pick free ports without any prompt"
  let promptUser = (apiPort !== 0 || gatewayPort !== 0)

  if (process.env.NODE_ENV === 'test' || process.env.CI != null) {
    logger.info('[daemon] CI or TEST mode, skipping busyPortDialog')
    promptUser = false
  }

  if (promptUser) {
    let useAlternativePorts = null

    if (busyApiPort && busyGatewayPort) {
      logger.info('[daemon] api and gateway ports busy')
      useAlternativePorts = dialogs.busyPortsDialog(apiPort, freeApiPort, gatewayPort, freeGatewayPort)
    } else if (busyApiPort) {
      logger.info('[daemon] api port busy')
      useAlternativePorts = dialogs.busyPortDialog(apiPort, freeApiPort)
    } else {
      logger.info('[daemon] gateway port busy')
      useAlternativePorts = dialogs.busyPortDialog(gatewayPort, freeGatewayPort)
    }

    if (!useAlternativePorts) {
      return false
    }
  }

  if (busyApiPort) {
    config.Addresses.API = config.Addresses.API.replace(apiPort.toString(), freeApiPort.toString())
  }

  if (busyGatewayPort) {
    config.Addresses.Gateway = config.Addresses.Gateway.replace(gatewayPort.toString(), freeGatewayPort.toString())
  }

  writeConfigFile(ipfsd, config)
  logger.info('[daemon] ports updated')
  return true
}

/**
 * Checks if the repository and the configuration file are valid.
 *
 * @param {import('ipfsd-ctl').Controller} ipfsd
 * @returns {boolean}
 */
function checkRepositoryAndConfiguration (ipfsd) {
  if (!fs.pathExistsSync(ipfsd.path)) {
    // If the repository doesn't exist, skip verification.
    return true
  }

  try {
    const stats = fs.statSync(ipfsd.path)
    if (!stats.isDirectory()) {
      logger.error(`${ipfsd.path} must be a directory`)
      dialogs.repositoryMustBeDirectoryDialog(ipfsd.path)
      return false
    }

    if (!apiFileExists(ipfsd)) {
      if (!configExists(ipfsd)) {
        // Config is generated automatically if it doesn't exist.
        logger.error(`configuration does not exist at ${ipfsd.path}`)
        dialogs.repositoryConfigurationIsMissingDialog(ipfsd.path)
        return true
      }

      // This should catch errors such having no configuration file,
      // IPFS_DIR not being a directory, or the configuration file
      // being corrupted.
      readConfigFile(ipfsd)
    }

    const swarmKeyPath = join(ipfsd.path, 'swarm.key')
    if (fs.pathExistsSync(swarmKeyPath)) {
      // IPFS Desktop does not support private network IPFS repositories.
      dialogs.repositoryIsPrivateDialog(ipfsd.path)
      return false
    }

    return true
  } catch (e) {
    // Save to error.log
    logger.error(e)
    dialogs.repositoryIsInvalidDialog(ipfsd.path)
    return false
  }
}

module.exports = Object.freeze({
  configExists,
  apiFileExists,
  removeApiFile,
  applyDefaults,
  migrateConfig,
  configureDesktopCors,
  checkPorts,
  checkRepositoryAndConfiguration
})
