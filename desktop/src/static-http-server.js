// @ts-check
const fs = require('fs')
const path = require('path')
const http = require('http')
const os = require('os')
const crypto = require('crypto')
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

const HOSTED_EPM_STORE_VERSION = 1
const HOSTED_EPM_STORE_FILE = 'sdn-hosted-epms.enc.json'
const HOSTED_EPM_STORE_SALT = 'space-data-network-hosted-epm-store-v1'
const SECRET_EPM_FIELD_PATTERN = /(^|[_-])(private|secret|mnemonic|seed|xpriv|core)([_-]|$)|privatekey|encryptedcore/i

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

function sendText (res, status, contentType, payload) {
  res.writeHead(status, {
    'Content-Type': contentType,
    'Cache-Control': 'no-store'
  })
  res.end(payload)
}

function sendBuffer (res, status, contentType, payload) {
  res.writeHead(status, {
    'Content-Type': contentType,
    'Cache-Control': 'no-store'
  })
  res.end(payload)
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
  res.writeHead(403, {
    'Content-Type': 'text/plain; charset=utf-8',
    'Cache-Control': 'no-store'
  })
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

async function buildDesktopNodeEPM (profile) {
  const flatbuffers = require('flatbuffers')
  const builder = new flatbuffers.Builder(2048)

  const dnOff = createStringOffset(builder, readEpmString(profile, ['dn', 'display_name', 'displayName', 'name']))
  const legalNameOff = createStringOffset(builder, readEpmString(profile, ['legal_name', 'legalName']))
  const familyNameOff = createStringOffset(builder, readEpmString(profile, ['family_name', 'familyName']))
  const givenNameOff = createStringOffset(builder, readEpmString(profile, ['given_name', 'givenName']))
  const emailOff = createStringOffset(builder, readEpmString(profile, ['email']))
  const telephoneOff = createStringOffset(builder, readEpmString(profile, ['telephone', 'phone']))
  const addresses = desktopEpmAddresses(profile)
  const addressesOff = createOffsetVector(builder, addresses.map(address => builder.createString(address)))

  builder.startObject(19)
  builder.addFieldInt8(18, 1, 0) // EPM.EntityType.Node
  if (dnOff) builder.addFieldOffset(0, dnOff, 0)
  if (legalNameOff) builder.addFieldOffset(1, legalNameOff, 0)
  if (familyNameOff) builder.addFieldOffset(2, familyNameOff, 0)
  if (givenNameOff) builder.addFieldOffset(3, givenNameOff, 0)
  if (emailOff) builder.addFieldOffset(11, emailOff, 0)
  if (telephoneOff) builder.addFieldOffset(12, telephoneOff, 0)
  if (addressesOff) builder.addFieldOffset(14, addressesOff, 0)
  const epm = builder.endObject()
  builder.finish(epm, '$EPM', true)
  return Buffer.from(builder.asUint8Array())
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

function publicIdentityRecord (id, kind, epmJson, updatedAt = new Date().toISOString()) {
  const publicEpm = sanitizePublicEPM(epmJson || {})
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
  return publicIdentityRecord('self', 'node-self', await readDesktopNodeProfile())
}

function hostedIdentityRecord (id, payload, existing = {}) {
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

function identityRecordToVCard (record) {
  const publicKey = readEpmString(record.epmJson, [
    'public_key',
    'publicKey',
    'signing_public_key',
    'signingPublicKey',
    'encryption_public_key',
    'encryptionPublicKey'
  ])
  const signingPublicKey = readEpmString(record.epmJson, [
    'signing_public_key',
    'signingPublicKey',
    'signing_pubkey_hex'
  ])
  const encryptionPublicKey = readEpmString(record.epmJson, [
    'encryption_public_key',
    'encryptionPublicKey',
    'encryption_pubkey_hex'
  ])
  const lines = [
    'BEGIN:VCARD',
    'VERSION:4.0',
    `FN:${escapeVCardValue(record.label)}`
  ]

  if (record.peerId) lines.push(`X-SDN-PEER-ID:${escapeVCardValue(record.peerId)}`)
  if (record.epmCid) lines.push(`X-SDN-EPM-CID:${escapeVCardValue(record.epmCid)}`)
  const publicKeyEmail = publicKeyEmailAddress(publicKey)
  if (publicKeyEmail) lines.push(`EMAIL;TYPE=INTERNET:${escapeVCardValue(publicKeyEmail)}`)
  if (publicKey) lines.push(`X-SDN-PUBLIC-KEY:${escapeVCardValue(publicKey)}`)
  if (signingPublicKey) lines.push(`X-SDN-SIGNING-PUBLIC-KEY:${escapeVCardValue(signingPublicKey)}`)
  if (encryptionPublicKey) lines.push(`X-SDN-ENCRYPTION-PUBLIC-KEY:${escapeVCardValue(encryptionPublicKey)}`)
  lines.push('END:VCARD')

  return `${lines.join('\r\n')}\r\n`
}

function publicKeyEmailAddress (publicKey) {
  const localPart = String(publicKey || '').trim().replace(/\s+/g, '').replace(/[^A-Za-z0-9._%+-]/g, '')
  return localPart ? `${localPart}@spacedatanetwork.org` : ''
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
  const hosted = Object.entries(store.records)
    .map(([id, record]) => publicIdentityRecord(id, 'hosted', record.epmJson || record.epm_json || record, record.updatedAt || record.updated_at))
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
      sendJSON(res, 200, publicIdentityRecord('self', 'node-self', next))
      return true
    }

    const store = await readHostedEpmStore()
    const record = hostedIdentityRecord(identityId, payload, store.records[identityId])
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

async function serveDesktopNodeEPMAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)
  if (parsed.pathname !== '/api/node/epm/json' && parsed.pathname !== '/api/node/epm') {
    return false
  }

  if (req.method === 'GET' && parsed.pathname === '/api/node/epm/json') {
    sendJSON(res, 200, await readDesktopNodeProfile())
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

async function serveDesktopLocalDataAPI (req, res) {
  const parsed = new URL(req.url || '/', `http://${HOST}`)

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

    if (matches) {
      sendJSON(res, 200, { records: [desktopLocalEpmDataJson(record)] })
      return true
    }

    sendJSON(res, 200, {
      records: []
    })
    return true
  }

  return false
}

async function desktopLocalEpmDataRecord () {
  const profile = await readDesktopNodeProfile()
  const bytes = await buildDesktopNodeEPM(profile)
  const peerID = readEpmString(profile, ['peer_id', 'peerId', 'PeerID', 'ID']) || 'self'
  return {
    schema_name: 'EPM.fbs',
    cid: peerID,
    peer_id: peerID,
    provider_id: 'local-node',
    source_name: 'local-epm',
    batch_id: 'local',
    timestamp: new Date().toISOString(),
    size_bytes: bytes.byteLength,
    bytes
  }
}

function desktopLocalEpmDataJson (record) {
  return {
    schema_name: record.schema_name,
    cid: record.cid,
    peer_id: record.peer_id,
    provider_id: record.provider_id,
    source_name: record.source_name,
    batch_id: record.batch_id,
    timestamp: record.timestamp,
    size_bytes: record.size_bytes,
    data_base64: record.bytes.toString('base64')
  }
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
  isAllowedLoopbackHostHeader,
  isSdnSSHHostAlias,
  kuboSwarmPeersToDesktopSdnPeers,
  serveDesktopDirectoryAPI,
  serveDesktopIdentityAPI,
  serveDesktopLocalDataAPI,
  serveDesktopNodeEPMAPI,
  startDesktopStaticServer
}
