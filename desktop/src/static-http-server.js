// @ts-check
const fs = require('fs')
const path = require('path')
const http = require('http')
const os = require('os')
const crypto = require('crypto')
const net = require('net')
const { secp256k1 } = require('@noble/curves/secp256k1')
const { CID } = require('multiformats/cid')
const rawCodec = require('multiformats/codecs/raw')
const { sha256: multiformatsSha256 } = require('multiformats/hashes/sha2')
const { app, safeStorage, dialog } = require('electron')
const portfinder = require('portfinder')
const logger = require('./common/logger')

const HOST = '127.0.0.1'
const START_PORT = 17890
const FLATSQL_SYNC_PROTOCOL_ID = '/space-data-network/flatsql-sync/1.0.0'
const CONFIGURED_SDN_NODE_SYNC_WS_PORT = 8080
const CONFIGURED_SDN_NODE_ARTIFACT_PORT = 4002
const DESKTOP_SDN_SEED_PEERS = Object.freeze([
  '/dns4/sdn.spaceaware.io/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  '/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  '/dns4/celestrak.eth/tcp/4001/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
  '/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3'
])
const ROUTES = Object.freeze({
  sdn: 'assets/sdn-ui',
  webui: 'assets/webui'
})

let serverPromise = null

const HOSTED_EPM_STORE_VERSION = 1
const HOSTED_EPM_STORE_FILE = 'sdn-hosted-epms.enc.json'
const HOSTED_EPM_STORE_SALT = 'space-data-network-hosted-epm-store-v1'
const DESKTOP_AUTH_USERS_FILE = 'desktop-auth-users.json'
// Canonical PGP/GPG ownertrust scale (Task F1): matches
// sdn-server/internal/peers.TrustLevel.String() exactly — never / unknown /
// marginal / standard / full / admin / ultimate. This desktop server used to
// speak a parallel, desktop-only vocabulary ('trusted' for operator-
// configured nodes, 'observed' for swarm-discovered peers, 'local' for
// desktop-managed admin users) that does not exist server-side. Both are
// normalized to the one canonical scale so an xpub-registered user (or a
// trust_level shown for a peer) reads identically whether it came from the
// desktop app or the server. Legacy string values are accepted on input
// (e.g. reading an older desktop-auth-users.json) and always normalized to
// the canonical name on output — mirroring peers.ParseTrustLevel's
// legacy-in/canonical-out contract on the Go side.
const CANONICAL_TRUST_LEVELS = Object.freeze([
  'never', 'unknown', 'marginal', 'standard', 'full', 'admin', 'ultimate'
])
const LEGACY_TRUST_LEVEL_ALIASES = Object.freeze({
  // Server-side legacy aliases (see peers.ParseTrustLevel).
  untrusted: 'unknown',
  limited: 'marginal',
  trusted: 'full',
  // Desktop-only legacy vocabulary being retired by this reconciliation.
  observed: 'unknown',
  local: 'admin'
})

function normalizeDesktopTrustLevel (value, fallback = 'unknown') {
  const normalized = String(value ?? '').trim().toLowerCase()
  if (CANONICAL_TRUST_LEVELS.includes(normalized)) return normalized
  if (Object.prototype.hasOwnProperty.call(LEGACY_TRUST_LEVEL_ALIASES, normalized)) {
    return LEGACY_TRUST_LEVEL_ALIASES[normalized]
  }
  return fallback
}
const NODE_IDENTITY_SETTINGS_FILE = 'sdn-node-identity-settings.json'
const NODE_IDENTITY_SESSION_FILE = 'sdn-node-identity-session.json'
const NODE_WALLET_STORAGE_FILE = 'sdn-wallet-local-storage.enc.json'
const DEFAULT_NODE_IDENTITY_TTL_MS = 60 * 60 * 1000
const NODE_IDENTITY_APP_RUN_ID = typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : crypto.randomBytes(16).toString('hex')
const SECRET_EPM_FIELD_PATTERN = /(^|[_-])(private|secret|mnemonic|seed|xpriv|core)([_-]|$)|privatekey|encryptedcore/i
const WALLET_STORAGE_PREFIX = 'wallet_storage_'
const HD_XPUB_PURPOSE = 44
const HD_XPUB_COIN_TYPE = 0
const HD_XPUB_ACCOUNT = 0
const HD_XPUB_SIGNING_CHANGE = 0
const HD_XPUB_ENCRYPTION_CHANGE = 1
const HD_XPUB_PUBLIC_VERSION = 0x0488b21e
const BASE58_ALPHABET = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'
const WALLET_LOCAL_STORAGE_KEYS = Object.freeze(new Set([
  'encrypted_wallet',
  'passkey_credential',
  'passkey_wallet',
  'hd-wallet-wallets',
  'hd-wallet-active-accounts',
  'hd-wallet-vcard-identity',
  'hd-wallet-messaging-key-config-v1',
  'wallet-pki-keys'
]))

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
  res.writeHead(status, staticAssetHeaders('application/json; charset=utf-8'))
  res.end(JSON.stringify(payload))
}

function sendText (res, status, contentType, payload) {
  res.writeHead(status, staticAssetHeaders(contentType))
  res.end(payload)
}

function sendBuffer (res, status, contentType, payload) {
  res.writeHead(status, staticAssetHeaders(contentType))
  res.end(payload)
}

function staticAssetHeaders (contentType, extra = {}) {
  return {
    'Content-Type': contentType,
    'Cache-Control': 'no-store',
    'Cross-Origin-Opener-Policy': 'same-origin',
    'Cross-Origin-Embedder-Policy': 'require-corp',
    'Cross-Origin-Resource-Policy': 'same-origin',
    ...extra
  }
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

  res.writeHead(301, staticAssetHeaders('text/plain; charset=utf-8', { Location: `/${routeName}/${parsed.search}${parsed.hash}` }))
  res.end()
  return true
}

function isAllowedLoopbackHostHeader (hostHeader) {
  const host = normalizeHostHeader(hostHeader)
  return host === 'localhost' ||
    host === '127.0.0.1' ||
    host === '0.0.0.0' ||
    host === '::1'
}

function normalizeHostHeader (hostHeader) {
  if (Array.isArray(hostHeader)) {
    hostHeader = hostHeader[0]
  }
  if (typeof hostHeader !== 'string') {
    return null
  }

  const value = hostHeader.trim().toLowerCase()
  if (!value) {
    return null
  }

  if (value.startsWith('[')) {
    const match = value.match(/^\[([^\]]+)\](?::\d+)?$/)
    return match ? match[1] : null
  }

  if (value === '::1') {
    return value
  }

  const hostPort = value.match(/^([^:]+)(?::\d+)?$/)
  return hostPort ? hostPort[1] : null
}

function rejectNonLoopbackHostHeader (req, res) {
  if (isAllowedLoopbackHostHeader(req.headers.host)) {
    return false
  }

  logger.warn(`[desktop static] rejected non-loopback Host header: ${req.headers.host || 'missing'}`)
  res.writeHead(403, staticAssetHeaders('text/plain; charset=utf-8'))
  res.end('Forbidden')
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

const CONFIGURED_SDN_NODE_IDENTITIES = [
  {
    aliases: ['space-data-network-01', 'sdn.spaceaware.io'],
    name: 'SpaceAware.io',
    // The SpaceAware identity owns tcp/quic 4001 and ws/8080 on this host.
    peer_id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
    epm_cid: 'bafkreiggawraezbltnl3anwmabtuhvmlhdiotx5pxuqa7zmxkfjjjq35d4',
    xpub: 'xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa',
    signing_public_key: '038664c404be42123ce709e53da2b63fc24c091a968dd2200c443d7470f73fb1e6',
    signing_key_path: "m/44'/0'/0'/0/0",
    encryption_public_key: '0213dc855b71c36b4a7e47b034e5f0bcce8b5fdbdae95a04b3441bc8ac2db3cb41',
    encryption_key_path: "m/44'/0'/0'/1/0",
    public_key: '038664c404be42123ce709e53da2b63fc24c091a968dd2200c443d7470f73fb1e6',
    ipfs_artifact_peer_id: '12D3KooWMtfuRiHtDuzMMRYB2oX8UKVqP43hZQakGBLhWsMnCd7K'
  },
  {
    aliases: ['space-data-network-02', 'celestrak.eth'],
    name: 'CelesTrak Provider',
    peer_id: '16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
    epm_cid: 'bafkreiekghfegduqfol5jemuagc7rpqnvfw5ilk67d5nybhred6ubfxwr4',
    xpub: 'xpub6D36ciSsN66eJutmvXs1VXmtqnWkcMqZEbMh4FP6bpANfJpfP6oY48P7XnCWdd4NwfpHir8bU7eo3KcC45jsuN6LXwA5SYmL6sNeQwYPJjY',
    signing_public_key: '02342309cef261ec3535b5a3e7596d5a838366697bc554e68965723584184fd57c',
    signing_key_path: "m/44'/0'/0'/0/0",
    encryption_public_key: '0353b985339195a698c276925e379ba216c90dff1a9b98ec691bc466ea7176f1af',
    encryption_key_path: "m/44'/0'/0'/1/0",
    public_key: '02342309cef261ec3535b5a3e7596d5a838366697bc554e68965723584184fd57c',
    provider_id: 'space-data-network-02',
    ipfs_artifact_peer_id: '12D3KooWGhZfrxQVvwQHNGRkeJhGqMbkDqjktfpBXzn47N78XY9j'
  }
]

function configuredNodeIdentityMetadata (identity) {
  if (!identity) return {}
  return {
    ...(identity.peer_id ? { peer_id: identity.peer_id } : {}),
    ...(identity.epm_cid ? { epm_cid: identity.epm_cid } : {}),
    ...(identity.public_key ? { public_key: identity.public_key } : {}),
    ...(identity.xpub ? { xpub: identity.xpub } : {}),
    ...(identity.signing_public_key ? { signing_public_key: identity.signing_public_key } : {}),
    ...(identity.signing_key_path ? { signing_key_path: identity.signing_key_path } : {}),
    ...(identity.encryption_public_key ? { encryption_public_key: identity.encryption_public_key } : {}),
    ...(identity.encryption_key_path ? { encryption_key_path: identity.encryption_key_path } : {}),
    ...(identity.provider_id ? { provider_id: identity.provider_id } : {}),
    ...(identity.source_name ? { source_name: identity.source_name } : {})
  }
}

function configuredSdnNodesFromSshConfig (configPath = path.join(os.homedir(), '.ssh', 'config')) {
  let config
  try {
    config = fs.readFileSync(configPath, 'utf8')
  } catch {
    return []
  }

  const nodes = []
  let current = null

  const flush = () => {
    if (!current || current.aliases.length === 0) return
    const alias = current.aliases[0]
    const identity = configuredNodeIdentityForSdnSSHHost(alias, current.aliases)
    const addrs = configuredNodeLibp2pSyncAddrs(current.hostName || alias, identity)
    const ipfsArtifactAddrs = configuredNodeIpfsArtifactAddrs(current.hostName || alias, identity)
    nodes.push({
      id: alias,
      name: displayNameForSdnSSHHost(alias, current.aliases),
      addrs,
      // Explicitly operator-configured (~/.ssh/config) SDN node: full
      // confidence, matching sdn-server's peers.Full (== peers.Trusted).
      trust_level: 'full',
      metadata: {
        agent_version: 'sdn-configured-node',
        protocols: `/space-data-network/configured-node/1.0.0,${FLATSQL_SYNC_PROTOCOL_ID}`,
        sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
        ssh_aliases: current.aliases.join(','),
        ...configuredNodeIdentityMetadata(identity),
        ...(ipfsArtifactAddrs.length > 0 ? { ipfs_artifact_addrs: ipfsArtifactAddrs } : {}),
        ...(current.hostName ? { host_name: current.hostName } : {})
      }
    })
  }

  for (const rawLine of config.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const fields = line.split(/\s+/)
    const key = fields[0].toLowerCase()

    if (key === 'host') {
      flush()
      current = {
        aliases: fields.slice(1).filter(isSdnSSHHostAlias),
        hostName: ''
      }
      continue
    }

    if (!current) continue
    if (key === 'hostname') current.hostName = fields[1] || ''
  }

  flush()
  return nodes
}

function displayNameForSdnSSHHost (alias, aliases = []) {
  return configuredNodeIdentityForSdnSSHHost(alias, aliases)?.name || alias
}

function configuredNodeLibp2pSyncAddrs (hostName, identity) {
  const host = String(hostName || '').trim()
  const peerId = String(identity?.peer_id || '').trim()
  if (!host || !peerId || host.startsWith('space-data-network-')) return []
  const hostProtocol = multiaddrHostProtocol(host)
  if (!hostProtocol) return []
  return [`/${hostProtocol}/${host}/tcp/${CONFIGURED_SDN_NODE_SYNC_WS_PORT}/ws/p2p/${peerId}`]
}

function configuredNodeIpfsArtifactAddrs (hostName, identity) {
  const host = String(hostName || '').trim()
  const peerId = String(identity?.ipfs_artifact_peer_id || '').trim()
  if (!host || !peerId || host.startsWith('space-data-network-')) return []
  const hostProtocol = multiaddrHostProtocol(host)
  if (!hostProtocol) return []
  return [`/${hostProtocol}/${host}/tcp/${CONFIGURED_SDN_NODE_ARTIFACT_PORT}/p2p/${peerId}`]
}

function multiaddrHostProtocol (hostName) {
  const ipVersion = net.isIP(hostName)
  if (ipVersion === 4) return 'ip4'
  if (ipVersion === 6) return 'ip6'
  if (/^[a-z0-9.-]+$/i.test(hostName)) return 'dns4'
  return null
}

function configuredNodeIdentityForSdnSSHHost (alias, aliases = []) {
  const names = new Set([alias, ...aliases].map(value => String(value || '').toLowerCase()))
  return CONFIGURED_SDN_NODE_IDENTITIES.find(identity => (
    identity.aliases.some(candidate => names.has(candidate.toLowerCase()))
  )) || null
}

function configuredNodeIdentityForSdnPeerID (peerId) {
  const normalized = String(peerId || '').trim()
  if (!normalized) return null
  return CONFIGURED_SDN_NODE_IDENTITIES.find(identity => identity.peer_id === normalized) || null
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

function multiaddrHostName (address) {
  const parts = String(address ?? '').trim().split('/').filter(Boolean)
  for (let index = 0; index < parts.length - 1; index += 1) {
    if (parts[index] === 'ip4' || parts[index] === 'ip6' || parts[index] === 'dns4' || parts[index] === 'dns6') {
      return parts[index + 1] || ''
    }
  }
  return ''
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

      const identity = configuredNodeIdentityForSdnPeerID(peerId)
      const metadata = configuredNodeIdentityMetadata(identity)
      if (agentVersion) metadata.agent_version = agentVersion
      if (protocols.length > 0) metadata.protocols = protocols.join(',')
      const ipfsArtifactAddrs = configuredNodeIpfsArtifactAddrs(multiaddrHostName(peer?.Addr), identity)
      if (ipfsArtifactAddrs.length > 0) metadata.ipfs_artifact_addrs = ipfsArtifactAddrs

      return {
        id: peerId,
        name: identity?.name || peerId,
        addrs: [normalizeKuboPeerAddress(peer?.Addr, peerId)].filter(Boolean),
        // Seen on the swarm with no operator assertion made yet: matches
        // sdn-server's peers.Unknown (fail-closed default, no opinion).
        trust_level: 'unknown',
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

function nodeIdentitySettingsPath () {
  return path.join(app.getPath('userData'), NODE_IDENTITY_SETTINGS_FILE)
}

function defaultFlatbufferStoragePath () {
  return path.join(app.getPath('userData'), 'flatbuffers')
}

function nodeIdentitySessionPath () {
  return path.join(app.getPath('userData'), NODE_IDENTITY_SESSION_FILE)
}

function nodeWalletStoragePath () {
  return path.join(app.getPath('userData'), NODE_WALLET_STORAGE_FILE)
}

function normalizeFlatbufferStoragePath (value) {
  const candidate = typeof value === 'string' ? value.trim() : ''
  return candidate || defaultFlatbufferStoragePath()
}

function isPersistedWalletLocalStorageKey (key) {
  const value = String(key || '')
  return value.startsWith(WALLET_STORAGE_PREFIX) || WALLET_LOCAL_STORAGE_KEYS.has(value)
}

function emptyNodeWalletStorageSnapshot () {
  return {
    entries: {},
    encrypted_at_rest: canUseSafeStorage(),
    storage: canUseSafeStorage() ? 'electron-safe-storage' : 'plain-json',
    updated_at: null
  }
}

function canUseSafeStorage () {
  try {
    return Boolean(safeStorage && typeof safeStorage.isEncryptionAvailable === 'function' && safeStorage.isEncryptionAvailable())
  } catch {
    return false
  }
}

function normalizeWalletStorageEntries (entries) {
  if (!entries || typeof entries !== 'object' || Array.isArray(entries)) return {}
  const normalized = {}
  for (const [key, value] of Object.entries(entries)) {
    if (!isPersistedWalletLocalStorageKey(key)) continue
    if (typeof value === 'string') normalized[key] = value
  }
  return normalized
}

function normalizeWalletStoragePatch (entries) {
  if (!entries || typeof entries !== 'object' || Array.isArray(entries)) return {}
  const normalized = {}
  for (const [key, value] of Object.entries(entries)) {
    if (!isPersistedWalletLocalStorageKey(key)) continue
    if (value === null) normalized[key] = null
    if (typeof value === 'string') normalized[key] = value
  }
  return normalized
}

async function readNodeWalletStorage () {
  let raw
  try {
    raw = await fs.promises.readFile(nodeWalletStoragePath(), 'utf8')
  } catch (err) {
    if (err.code !== 'ENOENT') {
      logger.warn(`[static-server] failed to read wallet storage mirror: ${err.message || err}`)
    }
    return emptyNodeWalletStorageSnapshot()
  }

  try {
    const envelope = JSON.parse(raw)
    if (envelope.encoding === 'electron-safe-storage') {
      if (!canUseSafeStorage() || typeof safeStorage.decryptString !== 'function') {
        throw new Error('Electron safeStorage decryption is unavailable')
      }
      const plaintext = safeStorage.decryptString(Buffer.from(String(envelope.ciphertext || ''), 'base64'))
      const payload = JSON.parse(plaintext)
      return {
        entries: normalizeWalletStorageEntries(payload.entries),
        encrypted_at_rest: true,
        storage: 'electron-safe-storage',
        updated_at: typeof envelope.updated_at === 'string' ? envelope.updated_at : null
      }
    }

    return {
      entries: normalizeWalletStorageEntries(envelope.entries),
      encrypted_at_rest: false,
      storage: 'plain-json',
      updated_at: typeof envelope.updated_at === 'string' ? envelope.updated_at : null
    }
  } catch (err) {
    logger.warn(`[static-server] failed to decode wallet storage mirror: ${err.message || err}`)
    return emptyNodeWalletStorageSnapshot()
  }
}

async function writeNodeWalletStorage (entries) {
  const normalized = normalizeWalletStorageEntries(entries)
  const updatedAt = new Date().toISOString()
  let envelope
  if (canUseSafeStorage() && typeof safeStorage.encryptString === 'function') {
    envelope = {
      version: 1,
      encoding: 'electron-safe-storage',
      updated_at: updatedAt,
      ciphertext: safeStorage.encryptString(JSON.stringify({ entries: normalized })).toString('base64')
    }
  } else {
    envelope = {
      version: 1,
      encoding: 'plain-json',
      updated_at: updatedAt,
      entries: normalized
    }
  }

  await fs.promises.mkdir(path.dirname(nodeWalletStoragePath()), { recursive: true })
  await fs.promises.writeFile(nodeWalletStoragePath(), JSON.stringify(envelope, null, 2))
  return {
    entries: normalized,
    encrypted_at_rest: envelope.encoding === 'electron-safe-storage',
    storage: envelope.encoding,
    updated_at: updatedAt
  }
}

async function patchNodeWalletStorage (patch) {
  const snapshot = await readNodeWalletStorage()
  const entries = { ...snapshot.entries }
  for (const [key, value] of Object.entries(normalizeWalletStoragePatch(patch))) {
    if (value === null) {
      delete entries[key]
    } else {
      entries[key] = value
    }
  }
  return writeNodeWalletStorage(entries)
}

async function clearNodeWalletStorage () {
  await fs.promises.rm(nodeWalletStoragePath(), { force: true })
  return emptyNodeWalletStorageSnapshot()
}

function defaultNodeIdentitySettings () {
  return {
    ttl_ms: DEFAULT_NODE_IDENTITY_TTL_MS,
    flatbuffer_storage_path: defaultFlatbufferStoragePath(),
    updated_at: new Date(0).toISOString()
  }
}

async function readNodeIdentitySettingsOnly () {
  try {
    const raw = JSON.parse(await fs.promises.readFile(nodeIdentitySettingsPath(), 'utf8'))
    const ttl = raw.ttl_ms === 'app' ? 'app' : Number.parseInt(String(raw.ttl_ms ?? raw.ttlMs ?? ''), 10)
    return {
      ttl_ms: ttl === 'app' ? 'app' : Number.isFinite(ttl) && ttl > 0 ? ttl : DEFAULT_NODE_IDENTITY_TTL_MS,
      flatbuffer_storage_path: normalizeFlatbufferStoragePath(raw.flatbuffer_storage_path ?? raw.flatbufferStoragePath),
      updated_at: typeof raw.updated_at === 'string' ? raw.updated_at : new Date(0).toISOString()
    }
  } catch {
    return defaultNodeIdentitySettings()
  }
}

async function readNodeIdentitySettings () {
  const settings = await readNodeIdentitySettingsOnly()
  await fs.promises.mkdir(settings.flatbuffer_storage_path, { recursive: true }).catch(err => {
    logger.warn(`[static-server] failed to create FlatBuffer storage directory ${settings.flatbuffer_storage_path}: ${err.message || err}`)
  })
  return {
    ...settings,
    session: await readNodeIdentitySession()
  }
}

async function writeNodeIdentitySettings (settings) {
  const ttl = settings && (settings.ttl_ms ?? settings.ttlMs)
  const flatbufferStoragePath = normalizeFlatbufferStoragePath(settings && (settings.flatbuffer_storage_path ?? settings.flatbufferStoragePath))
  const normalized = {
    ttl_ms: ttl === 'app' ? 'app' : Math.max(60 * 1000, Number.parseInt(String(ttl || DEFAULT_NODE_IDENTITY_TTL_MS), 10) || DEFAULT_NODE_IDENTITY_TTL_MS),
    flatbuffer_storage_path: flatbufferStoragePath,
    updated_at: new Date().toISOString()
  }
  await fs.promises.mkdir(flatbufferStoragePath, { recursive: true })
  await fs.promises.mkdir(path.dirname(nodeIdentitySettingsPath()), { recursive: true })
  await fs.promises.writeFile(nodeIdentitySettingsPath(), JSON.stringify(normalized, null, 2))
  const currentSession = await readNodeIdentitySession()
  if (currentSession.unlocked && currentSession.profile) {
    await writeNodeIdentitySession(currentSession.profile, normalized)
  }
  return {
    ...normalized,
    session: await readNodeIdentitySession()
  }
}

function flatSqlPersistenceFilename (key) {
  const hash = crypto.createHash('sha256').update(String(key)).digest('hex').slice(0, 16)
  const readable = String(key)
    .trim()
    .replace(/[^A-Za-z0-9._-]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 96) || 'flatsql'
  return `${readable}-${hash}.bin`
}

async function flatSqlPersistenceFilePath (key) {
  const settings = await readNodeIdentitySettingsOnly()
  const storagePath = normalizeFlatbufferStoragePath(settings.flatbuffer_storage_path)
  await fs.promises.mkdir(storagePath, { recursive: true })
  return path.join(storagePath, flatSqlPersistenceFilename(key))
}

function lockedNodeIdentitySession () {
  return {
    unlocked: false,
    expires_at: null,
    profile: null
  }
}

async function readNodeIdentitySession () {
  try {
    const raw = JSON.parse(await fs.promises.readFile(nodeIdentitySessionPath(), 'utf8'))
    const session = normalizeNodeIdentitySession(raw)
    if (!session.unlocked) await clearNodeIdentitySession()
    return session
  } catch {
    return lockedNodeIdentitySession()
  }
}

function normalizeNodeIdentitySession (raw) {
  if (!raw || typeof raw !== 'object' || raw.unlocked !== true) return lockedNodeIdentitySession()
  const mode = raw.mode === 'app' ? 'app' : 'ttl'
  if (mode === 'app' && raw.app_run_id !== NODE_IDENTITY_APP_RUN_ID) return lockedNodeIdentitySession()
  const expiresAt = typeof raw.expires_at === 'string'
    ? raw.expires_at
    : typeof raw.expiresAt === 'string' ? raw.expiresAt : null
  const expiresAtMs = expiresAt ? Date.parse(expiresAt) : NaN
  if (mode !== 'app' && (!Number.isFinite(expiresAtMs) || expiresAtMs <= Date.now())) return lockedNodeIdentitySession()
  const profile = raw.profile && typeof raw.profile === 'object' && !Array.isArray(raw.profile)
    ? sanitizePublicEPM(raw.profile)
    : null
  if (!profile) return lockedNodeIdentitySession()
  return {
    unlocked: true,
    mode,
    expires_at: mode === 'app' ? null : new Date(expiresAtMs).toISOString(),
    profile,
    updated_at: typeof raw.updated_at === 'string' ? raw.updated_at : new Date(0).toISOString()
  }
}

async function writeNodeIdentitySession (profile, settings = null) {
  const activeSettings = settings || await readNodeIdentitySettingsOnly()
  const ttl = activeSettings.ttl_ms === 'app'
    ? 'app'
    : Math.max(60 * 1000, Number.parseInt(String(activeSettings.ttl_ms || DEFAULT_NODE_IDENTITY_TTL_MS), 10) || DEFAULT_NODE_IDENTITY_TTL_MS)
  const session = {
    unlocked: true,
    mode: ttl === 'app' ? 'app' : 'ttl',
    ...(ttl === 'app' ? { app_run_id: NODE_IDENTITY_APP_RUN_ID } : {}),
    expires_at: ttl === 'app' ? null : new Date(Date.now() + ttl).toISOString(),
    profile: sanitizePublicEPM(profile),
    updated_at: new Date().toISOString()
  }
  await fs.promises.mkdir(path.dirname(nodeIdentitySessionPath()), { recursive: true })
  await fs.promises.writeFile(nodeIdentitySessionPath(), JSON.stringify(session, null, 2))
  return normalizeNodeIdentitySession(session)
}

async function clearNodeIdentitySession () {
  await fs.promises.rm(nodeIdentitySessionPath(), { force: true })
  return lockedNodeIdentitySession()
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

async function buildDesktopNodeEPM (profile) {
  const publicProfile = await normalizeEpmPublicIdentityKeys(profile)
  const flatbuffers = require('flatbuffers')
  const builder = new flatbuffers.Builder(2048)

  const dnOff = createStringOffset(builder, readEpmString(publicProfile, ['dn', 'display_name', 'displayName', 'name']))
  const legalNameOff = createStringOffset(builder, readEpmString(publicProfile, ['legal_name', 'legalName']))
  const familyNameOff = createStringOffset(builder, readEpmString(publicProfile, ['family_name', 'familyName']))
  const givenNameOff = createStringOffset(builder, readEpmString(publicProfile, ['given_name', 'givenName']))
  const emailOff = createStringOffset(builder, readEpmString(publicProfile, ['email']))
  const telephoneOff = createStringOffset(builder, readEpmString(publicProfile, ['telephone', 'phone']))
  const keys = desktopEpmPublicKeys(publicProfile)
  const keysOff = createOffsetVector(builder, keys.map(key => createDesktopEpmCryptoKey(builder, key)))
  const addresses = desktopEpmAddresses(publicProfile)
  const addressesOff = createOffsetVector(builder, addresses.map(address => builder.createString(address)))
  const signatureOff = createStringOffset(builder, readEpmString(publicProfile, ['signature', 'epm_signature']))
  const signatureTimestamp = readEpmInteger(publicProfile, ['signature_timestamp', 'signatureTimestamp', 'epm_signature_timestamp'])

  builder.startObject(19)
  builder.addFieldInt8(18, 1, 0) // EPM.EntityType.Node
  if (dnOff) builder.addFieldOffset(0, dnOff, 0)
  if (legalNameOff) builder.addFieldOffset(1, legalNameOff, 0)
  if (familyNameOff) builder.addFieldOffset(2, familyNameOff, 0)
  if (givenNameOff) builder.addFieldOffset(3, givenNameOff, 0)
  if (emailOff) builder.addFieldOffset(11, emailOff, 0)
  if (telephoneOff) builder.addFieldOffset(12, telephoneOff, 0)
  if (keysOff) builder.addFieldOffset(13, keysOff, 0)
  if (addressesOff) builder.addFieldOffset(14, addressesOff, 0)
  if (signatureOff) builder.addFieldOffset(15, signatureOff, 0)
  if (signatureTimestamp > 0) builder.addFieldInt64(16, BigInt(signatureTimestamp), 0n)
  const epm = builder.endObject()
  builder.finish(epm, '$EPM', true)
  return Buffer.from(builder.asUint8Array())
}

async function computeRawCid (bytes) {
  const digest = await multiformatsSha256.digest(bytes)
  return CID.createV1(rawCodec.code, digest).toString()
}

function createStringOffset (builder, value) {
  const trimmed = String(value || '').trim()
  return trimmed ? builder.createString(trimmed) : 0
}

function createOffsetVector (builder, offsets) {
  if (offsets.length === 0) return 0
  builder.startVector(4, offsets.length, 4)
  for (let index = offsets.length - 1; index >= 0; index--) {
    builder.addOffset(offsets[index])
  }
  return builder.endVector()
}

function createDesktopEpmCryptoKey (builder, key) {
  const publicKeyOff = createStringOffset(builder, key.publicKey)
  const xpubOff = createStringOffset(builder, key.xpub)
  const addressOff = createStringOffset(builder, key.keyAddress)
  const addressTypeOff = createStringOffset(builder, key.addressType)

  builder.startObject(7)
  if (publicKeyOff) builder.addFieldOffset(0, publicKeyOff, 0)
  if (xpubOff) builder.addFieldOffset(1, xpubOff, 0)
  if (addressOff) builder.addFieldOffset(4, addressOff, 0)
  if (addressTypeOff) builder.addFieldOffset(5, addressTypeOff, 0)
  builder.addFieldInt8(6, key.keyType === 'encryption' ? 1 : 0, 0)
  return builder.endObject()
}

function desktopEpmPublicKeys (profile) {
  const records = []
  const keys = Array.isArray(profile && profile.keys) ? profile.keys : []

  for (const key of keys) {
    if (!key || typeof key !== 'object') continue
    const keyType = readEpmString(key, ['key_type', 'keyType', 'KEY_TYPE']).toLowerCase()
    const publicKey = readEpmString(key, ['public_key', 'publicKey', 'PUBLIC_KEY'])
    if (!publicKey) continue
    records.push({
      keyType: keyType.includes('encrypt') ? 'encryption' : 'signing',
      publicKey,
      xpub: readEpmString(key, ['xpub', 'XPUB']),
      keyAddress: readEpmString(key, ['key_address', 'keyAddress', 'KEY_ADDRESS']),
      addressType: readEpmString(key, ['address_type', 'addressType', 'ADDRESS_TYPE'])
    })
  }

  const signingPublicKey = readEpmString(profile, ['signing_public_key', 'signingPublicKey', 'signing_pubkey_hex'])
  const encryptionPublicKey = readEpmString(profile, ['encryption_public_key', 'encryptionPublicKey', 'encryption_pubkey_hex'])
  const identityPublicKey = readEpmString(profile, ['identity_public_key', 'identityPublicKey', 'public_key', 'publicKey'])
  const xpub = readEpmString(profile, ['xpub', 'XPUB'])
  if (signingPublicKey && !records.some(key => key.keyType === 'signing' && key.publicKey === signingPublicKey)) {
    records.unshift({ keyType: 'signing', publicKey: signingPublicKey, xpub, keyAddress: identityPublicKey, addressType: 'ed25519' })
  }
  if (encryptionPublicKey && !records.some(key => key.keyType === 'encryption' && key.publicKey === encryptionPublicKey)) {
    records.push({ keyType: 'encryption', publicKey: encryptionPublicKey, xpub, keyAddress: '', addressType: 'x25519' })
  }

  return records.slice(0, 16)
}

function readEpmInteger (epm, names) {
  const value = readEpmString(epm, names)
  const parsed = Number.parseInt(value, 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}

function desktopEpmAddresses (profile) {
  const raw = profile && (profile.multiformat_address || profile.multiaddrs || profile.addresses)
  const addresses = Array.isArray(raw) ? raw.filter(value => typeof value === 'string' && value.trim()).map(value => value.trim()) : []
  const peerID = readEpmString(profile, ['peer_id', 'peerId', 'PeerID', 'ID'])
  if (peerID && !addresses.some(address => address.includes(`/p2p/${peerID}`) || address.includes(`/ipns/${peerID}`))) {
    addresses.unshift(`/ipns/${peerID}`)
  }
  return addresses
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

async function readRequestBuffer (req) {
  return await new Promise((resolve, reject) => {
    const chunks = []
    req.on('data', chunk => {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
    })
    req.on('end', () => resolve(Buffer.concat(chunks)))
    req.on('error', reject)
  })
}

function hostedEpmStorePath () {
  return path.join(app.getPath('userData'), HOSTED_EPM_STORE_FILE)
}

function identityStorePassword () {
  if (process.env.SDN_KEY_PASSWORD) return process.env.SDN_KEY_PASSWORD
  if (process.env.SDN_DESKTOP_KEY_PASSWORD) return process.env.SDN_DESKTOP_KEY_PASSWORD

  return [
    'sdn-desktop-hosted-epms',
    os.hostname(),
    os.platform(),
    os.arch(),
    os.homedir(),
    app.getPath('userData')
  ].join('|')
}

function hostedEpmStoreKey () {
  return crypto.scryptSync(identityStorePassword(), HOSTED_EPM_STORE_SALT, 32)
}

function emptyHostedEpmStore () {
  return {
    version: HOSTED_EPM_STORE_VERSION,
    records: {}
  }
}

async function readHostedEpmStore () {
  let raw
  try {
    raw = await fs.promises.readFile(hostedEpmStorePath(), 'utf8')
  } catch (err) {
    if (err.code !== 'ENOENT') {
      logger.warn(`[static-server] failed to read hosted EPM store: ${err.message || err}`)
    }
    return emptyHostedEpmStore()
  }

  try {
    const envelope = JSON.parse(raw)
    const decipher = crypto.createDecipheriv(
      'aes-256-gcm',
      hostedEpmStoreKey(),
      Buffer.from(envelope.iv || '', 'base64')
    )
    decipher.setAuthTag(Buffer.from(envelope.tag || '', 'base64'))
    const plaintext = Buffer.concat([
      decipher.update(Buffer.from(envelope.ciphertext || '', 'base64')),
      decipher.final()
    ]).toString('utf8')
    const store = JSON.parse(plaintext)
    return {
      version: HOSTED_EPM_STORE_VERSION,
      records: store.records && typeof store.records === 'object' ? store.records : {}
    }
  } catch (err) {
    logger.warn(`[static-server] failed to decrypt hosted EPM store: ${err.message || err}`)
    return emptyHostedEpmStore()
  }
}

async function writeHostedEpmStore (store) {
  const iv = crypto.randomBytes(12)
  const cipher = crypto.createCipheriv('aes-256-gcm', hostedEpmStoreKey(), iv)
  const ciphertext = Buffer.concat([
    cipher.update(JSON.stringify({
      version: HOSTED_EPM_STORE_VERSION,
      records: store.records || {}
    }), 'utf8'),
    cipher.final()
  ])

  const envelope = {
    version: HOSTED_EPM_STORE_VERSION,
    algorithm: 'aes-256-gcm',
    kdf: 'scrypt-system-derived',
    iv: iv.toString('base64'),
    tag: cipher.getAuthTag().toString('base64'),
    ciphertext: ciphertext.toString('base64')
  }

  await fs.promises.mkdir(path.dirname(hostedEpmStorePath()), { recursive: true })
  await fs.promises.writeFile(hostedEpmStorePath(), JSON.stringify(envelope, null, 2))
}

function normalizeEpmFieldName (name) {
  return String(name || '').toLowerCase().replace(/[^a-z0-9]/g, '')
}

function isSecretEpmField (name) {
  return SECRET_EPM_FIELD_PATTERN.test(String(name || '')) ||
    SECRET_EPM_FIELD_PATTERN.test(normalizeEpmFieldName(name))
}

function sanitizePublicEPM (value) {
  if (Array.isArray(value)) {
    return value.map(item => sanitizePublicEPM(item))
  }

  if (!value || typeof value !== 'object') {
    return value
  }

  return Object.fromEntries(
    Object.entries(value)
      .filter(([key]) => !isSecretEpmField(key))
      .map(([key, item]) => [key, sanitizePublicEPM(item)])
  )
}

function readEpmString (epm, names) {
  if (!epm || typeof epm !== 'object') return ''

  const normalizedNames = new Set(names.map(normalizeEpmFieldName))
  for (const [key, value] of Object.entries(epm)) {
    if (!normalizedNames.has(normalizeEpmFieldName(key))) continue
    if (typeof value === 'string') return value.trim()
    if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  }

  return ''
}

function bytesToHex (bytes) {
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
}

function uint32BE (value) {
  const buffer = Buffer.alloc(4)
  buffer.writeUInt32BE(value >>> 0, 0)
  return buffer
}

function base58CheckDecode (value) {
  let number = 0n
  for (const char of String(value || '')) {
    const digit = BASE58_ALPHABET.indexOf(char)
    if (digit < 0) throw new Error('invalid xpub base58 character')
    number = number * 58n + BigInt(digit)
  }

  const bytes = []
  while (number > 0n) {
    bytes.unshift(Number(number & 0xffn))
    number >>= 8n
  }
  for (const char of String(value || '')) {
    if (char !== '1') break
    bytes.unshift(0)
  }

  const payload = Buffer.from(bytes)
  if (payload.length < 5) throw new Error('invalid xpub payload')
  const body = payload.subarray(0, payload.length - 4)
  const checksum = payload.subarray(payload.length - 4)
  const expected = crypto.createHash('sha256').update(crypto.createHash('sha256').update(body).digest()).digest().subarray(0, 4)
  if (!checksum.equals(expected)) throw new Error('invalid xpub checksum')
  return body
}

function parseXpub (xpub) {
  const payload = base58CheckDecode(xpub)
  if (payload.length !== 78) throw new Error('invalid xpub length')
  const version = payload.readUInt32BE(0)
  if (version !== HD_XPUB_PUBLIC_VERSION) throw new Error('unsupported xpub version')
  const publicKey = payload.subarray(45, 78)
  secp256k1.ProjectivePoint.fromHex(publicKey)
  return {
    chainCode: payload.subarray(13, 45),
    publicKey
  }
}

function deriveXpubPublicChild (node, index) {
  if (!Number.isInteger(index) || index < 0 || index >= 0x80000000) {
    throw new Error('xpub child index must be a non-hardened integer')
  }
  const digest = crypto.createHmac('sha512', node.chainCode)
    .update(Buffer.concat([node.publicKey, uint32BE(index)]))
    .digest()
  const tweak = BigInt(`0x${digest.subarray(0, 32).toString('hex')}`)
  if (tweak <= 0n || tweak >= secp256k1.CURVE.n) {
    throw new Error('invalid xpub child tweak')
  }
  const publicKey = secp256k1.ProjectivePoint.BASE
    .multiply(tweak)
    .add(secp256k1.ProjectivePoint.fromHex(node.publicKey))
    .toRawBytes(true)
  return {
    chainCode: digest.subarray(32),
    publicKey: Buffer.from(publicKey)
  }
}

function xpubIdentityPath (account = HD_XPUB_ACCOUNT) {
  return `m/${HD_XPUB_PURPOSE}'/${HD_XPUB_COIN_TYPE}'/${account}'`
}

function xpubSigningPath (account = HD_XPUB_ACCOUNT, index = 0) {
  return `${xpubIdentityPath(account)}/${HD_XPUB_SIGNING_CHANGE}/${index}`
}

function xpubEncryptionPath (account = HD_XPUB_ACCOUNT, index = 0) {
  return `${xpubIdentityPath(account)}/${HD_XPUB_ENCRYPTION_CHANGE}/${index}`
}

function readEpmXpub (epm) {
  const direct = readEpmString(epm, ['xpub', 'XPUB', 'extended_public_key', 'extendedPublicKey', 'hd_xpub', 'hdXpub'])
  if (direct) return direct
  const keys = Array.isArray(epm && epm.keys) ? epm.keys : []
  for (const key of keys) {
    const xpub = readEpmString(key, ['xpub', 'XPUB', 'extended_public_key', 'extendedPublicKey', 'hd_xpub', 'hdXpub'])
    if (xpub) return xpub
  }
  return ''
}

function epmAccount (epm) {
  const parsed = Number.parseInt(readEpmString(epm, ['account', 'wallet_account', 'walletAccount']), 10)
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : HD_XPUB_ACCOUNT
}

function derivePublicIdentityKeysFromXpub (xpub, account = HD_XPUB_ACCOUNT, index = 0) {
  const trimmedXpub = String(xpub || '').trim()
  if (!trimmedXpub) return null
  const accountKey = parseXpub(trimmedXpub)
  const signingKey = deriveXpubPublicChild(deriveXpubPublicChild(accountKey, HD_XPUB_SIGNING_CHANGE), index)
  const encryptionKey = deriveXpubPublicChild(deriveXpubPublicChild(accountKey, HD_XPUB_ENCRYPTION_CHANGE), index)
  return {
    xpub: trimmedXpub,
    signing_public_key: bytesToHex(signingKey.publicKey),
    encryption_public_key: bytesToHex(encryptionKey.publicKey),
    signing_key_path: xpubSigningPath(account, index),
    encryption_key_path: xpubEncryptionPath(account, index)
  }
}

async function normalizeEpmPublicIdentityKeys (epmJson) {
  const publicEpm = sanitizePublicEPM(epmJson || {})
  const xpub = readEpmXpub(publicEpm)
  if (!xpub) return publicEpm

  const account = epmAccount(publicEpm)
  let derived
  try {
    derived = derivePublicIdentityKeysFromXpub(xpub, account)
  } catch (err) {
    logger.warn(`[static-server] failed to derive public keys from xpub: ${err.message || err}`)
    publicEpm.xpub = xpub
    return publicEpm
  }
  if (!derived) return publicEpm

  const identityPublicKey = readEpmString(publicEpm, ['identity_public_key', 'identityPublicKey', 'public_key', 'publicKey'])
  const preservedKeys = (Array.isArray(publicEpm.keys) ? publicEpm.keys : [])
    .filter(key => {
      const type = epmPublicKeyType(key)
      return type !== 'signing' && type !== 'encryption'
    })

  publicEpm.xpub = derived.xpub
  publicEpm.signing_public_key = derived.signing_public_key
  publicEpm.encryption_public_key = derived.encryption_public_key
  publicEpm.keys = [
    {
      key_type: 'signing',
      public_key: derived.signing_public_key,
      xpub: derived.xpub,
      key_address: identityPublicKey,
      address_type: 'secp256k1',
      derivation_path: derived.signing_key_path
    },
    {
      key_type: 'encryption',
      public_key: derived.encryption_public_key,
      xpub: derived.xpub,
      key_address: '',
      address_type: 'secp256k1',
      derivation_path: derived.encryption_key_path
    },
    ...preservedKeys
  ]
  return publicEpm
}

async function publicIdentityRecord (id, kind, epmJson, updatedAt = new Date().toISOString()) {
  const publicEpm = await normalizeEpmPublicIdentityKeys(epmJson || {})
  const label = readEpmString(publicEpm, ['dn', 'display_name', 'displayName', 'name']) || id
  const peerId = readEpmString(publicEpm, ['peer_id', 'peerId', 'ipfs_peer_id', 'ipfsPeerId'])
  const epmCid = readEpmString(publicEpm, ['epm_cid', 'epmCid', 'cid'])

  return {
    id,
    kind,
    label,
    peerId,
    epmCid,
    epmJson: publicEpm,
    epm_json: publicEpm,
    updatedAt,
    updated_at: updatedAt
  }
}

async function nodeSelfIdentityRecord () {
  const profile = await readDesktopNodeProfile()
  const record = await publicIdentityRecord('self', 'node-self', profile)
  if (!record.epmCid) {
    const localRecord = await desktopLocalEpmDataRecord(profile)
    record.epmCid = localRecord.cid
    record.epmJson.epm_cid = localRecord.cid
    record.epmJson.epmCid = localRecord.cid
    record.epm_json = record.epmJson
  }
  // Task F2 follow-up: EVERY view of the node's own record carries the
  // self-recognition ceiling, exactly like applyWalletNodeIdentity's
  // wallet-apply path (see the F2 comment there) — the self record IS this
  // node's web-of-trust root (mirrors Registry.SetRootIdentity, C6), and
  // 'ultimate' is reserved for that root. Structurally self-only: this
  // function exists solely for the 'self'/'node-self' record; hosted
  // identities never pass through here.
  record.epmJson.trust_level = normalizeDesktopTrustLevel('ultimate')
  record.epm_json = record.epmJson
  return record
}

async function hostedIdentityRecord (id, payload, existing = {}) {
  const epmJson = payload.epm_json || payload.epmJson || payload
  return publicIdentityRecord(id, 'hosted', epmJson, existing.updatedAt || existing.updated_at || new Date().toISOString())
}

function escapeVCardValue (value) {
  return String(value || '')
    .replace(/\\/g, '\\\\')
    .replace(/\r?\n/g, '\\n')
    .replace(/,/g, '\\,')
    .replace(/;/g, '\\;')
}

const IDENTITY_ALIAS_DOMAINS = {
  signing: 'signing.spacedatanetwork.org',
  encryption: 'encryption.spacedatanetwork.org',
  bitcoin: 'bitcoin.spacedatanetwork.org',
  ethereum: 'ethereum.spacedatanetwork.org',
  solana: 'solana.spacedatanetwork.org'
}

function identityRecordToVCard (record) {
  const epm = sanitizePublicEPM(record.epmJson || {})
  const publicKey = readEpmString(epm, [
    'public_key',
    'PUBLIC_KEY',
    'publicKey',
    'signing_public_key',
    'signingPublicKey',
    'signing_pubkey_hex',
    'encryption_public_key',
    'encryptionPublicKey',
    'encryption_pubkey_hex'
  ])
  const signingPublicKey = readEpmString(epm, [
    'signing_public_key',
    'signingPublicKey',
    'signing_pubkey_hex',
    'signingPubkeyHex'
  ]) || findIdentityKey(epm, 'signing')
  const encryptionPublicKey = readEpmString(epm, [
    'encryption_public_key',
    'encryptionPublicKey',
    'encryption_pubkey_hex',
    'encryptionPubkeyHex'
  ]) || findIdentityKey(epm, 'encryption')
  const displayName = readEpmString(epm, ['dn', 'DN', 'display_name', 'displayName', 'name']) || record.label || 'Space Data Network'
  const lines = [
    'BEGIN:VCARD',
    'VERSION:3.0',
    'PRODID;VALUE=TEXT:-//Space Data Network//Desktop//EN'
  ]

  addVCardStructuredName(lines, epm, displayName)
  addVCardLine(lines, 'FN', displayName)
  addVCardLine(lines, 'ORG', readEpmString(epm, ['legal_name', 'legalName', 'organization', 'org']))
  addVCardLine(lines, 'EMAIL;TYPE=INTERNET', readEpmString(epm, ['email', 'email_address', 'emailAddress', 'mail']))
  addVCardLine(lines, 'TEL', readEpmString(epm, ['telephone', 'phone', 'tel']))
  addVCardLine(lines, 'TITLE', readEpmString(epm, ['job_title', 'jobTitle', 'title']))
  addVCardLine(lines, 'ROLE', readEpmString(epm, ['occupation', 'role']))
  addVCardAddressLine(lines, epm)
  addVCardLine(lines, 'UID', record.peerId)
  addVCardLine(lines, 'X-SDN-DIRECTORY-KIND', record.kind === 'node-self' ? 'node' : 'user')
  addVCardLine(lines, 'X-SDN-PEER-ID', record.peerId)
  addVCardLine(lines, 'X-SDN-EPM-CID', record.epmCid)
  addVCardLine(lines, 'X-SDN-XPUB', readEpmXpub(epm))
  const publicKeyEmail = publicKeyEmailAddress(publicKey)
  addVCardLine(lines, 'EMAIL;TYPE=INTERNET', publicKeyEmail)
  addVCardIdentityEmailLines(lines, epm, signingPublicKey, encryptionPublicKey)
  addVCardLine(lines, 'X-SDN-PUBLIC-KEY', publicKey)
  addVCardLine(lines, 'X-SDN-SIGNING-PUBLIC-KEY', signingPublicKey)
  addVCardLine(lines, 'X-SDN-ENCRYPTION-PUBLIC-KEY', encryptionPublicKey)
  lines.push('END:VCARD')

  return `${lines.map(foldVCardLine).join('\r\n')}\r\n`
}

function publicKeyEmailAddress (publicKey) {
  const localPart = String(publicKey || '').trim().replace(/\s+/g, '').replace(/[^A-Za-z0-9._%+-]/g, '')
  return localPart ? `${localPart}@spacedatanetwork.org` : ''
}

function addVCardStructuredName (lines, epm, displayName) {
  const familyName = readEpmString(epm, ['family_name', 'familyName'])
  let givenName = readEpmString(epm, ['given_name', 'givenName'])
  const additionalName = readEpmString(epm, ['additional_name', 'additionalName'])
  const honorificPrefix = readEpmString(epm, ['honorific_prefix', 'honorificPrefix'])
  const honorificSuffix = readEpmString(epm, ['honorific_suffix', 'honorificSuffix'])
  if (!familyName && !givenName && !additionalName && !honorificPrefix && !honorificSuffix) {
    givenName = displayName
  }
  lines.push(`N:${[familyName, givenName, additionalName, honorificPrefix, honorificSuffix].map(escapeVCardValue).join(';')}`)
}

function addVCardAddressLine (lines, epm) {
  const address = epm.address && typeof epm.address === 'object' && !Array.isArray(epm.address) ? epm.address : epm
  const parts = [
    readEpmString(address, ['po_box', 'poBox']),
    '',
    readEpmString(address, ['street', 'street_address', 'streetAddress']),
    readEpmString(address, ['locality', 'city']),
    readEpmString(address, ['region', 'state', 'province']),
    readEpmString(address, ['postal_code', 'postalCode', 'zip']),
    readEpmString(address, ['country', 'country_name', 'countryName'])
  ]
  if (parts.some(Boolean)) {
    lines.push(`ADR;TYPE=WORK:${parts.map(escapeVCardValue).join(';')}`)
  }
}

function addVCardIdentityEmailLines (lines, epm, signingPublicKey, encryptionPublicKey) {
  const seen = new Set()
  const addAlias = (type, value) => {
    const trimmed = String(value || '').trim()
    if (!trimmed || !IDENTITY_ALIAS_DOMAINS[type] || !isSafeEmailLocalPart(trimmed)) return
    const line = `EMAIL;type=INTERNET;type=${type}:${trimmed}@${IDENTITY_ALIAS_DOMAINS[type]}`
    if (seen.has(line)) return
    seen.add(line)
    lines.push(line)
  }

  addAlias('signing', signingPublicKey)
  addAlias('encryption', encryptionPublicKey)
  addAlias('bitcoin', readEpmString(epm, ['bitcoin_address', 'bitcoinAddress']) || findChainAddress(epm, 'bitcoin'))
  addAlias('ethereum', readEpmString(epm, ['ethereum_address', 'ethereumAddress']) || findChainAddress(epm, 'ethereum'))
  addAlias('solana', readEpmString(epm, ['solana_address', 'solanaAddress']) || findChainAddress(epm, 'solana'))
}

function findIdentityKey (epm, type) {
  const keys = Array.isArray(epm.keys) ? epm.keys : []
  for (const key of keys) {
    if (!key || typeof key !== 'object') continue
    const publicKey = readEpmString(key, ['public_key', 'PUBLIC_KEY', 'publicKey'])
    if (!publicKey) continue
    const keyType = readEpmString(key, ['key_type', 'KEY_TYPE', 'keyType']).toLowerCase()
    const addressType = readEpmString(key, ['address_type', 'ADDRESS_TYPE', 'addressType']).toLowerCase()
    if (type === 'encryption' && (keyType === 'encryption' || addressType === 'x25519')) return publicKey
    if (type === 'signing' && (keyType === 'signing' || (addressType && addressType !== 'x25519'))) return publicKey
  }
  return ''
}

function findChainAddress (epm, chain) {
  const proofs = Array.isArray(epm.chain_proofs) ? epm.chain_proofs : []
  for (const proof of proofs) {
    if (!proof || typeof proof !== 'object') continue
    if (readEpmString(proof, ['chain', 'CHAIN']).toLowerCase() === chain) {
      return readEpmString(proof, ['address', 'ADDRESS'])
    }
  }
  return ''
}

function addVCardLine (lines, key, value) {
  if (String(value || '').trim()) lines.push(`${key}:${escapeVCardValue(value)}`)
}

function foldVCardLine (line) {
  const value = String(line)
  if (value.length <= 74) return value
  const chunks = []
  for (let offset = 0; offset < value.length; offset += 74) {
    chunks.push(value.slice(offset, offset + 74))
  }
  return chunks.join('\r\n ')
}

function isSafeEmailLocalPart (value) {
  return /^[A-Za-z0-9._+-]+$/.test(String(value || '').trim())
}

async function readIdentityRecord (id) {
  if (id === 'self') {
    return nodeSelfIdentityRecord()
  }

  const store = await readHostedEpmStore()
  const stored = store.records[id]
  if (!stored) return null
  return publicIdentityRecord(id, 'hosted', stored.epmJson || stored.epm_json || stored, stored.updatedAt || stored.updated_at)
}

async function listIdentityRecords () {
  const store = await readHostedEpmStore()
  const hosted = await Promise.all(Object.entries(store.records)
    .map(([id, record]) => publicIdentityRecord(id, 'hosted', record.epmJson || record.epm_json || record, record.updatedAt || record.updated_at)))
  return [
    await nodeSelfIdentityRecord(),
    ...hosted
  ]
}

function directoryKindForIdentity (record) {
  const entityType = readEpmString(record.epmJson, ['entity_type', 'entityType', 'type']).toLowerCase()
  if (record.kind === 'node-self' || entityType.includes('node')) return 'node'
  return 'user'
}

function identityRecordToDirectoryEntry (record) {
  const publicKey = readEpmString(record.epmJson, [
    'public_key',
    'publicKey',
    'signing_public_key',
    'signingPublicKey',
    'encryption_public_key',
    'encryptionPublicKey'
  ])

  return {
    id: record.id,
    dn: record.label,
    entity_type: directoryKindForIdentity(record) === 'node' ? 'Node' : readEpmString(record.epmJson, ['entity_type', 'entityType']) || 'Person',
    peer_id: record.peerId,
    epm_cid: record.epmCid,
    public_key: publicKey,
    epm_json: record.epmJson,
    updated_at: record.updated_at
  }
}

function matchesDirectoryQuery (entry, query) {
  if (!query) return true
  return JSON.stringify(entry).toLowerCase().includes(query.toLowerCase())
}

async function serveDesktopDirectoryAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (req.method !== 'GET' || (parsed.pathname !== '/api/directory/nodes' && parsed.pathname !== '/api/directory/users')) {
    return false
  }

  const requestedKind = parsed.pathname.endsWith('/nodes') ? 'node' : 'user'
  const query = parsed.searchParams.get('q') || ''
  const limit = Math.max(1, Math.min(Number.parseInt(parsed.searchParams.get('limit') || '50', 10) || 50, 200))
  const records = (await listIdentityRecords())
    .filter(record => directoryKindForIdentity(record) === requestedKind)
    .map(identityRecordToDirectoryEntry)
    .filter(entry => matchesDirectoryQuery(entry, query))
    .slice(0, limit)

  sendJSON(res, 200, requestedKind === 'node' ? { nodes: records } : { users: records })
  return true
}

async function serveDesktopIdentityAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  const segments = parsed.pathname.split('/').filter(Boolean)

  if (segments[0] !== 'api' || segments[1] !== 'identity' || segments[2] !== 'epms') {
    return false
  }

  if (req.method === 'GET' && segments.length === 3) {
    sendJSON(res, 200, {
      epms: await listIdentityRecords()
    })
    return true
  }

  const identityId = segments[3] ? decodeURIComponent(segments[3]) : ''
  const suffix = segments[4] || ''
  if (!identityId) {
    sendJSON(res, 404, { error: 'identity not found' })
    return true
  }

  if (req.method === 'GET' && segments.length >= 4) {
    const record = await readIdentityRecord(identityId)
    if (!record) {
      sendJSON(res, 404, { error: 'identity not found' })
      return true
    }

    if (suffix === 'vcard' || suffix === 'vcf') {
      sendText(res, 200, 'text/vcard; charset=utf-8', identityRecordToVCard(record))
      return true
    }

    if (suffix === 'epm') {
      sendJSON(res, 200, record.epmJson)
      return true
    }

    sendJSON(res, 200, record)
    return true
  }

  if (req.method === 'PUT' && segments.length === 4) {
    let payload
    try {
      payload = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON identity' })
      return true
    }

    if (identityId === 'self') {
      const epmJson = sanitizePublicEPM(payload.epm_json || payload.epmJson || payload)
      const next = { ...(await readDesktopNodeProfile()), ...epmJson }
      await fs.promises.mkdir(path.dirname(localProfilePath()), { recursive: true })
      await fs.promises.writeFile(localProfilePath(), JSON.stringify(next, null, 2))
      // F2 follow-up: respond via nodeSelfIdentityRecord (re-reads the
      // just-written profile) so the PUT response matches every other view
      // of the self record — same ultimate self-recognition stamp, same
      // epmCid fallback — instead of a divergent inline rebuild.
      sendJSON(res, 200, await nodeSelfIdentityRecord())
      return true
    }

    const store = await readHostedEpmStore()
    const record = await hostedIdentityRecord(identityId, payload, store.records[identityId])
    store.records[identityId] = record
    await writeHostedEpmStore(store)
    sendJSON(res, 200, record)
    return true
  }

  if (req.method === 'DELETE' && segments.length === 4 && identityId !== 'self') {
    const store = await readHostedEpmStore()
    delete store.records[identityId]
    await writeHostedEpmStore(store)
    sendJSON(res, 200, { ok: true })
    return true
  }

  sendJSON(res, 405, { error: 'method not allowed' })
  return true
}

async function serveDesktopPeerEPMAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  const match = parsed.pathname.match(/^\/api\/peers\/([^/]+)\/epm(?:\/vcard)?$/)
  if (!match) return false

  if (req.method !== 'GET') {
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  let peerId
  try {
    peerId = decodeURIComponent(match[1])
  } catch {
    sendJSON(res, 400, { error: 'invalid peer ID' })
    return true
  }

  const record = (await listIdentityRecords())
    .find(candidate => candidate.peerId === peerId)
  if (!record) {
    sendJSON(res, 404, { error: 'peer EPM not found' })
    return true
  }

  if (parsed.pathname.endsWith('/vcard')) {
    sendText(res, 200, 'text/vcard; charset=utf-8', identityRecordToVCard(record))
    return true
  }

  sendBuffer(res, 200, 'application/x-flatbuffers', await buildDesktopNodeEPM(record.epmJson))
  return true
}

function desktopAuthUsersPath () {
  return path.join(app.getPath('userData'), DESKTOP_AUTH_USERS_FILE)
}

async function readDesktopAuthUsers () {
  try {
    const parsed = JSON.parse(await fs.promises.readFile(desktopAuthUsersPath(), 'utf8'))
    const users = Array.isArray(parsed.users) ? parsed.users : []
    // Migrate any pre-F1 desktop-only trust_level values ('local', etc.) to
    // the canonical scale on read so already-registered xpub users keep
    // resolving identically to freshly-created ones and to the server.
    return users.map(user => ({
      ...user,
      trust_level: normalizeDesktopTrustLevel(user && user.trust_level, 'admin')
    }))
  } catch {
    return []
  }
}

async function writeDesktopAuthUsers (users) {
  await fs.promises.mkdir(path.dirname(desktopAuthUsersPath()), { recursive: true })
  await fs.promises.writeFile(desktopAuthUsersPath(), JSON.stringify({ users }, null, 2))
}

function sanitizeDesktopAuthUser (payload) {
  return {
    xpub: readEpmString(payload, ['xpub']),
    label: readEpmString(payload, ['label', 'name', 'dn']),
    role: readEpmString(payload, ['role']) || 'admin',
    // Desktop auth users are locally-managed operator accounts, the same
    // role the server bootstraps its first admin into (peers.Admin) — see
    // internal/auth/handler.go's firstAdminBootstrap. Any legacy or
    // server-canonical value supplied by the caller is normalized to the
    // canonical PGP scale (see CANONICAL_TRUST_LEVELS above); unrecognized
    // input falls back to 'admin' rather than silently becoming 'unknown',
    // since every user reaching this store is, by construction, a desktop
    // admin account.
    trust_level: normalizeDesktopTrustLevel(readEpmString(payload, ['trust_level', 'trustLevel']), 'admin')
  }
}

async function readDesktopAuthUserPayload (req, res) {
  try {
    const body = typeof req === 'string' ? req : await readRequestBody(req)
    return JSON.parse(body || '{}')
  } catch {
    sendJSON(res, 400, { error: 'invalid JSON auth user' })
    return null
  }
}

async function serveDesktopAuthUsersAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (parsed.pathname !== '/api/auth/users' && !parsed.pathname.startsWith('/api/auth/users/')) {
    return false
  }

  const bodyPromise = req.method === 'POST' || req.method === 'PUT'
    ? readRequestBody(req)
    : null
  const users = await readDesktopAuthUsers()

  if (req.method === 'GET' && parsed.pathname === '/api/auth/users') {
    sendJSON(res, 200, { users })
    return true
  }

  if (req.method === 'POST' && parsed.pathname === '/api/auth/users') {
    const payload = await readDesktopAuthUserPayload(await bodyPromise, res)
    if (!payload) return true
    const user = sanitizeDesktopAuthUser(payload)
    if (!user.xpub) {
      sendJSON(res, 400, { error: 'xpub is required' })
      return true
    }
    if (users.some(existing => existing.xpub === user.xpub)) {
      sendJSON(res, 409, { error: 'user already exists' })
      return true
    }
    users.push(user)
    await writeDesktopAuthUsers(users)
    sendJSON(res, 200, { user })
    return true
  }

  if (req.method === 'PUT' && parsed.pathname.startsWith('/api/auth/users/')) {
    let xpub
    try {
      xpub = decodeURIComponent(parsed.pathname.slice('/api/auth/users/'.length))
    } catch {
      sendJSON(res, 400, { error: 'invalid xpub' })
      return true
    }
    const payload = await readDesktopAuthUserPayload(await bodyPromise, res)
    if (!payload) return true
    const user = sanitizeDesktopAuthUser({ ...payload, xpub })
    if (!user.xpub) {
      sendJSON(res, 400, { error: 'xpub is required' })
      return true
    }
    const index = users.findIndex(existing => existing.xpub === user.xpub)
    if (index === -1) users.push(user)
    else users[index] = user
    await writeDesktopAuthUsers(users)
    sendJSON(res, 200, { user })
    return true
  }

  sendJSON(res, 405, { error: 'method not allowed' })
  return true
}

async function serveDesktopNodeIdentityAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)

  if (parsed.pathname === '/api/node/identity/settings/flatbuffer-storage-location') {
    if (req.method !== 'POST') {
      sendJSON(res, 405, { error: 'method not allowed' })
      return true
    }
    if (!dialog || typeof dialog.showOpenDialog !== 'function') {
      sendJSON(res, 501, { error: 'directory picker unavailable' })
      return true
    }
    let payload
    try {
      payload = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON settings' })
      return true
    }
    const currentPath = normalizeFlatbufferStoragePath(payload && (payload.current_path ?? payload.currentPath))
    const result = await dialog.showOpenDialog({
      title: 'Select FlatBuffer storage location',
      defaultPath: currentPath,
      properties: ['openDirectory', 'createDirectory']
    })
    sendJSON(res, 200, {
      canceled: Boolean(result.canceled) || !result.filePaths || !result.filePaths[0],
      path: result.filePaths && result.filePaths[0] ? result.filePaths[0] : null
    })
    return true
  }

  if (parsed.pathname === '/api/node/identity/settings') {
    if (req.method === 'GET') {
      sendJSON(res, 200, await readNodeIdentitySettings())
      return true
    }
    if (req.method === 'PUT') {
      let payload
      try {
        payload = JSON.parse(await readRequestBody(req) || '{}')
      } catch {
        sendJSON(res, 400, { error: 'invalid JSON settings' })
        return true
      }
      sendJSON(res, 200, await writeNodeIdentitySettings(payload))
      return true
    }
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  if (parsed.pathname === '/api/node/identity/session') {
    if (req.method === 'GET') {
      sendJSON(res, 200, await readNodeIdentitySession())
      return true
    }
    if (req.method === 'DELETE') {
      sendJSON(res, 200, await clearNodeIdentitySession())
      return true
    }
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  if (parsed.pathname === '/api/node/identity/wallet-storage') {
    if (req.method === 'GET') {
      sendJSON(res, 200, await readNodeWalletStorage())
      return true
    }
    if (req.method === 'PUT') {
      let payload
      try {
        payload = JSON.parse(await readRequestBody(req) || '{}')
      } catch {
        sendJSON(res, 400, { error: 'invalid JSON wallet storage' })
        return true
      }
      sendJSON(res, 200, await patchNodeWalletStorage(payload.entries || payload))
      return true
    }
    if (req.method === 'DELETE') {
      sendJSON(res, 200, await clearNodeWalletStorage())
      return true
    }
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  if (parsed.pathname !== '/api/node/identity/wallet') {
    return false
  }

  if (req.method !== 'PUT') {
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  let payload
  try {
    payload = JSON.parse(await readRequestBody(req) || '{}')
  } catch {
    sendJSON(res, 400, { error: 'invalid JSON wallet identity' })
    return true
  }

  const proposed = await normalizeWalletNodeIdentity(payload.wallet_identity || payload.walletIdentity || payload)
  if (!proposed.peer_id || !proposed.xpub || !proposed.signing_public_key || !proposed.encryption_public_key) {
    sendJSON(res, 400, { error: 'wallet identity requires peer_id and an xpub that derives signing and encryption public keys' })
    return true
  }

  const current = await readDesktopNodeProfile()
  const currentKeys = nodeIdentityKeySummary(await normalizeEpmPublicIdentityKeys(current))
  const proposedKeys = nodeIdentityKeySummary(proposed)
  const hasCurrentKeys = Boolean(currentKeys.signing_public_key || currentKeys.encryption_public_key || currentKeys.identity_public_key)
  const sameNodePeer = Boolean(currentKeys.peer_id && proposedKeys.peer_id && currentKeys.peer_id === proposedKeys.peer_id)
  const keysMatch = (
    (!currentKeys.peer_id || currentKeys.peer_id === proposedKeys.peer_id) &&
    (!currentKeys.identity_public_key || currentKeys.identity_public_key === proposedKeys.identity_public_key) &&
    (!currentKeys.signing_public_key || currentKeys.signing_public_key === proposedKeys.signing_public_key) &&
    (!currentKeys.encryption_public_key || currentKeys.encryption_public_key === proposedKeys.encryption_public_key)
  )
  const matching = !hasCurrentKeys || sameNodePeer || keysMatch

  if (!matching && payload.replace !== true) {
    sendJSON(res, 409, {
      status: 'mismatch',
      current: currentKeys,
      proposed: proposedKeys
    })
    return true
  }

  const next = {
    ...current,
    ...proposed,
    entity_type: 'Node',
    updated_at: new Date().toISOString()
  }
  await fs.promises.mkdir(path.dirname(localProfilePath()), { recursive: true })
  await fs.promises.writeFile(localProfilePath(), JSON.stringify(next, null, 2))
  const publicProfile = await applyWalletNodeIdentity(next)
  const session = await writeNodeIdentitySession(publicProfile)
  sendJSON(res, 200, {
    status: keysMatch && hasCurrentKeys ? 'unchanged' : 'updated',
    profile: publicProfile,
    session
  })
  return true
}

// Task F2: the wallet key applied through PUT /api/node/identity/wallet
// (above) *becomes* the node's own identity in the browser — in the desktop
// app the user's wallet key IS the node key, there is no separate operator
// identity. Recognizing that identity as this node's own root is
// self-recognition, not an operator-style trust assignment: it mirrors
// sdn-server's Registry.SetRootIdentity (internal/peers/trust.go, commit
// 283cbf95 C6), where a node's own key is planted as the web-of-trust root,
// and Ultimate (peers.Ultimate == 5) is the PGP-scale ceiling reserved for
// that root. This never routes through the C7-blocked operator-assignment
// path — the /api/auth/users add/update-trust handlers' assignableTrustLevel
// intentionally rejects Ultimate/Never (handler.go), and this function calls
// none of that; it only ever stamps the record produced for 'self' /
// 'node-self' by publicIdentityRecord, which is used exclusively for this
// node's own profile (see nodeSelfIdentityRecord) and never for a hosted EPM
// belonging to someone else. A hosted (non-self) identity is therefore
// structurally unreachable from this function and never receives 'ultimate'.
async function applyWalletNodeIdentity (profile) {
  const record = await publicIdentityRecord('self', 'node-self', profile)
  record.epmJson.trust_level = normalizeDesktopTrustLevel('ultimate')
  record.epm_json = record.epmJson
  return record.epmJson
}

async function normalizeWalletNodeIdentity (value) {
  const source = value && typeof value === 'object' ? sanitizePublicEPM(value) : {}
  const peerID = readEpmString(source, ['peer_id', 'peerId'])
  const xpub = readEpmString(source, ['xpub', 'XPUB'])
  const account = epmAccount(source)
  const identityPublicKey = readEpmString(source, ['identity_public_key', 'identityPublicKey', 'public_key', 'publicKey'])
  let derived = null
  try {
    derived = derivePublicIdentityKeysFromXpub(xpub, account)
  } catch {
    derived = null
  }
  const signingPublicKey = derived?.signing_public_key ||
    readEpmString(source, ['signing_public_key', 'signingPublicKey', 'signing_pubkey_hex'])
  const encryptionPublicKey = derived?.encryption_public_key ||
    readEpmString(source, ['encryption_public_key', 'encryptionPublicKey', 'encryption_pubkey_hex'])
  const signatureTimestamp = readEpmInteger(source, ['signature_timestamp', 'signatureTimestamp'])
  const keys = []
  if (signingPublicKey) {
    keys.push({
      key_type: 'signing',
      public_key: signingPublicKey,
      xpub,
      key_address: identityPublicKey,
      address_type: derived ? 'secp256k1' : 'ed25519',
      derivation_path: derived?.signing_key_path || xpubSigningPath(account)
    })
  }
  if (encryptionPublicKey) {
    keys.push({
      key_type: 'encryption',
      public_key: encryptionPublicKey,
      xpub,
      address_type: derived ? 'secp256k1' : 'x25519',
      derivation_path: derived?.encryption_key_path || xpubEncryptionPath(account)
    })
  }

  return {
    peer_id: peerID,
    xpub,
    wallet_account_id: readEpmString(source, ['wallet_account_id', 'walletAccountId']),
    wallet_account_label: readEpmString(source, ['wallet_account_label', 'walletAccountLabel']),
    identity_public_key: identityPublicKey,
    public_key: identityPublicKey || signingPublicKey,
    signing_public_key: signingPublicKey,
    encryption_public_key: encryptionPublicKey,
    signature: readEpmString(source, ['signature', 'epm_signature']),
    signature_payload: readEpmString(source, ['signature_payload', 'signaturePayload']),
    signature_timestamp: signatureTimestamp || Math.floor(Date.now() / 1000),
    keys
  }
}

function nodeIdentityKeySummary (profile) {
  const signingKey = epmPublicKeyRecord(profile, 'signing')
  const encryptionKey = epmPublicKeyRecord(profile, 'encryption')
  return {
    peer_id: readEpmString(profile, ['peer_id', 'peerId']),
    identity_public_key: readEpmString(profile, ['identity_public_key', 'identityPublicKey', 'public_key', 'publicKey']) ||
      readEpmString(signingKey, ['key_address', 'keyAddress', 'KEY_ADDRESS']),
    signing_public_key: readEpmString(profile, ['signing_public_key', 'signingPublicKey', 'signing_pubkey_hex']) ||
      readEpmString(signingKey, ['public_key', 'publicKey', 'PUBLIC_KEY']),
    encryption_public_key: readEpmString(profile, ['encryption_public_key', 'encryptionPublicKey', 'encryption_pubkey_hex']) ||
      readEpmString(encryptionKey, ['public_key', 'publicKey', 'PUBLIC_KEY']),
    wallet_account_id: readEpmString(profile, ['wallet_account_id', 'walletAccountId']),
    wallet_account_label: readEpmString(profile, ['wallet_account_label', 'walletAccountLabel'])
  }
}

function epmPublicKeyRecord (profile, keyType) {
  const keys = Array.isArray(profile && profile.keys)
    ? profile.keys
    : Array.isArray(profile && profile.KEYS)
      ? profile.KEYS
      : []
  return keys.find(key => epmPublicKeyType(key) === keyType) || {}
}

function epmPublicKeyType (key) {
  const raw = readEpmString(key, ['key_type', 'keyType', 'KEY_TYPE'])
  const numeric = Number.parseInt(raw, 10)
  if (Number.isFinite(numeric)) return numeric === 1 ? 'encryption' : 'signing'
  const normalized = raw.toLowerCase()
  if (normalized.includes('encrypt') || normalized.includes('x25519')) return 'encryption'
  if (normalized.includes('sign') || normalized.includes('ed25519')) return 'signing'
  return ''
}

async function serveDesktopNodeEPMAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (
    parsed.pathname !== '/api/node/epm/json' &&
    parsed.pathname !== '/api/node/epm/vcard' &&
    parsed.pathname !== '/api/node/epm'
  ) {
    return false
  }

  if (req.method === 'GET' && parsed.pathname === '/api/node/epm/json') {
    sendJSON(res, 200, await normalizeEpmPublicIdentityKeys(await readDesktopNodeProfile()))
    return true
  }

  if (req.method === 'GET' && parsed.pathname === '/api/node/epm/vcard') {
    sendText(res, 200, 'text/vcard; charset=utf-8', identityRecordToVCard(await publicIdentityRecord('self', 'node-self', await readDesktopNodeProfile())))
    return true
  }

  if (req.method === 'GET' && parsed.pathname === '/api/node/epm') {
    sendBuffer(res, 200, 'application/x-flatbuffers', await buildDesktopNodeEPM(await readDesktopNodeProfile()))
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
    sendBuffer(res, 200, 'application/x-flatbuffers', await buildDesktopNodeEPM(next))
    return true
  }

  sendJSON(res, 405, { error: 'method not allowed' })
  return true
}

async function serveDesktopFlatSqlPersistenceAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  const prefix = '/api/flatsql/persistence/'
  if (!parsed.pathname.startsWith(prefix)) return false

  let key
  try {
    key = decodeURIComponent(parsed.pathname.slice(prefix.length))
  } catch {
    sendJSON(res, 400, { error: 'invalid persistence key' })
    return true
  }

  if (!key.trim()) {
    sendJSON(res, 400, { error: 'persistence key is required' })
    return true
  }

  const bodyPromise = req.method === 'PUT' ? readRequestBuffer(req) : null
  const filePath = await flatSqlPersistenceFilePath(key)

  if (req.method === 'GET') {
    try {
      sendBuffer(res, 200, 'application/octet-stream', await fs.promises.readFile(filePath))
    } catch {
      sendJSON(res, 404, { error: 'persistence blob not found' })
    }
    return true
  }

  if (req.method === 'PUT') {
    await fs.promises.writeFile(filePath, await bodyPromise)
    res.writeHead(204, staticAssetHeaders('application/octet-stream'))
    res.end()
    return true
  }

  if (req.method === 'DELETE') {
    await fs.promises.rm(filePath, { force: true })
    res.writeHead(204, staticAssetHeaders('application/octet-stream'))
    res.end()
    return true
  }

  sendJSON(res, 405, { error: 'method not allowed' })
  return true
}

async function serveDesktopLocalDataAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)

  if (req.method === 'POST' && parsed.pathname === '/api/v1/conjunction/screen') {
    let payload = {}
    try {
      payload = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON conjunction request' })
      return true
    }
    sendJSON(res, 200, desktopConjunctionScreenResult(payload))
    return true
  }

  if (parsed.pathname === '/api/v1/conjunction/screen') {
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  if (req.method === 'POST' && parsed.pathname === '/api/v1/search/providers') {
    let payload = {}
    try {
      payload = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON search request' })
      return true
    }
    sendJSON(res, 200, await desktopLocalProviderSearchResult(payload))
    return true
  }

  if (req.method === 'POST' && parsed.pathname === '/api/v1/search/data') {
    let payload = {}
    try {
      payload = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON search request' })
      return true
    }
    sendJSON(res, 200, await desktopLocalDataSearchResult(payload))
    return true
  }

  if (parsed.pathname === '/api/v1/search/providers' || parsed.pathname === '/api/v1/search/data') {
    sendJSON(res, 405, { error: 'method not allowed' })
    return true
  }

  if (req.method === 'GET' && parsed.pathname === '/api/v1/data/health') {
    sendJSON(res, 200, {
      healthy: true,
      details: {
        runtime: 'desktop-local',
        object_index: 'degraded',
        message: 'local SQL index is not wired in desktop-local yet'
      }
    })
    return true
  }

  if (req.method === 'GET' && parsed.pathname === '/api/v1/data/summary') {
    const record = await desktopLocalEpmDataRecord()
    sendJSON(res, 200, {
      total_records: 1,
      total_bytes: record.size_bytes,
      schemas: [
        {
          schema_name: 'EPM.fbs',
          count: 1,
          total_bytes: record.size_bytes
        }
      ],
      sources: [
        {
          schema_name: 'EPM.fbs',
          provider_id: 'local-node',
          source_name: 'local-epm',
          batch_id: 'local',
          count: 1,
          total_bytes: record.size_bytes
        }
      ]
    })
    return true
  }

  if (req.method === 'GET' && parsed.pathname === '/api/v1/data/objects') {
    sendJSON(res, 200, { objects: [] })
    return true
  }

  if (req.method === 'GET' && parsed.pathname.startsWith('/api/v1/data/records/')) {
    const segments = parsed.pathname.split('/').filter(Boolean)
    const schemaName = decodeURIComponent(segments[4] || '')
    const cid = decodeURIComponent(segments[5] || '')
    const record = await desktopLocalEpmDataRecord()
    if (schemaName !== 'EPM.fbs' || cid !== record.cid) {
      sendJSON(res, 404, { error: 'record not found' })
      return true
    }

    sendBuffer(res, 200, 'application/x-flatbuffers', record.bytes)
    return true
  }

  if (req.method === 'POST' && parsed.pathname === '/api/v1/data/query') {
    let payload = {}
    try {
      payload = JSON.parse(await readRequestBody(req) || '{}')
    } catch {
      sendJSON(res, 400, { error: 'invalid JSON query' })
      return true
    }

    const record = await desktopLocalEpmDataRecord()
    const schema = readEpmString(payload, ['schema', 'schema_name', 'schemaName'])
    const provider = readEpmString(payload, ['provider_id', 'providerId'])
    const source = readEpmString(payload, ['source_name', 'sourceName'])
    const batch = readEpmString(payload, ['batch_id', 'batchId'])
    const peer = readEpmString(payload, ['peer_id', 'peerId'])
    const matches = (!schema || schema === record.schema_name) &&
      (!provider || provider === record.provider_id) &&
      (!source || source === record.source_name) &&
      (!batch || batch === record.batch_id) &&
      (!peer || peer === record.peer_id)

    if (matches && acceptsRawFlatbufferStream(req.headers?.accept)) {
      sendBuffer(res, 200, 'application/vnd.sdn.flatbuffers.stream', rawFlatbufferStream([record.bytes]))
      return true
    }

    if (matches) {
      sendJSON(res, 200, { records: [desktopLocalEpmDataJson(record, Boolean(payload.include_data))] })
      return true
    }

    sendJSON(res, 200, {
      records: []
    })
    return true
  }

  return false
}

async function desktopLocalProviderSearchResult (payload) {
  const profile = await readDesktopNodeProfile()
  const record = await desktopLocalEpmDataRecord(profile)
  const row = {
    peer_id: record.peer_id,
    dn: readEpmString(profile, ['dn', 'display_name', 'displayName', 'name']) || 'Space Data Network Desktop',
    legal_name: readEpmString(profile, ['legal_name', 'legalName']) || '',
    bitcoin_address: readEpmString(profile, ['bitcoin_address', 'bitcoinAddress']) || '',
    epm_cid: record.cid,
    source: 'desktop-local',
    updated_at: record.timestamp,
    schema_name: record.schema_name,
    provider_peer_id: record.peer_id,
    provider_public_key: readEpmString(profile, ['signing_public_key', 'signingPublicKey']) || '',
    provider_id: record.provider_id,
    source_name: record.source_name,
    batch_id: record.batch_id,
    query_profile: 'desktop-local-epm-v1',
    local_rows: 1,
    pinned_rows: 0,
    cached_bytes: record.size_bytes,
    pinned_bytes: 0,
    snapshot_id: record.cid,
    head: record.cid,
    high_water_mark: 'desktop-local:1',
    last_synced_at: record.timestamp
  }
  const results = desktopSearchMatches(row, payload) ? [row] : []
  return { count: results.length, results }
}

async function desktopLocalDataSearchResult (payload) {
  const record = await desktopLocalEpmDataRecord()
  const row = {
    schema_name: record.schema_name,
    provider_id: record.provider_id,
    source_name: record.source_name,
    batch_id: record.batch_id,
    query_profile: 'desktop-local-epm-v1',
    provider_peer_id: record.peer_id,
    provider_public_key: '',
    local_rows: 1,
    pinned_rows: 0,
    cached_bytes: record.size_bytes,
    pinned_bytes: 0,
    snapshot_id: record.cid,
    head: record.cid,
    high_water_mark: 'desktop-local:1',
    last_synced_at: record.timestamp
  }
  const results = desktopSearchMatches(row, payload) ? [row] : []
  return { count: results.length, results }
}

function desktopSearchMatches (row, payload) {
  const rawSchema = readEpmString(payload, ['schema', 'schema_name', 'schemaName'])
  const schema = rawSchema ? normalizeDesktopSchemaName(rawSchema) : ''
  const provider = readEpmString(payload, ['provider_id', 'providerId'])
  const source = readEpmString(payload, ['source_name', 'sourceName'])
  const batch = readEpmString(payload, ['batch_id', 'batchId'])
  const query = String(readEpmString(payload, ['query']) || '').trim().toLowerCase()
  return (!schema || schema === row.schema_name) &&
    (!provider || provider === row.provider_id || provider === row.peer_id || provider === row.provider_peer_id) &&
    (!source || source === row.source_name) &&
    (!batch || batch === row.batch_id) &&
    (!query || JSON.stringify(row).toLowerCase().includes(query))
}

function desktopConjunctionScreenResult (payload) {
  const primarySchema = normalizeDesktopSchemaName(readEpmString(payload, ['primary_schema', 'primarySchema']) || 'MPE.fbs')
  const secondarySchema = normalizeDesktopSchemaName(readEpmString(payload, ['secondary_schema', 'secondarySchema']) || 'OMM.fbs')
  const encrypted = payload.encrypted !== false
  const grantID = readEpmString(payload, ['grant_id', 'grantId']) || ''
  const channelID = readEpmString(payload, ['channel_id', 'channelId']) || ''
  const resultChannelID = readEpmString(payload, ['result_channel_id', 'resultChannelId']) || ''
  const assessorPeerID = readEpmString(payload, ['assessor_peer_id', 'assessorPeerId']) || ''
  const limit = Number.isFinite(Number(payload.limit)) && Number(payload.limit) > 0 ? Number(payload.limit) : 100
  const sources = desktopConjunctionSources(payload, primarySchema, secondarySchema, encrypted)
  const module = desktopConjunctionModule(payload)
  return {
    workflow: 'encrypted-conjunction-assessment',
    mode: encrypted && primarySchema === 'MPE.fbs' ? 'private-maneuver-ephemeris' : encrypted ? 'private-ephemeris' : 'local-screening',
    status: 'pending-module-execution',
    primary_schema: primarySchema,
    secondary_schema: secondarySchema,
    encrypted,
    grant_id: grantID,
    channel_id: channelID,
    result_channel_id: resultChannelID,
    assessor_peer_id: assessorPeerID,
    limit,
    count: 0,
    events: [],
    sources,
    provenance: {
      run_at: new Date().toISOString(),
      source_schemas: [primarySchema, secondarySchema],
      sources,
      module,
      encrypted,
      grant_id: grantID,
      channel_id: channelID,
      result_channel_id: resultChannelID,
      assessor_peer_id: assessorPeerID,
      result_delivery: 'local-private',
      module_status: 'pending-module-execution',
      include_provenance: payload.include_provenance !== false
    }
  }
}

function desktopConjunctionSources (payload, primarySchema, secondarySchema, encrypted) {
  const requested = Array.isArray(payload.sources) ? payload.sources : []
  return [
    desktopConjunctionSource(requested.find(source => readEpmString(source, ['role']) === 'primary'), 'primary', primarySchema, encrypted),
    desktopConjunctionSource(requested.find(source => readEpmString(source, ['role']) === 'secondary'), 'secondary', secondarySchema, false)
  ]
}

function desktopConjunctionSource (source, role, schema, encrypted) {
  const sourceEncrypted = typeof source?.encrypted === 'boolean' ? source.encrypted : encrypted
  return {
    role,
    schema,
    provider_id: readEpmString(source, ['provider_id', 'providerId']),
    source_name: readEpmString(source, ['source_name', 'sourceName']),
    pnm_cid: readEpmString(source, ['pnm_cid', 'pnmCid']),
    query: readEpmString(source, ['query']),
    encrypted: sourceEncrypted,
    available: false,
    count: 0
  }
}

function desktopConjunctionModule (payload) {
  const module = payload && typeof payload.module === 'object' ? payload.module : {}
  return {
    id: readEpmString(module, ['id', 'module_id', 'moduleId']) || 'com.space-data-network.conjunction-assessment',
    version: readEpmString(module, ['version', 'module_version', 'moduleVersion']) || 'latest'
  }
}

function normalizeDesktopSchemaName (value) {
  const trimmed = String(value || '').trim()
  if (!trimmed) return 'MPE.fbs'
  return `${trimmed.replace(/\.fbs$/i, '').toUpperCase()}.fbs`
}

async function desktopLocalEpmDataRecord (profile = null) {
  profile = profile || await readDesktopNodeProfile()
  const bytes = await buildDesktopNodeEPM(profile)
  const peerID = readEpmString(profile, ['peer_id', 'peerId', 'PeerID', 'ID']) || 'self'
  const cid = await computeRawCid(bytes)
  return {
    schema_name: 'EPM.fbs',
    cid,
    peer_id: peerID,
    provider_id: 'local-node',
    source_name: 'local-epm',
    batch_id: 'local',
    timestamp: new Date().toISOString(),
    size_bytes: bytes.byteLength,
    bytes
  }
}

function desktopLocalEpmDataJson (record, includeData = false) {
  const row = {
    schema_name: record.schema_name,
    cid: record.cid,
    peer_id: record.peer_id,
    provider_id: record.provider_id,
    source_name: record.source_name,
    batch_id: record.batch_id,
    timestamp: record.timestamp,
    size_bytes: record.size_bytes
  }
  if (includeData) row.data_base64 = record.bytes.toString('base64')
  return row
}

function acceptsRawFlatbufferStream (value) {
  return String(value || '').toLowerCase().includes('application/vnd.sdn.flatbuffers.stream')
}

function rawFlatbufferStream (records) {
  const chunks = []
  for (const record of records) {
    const header = Buffer.alloc(4)
    header.writeUInt32BE(record.byteLength, 0)
    chunks.push(header, record)
  }
  return Buffer.concat(chunks)
}

function serveFile (res, filePath) {
  fs.readFile(filePath, (err, body) => {
    if (err) {
      res.writeHead(404, staticAssetHeaders('text/plain; charset=utf-8'))
      res.end('Not found')
      return
    }

    res.writeHead(200, staticAssetHeaders(contentTypes[path.extname(filePath)] || 'application/octet-stream'))
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
      if (rejectNonLoopbackHostHeader(req, res)) {
        return
      }

      if (serveConfiguredSdnNodes(req, res)) {
        return
      }

      Promise.resolve()
        .then(() => serveDesktopPeerAPI(req, res))
        .then(handled => handled || serveDesktopDirectoryAPI(req, res))
        .then(handled => handled || serveDesktopIdentityAPI(req, res))
        .then(handled => handled || serveDesktopPeerEPMAPI(req, res))
        .then(handled => handled || serveDesktopAuthUsersAPI(req, res))
        .then(handled => handled || serveDesktopNodeIdentityAPI(req, res))
        .then(handled => handled || serveDesktopNodeEPMAPI(req, res))
        .then(handled => handled || serveDesktopFlatSqlPersistenceAPI(req, res))
        .then(handled => handled || serveDesktopLocalDataAPI(req, res))
        .then(handled => {
          if (handled) return

          if (redirectBareAppRoute(req, res)) {
            return
          }

          const filePath = routeForUrl(req.url || '/')

          if (!filePath) {
            res.writeHead(404, staticAssetHeaders('text/plain; charset=utf-8'))
            res.end('Not found')
            return
          }

          serveFile(res, filePath)
        })
        .catch(err => {
          logger.error(`[static-server] failed to serve request: ${err.message || err}`)
          res.writeHead(500, staticAssetHeaders('text/plain; charset=utf-8'))
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
  applyWalletNodeIdentity,
  connectDesktopSdnSeedPeers,
  configuredSdnNodesFromSshConfig,
  DESKTOP_SDN_SEED_PEERS,
  displayNameForSdnSSHHost,
  getDesktopStaticOrigin,
  getDesktopStaticUrl,
  isAllowedLoopbackHostHeader,
  isSdnSSHHostAlias,
  kuboSwarmPeersToDesktopSdnPeers,
  CANONICAL_TRUST_LEVELS,
  normalizeDesktopTrustLevel,
  serveDesktopDirectoryAPI,
  serveDesktopIdentityAPI,
  serveDesktopPeerEPMAPI,
  serveDesktopAuthUsersAPI,
  serveDesktopLocalDataAPI,
  serveDesktopNodeIdentityAPI,
  serveDesktopNodeEPMAPI,
  serveDesktopFlatSqlPersistenceAPI,
  staticAssetHeaders,
  startDesktopStaticServer
}
