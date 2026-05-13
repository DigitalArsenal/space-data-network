const fs = require('fs')
const os = require('os')
const path = require('path')
const { EventEmitter } = require('events')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

test.describe('desktop static identity API', () => {
  test('stores hosted EPM records encrypted at rest and serves public exports', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-identity-api-'))
    const { serveDesktopIdentityAPI } = loadStaticServer(userData)

    const put = await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/alice', {
      epm_json: {
        dn: 'Alice Example',
        peer_id: '16Uiu2Alice',
        epm_cid: 'bafy-alice-epm',
        public_key: 'abcdef',
        signing_public_key: 'signing-public',
        encryption_public_key: 'encryption-public',
        private_key: 'must-not-be-exported'
      }
    })

    expect(put.statusCode).toBe(200)
    expect(put.json).toMatchObject({
      id: 'alice',
      kind: 'hosted',
      label: 'Alice Example',
      peerId: '16Uiu2Alice'
    })

    const diskContents = fs.readdirSync(userData)
      .map(name => fs.readFileSync(path.join(userData, name), 'utf8'))
      .join('\n')
    expect(diskContents).not.toContain('Alice Example')
    expect(diskContents).not.toContain('must-not-be-exported')

    const list = await requestJson(serveDesktopIdentityAPI, 'GET', '/api/identity/epms')
    expect(list.statusCode).toBe(200)
    expect(list.json.epms).toEqual(expect.arrayContaining([
      expect.objectContaining({ id: 'self', kind: 'node-self' }),
      expect.objectContaining({ id: 'alice', label: 'Alice Example' })
    ]))

    const read = await requestJson(serveDesktopIdentityAPI, 'GET', '/api/identity/epms/alice')
    expect(read.statusCode).toBe(200)
    expect(read.body).toContain('Alice Example')
    expect(read.body).not.toContain('must-not-be-exported')

    const vcard = await requestRaw(serveDesktopIdentityAPI, 'GET', '/api/identity/epms/alice/vcard')
    expect(vcard.statusCode).toBe(200)
    expect(vcard.body).toContain('BEGIN:VCARD')
    expect(vcard.body).toContain('FN:Alice Example')
    expect(vcard.body).toContain('EMAIL;TYPE=INTERNET:abcdef@spacedatanetwork.org')
    expect(vcard.body).toContain('X-SDN-PEER-ID:16Uiu2Alice')
    expect(vcard.body).toContain('X-SDN-PUBLIC-KEY:abcdef')
    expect(vcard.body).toContain('X-SDN-SIGNING-PUBLIC-KEY:signing-public')
    expect(vcard.body).toContain('X-SDN-ENCRYPTION-PUBLIC-KEY:encryption-public')
    expect(vcard.body).not.toContain('must-not-be-exported')
  })

  test('serves local node and person directory search from public hosted EPMs', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-directory-api-'))
    const { serveDesktopDirectoryAPI, serveDesktopIdentityAPI } = loadStaticServer(userData)

    await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/alice', {
      epm_json: {
        dn: 'Alice Operator',
        entity_type: 'Person',
        peer_id: '16Uiu2Alice',
        public_key: 'abcdef',
        private_key: 'must-not-be-indexed'
      }
    })
    await requestJson(serveDesktopIdentityAPI, 'PUT', '/api/identity/epms/provider-node', {
      epm_json: {
        dn: 'CelesTrak Provider',
        entity_type: 'Node',
        peer_id: '16Uiu2Provider',
        public_key: '123456',
        private_key: 'must-not-be-indexed'
      }
    })

    const nodes = await requestJson(serveDesktopDirectoryAPI, 'GET', '/api/directory/nodes?q=provider')
    expect(nodes.statusCode).toBe(200)
    expect(nodes.json.nodes).toEqual(expect.arrayContaining([
      expect.objectContaining({ dn: 'CelesTrak Provider', peer_id: '16Uiu2Provider' })
    ]))
    expect(nodes.body).not.toContain('must-not-be-indexed')

    const users = await requestJson(serveDesktopDirectoryAPI, 'GET', '/api/directory/users?q=alice')
    expect(users.statusCode).toBe(200)
    expect(users.json.users).toEqual(expect.arrayContaining([
      expect.objectContaining({ dn: 'Alice Operator', peer_id: '16Uiu2Alice' })
    ]))
    expect(users.body).not.toContain('must-not-be-indexed')
  })

  test('serves the local node EPM route as a raw FlatBuffer', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-node-epm-api-'))
    const { serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const put = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Node',
      email: 'node@example.invalid',
      peer_id: '12D3KooWDesktopNode'
    }))
    expect(put.statusCode).toBe(200)
    expect(put.headers['Content-Type']).toBe('application/x-flatbuffers')

    const get = await requestRaw(serveDesktopNodeEPMAPI, 'GET', '/api/node/epm')
    expect(get.statusCode).toBe(200)
    expect(get.headers['Content-Type']).toBe('application/x-flatbuffers')

    const flatbuffers = await import('flatbuffers')
    const { EPM } = await import('spacedatastandards.org/lib/js/EPM/EPM.js')
    const epm = EPM.getSizePrefixedRootAsEPM(new flatbuffers.ByteBuffer(new Uint8Array(get.bodyBuffer)))
    expect(epm.DN()).toBe('Desktop Node')
    expect(epm.EMAIL()).toBe('node@example.invalid')
  })

  test('serves local node EPM through desktop raw data query routes', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-local-data-api-'))
    const { serveDesktopLocalDataAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Node',
      email: 'node@example.invalid',
      peer_id: '12D3KooWDesktopNode'
    }))

    const summary = await requestJson(serveDesktopLocalDataAPI, 'GET', '/api/v1/data/summary')
    expect(summary.statusCode).toBe(200)
    expect(summary.json.total_records).toBe(1)
    expect(summary.json.schemas).toEqual([
      expect.objectContaining({ schema_name: 'EPM.fbs', count: 1 })
    ])
    expect(summary.json.sources).toEqual([
      expect.objectContaining({ schema_name: 'EPM.fbs', provider_id: 'local-node', source_name: 'local-epm' })
    ])

    const query = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/data/query', {
      schema: 'EPM.fbs',
      provider_id: 'local-node',
      source_name: 'local-epm',
      limit: 5
    })
    expect(query.statusCode).toBe(200)
    expect(query.json.records).toEqual([
      expect.objectContaining({
        schema_name: 'EPM.fbs',
        cid: '12D3KooWDesktopNode',
        peer_id: '12D3KooWDesktopNode',
        provider_id: 'local-node',
        source_name: 'local-epm'
      })
    ])
    expect(query.json.records[0].data_base64).toBeUndefined()

    const stream = await requestRaw(serveDesktopLocalDataAPI, 'POST', '/api/v1/data/query', JSON.stringify({
      schema: 'EPM.fbs',
      provider_id: 'local-node',
      source_name: 'local-epm',
      limit: 5
    }), { accept: 'application/vnd.sdn.flatbuffers.stream', 'content-type': 'application/json' })
    expect(stream.statusCode).toBe(200)
    expect(stream.headers['Content-Type']).toBe('application/vnd.sdn.flatbuffers.stream')
    expect(stream.bodyBuffer.readUInt32BE(0)).toBeGreaterThan(0)

    const record = await requestRaw(serveDesktopLocalDataAPI, 'GET', '/api/v1/data/records/EPM.fbs/12D3KooWDesktopNode')
    expect(record.statusCode).toBe(200)
    expect(record.headers['Content-Type']).toBe('application/x-flatbuffers')
    expect(record.bodyBuffer.byteLength).toBeGreaterThan(0)
  })

  test('advertises configured remote data nodes with libp2p FlatSQL sync addresses only', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-configured-sync-addrs-'))
    const configPath = path.join(userData, 'ssh-config')
    fs.writeFileSync(configPath, [
      'Host space-data-network-02 celestrak.eth',
      '    HostName 167.172.219.213',
      '    User root'
    ].join('\n'))

    const staticServer = loadStaticServer(userData)
    const nodes = staticServer.configuredSdnNodesFromSshConfig(configPath)
    const celestrak = nodes.find(node => node.id === 'space-data-network-02')

    expect(celestrak).toEqual(expect.objectContaining({
      name: 'CelesTrak Provider',
      addrs: [
        '/ip4/167.172.219.213/tcp/8080/ws/p2p/16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4'
      ],
      metadata: expect.objectContaining({
        peer_id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
        provider_id: 'space-data-network-02',
        source_name: 'celestrak-gp',
        sync_protocol: '/space-data-network/flatsql-sync/1.0.0'
      })
    }))
    expect(celestrak.metadata.admin_proxy_path).toBeUndefined()
  })

  test('does not export a configured-node HTTP or SSH data proxy', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-no-remote-proxy-'))
    const staticServer = loadStaticServer(userData)

    expect(staticServer.serveConfiguredSdnNodeDataProxy).toBeUndefined()
    expect(staticServer.parseConfiguredSdnNodeRemoteResponse).toBeUndefined()
  })

  test('sends cross-origin isolation headers for worker and SharedArrayBuffer support', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-isolation-headers-'))
    const { staticAssetHeaders } = loadStaticServer(userData)

    expect(staticAssetHeaders('text/javascript; charset=utf-8')).toMatchObject({
      'Cross-Origin-Opener-Policy': 'same-origin',
      'Cross-Origin-Embedder-Policy': 'require-corp',
      'Cross-Origin-Resource-Policy': 'same-origin'
    })
  })

  test('maps observed configured SDN peer IDs to EPM display names', () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-peer-name-api-'))
    const { kuboSwarmPeersToDesktopSdnPeers } = loadStaticServer(userData)

    const peers = kuboSwarmPeersToDesktopSdnPeers({
      Peers: [
        {
          Peer: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
          Addr: '/ip4/104.131.11.220/tcp/4001',
          Identify: {
            ID: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
            AgentVersion: 'spacedatanetwork/1.0.3',
            Protocols: ['/space-data-network/module-delivery/1.0.0']
          }
        },
        {
          Peer: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
          Addr: '/ip4/167.172.219.213/tcp/4001',
          Identify: {
            ID: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
            AgentVersion: 'spacedatanetwork/1.0.3',
            Protocols: ['/space-data-network/module-delivery/1.0.0']
          }
        }
      ]
    })

    expect(peers).toEqual(expect.arrayContaining([
      expect.objectContaining({
        id: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
        name: 'SpaceAware.io',
        metadata: expect.objectContaining({
          public_key: '0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2'
        })
      }),
      expect.objectContaining({
        id: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
        name: 'CelesTrak Provider',
        metadata: expect.objectContaining({
          public_key: '90aa23ea4ff2d68cf8cb8155135fe5a25b580ec805e835aabb0e8905ffb2c3b2'
        })
      })
    ]))
  })

})

function loadStaticServer (userData) {
  const app = {
    getPath: (name) => {
      if (name === 'userData') return userData
      return userData
    },
    getAppPath: () => path.join(__dirname, '../..')
  }
  return proxyquire('../../src/static-http-server', {
    electron: { app },
    './common/logger': {
      error: () => {},
      warn: () => {},
      info: () => {},
      debug: () => {}
    }
  })
}

async function requestJson (handler, method, url, body) {
  const result = await requestRaw(handler, method, url, body ? JSON.stringify(body) : '')
  return {
    ...result,
    json: JSON.parse(result.body)
  }
}

async function requestRaw (handler, method, url, body = '', headers = {}) {
  const req = new EventEmitter()
  req.method = method
  req.url = url
  req.headers = headers
  req.setEncoding = () => {}

  const chunks = []
  const res = {
    statusCode: 0,
    headers: {},
    writeHead (statusCode, headers = {}) {
      this.statusCode = statusCode
      this.headers = headers
    },
    end (chunk = '') {
      chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk)))
      this.ended = true
    }
  }

  const handled = handler(req, res)
  process.nextTick(() => {
    if (body) req.emit('data', body)
    req.emit('end')
  })
  await handled

  const bodyBuffer = Buffer.concat(chunks)
  return {
    statusCode: res.statusCode,
    headers: res.headers,
    body: bodyBuffer.toString('utf8'),
    bodyBuffer
  }
}
