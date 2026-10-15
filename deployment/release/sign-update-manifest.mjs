import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const crypto = require('crypto');
// Shared with the verification side so signing and verification always agree
// on the canonical manifest bytes (sorted keys, signing.signature omitted).
const { canonicalManifestBytes, sha256Hex } = require('./sdn-updater/manifest');

export const MANIFEST_SCHEMA = 'org.spacedatanetwork.update.v1';

// SEC-001: the release signing key id comes from the ONE anchors file
// (deployment/identity/anchors.json updateRoots: the fleet distribution key),
// never a hand-kept default. The pre-rotation default was host-01's node key,
// whose seed was published; it is on the anchors denylist.
const ANCHORS_URL = new URL('../identity/anchors.json', import.meta.url);

function readAnchors() {
  try {
    return JSON.parse(require('fs').readFileSync(ANCHORS_URL, 'utf8'));
  } catch {
    return null; // a lane copied outside the repository has no anchors file
  }
}

/** The single update root anchors.json names, or '' when it names none or several. */
export function anchoredUpdateRootKeyId(anchors = readAnchors()) {
  const ids = Object.keys(anchors?.updateRoots || {});
  return ids.length === 1 ? ids[0] : '';
}

/**
 * The key id a release is signed under: --key-id, else the anchored root.
 * Throws for a denylisted key id ALWAYS, and for a missing or placeholder one
 * unless this is a dry run (which signs nothing).
 *
 * SEC-001 (owner, 2026-10-15): the fleet's update roots move OUT-OF-BAND — each
 * node's trust/update-roots.json is replaced directly (deployment/identity/
 * ROTATION.md). Nothing is ever signed with the published root again, so there
 * is no in-band "root migration" exception: a release under a denylisted key
 * id is refused whatever it carries.
 */
export function resolveReleaseKeyId(explicit, { dryRun = false, anchors = readAnchors() } = {}) {
  const keyId = String(explicit || anchoredUpdateRootKeyId(anchors) || '').trim();
  const denied = new Set((anchors?.denylist?.updateRootKeyIds || []).map((id) => id.toLowerCase()));
  if (keyId && denied.has(keyId.toLowerCase())) {
    throw new Error(`REFUSED: update key id ${keyId} is denylisted (SEC-001: its seed was published). Sign with the fleet update root in deployment/identity/anchors.json.`);
  }
  if (!dryRun && (!keyId || keyId.startsWith('PLACEHOLDER-'))) {
    throw new Error('REFUSED: no release signing key id: deployment/identity/anchors.json updateRoots is still a placeholder (SEC-001 rotation pending) — pass --key-id or land the owner\'s anchors.');
  }
  return keyId || 'PLACEHOLDER-SEC001-UPDATE-ROOT-KEY-ID';
}

const ED25519_SPKI_PREFIX = Buffer.from('302a300506032b6570032100', 'hex');

/**
 * A release's trust_roots (--trust-roots) must be EXACTLY the committed
 * anchors.json updateRoots (verifier S7): same key ids, same keys, each key id
 * the first 12 hex of sha256 over its Ed25519 key, none denylisted or a
 * placeholder. A typo or a swapped file would otherwise move every node that
 * applies the release to the wrong root.
 */
export function assertTrustRootsMatchAnchors(trustRoots, anchors = readAnchors()) {
  const entries = Object.entries(trustRoots || {});
  if (entries.length === 0) throw new Error('REFUSED: --trust-roots names no root.');
  const anchored = anchors?.updateRoots || {};
  const denied = new Set((anchors?.denylist?.updateRootKeyIds || []).map((id) => id.toLowerCase()));
  for (const [keyId, encoded] of entries) {
    if (keyId.startsWith('PLACEHOLDER-') || String(encoded).startsWith('PLACEHOLDER-')) {
      throw new Error(`REFUSED: --trust-roots names a placeholder root (${keyId}).`);
    }
    const der = Buffer.from(String(encoded), 'base64');
    if (der.length !== 44 || !der.subarray(0, 12).equals(ED25519_SPKI_PREFIX)) {
      throw new Error(`REFUSED: --trust-roots ${keyId} is not a base64 Ed25519 SPKI.`);
    }
    const computed = crypto.createHash('sha256').update(der.subarray(12)).digest('hex').slice(0, 12);
    if (computed !== keyId) throw new Error(`REFUSED: --trust-roots lists key ${computed} under key id ${keyId}.`);
    if (denied.has(keyId.toLowerCase())) throw new Error(`REFUSED: --trust-roots names denylisted root ${keyId} (SEC-001).`);
  }
  const want = JSON.stringify(Object.entries(anchored).sort());
  const got = JSON.stringify(entries.sort());
  if (want !== got) {
    throw new Error(
      'REFUSED: --trust-roots must equal deployment/identity/anchors.json updateRoots exactly ' +
        `(anchored: ${Object.keys(anchored).join(', ') || 'none'}; given: ${entries.map(([id]) => id).join(', ')}).`,
    );
  }
}
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
  // Optional provenance, carried INSIDE the signed document (canonicalization
  // covers the whole generic JSON doc on both the Go and JS verification
  // sides, so adding a field is signature-compatible and cannot be attached
  // after signing). It exists because on 2026-08-09 the only way to tell the
  // feed's artifact from the operator's build was to download and unpack the
  // carrier; echoing the source binary's sha256 and size makes that a string
  // comparison against a published field.
  if (options.provenance) {
    if (typeof options.provenance !== 'object' || Array.isArray(options.provenance)) {
      throw new Error('provenance must be an object');
    }
    manifest.provenance = { ...options.provenance };
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

// ---------------------------------------------------------------------------
// NODE-SIGNED RELEASES — the lane that actually ships.
//
// OWNER RULING 2026-07-30: the publisher key IS the node key, trusted through
// the Adversarial-Security bond on its derived chain addresses. That key is
// encrypted at rest with a machine-derived key and CANNOT leave host-01, while
// build-locally-ship-binaries says the release is built here and never there.
// So the bytes travel to the key: this function POSTs the manifest DOCUMENT to
// the node's content-bound endpoint, the node canonicalizes and hashes it
// itself, signs the domain-separated statement, and returns a detached
// signature. `signUpdateManifest` above (local Ed25519 key) remains for tests
// and for the legacy form; it is not the lane for a shipped release.

export const UPDATE_MANIFEST_STATEMENT_DOMAIN = 'SDN-UPDATE-MANIFEST-V1';
export const UPDATE_MANIFEST_SIGNING_PATH = '/api/v1/admin/updates/sign-manifest';

/**
 * Sign a manifest with the node's bonded key.
 *
 * The manifest is submitted WITH signing.statement_domain already set and
 * WITHOUT signing.signature. That ordering is not cosmetic: the domain lives
 * inside the canonical bytes, so it is covered by the signature it authorizes.
 * If the caller could add it afterwards, it would be an unsigned assertion
 * about a signed document.
 *
 * @param {object}  args.manifest  unsigned manifest (buildUpdateManifest output)
 * @param {string}  args.nodeUrl   e.g. https://sdn.spaceaware.io
 * @param {string}  args.token     admin session bearer token
 * @param {Function} [args.fetchImpl] injected for tests
 */
export async function signUpdateManifestWithNode({ manifest, nodeUrl, token, fetchImpl = fetch }) {
  if (!manifest || typeof manifest !== 'object') {
    throw new Error('manifest is required');
  }
  const base = new URL(requiredString(nodeUrl, 'nodeUrl'));
  if (base.protocol !== 'https:' && base.hostname !== 'localhost' && base.hostname !== '127.0.0.1') {
    // A bearer token for the bonded publisher key must not cross a cleartext
    // hop. Loopback is exempt because there is no hop.
    throw new Error('node URL must use HTTPS (or loopback) to carry an admin session token');
  }
  requiredString(token, 'token');

  const submitted = {
    ...manifest,
    signing: {
      ...manifest.signing,
      statement_domain: UPDATE_MANIFEST_STATEMENT_DOMAIN,
    },
  };
  delete submitted.signing.signature;

  const endpoint = new URL(UPDATE_MANIFEST_SIGNING_PATH, base).toString();
  const response = await fetchImpl(endpoint, {
    method: 'POST',
    headers: {
      // NOT application/json: the endpoint's digest guard rejects the digest
      // header/query positions, and the body is a manifest document. The
      // content type is declared plainly so a proxy cannot re-encode it.
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(submitted),
  });

  const text = await response.text();
  if (!response.ok) {
    throw new Error(`node refused to sign the update manifest (${response.status}): ${text.slice(0, 400)}`);
  }

  let result;
  try {
    result = JSON.parse(text);
  } catch (error) {
    throw new Error(`node signing response was not JSON: ${error.message}`);
  }
  if (result.statement_domain !== UPDATE_MANIFEST_STATEMENT_DOMAIN) {
    throw new Error(`node signed under an unexpected statement domain: ${result.statement_domain}`);
  }
  if (typeof result.signature !== 'string' || result.signature.length === 0) {
    throw new Error('node signing response carried no signature');
  }

  const signed = {
    ...submitted,
    signing: { ...submitted.signing, signature: result.signature },
  };

  // VERIFY BEFORE RETURNING. The producer must never publish a manifest it has
  // not itself confirmed verifiable: a release that fails at the client is
  // indistinguishable, from the fleet's side, from an outage. This re-derives
  // the statement locally and checks the signature against the key the node
  // reported, so a canonicalization drift between producer and node is caught
  // here rather than on every box in the cluster.
  assertNodeSignatureVerifies(signed, result.public_key);

  return { manifest: signed, signing: result };
}

function assertNodeSignatureVerifies(signed, publicKeyB64) {
  if (typeof publicKeyB64 !== 'string' || publicKeyB64.length === 0) {
    throw new Error('node signing response carried no public key to verify against');
  }
  const canonical = canonicalManifestBytes(signed);
  const statement = Buffer.concat([
    Buffer.from(UPDATE_MANIFEST_STATEMENT_DOMAIN, 'ascii'),
    Buffer.from([0x00]),
    Buffer.from(sha256Hex(canonical), 'hex'),
  ]);
  const publicKey = crypto.createPublicKey({
    key: Buffer.from(publicKeyB64, 'base64'),
    type: 'spki',
    format: 'der',
  });
  const ok = crypto.verify(null, statement, publicKey, Buffer.from(signed.signing.signature, 'base64'));
  if (!ok) {
    throw new Error(
      'the signature returned by the node does not verify over the manifest we submitted — ' +
      'producer and node disagree about the canonical bytes; do NOT publish this release'
    );
  }
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
