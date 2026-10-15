// @ts-check
//
// SEC-001 for the desktop shell. The node it ships trusts the identities and
// update roots in deployment/identity/anchors.json, copied here as
// identity-anchors.json by deployment/identity/sync-anchors.mjs (electron-builder
// packages only desktop/, so it cannot read the repo file at run time).
//
// The BIP-39 seed behind the previous sdn.spaceaware.io identity was published;
// every public value derived from it is on the denylist, and a bundle whose
// trust/update-roots.json still names that update root is refused — anyone
// holding the seed could push code to it.
const anchors = require('./identity-anchors.json')

const deny = anchors.denylist
const DENIED = new Set([
  ...deny.peerIds,
  ...deny.secp256k1PublicKeys.map((v) => v.toLowerCase()),
  ...deny.ed25519PublicKeys.map((v) => v.toLowerCase()),
  ...deny.xpubs,
  ...deny.updateRootKeyIds.map((v) => v.toLowerCase()),
  ...deny.discoveryCids,
  ...deny.chainAddresses
])

// Ed25519 SubjectPublicKeyInfo DER prefix (RFC 8410).
const ED25519_SPKI_PREFIX = Buffer.from('302a300506032b6570032100', 'hex')

/** @param {unknown} value */
function isDenied (value) {
  if (typeof value !== 'string') return false
  const trimmed = value.trim()
  return trimmed !== '' && (DENIED.has(trimmed) || DENIED.has(trimmed.toLowerCase()))
}

/**
 * The raw Ed25519 key (hex) in a base64 SPKI, raw base64 or hex update root, or ''.
 * @param {string} encoded
 */
function rootKeyHex (encoded) {
  if (/^[0-9a-f]{64}$/i.test(encoded)) return encoded.toLowerCase()
  const der = Buffer.from(String(encoded), 'base64')
  if (der.length === 32) return der.toString('hex')
  if (der.length === 44 && der.subarray(0, 12).equals(ED25519_SPKI_PREFIX)) return der.subarray(12).toString('hex')
  return ''
}

/**
 * Key ids of every denylisted root in an update-roots.json object.
 * @param {Record<string, string>} roots
 * @returns {string[]}
 */
function deniedUpdateRoots (roots) {
  return Object.entries(roots || {})
    .filter(([keyId, encoded]) => isDenied(keyId) || isDenied(rootKeyHex(encoded)))
    .map(([keyId]) => keyId)
}

module.exports = { anchors, isDenied, deniedUpdateRoots, rootKeyHex }
