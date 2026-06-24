import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { generateKeyPairSync, verify } from 'node:crypto';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { gzipSync } from 'node:zlib';
import { buildCliUpdatePayload } from './build-cli-update-payload.mjs';
import { extractBundleBytes } from './build-update-carrier.mjs';
import { publicKeyBase64 } from './sign-update-manifest.mjs';

const require = createRequire(import.meta.url);
const {
  canonicalManifestBytes,
  sha256Hex,
  verifyDownloadedUpdatePayload,
} = require('../../desktop/src/sdn-updater/manifest');

const cliPath = fileURLToPath(new URL('./build-cli-update-payload.mjs', import.meta.url));

async function fixtureArchive(root) {
  const archivePath = join(root, 'spacedatanetwork-1.2.3-linux-amd64.tar.gz');
  const archiveBytes = gzipSync(Buffer.from('tiny fixture cli bundle archive'));
  await writeFile(archivePath, archiveBytes);
  return { archivePath, archiveBytes };
}

test('buildCliUpdatePayload writes a signed manifest and carrier for the bundle archive', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-update-payload-'));
  const keys = generateKeyPairSync('ed25519');
  const { archivePath, archiveBytes } = await fixtureArchive(root);
  const outDir = join(root, 'dist', 'update');

  const result = await buildCliUpdatePayload({
    bundleArchive: archivePath,
    version: '1.2.3',
    sequence: 7,
    channel: 'beta',
    platform: 'linux',
    arch: 'amd64',
    keyId: 'sdn-test-root',
    privateKey: keys.privateKey,
    createdAt: '2026-06-10T00:00:00.000Z',
    outDir,
  });

  assert.equal(result.manifestPath, join(outDir, 'manifest.json'));
  assert.equal(result.carrierPath, join(outDir, 'update.wasm'));

  const manifest = JSON.parse(await readFile(result.manifestPath, 'utf8'));
  const wasmBytes = await readFile(result.carrierPath);
  assert.deepEqual(manifest, result.manifest);
  assert.equal(manifest.schema, 'org.spacedatanetwork.update.v1');
  assert.equal(manifest.update_id, 'cli-bundle-beta-linux-amd64-1.2.3');
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.sequence, 7);
  assert.equal(manifest.channel, 'beta');
  assert.deepEqual(manifest.target, { platform: 'linux', arch: 'amd64', kind: 'cli-bundle' });
  assert.equal(manifest.bundle.format, 'tar.gz');
  assert.equal(manifest.bundle.hash, sha256Hex(archiveBytes));
  assert.equal(manifest.bundle.size, archiveBytes.length);
  assert.equal(manifest.wasm.hash, sha256Hex(wasmBytes));
  assert.equal(manifest.created_at, '2026-06-10T00:00:00.000Z');
  assert.equal(manifest.expires_at, '2026-09-08T00:00:00.000Z');
  assert.equal(
    Date.parse(manifest.expires_at) - Date.parse(manifest.created_at),
    90 * 24 * 60 * 60 * 1000,
  );

  // Carrier embeds the exact archive bytes.
  assert.deepEqual(extractBundleBytes(wasmBytes), archiveBytes);

  // Signature verifies over the canonical manifest bytes.
  assert.equal(
    verify(null, canonicalManifestBytes(manifest), keys.publicKey, Buffer.from(manifest.signing.signature, 'base64')),
    true,
  );

  // Full payload passes the shared verifier against the embedded public key.
  const verified = verifyDownloadedUpdatePayload({
    manifest,
    wasmBytes,
    bundleBytes: archiveBytes,
    platform: 'linux',
    arch: 'amd64',
    currentSequence: 6,
    trustedRoots: { 'sdn-test-root': publicKeyBase64(keys.publicKey) },
    now: new Date('2026-06-11T00:00:00.000Z'),
  });
  assert.equal(verified.ok, true);
  assert.equal(verified.targetKind, 'cli-bundle');
  assert.equal(manifest.signing.public_key, publicKeyBase64(keys.publicKey));
});

test('buildCliUpdatePayload records Windows ZIP bundles as zip format', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-update-payload-zip-'));
  const keys = generateKeyPairSync('ed25519');
  const archivePath = join(root, 'spacedatanetwork-1.2.3-windows-amd64.zip');
  const archiveBytes = Buffer.from('tiny fixture windows cli bundle zip');
  await writeFile(archivePath, archiveBytes);
  const outDir = join(root, 'dist', 'update');

  const result = await buildCliUpdatePayload({
    bundleArchive: archivePath,
    version: '1.2.3',
    sequence: 7,
    channel: 'beta',
    platform: 'windows',
    arch: 'amd64',
    keyId: 'sdn-test-root',
    privateKey: keys.privateKey,
    createdAt: '2026-06-10T00:00:00.000Z',
    outDir,
  });

  const wasmBytes = await readFile(result.carrierPath);
  assert.equal(result.manifest.update_id, 'cli-bundle-beta-windows-amd64-1.2.3');
  assert.deepEqual(result.manifest.target, { platform: 'windows', arch: 'amd64', kind: 'cli-bundle' });
  assert.equal(result.manifest.bundle.format, 'zip');
  assert.equal(result.manifest.bundle.hash, sha256Hex(archiveBytes));
  assert.deepEqual(extractBundleBytes(wasmBytes), archiveBytes);
});

test('buildCliUpdatePayload records wrapped upstream Kubo provenance inside the SDN-signed manifest', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-update-payload-upstream-'));
  const keys = generateKeyPairSync('ed25519');
  const { archivePath } = await fixtureArchive(root);
  const outDir = join(root, 'dist', 'update');

  const result = await buildCliUpdatePayload({
    bundleArchive: archivePath,
    version: '1.2.3',
    sequence: 7,
    channel: 'beta',
    platform: 'linux',
    arch: 'amd64',
    keyId: 'sdn-test-root',
    privateKey: keys.privateKey,
    createdAt: '2026-06-10T00:00:00.000Z',
    outDir,
    upstreamKuboVersion: 'v0.35.0',
    upstreamKuboSource: 'ipfs/kubo',
  });

  assert.deepEqual(result.manifest.upstream, {
    kubo: {
      source: 'ipfs/kubo',
      version: 'v0.35.0',
    },
  });
  assert.equal(
    verify(null, canonicalManifestBytes(result.manifest), keys.publicKey, Buffer.from(result.manifest.signing.signature, 'base64')),
    true,
  );
});

test('buildCliUpdatePayload requires created-at and an integer sequence', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-update-payload-invalid-'));
  const keys = generateKeyPairSync('ed25519');
  const { archivePath } = await fixtureArchive(root);
  const base = {
    bundleArchive: archivePath,
    version: '1.2.3',
    sequence: 7,
    channel: 'beta',
    platform: 'linux',
    arch: 'amd64',
    keyId: 'sdn-test-root',
    privateKey: keys.privateKey,
    outDir: join(root, 'out'),
  };
  await assert.rejects(() => buildCliUpdatePayload(base), /createdAt is required/);
  await assert.rejects(
    () => buildCliUpdatePayload({ ...base, createdAt: 'not-a-date' }),
    /createdAt must be an RFC 3339 timestamp/,
  );
  await assert.rejects(
    () => buildCliUpdatePayload({ ...base, createdAt: '2026-06-10T00:00:00.000Z', sequence: '7.5' }),
    /sequence must be an integer/,
  );
});

test('CLI builds the payload end to end with a key file', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-update-payload-cli-'));
  const keys = generateKeyPairSync('ed25519');
  const { archivePath, archiveBytes } = await fixtureArchive(root);
  const keyPath = join(root, 'key.pem');
  await writeFile(keyPath, keys.privateKey.export({ type: 'pkcs8', format: 'pem' }));
  const outDir = join(root, 'dist', 'update');

  await runNode([
    cliPath,
    '--bundle-archive', archivePath,
    '--version', '1.2.3',
    '--sequence', '7',
    '--channel', 'beta',
    '--platform', 'darwin',
    '--arch', 'arm64',
    '--key-id', 'sdn-test-root',
    '--key', keyPath,
    '--created-at', '2026-06-10T00:00:00.000Z',
    '--out-dir', outDir,
  ]);

  const manifest = JSON.parse(await readFile(join(outDir, 'manifest.json'), 'utf8'));
  const wasmBytes = await readFile(join(outDir, 'update.wasm'));
  assert.equal(manifest.update_id, 'cli-bundle-beta-darwin-arm64-1.2.3');
  assert.deepEqual(manifest.target, { platform: 'darwin', arch: 'arm64', kind: 'cli-bundle' });
  assert.deepEqual(extractBundleBytes(wasmBytes), archiveBytes);
  assert.equal(
    verify(null, canonicalManifestBytes(manifest), keys.publicKey, Buffer.from(manifest.signing.signature, 'base64')),
    true,
  );
});

function runNode(args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => {
      stdout += chunk;
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('error', reject);
    child.on('close', (code) => {
      if (code === 0) {
        resolve(stdout);
        return;
      }
      reject(new Error(`node exited ${code}: ${stderr.trim()}`));
    });
  });
}
