// Trusted SDN update root keys are packaged with the application resources as
// a JSON map of key_id -> base64 SPKI Ed25519 public key. An empty map means
// no roots are configured and automatic SDN update checks must refuse to run.

const fs = require('fs')
const path = require('path')

const DEFAULT_TRUSTED_ROOTS_PATH = path.join(__dirname, '..', '..', 'assets', 'update-roots.json')

function loadTrustedUpdateRoots ({ rootsPath = DEFAULT_TRUSTED_ROOTS_PATH } = {}) {
  let raw
  try {
    raw = fs.readFileSync(rootsPath, 'utf8')
  } catch (err) {
    if (err && err.code === 'ENOENT') {
      return {}
    }
    throw err
  }

  const parsed = JSON.parse(raw)
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('invalid trusted update roots: expected a JSON object')
  }

  for (const [keyId, publicKey] of Object.entries(parsed)) {
    if (typeof publicKey !== 'string' || publicKey.length === 0) {
      throw new Error(`invalid trusted update root public key for ${keyId}`)
    }
  }

  return parsed
}

function hasTrustedUpdateRoots (trustedRoots) {
  return Boolean(trustedRoots) &&
    typeof trustedRoots === 'object' &&
    Object.keys(trustedRoots).length > 0
}

module.exports = {
  DEFAULT_TRUSTED_ROOTS_PATH,
  hasTrustedUpdateRoots,
  loadTrustedUpdateRoots
}
