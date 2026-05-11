const fs = require('fs')
const http = require('http')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

test.describe('desktop static security guardrails', () => {
  test('rejects non-loopback Host headers before desktop API routes respond', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-static-security-'))
    const { startDesktopStaticServer } = loadStaticServer(userData)
    const { origin, server } = await startDesktopStaticServer()

    try {
      const accepted = await requestServer(origin, '127.0.0.1', '/api/v1/data/objects')
      expect(accepted.statusCode).toBe(200)
      expect(accepted.body).toContain('"objects"')

      const rejected = await requestServer(origin, 'evil.example', '/api/v1/data/objects')
      expect(rejected.statusCode).toBe(403)
      expect(rejected.body).not.toContain('"objects"')
    } finally {
      await new Promise(resolve => server.close(resolve))
    }
  })

  test('accepts only local desktop Host header forms', () => {
    const { isAllowedLoopbackHostHeader } = loadStaticServer(fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-static-host-')))

    for (const host of ['localhost', 'localhost:17890', '127.0.0.1', '127.0.0.1:17890', '0.0.0.0', '0.0.0.0:17890', '[::1]', '[::1]:17890', '::1']) {
      expect(isAllowedLoopbackHostHeader(host), host).toBe(true)
    }

    for (const host of ['sdn.spaceaware.io', 'evil.example', '192.168.1.10', '10.0.0.2:17890', '[2001:db8::1]:17890']) {
      expect(isAllowedLoopbackHostHeader(host), host).toBe(false)
    }
  })
})

function loadStaticServer (userData) {
  const app = {
    getPath: () => userData,
    getAppPath: () => path.join(__dirname, '../..'),
    once: () => {}
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

function requestServer (origin, host, requestPath) {
  const url = new URL(requestPath, origin)
  return new Promise((resolve, reject) => {
    const req = http.request({
      hostname: url.hostname,
      port: url.port,
      path: `${url.pathname}${url.search}`,
      method: 'GET',
      headers: { Host: host }
    }, res => {
      const chunks = []
      res.on('data', chunk => chunks.push(chunk))
      res.on('end', () => {
        resolve({
          statusCode: res.statusCode,
          body: Buffer.concat(chunks).toString('utf8')
        })
      })
    })
    req.on('error', reject)
    req.end()
  })
}
