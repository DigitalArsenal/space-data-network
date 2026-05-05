const crypto = require('crypto')

function canonicalize (value) {
  if (Array.isArray(value)) {
    return value.map(canonicalize)
  }

  if (value && typeof value === 'object') {
    return Object.keys(value)
      .sort()
      .reduce((next, key) => {
        if (key === 'signature' && value === value.signing) {
          return next
        }
        next[key] = canonicalize(value[key])
        return next
      }, {})
  }

  return value
}

function manifestWithoutSignature (manifest) {
  return {
    ...manifest,
    signing: Object.keys(manifest.signing || {})
      .filter(key => key !== 'signature')
      .sort()
      .reduce((next, key) => {
        next[key] = manifest.signing[key]
        return next
      }, {})
  }
}

function canonicalManifestBytes (manifest) {
  return Buffer.from(JSON.stringify(canonicalize(manifestWithoutSignature(manifest))))
}

function assertString (value, message) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(message)
  }
}

function assertHash (value, message) {
  if (typeof value !== 'string' || !/^[a-f0-9]{64}$/i.test(value)) {
    throw new Error(message)
  }
}

function publicKeyFromBase64 (publicKey) {
  return crypto.createPublicKey({
    key: Buffer.from(publicKey, 'base64'),
    type: 'spki',
    format: 'der'
  })
}

function assertRequiredShape (manifest) {
  if (!manifest || typeof manifest !== 'object') {
    throw new Error('missing update manifest')
  }
  if (manifest.schema !== 'org.spacedatanetwork.update.v1') {
    throw new Error('unsupported update manifest schema')
  }
  assertString(manifest.update_id, 'missing update id')
  assertString(manifest.version, 'missing update version')
  if (!Number.isInteger(manifest.sequence)) {
    throw new Error('missing update sequence')
  }
  assertString(manifest.channel, 'missing update channel')
  assertString(manifest.created_at, 'missing update creation time')
  assertString(manifest.expires_at, 'missing update expiration')
  assertString(manifest.target?.platform, 'missing update target platform')
  assertString(manifest.target?.arch, 'missing update target arch')
  assertString(manifest.target?.kind, 'missing update target kind')
  assertHash(manifest.bundle?.hash, 'missing update bundle hash')
  if (!Number.isInteger(manifest.bundle?.size) || manifest.bundle.size < 0) {
    throw new Error('missing update bundle size')
  }
  assertString(manifest.bundle?.format, 'missing update bundle format')
  assertHash(manifest.wasm?.hash, 'missing update wasm hash')
  assertString(manifest.signing?.key_id, 'missing update signing key id')
  if (manifest.signing?.algorithm !== 'Ed25519') {
    throw new Error('unsupported update signing algorithm')
  }
  assertString(manifest.signing?.signature, 'missing update signature')
}

function assertTarget (manifest, options) {
  if (manifest.target.platform !== options.platform) {
    throw new Error('update target platform mismatch')
  }
  if (manifest.target.arch !== options.arch) {
    throw new Error('update target arch mismatch')
  }
}

function assertExpiration (manifest, now) {
  const expiresAt = Date.parse(manifest.expires_at)
  if (!Number.isFinite(expiresAt)) {
    throw new Error('invalid update expiration')
  }
  if (expiresAt <= now.getTime()) {
    throw new Error('update manifest expired')
  }
}

function assertSequence (manifest, currentSequence) {
  if (manifest.sequence > currentSequence) {
    return
  }

  if (!manifest.rollback?.reason || !Number.isInteger(manifest.rollback?.previous_sequence)) {
    throw new Error('update sequence rejected')
  }

  if (manifest.sequence < manifest.rollback.previous_sequence) {
    throw new Error('update rollback sequence rejected')
  }
}

function assertSignature (manifest, trustedRoots) {
  const trustedPublicKey = trustedRoots?.[manifest.signing.key_id]
  if (!trustedPublicKey) {
    throw new Error('untrusted update signing key')
  }
  if (manifest.signing.public_key && manifest.signing.public_key !== trustedPublicKey) {
    throw new Error('update signing key mismatch')
  }

  const valid = crypto.verify(
    null,
    canonicalManifestBytes(manifest),
    publicKeyFromBase64(trustedPublicKey),
    Buffer.from(manifest.signing.signature, 'base64')
  )

  if (!valid) {
    throw new Error('invalid update signature')
  }
}

function validateUpdateManifest (manifest, options) {
  assertRequiredShape(manifest)
  assertTarget(manifest, options)
  assertExpiration(manifest, options.now || new Date())
  assertSequence(manifest, options.currentSequence)

  if (manifest.bundle.hash !== options.bundleHash) {
    throw new Error('update bundle hash mismatch')
  }

  assertSignature(manifest, options.trustedRoots)

  return {
    ok: true,
    updateId: manifest.update_id,
    sequence: manifest.sequence,
    targetKind: manifest.target.kind
  }
}

module.exports = {
  canonicalManifestBytes,
  validateUpdateManifest
}
