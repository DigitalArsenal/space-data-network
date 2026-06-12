import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const crypto = require('crypto');
// Shared with the verification side so signing and verification always agree
// on the canonical manifest bytes (sorted keys, signing.signature omitted).
const { canonicalManifestBytes, sha256Hex } = require('../../desktop/src/sdn-updater/manifest');

export const MANIFEST_SCHEMA = 'org.spacedatanetwork.update.v1';
export const SIGNING_KEY_ENV = 'SDN_UPDATE_SIGNING_KEY_PEM';

export function normalizeSigningKey(privateKey) {
  if (!privateKey) {
    throw new Error('privateKey is required');
  }
  const keyObject = privateKey instanceof crypto.KeyObject
    ? privateKey
    : crypto.createPrivateKey(privateKey);
  if (keyObject.type !== 'private' || keyObject.asymmetricKeyType !== 'ed25519') {
    throw new Error('update signing key must be an Ed25519 private key');
  }
  return keyObject;
}

export function publicKeyBase64(publicKey) {
  if (typeof publicKey === 'string') {
    return publicKey;
  }
  const keyObject = publicKey instanceof crypto.KeyObject && publicKey.type === 'public'
    ? publicKey
    : crypto.createPublicKey(publicKey);
  if (keyObject.asymmetricKeyType !== 'ed25519') {
    throw new Error('update signing public key must be an Ed25519 key');
  }
  return keyObject.export({ type: 'spki', format: 'der' }).toString('base64');
}

export function buildUpdateManifest(options) {
  const updateId = requiredString(options.updateId, 'updateId');
  const version = requiredString(options.version, 'version');
  const channel = requiredString(options.channel, 'channel');
  const platform = requiredString(options.platform, 'platform');
  const arch = requiredString(options.arch, 'arch');
  const kind = requiredString(options.kind, 'kind');
  const keyId = requiredString(options.keyId, 'keyId');
  const createdAt = requiredTimestamp(options.createdAt, 'createdAt');
  const expiresAt = requiredTimestamp(options.expiresAt, 'expiresAt');
  const bundleBytes = requiredBytes(options.bundleBytes, 'bundleBytes');
  const wasmBytes = requiredBytes(options.wasmBytes, 'wasmBytes');
  if (!Number.isInteger(options.sequence)) {
    throw new Error('sequence must be an integer');
  }

  const manifest = {
    schema: MANIFEST_SCHEMA,
    update_id: updateId,
    version,
    sequence: options.sequence,
    channel,
    created_at: createdAt,
    expires_at: expiresAt,
    target: { platform, arch, kind },
    bundle: {
      hash: sha256Hex(bundleBytes),
      size: bundleBytes.length,
      format: options.format || 'tar.gz',
    },
    wasm: { hash: sha256Hex(wasmBytes) },
    signing: {
      key_id: keyId,
      algorithm: 'Ed25519',
    },
  };
  if (options.publicKey) {
    manifest.signing.public_key = publicKeyBase64(options.publicKey);
  }
  if (options.rollback) {
    const previousSequence = options.rollback.previous_sequence ?? options.rollback.previousSequence;
    if (!Number.isInteger(previousSequence)) {
      throw new Error('rollback.previousSequence must be an integer');
    }
    manifest.rollback = {
      previous_sequence: previousSequence,
      reason: requiredString(options.rollback.reason, 'rollback.reason'),
    };
  }
  return manifest;
}

export function signUpdateManifest({ manifest, privateKey }) {
  if (!manifest || typeof manifest !== 'object') {
    throw new Error('manifest is required');
  }
  const keyObject = normalizeSigningKey(privateKey);
  const signature = crypto
    .sign(null, canonicalManifestBytes(manifest), keyObject)
    .toString('base64');
  return {
    ...manifest,
    signing: {
      ...manifest.signing,
      signature,
    },
  };
}

function requiredString(value, name) {
  if (typeof value !== 'string' || value.length === 0) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function requiredTimestamp(value, name) {
  requiredString(value, name);
  if (!Number.isFinite(Date.parse(value))) {
    throw new Error(`${name} must be an RFC 3339 timestamp`);
  }
  return value;
}

function requiredBytes(value, name) {
  if (Buffer.isBuffer(value)) {
    return value;
  }
  if (value instanceof Uint8Array) {
    return Buffer.from(value.buffer, value.byteOffset, value.byteLength);
  }
  if (value instanceof ArrayBuffer) {
    return Buffer.from(value);
  }
  throw new Error(`${name} must be a Buffer, Uint8Array, or ArrayBuffer`);
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key.startsWith('--') || !value) {
      throw new Error(`Invalid argument near ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

export async function resolveSigningKeyPem(keyPath, env = process.env) {
  if (keyPath) {
    return readFile(resolve(keyPath), 'utf8');
  }
  const pem = env[SIGNING_KEY_ENV];
  if (!pem) {
    throw new Error(`signing key is required: pass --key key.pem or set ${SIGNING_KEY_ENV}`);
  }
  return pem;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const options = parseArgs(process.argv.slice(2));
  if (!options.manifest) {
    throw new Error('--manifest is required');
  }
  if (!options.out) {
    throw new Error('--out is required');
  }
  const manifest = JSON.parse(await readFile(resolve(options.manifest), 'utf8'));
  const privateKey = await resolveSigningKeyPem(options.key);
  const signed = signUpdateManifest({ manifest, privateKey });
  const outPath = resolve(options.out);
  await mkdir(dirname(outPath), { recursive: true });
  await writeFile(outPath, `${JSON.stringify(signed, null, 2)}\n`);
  console.log(outPath);
}
