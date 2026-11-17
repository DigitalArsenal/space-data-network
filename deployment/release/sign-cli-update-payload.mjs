// Signs the manifest of one CLI update payload in the job that holds the
// update signing key, over digests only:
//
//   node deployment/release/sign-cli-update-payload.mjs --payload-dir <dir> \
//     --version <v> --sequence <n> --channel release --platform <os> \
//     --arch <arch> --format <tar.gz|zip> --key-id <id> --created-at <time>
//
// <dir> holds update.wasm and bundle.json from build-cli-update-payload.mjs
// --carrier-out-dir, made in a job without the key. This reads bundle.json
// (sha256, size and format of the wrapped bundle, each checked for shape),
// hashes update.wasm, and writes <dir>/manifest.json. It opens no archive and
// runs no other program. Every other field of the manifest comes from the
// arguments. The key is SDN_UPDATE_SIGNING_KEY_PEM.
import { createPublicKey } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  buildUpdateManifest,
  normalizeSigningKey,
  resolveSigningKeyPem,
  signUpdateManifest,
} from './sign-update-manifest.mjs';

export const CLI_BUNDLE_KIND = 'cli-bundle';
export const EXPIRES_AFTER_DAYS = 90;

export async function signCliUpdatePayload(options) {
  const payloadDir = resolve(required(options.payloadDir, 'payloadDir'));
  const version = required(options.version, 'version');
  const channel = required(options.channel, 'channel');
  const platform = required(options.platform, 'platform');
  const arch = required(options.arch, 'arch');
  const format = required(options.format, 'format');
  const keyId = required(options.keyId, 'keyId');
  const createdAt = required(options.createdAt, 'createdAt');
  const createdAtMs = Date.parse(createdAt);
  if (!Number.isFinite(createdAtMs)) {
    throw new Error('createdAt must be an RFC 3339 timestamp');
  }
  const sequence = typeof options.sequence === 'string' ? Number(options.sequence) : options.sequence;
  if (!Number.isSafeInteger(sequence) || sequence < 1) {
    throw new Error('sequence must be a positive integer');
  }

  const recorded = JSON.parse(await readFile(join(payloadDir, 'bundle.json'), 'utf8'));
  if (!recorded || typeof recorded !== 'object' || Array.isArray(recorded) ||
      Object.keys(recorded).sort().join(',') !== 'format,hash,size') {
    throw new Error('bundle.json must hold exactly format, hash and size');
  }
  if (recorded.format !== format) {
    throw new Error(`bundle.json names format ${JSON.stringify(recorded.format)}, expected ${format}`);
  }
  const wasmBytes = await readFile(join(payloadDir, 'update.wasm'));

  const privateKey = normalizeSigningKey(options.privateKey ?? await resolveSigningKeyPem(options.key));
  const manifest = signUpdateManifest({
    manifest: buildUpdateManifest({
      updateId: `${CLI_BUNDLE_KIND}-${channel}-${platform}-${arch}-${version}`,
      version,
      sequence,
      channel,
      platform,
      arch,
      kind: CLI_BUNDLE_KIND,
      format,
      bundleDigest: { hash: recorded.hash, size: recorded.size },
      wasmBytes,
      keyId,
      publicKey: createPublicKey(privateKey),
      createdAt,
      expiresAt: new Date(createdAtMs + EXPIRES_AFTER_DAYS * 24 * 60 * 60 * 1000).toISOString(),
    }),
    privateKey,
  });
  const manifestPath = join(payloadDir, 'manifest.json');
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return { manifest, manifestPath };
}

function required(value, name) {
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
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

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const result = await signCliUpdatePayload(parseArgs(process.argv.slice(2)));
  console.log(JSON.stringify({ update_id: result.manifest.update_id, manifest: result.manifestPath }, null, 2));
}
