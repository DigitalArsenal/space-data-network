// Checks the release workflow's update signing key before anything is signed.
//
//   node deployment/release/check-update-signing-key.mjs --maturity <stable|beta> \
//     [--roots deployment/release/fleet-trust-roots.json] [--github-output "$GITHUB_OUTPUT"]
//
// Reads SDN_UPDATE_SIGNING_KEY_PEM and SDN_UPDATE_KEY_ID from the environment
// (the job's secrets) and never prints the key. It:
//   - FAILS a stable release that has no signing key: a stable release
//     without its self-upgrade feed is not a stable release. A beta without
//     the key still ships its binaries, with a warning, and no feed;
//   - derives the key id from the key (sha256 of the raw Ed25519 public key,
//     first 12 hex: update.RootKeyID) and fails if SDN_UPDATE_KEY_ID is unset
//     or differs — there is no fallback id;
//   - fails unless that id is in the fleet roots with this key's public half,
//     so CI cannot sign a feed no install trusts;
//   - fails for a key of the SEC-001 leaked identity
//     (sdn-server/internal/sec001/denylist.json).
// Writes available=true|false and key_id=<id> to --github-output.
import { createHash, createPrivateKey, createPublicKey } from 'node:crypto';
import { appendFile, readFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
export const DEFAULT_ROOTS_PATH = join(here, 'fleet-trust-roots.json');
export const SEC001_DENYLIST_PATH = join(here, '..', '..', 'sdn-server', 'internal', 'sec001', 'denylist.json');

// rawEd25519 is the raw 32-byte public key of an Ed25519 key object.
function rawEd25519(publicKey) {
  return publicKey.export({ type: 'spki', format: 'der' }).subarray(-32);
}

// rootKeyId is update.RootKeyID: sha256(raw public key), first 12 hex.
export function rootKeyId(publicKey) {
  return createHash('sha256').update(rawEd25519(publicKey)).digest('hex').slice(0, 12);
}

function publicKeyFromRoot(encoded) {
  const der = Buffer.from(String(encoded), 'base64');
  if (der.length === 32) {
    return createPublicKey({ key: Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), der]), format: 'der', type: 'spki' });
  }
  return createPublicKey({ key: der, format: 'der', type: 'spki' });
}

export async function loadSec001Keys(path = SEC001_DENYLIST_PATH) {
  const list = JSON.parse(await readFile(path, 'utf8'));
  return new Set((list.ed25519 || []).map((key) => String(key).toLowerCase()));
}

// checkUpdateSigningKey returns { available, keyId, warning } or throws.
export async function checkUpdateSigningKey({ pem, keyId, maturity, roots, denied }) {
  if (maturity !== 'stable' && maturity !== 'beta') {
    throw new Error(`--maturity must be stable or beta, not ${JSON.stringify(maturity)}`);
  }
  if (!pem || !String(pem).trim()) {
    if (maturity === 'stable') {
      throw new Error('SDN_UPDATE_SIGNING_KEY_PEM is not set: a stable release must ship its signed self-upgrade feed, so this release is stopped here. Set the secret, or cut a beta.');
    }
    return { available: false, warning: 'SDN_UPDATE_SIGNING_KEY_PEM is not set, so this beta ships no self-upgrade feed. Installed nodes will not see it as an update.' };
  }
  let privateKey;
  try {
    privateKey = createPrivateKey(String(pem));
  } catch {
    // The parser's message is not echoed: it could quote the secret.
    throw new Error('SDN_UPDATE_SIGNING_KEY_PEM is not a readable private key');
  }
  if (privateKey.asymmetricKeyType !== 'ed25519') {
    throw new Error(`SDN_UPDATE_SIGNING_KEY_PEM is a ${privateKey.asymmetricKeyType} key; update manifests are signed with Ed25519 only`);
  }
  const publicKey = createPublicKey(privateKey);
  const derived = rootKeyId(publicKey);
  const raw = rawEd25519(publicKey).toString('hex');
  if (denied.has(raw)) {
    throw new Error(`the update signing key (key id ${derived}) is a key of the SEC-001 leaked identity, which anyone can sign as; it can never sign a release`);
  }
  const named = String(keyId || '').trim();
  if (!named) {
    throw new Error(`SDN_UPDATE_KEY_ID is not set; the signing key's id is ${derived}. Set the secret to that id: there is no fallback key id.`);
  }
  if (named !== derived) {
    throw new Error(`SDN_UPDATE_KEY_ID is ${named}, but the signing key's id is ${derived}`);
  }
  if (!Object.hasOwn(roots, derived)) {
    throw new Error(`the signing key ${derived} is not an update root in fleet-trust-roots.json (${Object.keys(roots).join(', ') || 'none'}); installs would refuse every manifest it signs`);
  }
  if (!publicKeyFromRoot(roots[derived]).equals(publicKey)) {
    throw new Error(`fleet-trust-roots.json lists ${derived} with a different public key than the signing key's`);
  }
  for (const [id, encoded] of Object.entries(roots)) {
    const rootKey = publicKeyFromRoot(encoded);
    if (denied.has(rawEd25519(rootKey).toString('hex'))) {
      throw new Error(`fleet-trust-roots.json lists ${id}, a key of the SEC-001 leaked identity`);
    }
  }
  return { available: true, keyId: derived };
}

function parseArgs(argv) {
  const options = {};
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i];
    const value = argv[i + 1];
    if (!key?.startsWith('--') || value === undefined) {
      throw new Error(`invalid argument near ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = value;
  }
  return options;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const options = parseArgs(process.argv.slice(2));
  try {
    const roots = JSON.parse(await readFile(resolve(options.roots || DEFAULT_ROOTS_PATH), 'utf8'));
    const result = await checkUpdateSigningKey({
      pem: process.env.SDN_UPDATE_SIGNING_KEY_PEM,
      keyId: process.env.SDN_UPDATE_KEY_ID,
      maturity: options.maturity,
      roots,
      denied: await loadSec001Keys(),
    });
    if (result.warning) {
      console.log(`::warning title=No update feed::${result.warning}`);
    } else {
      console.log(`update signing key ${result.keyId} is a fleet root`);
    }
    if (options.githubOutput) {
      await appendFile(options.githubOutput, `available=${result.available}\nkey_id=${result.keyId || ''}\n`);
    }
  } catch (error) {
    console.log(`::error title=Update signing key::${error.message}`);
    process.exit(1);
  }
}
