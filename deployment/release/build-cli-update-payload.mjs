import { createPublicKey } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildCarrier } from './build-update-carrier.mjs';
import {
  buildUpdateManifest,
  normalizeSigningKey,
  resolveSigningKeyPem,
  signUpdateManifest,
} from './sign-update-manifest.mjs';

// Assembles the signed update payload for the self-contained CLI bundle lane:
// the inert WASM carrier wrapping the bundle archive, plus the signed
// manifest the Go updater (sdn-server/internal/update) verifies before any
// staged swap.
export const CLI_BUNDLE_KIND = 'cli-bundle';
export const CLI_BUNDLE_FORMAT = 'tar.gz';
export const EXPIRES_AFTER_DAYS = 90;

export async function buildCliUpdatePayload(options) {
  const bundleArchive = required(options.bundleArchive, 'bundleArchive');
  const version = required(options.version, 'version');
  const channel = required(options.channel, 'channel');
  const platform = required(options.platform, 'platform');
  const arch = required(options.arch, 'arch');
  const keyId = required(options.keyId, 'keyId');
  const outDir = resolve(required(options.outDir, 'outDir'));
  const createdAt = required(options.createdAt, 'createdAt');
  const createdAtMs = Date.parse(createdAt);
  if (!Number.isFinite(createdAtMs)) {
    throw new Error('createdAt must be an RFC 3339 timestamp');
  }
  const sequence = typeof options.sequence === 'string' ? Number(options.sequence) : options.sequence;
  if (!Number.isInteger(sequence)) {
    throw new Error('sequence must be an integer');
  }

  const privateKey = normalizeSigningKey(
    options.privateKey ?? await resolveSigningKeyPem(options.key),
  );
  const publicKey = createPublicKey(privateKey);

  const bundleBytes = await readFile(resolve(bundleArchive));
  const wasmBytes = buildCarrier(bundleBytes);
  const expiresAt = new Date(createdAtMs + EXPIRES_AFTER_DAYS * 24 * 60 * 60 * 1000).toISOString();
  const updateId = options.updateId || `${CLI_BUNDLE_KIND}-${channel}-${platform}-${arch}-${version}`;

  const manifest = signUpdateManifest({
    manifest: buildUpdateManifest({
      updateId,
      version,
      sequence,
      channel,
      platform,
      arch,
      kind: CLI_BUNDLE_KIND,
      format: CLI_BUNDLE_FORMAT,
      bundleBytes,
      wasmBytes,
      keyId,
      publicKey,
      createdAt,
      expiresAt,
      rollback: options.rollback,
    }),
    privateKey,
  });

  await mkdir(outDir, { recursive: true });
  const manifestPath = join(outDir, 'manifest.json');
  const carrierPath = join(outDir, 'update.wasm');
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(carrierPath, wasmBytes);

  return { manifest, manifestPath, carrierPath };
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
  const result = await buildCliUpdatePayload(parseArgs(process.argv.slice(2)));
  console.log(JSON.stringify({
    update_id: result.manifest.update_id,
    manifest: result.manifestPath,
    carrier: result.carrierPath,
  }, null, 2));
}
