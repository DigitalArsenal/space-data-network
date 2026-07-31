const fs = require('fs-extra')
const os = require('os')
const path = require('path')
const { test, expect } = require('@playwright/test')

const { buildFeedFromFiles } = require('../../../deployment/release/build-sdn-update-feed')
const {
  SDN_UPDATE_FEED_BASE_URL,
  assertSdnOwnedDesktopUpdateFeedUrl,
  buildReleaseIndex,
  desktopAppFeedUrls
} = require('../../src/sdn-updater/release-feed')

const baseManifest = {
  schema: 'org.spacedatanetwork.update.v1',
  update_id: 'desktop-stable-2026-05-05',
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
  bundle: {
    hash: 'a'.repeat(64),
    size: 1234,
    format: 'tar.zst'
  },
  wasm: {
    hash: 'b'.repeat(64)
  },
  signing: {
    key_id: 'release-2026q2',
    algorithm: 'Ed25519',
    signature: 'signed'
  }
}

test.describe('SDN updater release feed', () => {
  test('builds deterministic SDN-owned desktop feed URLs', () => {
    const urls = desktopAppFeedUrls({
      channel: 'stable',
      platform: 'darwin',
      arch: 'arm64',
      version: '0.48.0'
    })

    expect(urls).toEqual({
      baseUrl: SDN_UPDATE_FEED_BASE_URL,
      feedUrl: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64',
      indexUrl: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/index.json',
      manifestUrl: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.48.0/manifest.json',
      carrierUrl: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.48.0/update.wasm'
    })
    expect(JSON.stringify(urls)).not.toContain('ipfs-desktop')
    expect(JSON.stringify(urls)).not.toContain('github.com')
  })

  test('rejects non-SDN desktop application update feeds', () => {
    expect(() => assertSdnOwnedDesktopUpdateFeedUrl('https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64')).not.toThrow()
    expect(() => assertSdnOwnedDesktopUpdateFeedUrl('https://github.com/DigitalArsenal/space-data-network/releases/latest')).toThrow('SDN desktop updates must use the SDN update feed origin')
    expect(() => assertSdnOwnedDesktopUpdateFeedUrl('https://github.com/ipfs-shipyard/ipfs-desktop/releases/latest')).toThrow('SDN desktop updates must use the SDN update feed origin')
  })

  test('builds a signed-manifest release index with SDN-owned payload URLs', () => {
    const index = buildReleaseIndex({
      generatedAt: '2026-05-05T12:00:00Z',
      manifests: [
        {
          ...baseManifest,
          update_id: 'desktop-stable-2026-05-04',
          version: '0.47.1',
          sequence: 41
        },
        baseManifest
      ]
    })

    expect(index).toEqual({
      schema: 'org.spacedatanetwork.update.index.v1',
      generated_at: '2026-05-05T12:00:00Z',
      feed_base_url: 'https://sdn.spaceaware.io/updates',
      updates: [
        {
          update_id: 'desktop-stable-2026-05-05',
          version: '0.48.0',
          sequence: 42,
          channel: 'stable',
          target: {
            platform: 'darwin',
            arch: 'arm64',
            kind: 'desktop-app'
          },
          expires_at: '2026-06-05T00:00:00Z',
          bundle_hash: 'a'.repeat(64),
          wasm_hash: 'b'.repeat(64),
          signing_key_id: 'release-2026q2',
          manifest_url: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.48.0/manifest.json',
          carrier_url: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.48.0/update.wasm'
        },
        {
          update_id: 'desktop-stable-2026-05-04',
          version: '0.47.1',
          sequence: 41,
          channel: 'stable',
          target: {
            platform: 'darwin',
            arch: 'arm64',
            kind: 'desktop-app'
          },
          expires_at: '2026-06-05T00:00:00Z',
          bundle_hash: 'a'.repeat(64),
          wasm_hash: 'b'.repeat(64),
          signing_key_id: 'release-2026q2',
          manifest_url: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.47.1/manifest.json',
          carrier_url: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.47.1/update.wasm'
        }
      ]
    })
  })

  test('indexes non-desktop SDN payloads under their target-kind feed path', () => {
    const index = buildReleaseIndex({
      generatedAt: '2026-05-05T12:00:00Z',
      manifests: [
        {
          ...baseManifest,
          update_id: 'kubo-runtime-stable-2026-05-05',
          version: '0.39.1',
          target: {
            platform: 'darwin',
            arch: 'arm64',
            kind: 'kubo-runtime'
          }
        }
      ]
    })

    expect(index.updates[0]).toMatchObject({
      update_id: 'kubo-runtime-stable-2026-05-05',
      target: {
        platform: 'darwin',
        arch: 'arm64',
        kind: 'kubo-runtime'
      },
      manifest_url: 'https://sdn.spaceaware.io/updates/kubo-runtime/stable/darwin/arm64/0.39.1/manifest.json',
      carrier_url: 'https://sdn.spaceaware.io/updates/kubo-runtime/stable/darwin/arm64/0.39.1/update.wasm'
    })
  })

  test('assembles static feed artifacts from signed manifests and carriers', async () => {
    const rootDir = await fs.mkdtemp(path.join(os.tmpdir(), 'sdn-update-feed-'))
    const sourceDir = path.join(rootDir, 'source')
    const outDir = path.join(rootDir, 'out')
    const manifestPath = path.join(sourceDir, 'manifest.json')
    const carrierPath = path.join(sourceDir, 'update.wasm')
    await fs.outputJson(manifestPath, baseManifest, { spaces: 2 })
    await fs.outputFile(carrierPath, 'signed wasm carrier')

    const result = await buildFeedFromFiles({
      generatedAt: '2026-05-05T12:00:00Z',
      outDir,
      entries: [
        {
          manifestPath,
          carrierPath
        }
      ]
    })

    expect(result.indexPath).toBe(path.join(outDir, 'desktop', 'stable', 'darwin', 'arm64', 'index.json'))
    expect(result.entries).toEqual([
      {
        updateId: 'desktop-stable-2026-05-05',
        version: '0.48.0',
        manifestPath: path.join(outDir, 'desktop', 'stable', 'darwin', 'arm64', '0.48.0', 'manifest.json'),
        carrierPath: path.join(outDir, 'desktop', 'stable', 'darwin', 'arm64', '0.48.0', 'update.wasm')
      }
    ])
    await expect(fs.readJson(result.indexPath)).resolves.toMatchObject({
      schema: 'org.spacedatanetwork.update.index.v1',
      updates: [
        {
          update_id: 'desktop-stable-2026-05-05',
          manifest_url: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.48.0/manifest.json',
          carrier_url: 'https://sdn.spaceaware.io/updates/desktop/stable/darwin/arm64/0.48.0/update.wasm'
        }
      ]
    })
    await expect(fs.readFile(result.entries[0].manifestPath, 'utf8')).resolves.toContain('desktop-stable-2026-05-05')
    await expect(fs.readFile(result.entries[0].carrierPath, 'utf8')).resolves.toBe('signed wasm carrier')
  })

  test('writes a separate feed index for each target feed path', async () => {
    const rootDir = await fs.mkdtemp(path.join(os.tmpdir(), 'sdn-update-feed-'))
    const desktopManifestPath = path.join(rootDir, 'desktop-manifest.json')
    const runtimeManifestPath = path.join(rootDir, 'runtime-manifest.json')
    const desktopCarrierPath = path.join(rootDir, 'desktop.wasm')
    const runtimeCarrierPath = path.join(rootDir, 'runtime.wasm')
    const outDir = path.join(rootDir, 'out')

    await fs.outputJson(desktopManifestPath, baseManifest, { spaces: 2 })
    await fs.outputJson(runtimeManifestPath, {
      ...baseManifest,
      update_id: 'kubo-runtime-stable-2026-05-05',
      version: '0.39.1',
      target: {
        platform: 'darwin',
        arch: 'arm64',
        kind: 'kubo-runtime'
      }
    }, { spaces: 2 })
    await fs.outputFile(desktopCarrierPath, 'desktop carrier')
    await fs.outputFile(runtimeCarrierPath, 'runtime carrier')

    const result = await buildFeedFromFiles({
      generatedAt: '2026-05-05T12:00:00Z',
      outDir,
      entries: [
        { manifestPath: desktopManifestPath, carrierPath: desktopCarrierPath },
        { manifestPath: runtimeManifestPath, carrierPath: runtimeCarrierPath }
      ]
    })

    expect(result.indexPaths).toEqual([
      path.join(outDir, 'desktop', 'stable', 'darwin', 'arm64', 'index.json'),
      path.join(outDir, 'kubo-runtime', 'stable', 'darwin', 'arm64', 'index.json')
    ])

    await expect(fs.readJson(result.indexPaths[0])).resolves.toMatchObject({
      updates: [
        {
          update_id: 'desktop-stable-2026-05-05'
        }
      ]
    })
    await expect(fs.readJson(result.indexPaths[1])).resolves.toMatchObject({
      updates: [
        {
          update_id: 'kubo-runtime-stable-2026-05-05'
        }
      ]
    })
  })
})
