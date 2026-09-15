const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { test, expect } = require('@playwright/test')

const { bundleCandidates, isBundle, nodeEnv, resolveBundle } = require('../../src/node/paths')

function stageFakeBundle (root) {
  fs.mkdirSync(path.join(root, 'bin'), { recursive: true })
  fs.mkdirSync(path.join(root, 'runtime', 'sdn'), { recursive: true })
  fs.mkdirSync(path.join(root, 'runtime', 'wasmedge', 'lib'), { recursive: true })
  fs.writeFileSync(path.join(root, 'manifest.json'), JSON.stringify({ version: '1.2.3-beta.4' }))
  const exe = process.platform === 'win32'
    ? path.join(root, 'bin', 'spacedatanetwork.exe')
    : path.join(root, 'runtime', 'sdn', 'spacedatanetwork')
  fs.writeFileSync(exe, '#!/bin/sh\n', { mode: 0o755 })
  return root
}

test.describe('finding the bundled node', () => {
  test('prefers an explicit bundle directory, then resources, then the dev tree', () => {
    const candidates = bundleCandidates({
      resourcesPath: '/app/Contents/Resources',
      appPath: '/app/Contents/Resources/app.asar',
      env: { SDN_DESKTOP_BUNDLE_DIR: '/ci/bundle' }
    })

    expect(candidates).toEqual([
      '/ci/bundle',
      path.join('/app/Contents/Resources', 'sdn-node'),
      path.join('/app/Contents/Resources/app.asar', 'assets', 'sdn-node')
    ])
  })

  test('reads the version out of the staged manifest', () => {
    const dir = stageFakeBundle(fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-bundle-')))
    try {
      expect(isBundle(dir)).toBe(true)
      const bundle = resolveBundle({ appPath: '/nowhere', env: { SDN_DESKTOP_BUNDLE_DIR: dir } })
      expect(bundle.root).toBe(dir)
      expect(bundle.version).toBe('1.2.3-beta.4')
    } finally {
      fs.rmSync(dir, { recursive: true, force: true })
    }
  })

  test('says where it looked when nothing is staged', () => {
    expect(() => resolveBundle({ appPath: '/nowhere', env: {} })).toThrow(/no Space Data Network node bundle/)
  })

  test('points the child at the WasmEdge runtime inside the bundle', () => {
    const env = nodeEnv('/b', { PATH: '/usr/bin' })

    expect(env.WASMEDGE_DIR).toBe(path.join('/b', 'runtime', 'wasmedge'))
    expect(env.HD_WALLET_WASM_PATH).toBe(path.join('/b', 'runtime', 'modules', 'hd-wallet-wasi.wasm'))
    expect(env.LD_LIBRARY_PATH.split(path.delimiter)[0]).toBe(path.join('/b', 'runtime', 'wasmedge', 'lib'))
    if (process.platform === 'darwin') {
      expect(env.DYLD_LIBRARY_PATH.split(path.delimiter)[0]).toBe(path.join('/b', 'runtime', 'wasmedge', 'lib'))
    }
    expect(env.PATH.split(path.delimiter)).toContain('/usr/bin')
  })
})
