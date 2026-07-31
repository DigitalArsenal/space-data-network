const crypto = require('crypto')
const fs = require('fs-extra')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')
const { WASM_BUNDLE_SECTION_NAME } = require('../../src/sdn-updater/carrier')
const { createSdnFeedUpdater } = require('../../src/sdn-updater/feed-updater')
const { canonicalManifestBytes, sha256Hex } = require('../../src/sdn-updater/manifest')
const { buildReleaseIndex } = require('../../src/sdn-updater/release-feed')

const NOW = new Date('2026-05-06T00:00:00Z')

function keyPair () {
  return crypto.generateKeyPairSync('ed25519')
}

function publicKeyBase64 (publicKey) {
  return publicKey.export({ type: 'spki', format: 'der' }).toString('base64')
}

function leb128 (value) {
  const bytes = []
  do {
    let byte = value & 0x7f
    value = Math.floor(value / 128)
    if (value > 0) {
      byte |= 0x80
    }
    bytes.push(byte)
  } while (value > 0)
  return Buffer.from(bytes)
}

function buildCarrier (bundleBytes) {
  const nameBytes = Buffer.from(WASM_BUNDLE_SECTION_NAME, 'utf8')
  const payload = Buffer.concat([leb128(nameBytes.length), nameBytes, bundleBytes])
  return Buffer.concat([
    Buffer.from([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]),
    Buffer.from([0x00]),
    leb128(payload.length),
    payload
  ])
}

function signedManifest ({ privateKey, publicKey, bundleBytes, wasmBytes, updateId, version, sequence }) {
  const manifest = {
    schema: 'org.spacedatanetwork.update.v1',
    update_id: updateId,
    version,
    sequence,
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

function signedPayload ({ privateKey, publicKey, updateId, version, sequence, bundleText }) {
  const bundleBytes = Buffer.from(bundleText)
  const wasmBytes = buildCarrier(bundleBytes)
  const manifest = signedManifest({ privateKey, publicKey, bundleBytes, wasmBytes, updateId, version, sequence })
  return { bundleBytes, wasmBytes, manifest }
}

function feedFetchers (index, payloadsByVersion) {
  const indexUrl = 'https://sdn.spaceaware.io/api/v1/updates/desktop/stable/darwin/arm64/index.json'
  const jsonByUrl = { [indexUrl]: index }
  const bytesByUrl = {}

  for (const entry of index.updates) {
    const payload = payloadsByVersion[entry.version]
    jsonByUrl[entry.manifest_url] = payload.manifest
    bytesByUrl[entry.carrier_url] = payload.wasmBytes
  }

  return {
    fetchJson: async url => {
      if (!(url in jsonByUrl)) {
        throw new Error(`unexpected json url ${url}`)
      }
      return jsonByUrl[url]
    },
    fetchBytes: async url => {
      if (!(url in bytesByUrl)) {
        throw new Error(`unexpected bytes url ${url}`)
      }
      return bytesByUrl[url]
    }
  }
}

async function tempRoot () {
  return fs.mkdtemp(path.join(os.tmpdir(), 'sdn-feed-updater-'))
}

function updaterOptions (overrides = {}) {
  return {
    channel: 'stable',
    platform: 'darwin',
    arch: 'arm64',
    currentSequence: 41,
    now: NOW,
    ...overrides
  }
}

test.describe('SDN feed updater', () => {
  test('checks the SDN index, stages the newest signed update, and commits it', async () => {
    const rootDir = await tempRoot()
    const { publicKey, privateKey } = keyPair()
    const older = signedPayload({
      privateKey,
      publicKey,
      updateId: 'desktop-stable-2026-05-05',
      version: '0.48.0',
      sequence: 42,
      bundleText: 'older desktop bundle'
    })
    const newer = signedPayload({
      privateKey,
      publicKey,
      updateId: 'desktop-stable-2026-05-06',
      version: '0.49.0',
      sequence: 43,
      bundleText: 'newer desktop bundle'
    })
    const index = buildReleaseIndex({ manifests: [older.manifest, newer.manifest] })
    const lifecycleCalls = []
    const updater = createSdnFeedUpdater(updaterOptions({
      rootDir,
      trustedRoots: { 'release-2026q2': publicKeyBase64(publicKey) },
      lifecycle: {
        getIpfsd: async () => ({ id: 'running-node' }),
        stopIpfs: async () => lifecycleCalls.push('stop'),
        startIpfs: async () => lifecycleCalls.push('start')
      },
      ...feedFetchers(index, { '0.48.0': older, '0.49.0': newer })
    }))

    const staged = await updater.checkAndStage()

    expect(staged).toMatchObject({
      updateId: 'desktop-stable-2026-05-06',
      version: '0.49.0',
      sequence: 43,
      bundleHash: sha256Hex(newer.bundleBytes),
      wasmHash: sha256Hex(newer.wasmBytes),
      stagedPath: path.join(rootDir, 'staged', 'desktop-stable-2026-05-06')
    })
    await expect(fs.pathExists(path.join(staged.stagedPath, 'manifest.json'))).resolves.toBe(true)
    await expect(fs.readFile(path.join(staged.stagedPath, 'update.wasm'))).resolves.toEqual(newer.wasmBytes)
    await expect(fs.readFile(path.join(staged.stagedPath, 'bundle.tar.zst'))).resolves.toEqual(newer.bundleBytes)

    const committed = await updater.commit(staged.updateId)

    expect(committed).toMatchObject({ updateId: 'desktop-stable-2026-05-06', rolledBack: false })
    expect(lifecycleCalls).toEqual(['stop', 'start'])
    await expect(fs.readFile(path.join(rootDir, 'current', 'bundle.tar.zst'))).resolves.toEqual(newer.bundleBytes)
  })

  test('is a no-op when the index has no newer sequence for the target', async () => {
    const rootDir = await tempRoot()
    const { publicKey, privateKey } = keyPair()
    const payload = signedPayload({
      privateKey,
      publicKey,
      updateId: 'desktop-stable-2026-05-05',
      version: '0.48.0',
      sequence: 42,
      bundleText: 'desktop bundle'
    })
    const index = buildReleaseIndex({ manifests: [payload.manifest] })
    const fetchers = feedFetchers(index, { '0.48.0': payload })
    let fetchedBytes = false
    const updater = createSdnFeedUpdater(updaterOptions({
      rootDir,
      currentSequence: 42,
      trustedRoots: { 'release-2026q2': publicKeyBase64(publicKey) },
      fetchJson: fetchers.fetchJson,
      fetchBytes: async url => {
        fetchedBytes = true
        return fetchers.fetchBytes(url)
      }
    }))

    await expect(updater.checkAndStage()).resolves.toBeNull()
    expect(fetchedBytes).toBe(false)
    await expect(fs.pathExists(path.join(rootDir, 'staged'))).resolves.toBe(false)
  })

  test('refuses to check for updates when no trusted roots are configured', async () => {
    const rootDir = await tempRoot()
    const logs = []
    const updater = createSdnFeedUpdater(updaterOptions({
      rootDir,
      trustedRoots: {},
      log: message => logs.push(message),
      fetchJson: async () => {
        throw new Error('network must not be touched without trusted roots')
      },
      fetchBytes: async () => {
        throw new Error('network must not be touched without trusted roots')
      }
    }))

    await expect(updater.checkAndStage()).resolves.toBeNull()
    expect(logs.join('\n')).toContain('no trusted SDN update roots configured')
  })

  test('rejects index entries that point outside the SDN-owned update feed', async () => {
    const rootDir = await tempRoot()
    const { publicKey, privateKey } = keyPair()
    const payload = signedPayload({
      privateKey,
      publicKey,
      updateId: 'desktop-stable-2026-05-05',
      version: '0.48.0',
      sequence: 42,
      bundleText: 'desktop bundle'
    })
    const hostileIndex = buildReleaseIndex({
      baseUrl: 'https://evil.example.com',
      manifests: [payload.manifest]
    })
    const updater = createSdnFeedUpdater(updaterOptions({
      rootDir,
      trustedRoots: { 'release-2026q2': publicKeyBase64(publicKey) },
      fetchJson: async () => hostileIndex,
      fetchBytes: async () => payload.wasmBytes
    }))

    await expect(updater.checkAndStage()).rejects.toThrow('SDN desktop updates must use the SDN update feed origin')
    await expect(fs.pathExists(path.join(rootDir, 'staged'))).resolves.toBe(false)
  })
})
