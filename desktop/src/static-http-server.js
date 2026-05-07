// @ts-check
const fs = require('fs')
const path = require('path')
const http = require('http')
const os = require('os')
const { app } = require('electron')
const portfinder = require('portfinder')
const logger = require('./common/logger')

const HOST = '127.0.0.1'
const START_PORT = 17890
const DESKTOP_SDN_SEED_PEERS = Object.freeze([
  '/dns4/sdn.spaceaware.io/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  '/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  '/dns4/celestrak.eth/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
  '/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4'
])
const ROUTES = Object.freeze({
  sdn: 'assets/sdn-ui',
  webui: 'assets/webui'
})

let serverPromise = null

const contentTypes = Object.freeze({
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
  '.png': 'image/png',
  '.svg': 'image/svg+xml; charset=utf-8',
  '.wasm': 'application/wasm',
  '.woff': 'font/woff',
  '.woff2': 'font/woff2'
})

function sendJSON (res, status, payload) {
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Cache-Control': 'no-store'
  })
  res.end(JSON.stringify(payload))
}

function routeForUrl (requestUrl) {
  const parsed = new URL(requestUrl, `http://${HOST}`)
  const [, routeName, ...segments] = parsed.pathname.split('/')
  const routeDirectory = ROUTES[routeName]

  if (!routeDirectory) {
    return null
  }

  const root = path.resolve(app.getAppPath(), routeDirectory)
  const requestedPath = path.resolve(root, ...segments)

  if (!requestedPath.startsWith(root)) {
    return null
  }

  if (fs.existsSync(requestedPath) && fs.statSync(requestedPath).isFile()) {
    return requestedPath
  }

  return path.join(root, 'index.html')
}

function redirectBareAppRoute (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  const [, routeName] = parsed.pathname.split('/')

  if (parsed.pathname !== `/${routeName}` || !ROUTES[routeName]) {
    return false
  }

  res.writeHead(301, { Location: `/${routeName}/${parsed.search}${parsed.hash}` })
  res.end()
  return true
}

function isSdnSSHHostAlias (alias) {
  return typeof alias === 'string' &&
    alias.length > 0 &&
    !alias.includes('*') &&
    !alias.includes('?') &&
    (alias.startsWith('space-data-network-') ||
      alias === 'sdn.spaceaware.io' ||
      alias === 'celestrak.eth')
}

function configuredSdnNodesFromSshConfig (configPath = path.join(os.homedir(), '.ssh', 'config')) {
  let config
  try {
    config = fs.readFileSync(configPath, 'utf8')
  } catch {
    return []
  }

  return config
    .split(/\r?\n/)
    .map(line => line.trim().split(/\s+/))
    .filter(fields => fields.length > 1 && fields[0].toLowerCase() === 'host')
    .map(fields => fields.slice(1).find(isSdnSSHHostAlias))
    .filter(Boolean)
    .map(alias => ({
      id: alias,
      name: alias,
      addrs: [],
      trust_level: 'trusted',
      metadata: {
        agent_version: 'sdn-configured-node',
        protocols: '/space-data-network/configured-node/1.0.0'
      }
    }))
}

function serveConfiguredSdnNodes (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (parsed.pathname !== '/api/local/sdn-nodes') {
    return false
  }

  sendJSON(res, 200, {
    nodes: configuredSdnNodesFromSshConfig()
  })
  return true
}

function isSdnProtocol (protocol) {
  const value = String(protocol ?? '')
  return value.startsWith('/space-data-network/') || value.startsWith('/spacedatanetwork/')
}

function isSdnAgentVersion (agentVersion) {
  const value = String(agentVersion ?? '').toLowerCase()
  return value.includes('spacedatanetwork') || value.includes('space-data-network') || value.startsWith('sdn')
}

function normalizeKuboPeerAddress (address, peerId) {
  const value = String(address ?? '').trim()
  if (!value) return ''
  return value.includes('/p2p/') ? value : `${value}/p2p/${peerId}`
}

function kuboSwarmPeersToDesktopSdnPeers (payload) {
  return (Array.isArray(payload?.Peers) ? payload.Peers : [])
    .map(peer => {
      const identify = peer?.Identify || {}
      const peerId = String(identify.ID || peer?.Peer || '').trim()
      if (!peerId) return null

      const agentVersion = String(identify.AgentVersion || '').trim()
      const protocols = Array.isArray(identify.Protocols)
        ? identify.Protocols.map(protocol => String(protocol ?? '').trim()).filter(Boolean)
        : []

      if (!isSdnAgentVersion(agentVersion) && !protocols.some(isSdnProtocol)) {
        return null
      }

      const metadata = {}
      if (agentVersion) metadata.agent_version = agentVersion
      if (protocols.length > 0) metadata.protocols = protocols.join(',')

      return {
        id: peerId,
        name: peerId,
        addrs: [normalizeKuboPeerAddress(peer?.Addr, peerId)].filter(Boolean),
        trust_level: 'observed',
        metadata
      }
    })
    .filter(Boolean)
}

function requestKuboJSON (apiPath) {
  return new Promise((resolve, reject) => {
    const req = http.request({ hostname: HOST, port: 5001, path: apiPath, method: 'POST' }, response => {
      let raw = ''
      response.setEncoding('utf8')
      response.on('data', chunk => { raw += chunk })
      response.on('end', () => {
        if (response.statusCode < 200 || response.statusCode > 299) {
          reject(new Error(`Kubo API request failed (${response.statusCode})`))
          return
        }

        try {
          resolve(JSON.parse(raw))
        } catch (err) {
          reject(err)
        }
      })
    })
    req.on('error', reject)
    req.end()
  })
}

async function connectDesktopSdnSeedPeers (requestJSON = requestKuboJSON) {
  const results = []
  for (const peer of DESKTOP_SDN_SEED_PEERS) {
    try {
      const result = await requestJSON(`/api/v0/swarm/connect?timeout=5000ms&arg=${encodeURIComponent(peer)}`)
      results.push({ peer, ok: true, result })
    } catch (err) {
      results.push({ peer, ok: false, error: err })
    }
  }
  return results
}

async function serveDesktopPeerAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (parsed.pathname !== '/api/peers/sdn' && parsed.pathname !== '/api/peers' && parsed.pathname !== '/api/peers/graph') {
    return false
  }

  try {
    const swarm = await requestKuboJSON('/api/v0/swarm/peers?verbose=true&identify=true&timeout=10000ms')
    let peers = kuboSwarmPeersToDesktopSdnPeers(swarm)

    if (peers.length === 0) {
      await connectDesktopSdnSeedPeers()
      const refreshedSwarm = await requestKuboJSON('/api/v0/swarm/peers?verbose=true&identify=true&timeout=10000ms')
      peers = kuboSwarmPeersToDesktopSdnPeers(refreshedSwarm)
    }

    if (parsed.pathname === '/api/peers/graph') {
      sendJSON(res, 200, {
        local_peer_id: '',
        nodes: peers.map(peer => ({
          peer_id: peer.id,
          dn: peer.name,
          trust_level: peer.trust_level,
          multiformat_address: peer.addrs,
          is_online: true
        })),
        edges: []
      })
      return true
    }

    sendJSON(res, 200, peers)
  } catch (err) {
    logger.error(`[static-server] failed to serve desktop SDN peers: ${err.message || err}`)
    sendJSON(res, 200, [])
  }
  return true
}

function localProfilePath () {
  return path.join(app.getPath('userData'), 'sdn-node-profile.json')
}

async function defaultDesktopNodeProfile () {
  try {
    const identity = await requestKuboJSON('/api/v0/id')
    return {
      dn: 'Space Data Network Desktop',
      entity_type: 'Node',
      peer_id: identity.ID || '',
      agent_version: identity.AgentVersion || '',
      multiformat_address: Array.isArray(identity.Addresses) ? identity.Addresses : []
    }
  } catch {
    return {
      dn: 'Space Data Network Desktop',
      entity_type: 'Node',
      peer_id: '',
      multiformat_address: []
    }
  }
}

async function readDesktopNodeProfile () {
  try {
    return JSON.parse(await fs.promises.readFile(localProfilePath(), 'utf8'))
  } catch {
    return defaultDesktopNodeProfile()
  }
}

async function readRequestBody (req) {
  return await new Promise((resolve, reject) => {
    let body = ''
    req.setEncoding('utf8')
    req.on('data', chunk => { body += chunk })
    req.on('end', () => resolve(body))
    req.on('error', reject)
  })
}

async function serveDesktopNodeEPMAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (parsed.pathname !== '/api/node/epm/json' && parsed.pathname !== '/api/node/epm') {
    return false
  }

  if (req.method === 'GET' && parsed.pathname === '/api/node/epm/json') {
    sendJSON(res, 200, await readDesktopNodeProfile())
    return true
  }

  if (req.method === 'PUT' && parsed.pathname === '/api/node/epm') {
    let profile
    try {
      profile = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON profile' })
      return true
    }

    const next = { ...(await readDesktopNodeProfile()), ...profile }
    await fs.promises.mkdir(path.dirname(localProfilePath()), { recursive: true })
    await fs.promises.writeFile(localProfilePath(), JSON.stringify(next, null, 2))
    sendJSON(res, 200, next)
    return true
  }

  sendJSON(res, 405, { error: 'method not allowed' })
  return true
}

async function serveDesktopLocalDataAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)

  if (req.method === 'GET' && parsed.pathname === '/api/v1/data/health') {
    sendJSON(res, 200, {
      healthy: true,
      details: {
        runtime: 'desktop-local',
        object_index: 'degraded'
      }
    })
    return true
  }

  if (req.method === 'GET' && parsed.pathname === '/api/v1/data/objects') {
    sendJSON(res, 200, { objects: [] })
    return true
  }

  if (req.method === 'POST' && parsed.pathname === '/api/v1/data/query') {
    sendJSON(res, 200, {
      results: [],
      degraded: true,
      reason: 'local SQL index is not wired in desktop-local yet'
    })
    return true
  }

  return false
}

function serveFile (res, filePath) {
  fs.readFile(filePath, (err, body) => {
    if (err) {
      res.writeHead(404)
      res.end('Not found')
      return
    }

    res.writeHead(200, {
      'Content-Type': contentTypes[path.extname(filePath)] || 'application/octet-stream',
      'Cache-Control': 'no-store'
    })
    res.end(body)
  })
}

async function startDesktopStaticServer () {
  if (serverPromise) {
    return serverPromise
  }

  serverPromise = (async () => {
    const port = await portfinder.getPortPromise({ port: START_PORT })
    const server = http.createServer((req, res) => {
      if (serveConfiguredSdnNodes(req, res)) {
        return
      }

      Promise.resolve()
        .then(() => serveDesktopPeerAPI(req, res))
        .then(handled => handled || serveDesktopNodeEPMAPI(req, res))
        .then(handled => handled || serveDesktopLocalDataAPI(req, res))
        .then(handled => {
          if (handled) return

          if (redirectBareAppRoute(req, res)) {
            return
          }

          const filePath = routeForUrl(req.url || '/')

          if (!filePath) {
            res.writeHead(404)
            res.end('Not found')
            return
          }

          serveFile(res, filePath)
        })
        .catch(err => {
          logger.error(`[static-server] failed to serve request: ${err.message || err}`)
          res.writeHead(500)
          res.end('Internal server error')
        })
    })

    await new Promise((resolve, reject) => {
      server.once('error', reject)
      server.listen(port, HOST, resolve)
    })

    const origin = `http://${HOST}:${port}`
    logger.info(`[desktop static] serving SDN and WebUI assets at ${origin}`)

    app.once('before-quit', () => {
      server.close()
    })

    return { origin, server }
  })()

  return serverPromise
}

async function getDesktopStaticOrigin () {
  const { origin } = await startDesktopStaticServer()
  return origin
}

async function getDesktopStaticUrl (routeName, hash = '/') {
  const origin = await getDesktopStaticOrigin()
  const url = new URL(`/${routeName}/`, origin)
  url.hash = hash
  return url
}

module.exports = {
  connectDesktopSdnSeedPeers,
  configuredSdnNodesFromSshConfig,
  DESKTOP_SDN_SEED_PEERS,
  getDesktopStaticOrigin,
  getDesktopStaticUrl,
  isSdnSSHHostAlias,
  kuboSwarmPeersToDesktopSdnPeers,
  serveDesktopLocalDataAPI,
  startDesktopStaticServer
}
