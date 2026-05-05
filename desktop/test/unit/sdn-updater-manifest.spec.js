const crypto = require('crypto')
const { test, expect } = require('@playwright/test')
const {
  canonicalManifestBytes,
  sha256Hex,
  verifyDownloadedUpdatePayload,
  validateUpdateManifest
} = require('../../src/sdn-updater/manifest')

function keyPair () {
  return crypto.generateKeyPairSync('ed25519')
}

function publicKeyBase64 (publicKey) {
  return publicKey.export({ type: 'spki', format: 'der' }).toString('base64')
}

function signManifest (manifest, privateKey) {
  return crypto.sign(null, canonicalManifestBytes(manifest), privateKey).toString('base64')
}

function signedManifest ({ overrides = {}, privateKey, publicKey }) {
  const manifest = {
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
    compatibility: {
      min_app_version: '0.47.0',
      max_app_version: '0.49.0'
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
      public_key: publicKeyBase64(publicKey)
    },
    rollback: {
      previous_sequence: 40
    },
    ...overrides
  }

  manifest.signing = {
    ...manifest.signing,
    signature: signManifest(manifest, privateKey)
  }

  return manifest
}

function validate (manifest, publicKey) {
  return validateUpdateManifest(manifest, {
    trustedRoots: {
      'release-2026q2': publicKeyBase64(publicKey)
    },
    platform: 'darwin',
    arch: 'arm64',
    currentSequence: 41,
    bundleHash: 'a'.repeat(64),
    now: new Date('2026-05-06T00:00:00Z')
  })
}

function payloadOptions (publicKey, overrides = {}) {
  return {
    trustedRoots: {
      'release-2026q2': publicKeyBase64(publicKey)
    },
    platform: 'darwin',
    arch: 'arm64',
    currentSequence: 41,
    now: new Date('2026-05-06T00:00:00Z'),
    ...overrides
  }
}

test.describe('SDN signed updater manifest verifier', () => {
  test('accepts a valid Ed25519-signed manifest', () => {
    const { publicKey, privateKey } = keyPair()
    const manifest = signedManifest({ privateKey, publicKey })

    expect(validate(manifest, publicKey)).toEqual({
      ok: true,
      updateId: 'desktop-stable-2026-05-05',
      sequence: 42,
      targetKind: 'desktop-app'
    })
  })

  test('rejects bad signatures', () => {
    const { publicKey, privateKey } = keyPair()
    const manifest = signedManifest({ privateKey, publicKey })
    manifest.signing.signature = Buffer.from('bad signature').toString('base64')

    expect(() => validate(manifest, publicKey)).toThrow('invalid update signature')
  })

  test('rejects wrong platform updates', () => {
    const { publicKey, privateKey } = keyPair()
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        target: {
          platform: 'linux',
          arch: 'arm64',
          kind: 'desktop-app'
        }
      }
    })

    expect(() => validate(manifest, publicKey)).toThrow('update target platform mismatch')
  })

  test('rejects tampered bundle hashes', () => {
    const { publicKey, privateKey } = keyPair()
    const manifest = signedManifest({ privateKey, publicKey })

    expect(() => validateUpdateManifest(manifest, {
      trustedRoots: {
        'release-2026q2': publicKeyBase64(publicKey)
      },
      platform: 'darwin',
      arch: 'arm64',
      currentSequence: 41,
      bundleHash: 'c'.repeat(64),
      now: new Date('2026-05-06T00:00:00Z')
    })).toThrow('update bundle hash mismatch')
  })

  test('rejects expired manifests', () => {
    const { publicKey, privateKey } = keyPair()
    const manifest = signedManifest({ privateKey, publicKey })

    expect(() => validateUpdateManifest(manifest, {
      trustedRoots: {
        'release-2026q2': publicKeyBase64(publicKey)
      },
      platform: 'darwin',
      arch: 'arm64',
      currentSequence: 41,
      bundleHash: 'a'.repeat(64),
      now: new Date('2026-07-01T00:00:00Z')
    })).toThrow('update manifest expired')
  })

  test('rejects rollback below the allowed sequence', () => {
    const { publicKey, privateKey } = keyPair()
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        sequence: 39,
        rollback: {
          previous_sequence: 40,
          reason: 'operator rollback'
        }
      }
    })

    expect(() => validate(manifest, publicKey)).toThrow('update rollback sequence rejected')
  })

  test('accepts signed downloaded WASM carrier and bundle bytes', () => {
    const { publicKey, privateKey } = keyPair()
    const wasmBytes = Buffer.from('sdn wasm carrier')
    const bundleBytes = Buffer.from('sdn desktop update bundle')
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        bundle: {
          hash: sha256Hex(bundleBytes),
          size: bundleBytes.byteLength,
          format: 'tar.zst'
        },
        wasm: {
          hash: sha256Hex(wasmBytes)
        }
      }
    })

    expect(verifyDownloadedUpdatePayload({
      manifest,
      wasmBytes,
      bundleBytes,
      ...payloadOptions(publicKey)
    })).toEqual({
      ok: true,
      updateId: 'desktop-stable-2026-05-05',
      sequence: 42,
      targetKind: 'desktop-app',
      bundleHash: sha256Hex(bundleBytes),
      wasmHash: sha256Hex(wasmBytes),
      bundleSize: bundleBytes.byteLength
    })
  })

  test('rejects downloaded updates without a bundle payload', () => {
    const { publicKey, privateKey } = keyPair()
    const wasmBytes = Buffer.from('sdn wasm carrier')
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        wasm: {
          hash: sha256Hex(wasmBytes)
        }
      }
    })

    expect(() => verifyDownloadedUpdatePayload({
      manifest,
      wasmBytes,
      ...payloadOptions(publicKey)
    })).toThrow('missing update bundle payload')
  })

  test('rejects downloaded updates with mismatched WASM bytes', () => {
    const { publicKey, privateKey } = keyPair()
    const bundleBytes = Buffer.from('sdn desktop update bundle')
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        bundle: {
          hash: sha256Hex(bundleBytes),
          size: bundleBytes.byteLength,
          format: 'tar.zst'
        },
        wasm: {
          hash: 'c'.repeat(64)
        }
      }
    })

    expect(() => verifyDownloadedUpdatePayload({
      manifest,
      wasmBytes: Buffer.from('unexpected wasm carrier'),
      bundleBytes,
      ...payloadOptions(publicKey)
    })).toThrow('update wasm hash mismatch')
  })

  test('rejects downloaded updates with mismatched bundle size', () => {
    const { publicKey, privateKey } = keyPair()
    const wasmBytes = Buffer.from('sdn wasm carrier')
    const bundleBytes = Buffer.from('sdn desktop update bundle')
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        bundle: {
          hash: sha256Hex(bundleBytes),
          size: bundleBytes.byteLength + 1,
          format: 'tar.zst'
        },
        wasm: {
          hash: sha256Hex(wasmBytes)
        }
      }
    })

    expect(() => verifyDownloadedUpdatePayload({
      manifest,
      wasmBytes,
      bundleBytes,
      ...payloadOptions(publicKey)
    })).toThrow('update bundle size mismatch')
  })

  test('rejects downloaded updates signed by untrusted roots', () => {
    const { publicKey, privateKey } = keyPair()
    const wasmBytes = Buffer.from('sdn wasm carrier')
    const bundleBytes = Buffer.from('sdn desktop update bundle')
    const manifest = signedManifest({
      privateKey,
      publicKey,
      overrides: {
        bundle: {
          hash: sha256Hex(bundleBytes),
          size: bundleBytes.byteLength,
          format: 'tar.zst'
        },
        wasm: {
          hash: sha256Hex(wasmBytes)
        }
      }
    })

    expect(() => verifyDownloadedUpdatePayload({
      manifest,
      wasmBytes,
      bundleBytes,
      ...payloadOptions(publicKey, { trustedRoots: {} })
    })).toThrow('untrusted update signing key')
  })
})
