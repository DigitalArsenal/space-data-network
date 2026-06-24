const fs = require('fs')
const os = require('os')
const path = require('path')
const { EventEmitter } = require('events')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

test.describe('desktop static data API', () => {
  test('serves local EPM data summary, filtered query rows, and record bytes', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-data-api-'))
    const { serveDesktopLocalDataAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const profile = {
      dn: 'Desktop Data Node',
      entity_type: 'Node',
      peer_id: '12D3KooWDesktopData',
      signing_public_key: 'ed25519-data-signing-public',
      encryption_public_key: 'x25519-data-encryption-public'
    }
    const put = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify(profile))
    expect(put.statusCode).toBe(200)

    const health = await requestJson(serveDesktopLocalDataAPI, 'GET', '/api/v1/data/health')
    expect(health.statusCode).toBe(200)
    expect(health.json).toMatchObject({
      healthy: true,
      details: { runtime: 'desktop-local' }
    })

    const summary = await requestJson(serveDesktopLocalDataAPI, 'GET', '/api/v1/data/summary')
    expect(summary.statusCode).toBe(200)
    expect(summary.json.schemas).toEqual([
      expect.objectContaining({ schema_name: 'EPM.fbs', count: 1 })
    ])
    expect(summary.json.sources).toEqual([
      expect.objectContaining({
        schema_name: 'EPM.fbs',
        provider_id: 'local-node',
        source_name: 'local-epm',
        batch_id: 'local'
      })
    ])

    const query = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/data/query', {
      schema: 'EPM.fbs',
      provider_id: 'local-node',
      source_name: 'local-epm',
      peer_id: '12D3KooWDesktopData',
      include_data: true
    })
    expect(query.statusCode).toBe(200)
    expect(query.json.records).toHaveLength(1)
    expect(query.json.records[0]).toMatchObject({
      schema_name: 'EPM.fbs',
      peer_id: '12D3KooWDesktopData',
      provider_id: 'local-node',
      source_name: 'local-epm',
      batch_id: 'local'
    })
    expect(query.json.records[0].cid).toMatch(/^bafkrei/)
    expect(query.json.records[0].data_base64).toEqual(expect.any(String))

    const missed = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/data/query', {
      schema: 'OMM.fbs',
      provider_id: 'local-node'
    })
    expect(missed.statusCode).toBe(200)
    expect(missed.json.records).toEqual([])

    const record = await requestRaw(
      serveDesktopLocalDataAPI,
      'GET',
      `/api/v1/data/records/EPM.fbs/${encodeURIComponent(query.json.records[0].cid)}`
    )
    expect(record.statusCode).toBe(200)
    expect(record.headers['Content-Type']).toBe('application/x-flatbuffers')
    expect(record.bodyBuffer.equals(Buffer.from(query.json.records[0].data_base64, 'base64'))).toBe(true)
  })

  test('streams matching query records as length-prefixed FlatBuffer bytes', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-data-stream-'))
    const { serveDesktopLocalDataAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const put = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Streaming Desktop Node',
      peer_id: '12D3KooWDesktopStream'
    }))
    expect(put.statusCode).toBe(200)

    const stream = await requestRaw(
      serveDesktopLocalDataAPI,
      'POST',
      '/api/v1/data/query',
      JSON.stringify({ schema: 'EPM.fbs', provider_id: 'local-node' }),
      { accept: 'application/vnd.sdn.flatbuffers.stream' }
    )

    expect(stream.statusCode).toBe(200)
    expect(stream.headers['Content-Type']).toBe('application/vnd.sdn.flatbuffers.stream')
    const length = stream.bodyBuffer.readUInt32BE(0)
    const payload = stream.bodyBuffer.subarray(4)
    expect(length).toBe(payload.byteLength)
    expect(payload.byteLength).toBeGreaterThan(0)
  })

  test('serves shared provider and data search routes for bundled UI parity', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-search-api-'))
    const { serveDesktopLocalDataAPI, serveDesktopNodeEPMAPI } = loadStaticServer(userData)

    const put = await requestRaw(serveDesktopNodeEPMAPI, 'PUT', '/api/node/epm', JSON.stringify({
      dn: 'Desktop Search Node',
      entity_type: 'Node',
      peer_id: '12D3KooWDesktopSearch',
      signing_public_key: 'ed25519-search-signing-public',
      encryption_public_key: 'x25519-search-encryption-public'
    }))
    expect(put.statusCode).toBe(200)

    const providers = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/search/providers', {
      query: 'desktop',
      schema: 'EPM',
      provider_id: 'local-node',
      source_name: 'local-epm'
    })
    expect(providers.statusCode).toBe(200)
    expect(providers.json).toMatchObject({
      count: 1,
      results: [expect.objectContaining({
        peer_id: '12D3KooWDesktopSearch',
        dn: 'Desktop Search Node',
        provider_id: 'local-node',
        schema_name: 'EPM.fbs',
        source_name: 'local-epm',
        local_rows: 1
      })]
    })

    const data = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/search/data', {
      schema: 'EPM',
      provider_id: 'local-node',
      source_name: 'local-epm'
    })
    expect(data.statusCode).toBe(200)
    expect(data.json).toMatchObject({
      count: 1,
      results: [expect.objectContaining({
        schema_name: 'EPM.fbs',
        provider_id: 'local-node',
        source_name: 'local-epm',
        batch_id: 'local',
        local_rows: 1
      })]
    })
  })

  test('serves encrypted conjunction screening workflow metadata for private MPE requests', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-conjunction-'))
    const { serveDesktopLocalDataAPI } = loadStaticServer(userData)

    const result = await requestJson(serveDesktopLocalDataAPI, 'POST', '/api/v1/conjunction/screen', {
      primary_schema: 'MPE.fbs',
      secondary_schema: 'OMM.fbs',
      encrypted: true,
      grant_id: 'grant-private-mpe',
      channel_id: 'channel-private-ca',
      assessor_peer_id: '16Uiu2HAssessor',
      include_provenance: true,
      limit: 25
    })

    expect(result.statusCode).toBe(200)
    expect(result.json).toMatchObject({
      workflow: 'encrypted-conjunction-assessment',
      mode: 'private-maneuver-ephemeris',
      primary_schema: 'MPE.fbs',
      secondary_schema: 'OMM.fbs',
      encrypted: true,
      grant_id: 'grant-private-mpe',
      channel_id: 'channel-private-ca',
      assessor_peer_id: '16Uiu2HAssessor',
      count: 0,
      events: [],
      provenance: {
        assessor_peer_id: '16Uiu2HAssessor',
        source_schemas: ['MPE.fbs', 'OMM.fbs']
      }
    })
  })
})

function loadStaticServer (userData, overrides = {}) {
  const app = {
    getPath: (name) => {
      if (name === 'userData') return userData
      return userData
    },
    getAppPath: () => path.join(__dirname, '../..')
  }
  const safeStorage = {
    isEncryptionAvailable: () => true,
    encryptString: (value) => Buffer.from(`sealed:${value}`, 'utf8'),
    decryptString: (buffer) => {
      const value = Buffer.from(buffer).toString('utf8')
      if (!value.startsWith('sealed:')) throw new Error('invalid safeStorage payload')
      return value.slice('sealed:'.length)
    }
  }
  return proxyquire('../../src/static-http-server', {
    electron: { app, safeStorage, ...overrides },
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
