const crypto = require('crypto')
const fs = require('fs-extra')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')
const { canonicalManifestBytes, sha256Hex } = require('../../src/sdn-updater/manifest')
const { createStagedUpdater } = require('../../src/sdn-updater/staged-updater')

function keyPair () {
  return crypto.generateKeyPairSync('ed25519')
}

function publicKeyBase64 (publicKey) {
  return publicKey.export({ type: 'spki', format: 'der' }).toString('base64')
}

function signedManifest ({ privateKey, publicKey, bundleBytes, wasmBytes, updateId = 'desktop-stable-2026-05-05' }) {
  const manifest = {
    schema: 'org.spacedatanetwork.update.v1',
    update_id: updateId,
    version: '0.48.0',
    sequence: 42,
    channel: 'stable',
    created_at: '2026-05-05T00:00:00Z',
    expires_at: '2026-06-05T00:00:00Z',
    target: {
      platform: 'darwin',
      arch: 'arm64',
      kind: 'desktop-app'
    },
    compatibility: {
      min_app_version: '0.47.0',
      max_app_version: '0.49.0'
    },
    bundle: {
      hash: sha256Hex(bundleBytes),
      size: bundleBytes.byteLength,
      format: 'tar.zst'
    },
    wasm: {
      hash: sha256Hex(wasmBytes)
    },
    signing: {
      key_id: 'release-2026q2',
      algorithm: 'Ed25519',
      public_key: publicKeyBase64(publicKey)
    },
    rollback: {
      previous_sequence: 40
    }
  }
  manifest.signing.signature = crypto.sign(null, canonicalManifestBytes(manifest), privateKey).toString('base64')
  return manifest
}

function verifyOptions (publicKey) {
  return {
    trustedRoots: {
      'release-2026q2': publicKeyBase64(publicKey)
    },
    platform: 'darwin',
    arch: 'arm64',
    currentSequence: 41,
    now: new Date('2026-05-06T00:00:00Z')
  }
}

async function tempRoot () {
  return fs.mkdtemp(path.join(os.tmpdir(), 'sdn-updater-'))
}

test.describe('SDN staged updater install flow', () => {
  test('downloads, extracts, verifies, and stages an update without installing it', async () => {
    const rootDir = await tempRoot()
    const { publicKey, privateKey } = keyPair()
    const wasmBytes = Buffer.from('signed inert wasm carrier')
    const bundleBytes = Buffer.from('compressed desktop bundle')
    const manifest = signedManifest({ privateKey, publicKey, wasmBytes, bundleBytes })
    const updater = createStagedUpdater({ rootDir })

    const result = await updater.downloadVerifyAndStageUpdate({
      manifestUrl: 'https://sdn.spaceaware.io/updates/desktop/manifest.json',
      carrierUrl: 'https://sdn.spaceaware.io/updates/desktop/update.wasm',
      fetchJson: async () => manifest,
      fetchBytes: async () => wasmBytes,
      extractBundleBytes: async bytes => {
        expect(Buffer.from(bytes).equals(wasmBytes)).toBe(true)
        return bundleBytes
      },
      verifyOptions: verifyOptions(publicKey)
    })

    expect(result).toMatchObject({
      updateId: 'desktop-stable-2026-05-05',
      sequence: 42,
      bundleHash: sha256Hex(bundleBytes),
      wasmHash: sha256Hex(wasmBytes),
      stagedPath: path.join(rootDir, 'staged', 'desktop-stable-2026-05-05')
    })
    await expect(fs.pathExists(path.join(result.stagedPath, 'manifest.json'))).resolves.toBe(true)
    await expect(fs.readFile(path.join(result.stagedPath, 'update.wasm'))).resolves.toEqual(wasmBytes)
    await expect(fs.readFile(path.join(result.stagedPath, 'bundle.tar.zst'))).resolves.toEqual(bundleBytes)
  })

  test('commits a staged update by stopping the daemon, swapping files, and restarting it', async () => {
    const rootDir = await tempRoot()
    const updater = createStagedUpdater({ rootDir })
    const calls = []
    await fs.outputFile(path.join(rootDir, 'current', 'app.txt'), 'old app')
    await fs.outputFile(path.join(rootDir, 'staged', 'desktop-stable-2026-05-05', 'app.txt'), 'new app')

    const result = await updater.commitStagedUpdate({
      updateId: 'desktop-stable-2026-05-05',
      lifecycle: {
        getIpfsd: async () => ({ id: 'running-node' }),
        stopIpfs: async () => calls.push('stop'),
        startIpfs: async () => calls.push('start')
      },
      healthCheck: async currentPath => fs.readFile(path.join(currentPath, 'app.txt'), 'utf8')
    })

    expect(calls).toEqual(['stop', 'start'])
    expect(result).toMatchObject({ updateId: 'desktop-stable-2026-05-05', rolledBack: false })
    await expect(fs.readFile(path.join(rootDir, 'current', 'app.txt'), 'utf8')).resolves.toBe('new app')
    await expect(fs.readFile(path.join(rootDir, 'rollback', 'desktop-stable-2026-05-05', 'app.txt'), 'utf8')).resolves.toBe('old app')
  })

  test('verifies daemon health after restarting the updated daemon', async () => {
    const rootDir = await tempRoot()
    const updater = createStagedUpdater({ rootDir })
    const calls = []
    await fs.outputFile(path.join(rootDir, 'current', 'app.txt'), 'old app')
    await fs.outputFile(path.join(rootDir, 'staged', 'desktop-stable-2026-05-05', 'app.txt'), 'new app')

    await updater.commitStagedUpdate({
      updateId: 'desktop-stable-2026-05-05',
      lifecycle: {
        getIpfsd: async () => ({ id: 'running-node' }),
        stopIpfs: async () => calls.push('stop'),
        startIpfs: async () => calls.push('start')
      },
      healthCheck: async currentPath => {
        calls.push('health')
        expect(calls).toEqual(['stop', 'start', 'health'])
        return fs.readFile(path.join(currentPath, 'app.txt'), 'utf8')
      }
    })

    expect(calls).toEqual(['stop', 'start', 'health'])
  })

  test('rolls back and restarts the prior daemon when post-swap health fails', async () => {
    const rootDir = await tempRoot()
    const updater = createStagedUpdater({ rootDir })
    const calls = []
    await fs.outputFile(path.join(rootDir, 'current', 'app.txt'), 'old app')
    await fs.outputFile(path.join(rootDir, 'staged', 'desktop-stable-2026-05-05', 'app.txt'), 'broken app')

    await expect(updater.commitStagedUpdate({
      updateId: 'desktop-stable-2026-05-05',
      lifecycle: {
        getIpfsd: async () => ({ id: 'running-node' }),
        stopIpfs: async () => calls.push('stop'),
        startIpfs: async () => calls.push('start')
      },
      healthCheck: async () => {
        throw new Error('startup health failed')
      }
    })).rejects.toThrow('startup health failed')

    expect(calls).toEqual(['stop', 'start', 'stop', 'start'])
    await expect(fs.readFile(path.join(rootDir, 'current', 'app.txt'), 'utf8')).resolves.toBe('old app')
    await expect(fs.pathExists(path.join(rootDir, 'failed', 'desktop-stable-2026-05-05'))).resolves.toBe(true)
  })
})
