// The node identity's public trust anchors: one committed file
// (deployment/identity/anchors.json) that every trust site reads, and the
// denylist of the identity it replaced (SEC-001).
//
// Everything here handles PUBLIC values only: peer IDs, public keys, xpubs,
// key ids, CIDs. Nothing in this module reads, derives or prints a mnemonic or
// a private key.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const ANCHORS_SCHEMA = 'org.spacedatanetwork.identity-anchors.v1';
export const ANCHORS_PATH = join(dirname(fileURLToPath(import.meta.url)), 'anchors.json');
export const PLACEHOLDER_PREFIX = 'PLACEHOLDER-';

// The DHT namespace the provider's discovery CID is computed under; the same
// constant sdn-js/src/discovery.ts and sdn-server/internal/node/node.go use.
export const MODULE_DELIVERY_DISCOVERY_NAMESPACE = 'space-data-network/module-delivery/provider-pubkey';

// Ed25519 SubjectPublicKeyInfo DER prefix (RFC 8410): what fleet-trust-roots.json
// and the update verifier take as a base64 root.
const ED25519_SPKI_PREFIX = Buffer.from('302a300506032b6570032100', 'hex');

const BASE58_ALPHABET = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';
const BASE32_ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567';

export function loadAnchors(path = ANCHORS_PATH) {
  return JSON.parse(readFileSync(path, 'utf8'));
}

export function sha256(bytes) {
  return createHash('sha256').update(bytes).digest();
}

export function base58Encode(bytes) {
  const input = Buffer.from(bytes);
  let zeros = 0;
  while (zeros < input.length && input[zeros] === 0) zeros += 1;
  let value = BigInt(`0x${input.toString('hex') || '0'}`);
  let out = '';
  while (value > 0n) {
    out = BASE58_ALPHABET[Number(value % 58n)] + out;
    value /= 58n;
  }
  return '1'.repeat(zeros) + out;
}

export function base58Decode(text) {
  let value = 0n;
  for (const ch of text) {
    const digit = BASE58_ALPHABET.indexOf(ch);
    if (digit < 0) throw new Error(`invalid base58 character ${JSON.stringify(ch)}`);
    value = value * 58n + BigInt(digit);
  }
  let hex = value === 0n ? '' : value.toString(16);
  if (hex.length % 2) hex = `0${hex}`;
  let zeros = 0;
  while (zeros < text.length && text[zeros] === '1') zeros += 1;
  return Buffer.concat([Buffer.alloc(zeros), Buffer.from(hex, 'hex')]);
}

function base32Encode(bytes) {
  let bits = 0;
  let value = 0;
  let out = '';
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += BASE32_ALPHABET[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += BASE32_ALPHABET[(value << (5 - bits)) & 31];
  return out;
}

function isCompressedSecp256k1(hex) {
  return /^0[23][0-9a-f]{64}$/.test(hex);
}

function isEd25519Hex(hex) {
  return /^[0-9a-f]{64}$/.test(hex);
}

/**
 * The libp2p PeerID of a compressed secp256k1 public key: an identity
 * multihash over the protobuf PublicKey {Type: Secp256k1, Data: key}, base58btc.
 */
export function peerIdFromSecp256k1(publicKeyHex) {
  if (!isCompressedSecp256k1(publicKeyHex)) {
    throw new Error('expected a 33-byte compressed secp256k1 public key (hex)');
  }
  const key = Buffer.from(publicKeyHex, 'hex');
  const protobuf = Buffer.concat([Buffer.from([0x08, 0x02, 0x12, key.length]), key]);
  const multihash = Buffer.concat([Buffer.from([0x00, protobuf.length]), protobuf]);
  return base58Encode(multihash);
}

/** The compressed secp256k1 key a 16Uiu2… PeerID embeds. */
export function secp256k1FromPeerId(peerId) {
  const bytes = base58Decode(peerId);
  if (bytes.length !== 39 || bytes[0] !== 0x00 || bytes[1] !== 0x25 || bytes[2] !== 0x08 || bytes[3] !== 0x02) {
    throw new Error('not a secp256k1 identity-multihash PeerID');
  }
  return bytes.subarray(6).toString('hex');
}

/** The provider discovery CID (CIDv1 raw, sha2-256) for a provider identity key. */
export function discoveryCidFor(publicKeyHex, namespace = MODULE_DELIVERY_DISCOVERY_NAMESPACE) {
  if (!isCompressedSecp256k1(publicKeyHex)) {
    throw new Error('expected a 33-byte compressed secp256k1 public key (hex)');
  }
  const digest = sha256(Buffer.concat([Buffer.from(namespace, 'utf8'), Buffer.from(publicKeyHex, 'hex')]));
  return `b${base32Encode(Buffer.concat([Buffer.from([0x01, 0x55, 0x12, 0x20]), digest]))}`;
}

/** The update-root key id: the first 12 hex chars of sha256(raw Ed25519 key). */
export function updateRootKeyId(ed25519Hex) {
  if (!isEd25519Hex(ed25519Hex)) throw new Error('expected a 32-byte Ed25519 public key (hex)');
  return sha256(Buffer.from(ed25519Hex, 'hex')).toString('hex').slice(0, 12);
}

export function ed25519Spki(ed25519Hex) {
  if (!isEd25519Hex(ed25519Hex)) throw new Error('expected a 32-byte Ed25519 public key (hex)');
  return Buffer.concat([ED25519_SPKI_PREFIX, Buffer.from(ed25519Hex, 'hex')]).toString('base64');
}

/** The raw Ed25519 key in a base64 SPKI (or raw base64 / hex) update root. */
export function ed25519FromRoot(encoded) {
  if (/^[0-9a-f]{64}$/i.test(encoded)) return encoded.toLowerCase();
  const der = Buffer.from(encoded, 'base64');
  if (der.length === 32) return der.toString('hex');
  if (der.length === 44 && der.subarray(0, 12).equals(ED25519_SPKI_PREFIX)) return der.subarray(12).toString('hex');
  throw new Error('update root is not an Ed25519 key');
}

/** The compressed public key an xpub carries (BIP-32 serialization, checksummed). */
export function publicKeyFromXpub(xpub) {
  const raw = base58Decode(xpub);
  if (raw.length !== 82) throw new Error('xpub has the wrong length');
  const payload = raw.subarray(0, 78);
  const checksum = sha256(sha256(payload)).subarray(0, 4);
  if (!checksum.equals(raw.subarray(78))) throw new Error('xpub checksum mismatch');
  if (payload.readUInt32BE(0) !== 0x0488b21e) throw new Error('not a mainnet xpub');
  return payload.subarray(45, 78).toString('hex');
}

export function isPlaceholder(value) {
  return typeof value === 'string' && value.startsWith(PLACEHOLDER_PREFIX);
}

/** Every string leaf of a JSON value, with its JSON path. */
export function stringLeaves(value, path = '') {
  if (typeof value === 'string') return [[path, value]];
  if (Array.isArray(value)) return value.flatMap((item, i) => stringLeaves(item, `${path}[${i}]`));
  if (value && typeof value === 'object') {
    return Object.entries(value).flatMap(([key, item]) => [
      ...(isPlaceholder(key) ? [[`${path}.${key}`, key]] : []),
      ...stringLeaves(item, path ? `${path}.${key}` : key),
    ]);
  }
  return [];
}

/** All denied values, lower-cased for hex/base32 and verbatim otherwise. */
export function deniedValues(anchors) {
  const deny = anchors.denylist || {};
  return new Set([
    ...(deny.peerIds || []),
    ...(deny.secp256k1PublicKeys || []).map((v) => v.toLowerCase()),
    ...(deny.ed25519PublicKeys || []).map((v) => v.toLowerCase()),
    ...(deny.x25519PublicKeys || []).map((v) => v.toLowerCase()),
    ...(deny.xpubs || []),
    ...(deny.updateRootKeyIds || []).map((v) => v.toLowerCase()),
    ...(deny.discoveryCids || []),
    ...(deny.chainAddresses || []),
  ]);
}

export function isDenied(anchors, value) {
  if (typeof value !== 'string' || value === '') return false;
  const denied = deniedValues(anchors);
  return denied.has(value) || denied.has(value.toLowerCase());
}

/** A multiaddr's /p2p/ peer, or ''. */
export function peerOfMultiaddr(addr) {
  const match = /\/(?:p2p|ipfs)\/([1-9A-HJ-NP-Za-km-z]+)(?:\/|$)/.exec(addr);
  return match ? match[1] : '';
}

/** The provider's and peers' dialable multiaddrs, skipping placeholder identities. */
export function bootstrapMultiaddrs(anchors, kind = 'bootstrapTransports') {
  const out = [];
  for (const entry of [anchors.provider, ...(anchors.peers || [])]) {
    if (!entry || isPlaceholder(entry.peerId) || isDenied(anchors, entry.peerId)) continue;
    for (const transport of entry[kind] || []) out.push(`${transport}/p2p/${entry.peerId}`);
  }
  return out;
}

/**
 * Structural problems that make the anchors file unusable in ANY state:
 * malformed values, an anchor that is itself denylisted, a denylist that lost
 * its entries. A placeholder value is not a structural problem.
 */
export function structuralProblems(anchors) {
  const problems = [];
  if (anchors.schema !== ANCHORS_SCHEMA) problems.push(`schema must be ${ANCHORS_SCHEMA}`);
  if (!['placeholder', 'released'].includes(anchors.status)) problems.push('status must be "placeholder" or "released"');
  const p = anchors.provider || {};
  const check = (label, value, valid) => {
    if (isPlaceholder(value)) return;
    if (typeof value !== 'string' || !valid(value)) problems.push(`${label} is malformed`);
    else if (isDenied(anchors, value)) problems.push(`${label} is DENYLISTED (SEC-001): it is a value of the leaked identity`);
  };
  check('provider.peerId', p.peerId, (v) => /^16Uiu2[1-9A-HJ-NP-Za-km-z]{47,}$/.test(v));
  check('provider.identityPublicKey', p.identityPublicKey, isCompressedSecp256k1);
  check('provider.xpub', p.xpub, (v) => v.startsWith('xpub'));
  check('provider.signingPublicKey', p.signingPublicKey, isCompressedSecp256k1);
  check('provider.encryptionPublicKey', p.encryptionPublicKey, isCompressedSecp256k1);
  check('provider.publisherPublicKey', p.publisherPublicKey, isEd25519Hex);
  check('provider.x25519PublicKey', p.x25519PublicKey, isEd25519Hex);
  check('provider.grantVerifierPublicKey', p.grantVerifierPublicKey, isEd25519Hex);
  check('provider.rootAdminPublicKey', p.rootAdminPublicKey, isEd25519Hex);
  check('provider.discoveryCid', p.discoveryCid, (v) => /^bafkrei[a-z2-7]{52}$/.test(v));

  if (!isPlaceholder(p.identityPublicKey) && isCompressedSecp256k1(p.identityPublicKey || '')) {
    if (!isPlaceholder(p.peerId) && peerIdFromSecp256k1(p.identityPublicKey) !== p.peerId) {
      problems.push('provider.peerId is not the PeerID of provider.identityPublicKey');
    }
    if (!isPlaceholder(p.discoveryCid) && discoveryCidFor(p.identityPublicKey) !== p.discoveryCid) {
      problems.push('provider.discoveryCid is not the discovery CID of provider.identityPublicKey');
    }
    if (!isPlaceholder(p.xpub)) {
      try {
        if (publicKeyFromXpub(p.xpub) !== p.identityPublicKey) problems.push('provider.xpub does not carry provider.identityPublicKey');
      } catch (error) {
        problems.push(`provider.xpub: ${error.message}`);
      }
    }
  }
  for (const [label, transports] of [['bootstrapTransports', p.bootstrapTransports], ['browserTransports', p.browserTransports]]) {
    if (!Array.isArray(transports) || transports.length === 0) problems.push(`provider.${label} must name at least one transport`);
    else if (transports.some((t) => typeof t !== 'string' || !t.startsWith('/') || t.includes('/p2p/'))) {
      problems.push(`provider.${label} entries are transports without /p2p/`);
    }
  }
  for (const [i, peer] of (anchors.peers || []).entries()) {
    if (isDenied(anchors, peer.peerId)) problems.push(`peers[${i}].peerId is DENYLISTED (SEC-001)`);
  }
  for (const [keyId, root] of Object.entries(anchors.updateRoots || {})) {
    if (isPlaceholder(keyId) || isPlaceholder(root)) continue;
    let raw;
    try {
      raw = ed25519FromRoot(root);
    } catch (error) {
      problems.push(`updateRoots.${keyId}: ${error.message}`);
      continue;
    }
    if (updateRootKeyId(raw) !== keyId) problems.push(`updateRoots.${keyId} is listed under the wrong key id`);
    if (isDenied(anchors, keyId) || isDenied(anchors, raw)) problems.push(`updateRoots.${keyId} is DENYLISTED (SEC-001)`);
    if (!isPlaceholder(p.publisherPublicKey) && raw === p.publisherPublicKey) {
      problems.push('the update root must be a distribution key separate from the node identity (it equals provider.publisherPublicKey)');
    }
  }
  if (Object.keys(anchors.updateRoots || {}).length === 0) problems.push('updateRoots must name at least one root');
  const deny = anchors.denylist || {};
  for (const key of ['peerIds', 'secp256k1PublicKeys', 'ed25519PublicKeys', 'xpubs', 'updateRootKeyIds', 'discoveryCids']) {
    if (!Array.isArray(deny[key]) || deny[key].length === 0) problems.push(`denylist.${key} must not be empty`);
  }
  return problems;
}

/**
 * The release gate: a node, sdn-js or desktop release must not ship while any
 * anchor is still a placeholder, or while the file is structurally unsound.
 */
export function releaseProblems(anchors) {
  const problems = structuralProblems(anchors);
  if (anchors.status !== 'released') problems.push('status is not "released"');
  for (const [path, value] of stringLeaves(anchors)) {
    if (isPlaceholder(value)) problems.push(`${path} is still a placeholder`);
  }
  return problems;
}
