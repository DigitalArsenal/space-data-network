import { spawn } from 'node:child_process';
import { createPublicKey } from 'node:crypto';
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
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

  const bundleFormat = bundleFormatForArchive(bundleArchive);
  const bundleBytes = await payloadArchiveBytes(resolve(bundleArchive), bundleFormat);
  const wasmBytes = buildCarrier(bundleBytes);
  const expiresAt = new Date(createdAtMs + EXPIRES_AFTER_DAYS * 24 * 60 * 60 * 1000).toISOString();
  const updateId = options.updateId || `${CLI_BUNDLE_KIND}-${channel}-${platform}-${arch}-${version}`;

  const unsignedManifest = buildUpdateManifest({
    updateId,
    version,
    sequence,
    channel,
    platform,
    arch,
    kind: CLI_BUNDLE_KIND,
    format: bundleFormat,
    bundleBytes,
    wasmBytes,
    keyId,
    publicKey,
    createdAt,
    expiresAt,
    rollback: options.rollback,
  });
  if (options.upstreamKuboVersion || options.upstreamKuboSource) {
    unsignedManifest.upstream = {
      kubo: {
        source: options.upstreamKuboSource || 'ipfs/kubo',
        version: required(options.upstreamKuboVersion, 'upstreamKuboVersion'),
      },
    };
  }

  const manifest = signUpdateManifest({
    manifest: unsignedManifest,
    privateKey,
  });

  await mkdir(outDir, { recursive: true });
  const manifestPath = join(outDir, 'manifest.json');
  const carrierPath = join(outDir, 'update.wasm');
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(carrierPath, wasmBytes);

  return { manifest, manifestPath, carrierPath };
}

// THE PAYLOAD CARRIES NO trust/.
//
// The CLI archive an operator downloads ships trust/update-roots.json: a fresh
// install needs its update roots. An UPDATE must not carry them. Roots live
// outside the swapped payload, so nothing but a signed trust_roots rotation
// can move them, and every node refuses a payload with a trust/ entry at its
// bundle root ("update bundle must not contain protected entry", internal/
// update/apply.go). The release feed used to wrap the downloadable archive
// unchanged, so every payload in it would have been refused by every node.
// The payload is that archive with trust/ removed and nothing else changed.

// The bundle root is where manifest.json sits: the archive root or the single
// wrapper directory the bundle is archived under.
const manifestEntryPattern = /^(?:\.\/)?([^/]+\/)?manifest\.json$/;

export async function archiveEntries(archivePath, format) {
  if (format === 'tar.gz') {
    return (await capture('tar', ['-tzf', archivePath])).split('\n').filter(Boolean);
  }
  if (format === 'zip') {
    return (await capture('unzip', ['-Z1', archivePath])).split('\n').filter(Boolean);
  }
  throw new Error(`cannot list a ${format} CLI bundle archive`);
}

// trustDirs names the trust/ directories at a bundle root in entries.
export function trustDirs(entries) {
  const roots = new Set(entries.map((name) => manifestEntryPattern.exec(name)).filter(Boolean).map((m) => m[1] || ''));
  const dirs = new Set();
  for (const name of entries) {
    const normalized = name.replace(/^\.\//, '');
    for (const root of roots) {
      if (normalized === `${root}trust` || normalized.startsWith(`${root}trust/`)) {
        dirs.add(`${root}trust`);
      }
    }
  }
  return [...dirs].sort();
}

// payloadArchiveBytes is the archive to wrap: the release archive itself when
// it has no trust/, else a repack of it without trust/.
export async function payloadArchiveBytes(archivePath, format) {
  if (format === 'tar.zst') {
    // Not produced by the release lane; listing it needs a zstd-capable tar.
    return readFile(archivePath);
  }
  const trust = trustDirs(await archiveEntries(archivePath, format));
  if (trust.length === 0) {
    return readFile(archivePath);
  }
  const work = await mkdtemp(join(tmpdir(), 'sdn-cli-payload-'));
  try {
    const tree = join(work, 'tree');
    await mkdir(tree);
    if (format === 'tar.gz') {
      await capture('tar', ['-xzf', archivePath, '-C', tree]);
    } else {
      await capture('unzip', ['-q', archivePath, '-d', tree]);
    }
    for (const dir of trust) {
      await rm(join(tree, dir), { recursive: true, force: true });
    }
    const top = (await readdir(tree)).sort();
    const repacked = join(work, `payload.${format}`);
    if (format === 'tar.gz') {
      await capture('tar', ['-czf', repacked, '-C', tree, ...top]);
    } else {
      await capture('zip', ['-qry', repacked, ...top], { cwd: tree });
    }
    const left = trustDirs(await archiveEntries(repacked, format));
    if (left.length > 0) {
      throw new Error(`the update payload still carries ${left.join(', ')}`);
    }
    return await readFile(repacked);
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}

function capture(command, args, options = {}) {
  return new Promise((resolvePromise, reject) => {
    // COPYFILE_DISABLE: macOS tar would otherwise add AppleDouble ._ entries.
    const child = spawn(command, args, { ...options, env: { ...process.env, COPYFILE_DISABLE: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', (code) => {
      if (code === 0) {
        resolvePromise(stdout);
        return;
      }
      reject(new Error(`${command} ${args.join(' ')} exited ${code}: ${stderr.trim()}`));
    });
  });
}

export function bundleFormatForArchive(archivePath) {
  const name = String(archivePath).toLowerCase();
  if (name.endsWith('.tar.gz')) {
    return 'tar.gz';
  }
  if (name.endsWith('.tar.zst')) {
    return 'tar.zst';
  }
  if (name.endsWith('.zip')) {
    return 'zip';
  }
  throw new Error(`unsupported CLI bundle archive format: ${archivePath}`);
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
