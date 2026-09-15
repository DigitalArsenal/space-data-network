const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test, expect } = require('@playwright/test')

const { buildNodeConfig, configPathFor, ensureNodeConfig, findFreePort, nodeHome } = require('../../src/node/config')

test.describe('the node configuration this shell writes', () => {
  test('puts the store, the keys and the managed Kubo repository under one home', () => {
    const config = buildNodeConfig({
      home: '/users/x/node', adminPort: 51000, swarmPort: 51001, wsPort: 51002, quicPort: 51003, webrtcPort: 51004
    })

    expect(config.storage.path).toBe(path.join('/users/x/node', 'data'))
    // The node derives its key directory as dirname(storage.path)/keys, so the
    // keys land beside the store rather than in a home-directory default.
    expect(path.join(path.dirname(config.storage.path), 'keys')).toBe(path.join('/users/x/node', 'keys'))
    expect(config.setup.data_path).toBe('/users/x/node')
  })

  test('binds the admin listener to loopback on the port it was given', () => {
    const config = buildNodeConfig({
      home: '/n', adminPort: 49222, swarmPort: 1, wsPort: 2, quicPort: 3, webrtcPort: 4
    })

    expect(config.admin.listen_addr).toBe('127.0.0.1:49222')
    expect(config.admin.enabled).toBe(true)
    expect(config.admin.require_auth).toBe(true)
  })

  test('leaves ipfs_api_url unset so the node manages the bundle Kubo itself', () => {
    const config = buildNodeConfig({
      home: '/n', adminPort: 1, swarmPort: 2, wsPort: 3, quicPort: 4, webrtcPort: 5
    })

    expect(config.admin.ipfs_api_url).toBeUndefined()
    expect(config.admin.dev_auto_admin).toBeUndefined()
  })

  test('keeps the libp2p websocket off 8080, where the managed Kubo gateway binds', () => {
    const config = buildNodeConfig({
      home: '/n', adminPort: 1, swarmPort: 2, wsPort: 3, quicPort: 4, webrtcPort: 5
    })

    expect(config.network.listen).toEqual([
      '/ip4/0.0.0.0/tcp/2',
      '/ip4/0.0.0.0/tcp/3/ws',
      '/ip4/0.0.0.0/udp/4/quic-v1',
      '/ip4/0.0.0.0/udp/5/webrtc-direct'
    ])
    expect(config.network.listen.some((addr) => addr.includes('/8080'))).toBe(false)
  })

  test('writes the config once and never overwrites an operator edit', async () => {
    const userData = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-config-'))
    try {
      let port = 40000
      const freePort = async () => ++port

      const first = await ensureNodeConfig({ userDataPath: userData, freePort })
      expect(first.created).toBe(true)
      expect(first.path).toBe(configPathFor(userData))
      expect(first.home).toBe(nodeHome(userData))

      // JSON is a subset of YAML, which is what the node parses.
      const written = JSON.parse(fs.readFileSync(first.path, 'utf8'))
      expect(written.admin.listen_addr).toBe('127.0.0.1:40001')

      fs.writeFileSync(first.path, 'admin:\n  listen_addr: 127.0.0.1:9999\n')
      const second = await ensureNodeConfig({ userDataPath: userData, freePort })
      expect(second.created).toBe(false)
      expect(fs.readFileSync(second.path, 'utf8')).toContain('127.0.0.1:9999')
    } finally {
      fs.rmSync(userData, { recursive: true, force: true })
    }
  })

  test('asks the operating system for a port nothing is listening on', async () => {
    const port = await findFreePort()
    expect(port).toBeGreaterThan(1024)
    expect(port).toBeLessThan(65536)
  })
})
