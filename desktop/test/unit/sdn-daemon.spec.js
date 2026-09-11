const fs = require('fs')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')
const proxyquire = require('proxyquire').noCallThru()

function loadSdnDaemon () {
  return proxyquire('../../src/daemon/sdn-daemon', {
    electron: {
      app: {
        isPackaged: false,
        getAppPath: () => path.join(__dirname, '../..'),
        getPath: () => fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-daemon-'))
      }
    },
    '../common/logger': {
      info: () => {},
      error: () => {}
    }
  })
}

test.describe('SDN desktop daemon wrapper', () => {
  test('accepts only explicit loopback HTTP admin URLs', () => {
    const { normalizeLoopbackAdminUrl } = loadSdnDaemon()
    expect(normalizeLoopbackAdminUrl('http://127.0.0.1:17950/')).toBe('http://127.0.0.1:17950')
    expect(normalizeLoopbackAdminUrl('http://localhost:17950/path')).toBe('http://localhost:17950')
    expect(() => normalizeLoopbackAdminUrl('https://127.0.0.1:17950')).toThrow(/must use http/)
    expect(() => normalizeLoopbackAdminUrl('http://192.0.2.10:17950')).toThrow(/loopback-only/)
    expect(() => normalizeLoopbackAdminUrl('http://127.0.0.1')).toThrow(/explicit port/)
  })

  test('writes a wallet-authenticated config with random peer ports and dev auto-admin off', () => {
    const { writeDesktopDaemonConfig } = loadSdnDaemon()
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-config-'))
    const configPath = path.join(root, 'config.yaml')
    const runtimeRoot = path.join(root, 'runtime-root')
    writeDesktopDaemonConfig(configPath, {
      adminUrl: 'http://127.0.0.1:17951',
      storagePath: path.join(root, 'data'),
      ipfsApiUrl: 'http://127.0.0.1:5001',
      ipfsGatewayUrl: 'http://127.0.0.1:8080',
      runtimeRoot
    })
    const config = fs.readFileSync(configPath, 'utf8')
    expect(config).toContain('listen_addr: "127.0.0.1:17951"')
    expect(config).toContain('require_auth: true')
    expect(config).toContain('dev_auto_admin: false')
    expect(config).toContain('/ip4/0.0.0.0/tcp/0')
    expect(config).toContain('ipfs_api_url: "http://127.0.0.1:5001"')
    expect(config).toContain('runtime/ui/wallet-wasm')
    expect(config).toContain('runtime/ui/wallet-ui')
    expect(config).toContain('enabled: false')
    expect(config).not.toContain('dev_payments')
  })

  test('reads the daemon admin address from an explicit config', () => {
    const { adminUrlFromConfig } = loadSdnDaemon()
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-desktop-read-config-'))
    const configPath = path.join(root, 'config.yaml')
    fs.writeFileSync(configPath, 'admin:\n  enabled: true\n  listen_addr: "127.0.0.1:17952"\n')
    expect(adminUrlFromConfig(configPath)).toBe('http://127.0.0.1:17952')
  })

  test('packages the daemon with local wallet assets and forces dev payments off', () => {
    const wrapper = fs.readFileSync(path.join(__dirname, '../../src/daemon/sdn-daemon.js'), 'utf8')
    const builder = fs.readFileSync(path.join(__dirname, '../../scripts/build-sdn-runtime.js'), 'utf8')
    const electronBuilder = fs.readFileSync(path.join(__dirname, '../../electron-builder.yml'), 'utf8')
    expect(wrapper).toContain("SDN_STOREFRONT_DEV_PAYMENTS: '0'")
    expect(wrapper).toContain('delete env.IPFS_PATH')
    expect(wrapper).toContain("'runtime', 'ui', 'wallet-wasm'")
    expect(wrapper).toContain("'runtime', 'ui', 'wallet-ui'")
    expect(builder).toContain("require('kubo').path()")
    expect(builder).toContain('stage-wallet-wasm.sh')
    expect(builder).toContain('@executable_path/../runtime/wasmedge/lib/libwasmedge.0.dylib')
    expect(builder).toContain("run('install_name_tool'")
    expect(electronBuilder).toContain('from: build/sdn-runtime')
    expect(electronBuilder).toContain('to: sdn-runtime')
  })
})
