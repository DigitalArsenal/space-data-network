// The update payload never carries trust/.
//
// The downloadable CLI archive ships trust/update-roots.json (a fresh install
// needs its roots). Every node refuses an update payload with trust/ at its
// bundle root, so a payload that wrapped the archive unchanged — as the
// release feed did — would be refused by every node. These tests build the
// payload from a release-shaped archive that carries trust/ and check what
// the carrier wraps.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { buildCliUpdatePayload } from './build-cli-update-payload.mjs';
import { extractBundleBytes } from './build-update-carrier.mjs';

const require = createRequire(import.meta.url);
const { sha256Hex } = require('./sdn-updater/manifest');

function run(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, env: { ...process.env, COPYFILE_DISABLE: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', (code) => (code === 0 ? resolve(stdout) : reject(new Error(`${command} exited ${code}: ${stderr.trim()}`))));
  });
}

async function releaseShapedTree(root, name) {
  const tree = join(root, 'tree');
  const bundle = join(tree, name);
  for (const dir of ['bin', 'runtime/sdn', 'runtime/ui/sdn', 'runtime/modules', 'trust']) {
    await mkdir(join(bundle, dir), { recursive: true });
  }
  await writeFile(join(bundle, 'bin', 'spacedatanetwork'), '#!/bin/sh\nexec "$0/../../runtime/sdn/spacedatanetwork" "$@"\n', { mode: 0o755 });
  await writeFile(join(bundle, 'runtime', 'sdn', 'spacedatanetwork'), 'daemon', { mode: 0o755 });
  await writeFile(join(bundle, 'runtime', 'ui', 'sdn', 'index.html'), '<!doctype html>');
  await writeFile(join(bundle, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(bundle, 'trust', 'update-roots.json'), '{"12cb25de07c0":"MCowBQYDK2VwAyEAi0cVzY4ICSRzddR7HYHKgEPXmXXFh1uvFizfMb/V5ZM="}\n');
  await writeFile(join(bundle, 'manifest.json'), '{"schema":"org.spacedatanetwork.bundle.v1","version":"1.2.3"}\n');
  await writeFile(join(bundle, 'checksums.txt'), 'x  bin/spacedatanetwork\n');
  return tree;
}

async function buildPayload(root, archivePath, platform) {
  const keys = generateKeyPairSync('ed25519');
  const result = await buildCliUpdatePayload({
    bundleArchive: archivePath,
    version: '1.2.3',
    sequence: 7,
    channel: 'release',
    platform,
    arch: 'amd64',
    keyId: 'sdn-test-root',
    privateKey: keys.privateKey,
    createdAt: '2026-11-15T00:00:00.000Z',
    outDir: join(root, 'out'),
  });
  const carrier = await readFile(result.carrierPath);
  const payload = extractBundleBytes(carrier);
  assert.equal(result.manifest.bundle.hash, sha256Hex(payload), 'the manifest signs the payload the carrier wraps');
  assert.equal(result.manifest.bundle.size, payload.length);
  return payload;
}

test('a tar.gz update payload carries no trust/, and the release archive keeps it', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-payload-trust-tgz-'));
  const name = 'spacedatanetwork-1.2.3-linux-amd64';
  const tree = await releaseShapedTree(root, name);
  const archivePath = join(root, `${name}.tar.gz`);
  await run('tar', ['-czf', archivePath, '-C', tree, name]);
  const released = await readFile(archivePath);

  const payload = await buildPayload(root, archivePath, 'linux');
  const payloadPath = join(root, 'payload.tar.gz');
  await writeFile(payloadPath, payload);
  const entries = (await run('tar', ['-tzf', payloadPath])).split('\n').filter(Boolean).map((e) => e.replace(/\/$/, ''));
  assert.deepEqual(entries.filter((e) => /(^|\/)trust(\/|$)/.test(e)), [], `payload entries: ${entries.join(', ')}`);
  for (const want of [`${name}/manifest.json`, `${name}/bin/spacedatanetwork`, `${name}/runtime/sdn/spacedatanetwork`, `${name}/checksums.txt`]) {
    assert.ok(entries.includes(want), `payload lost ${want}: ${entries.join(', ')}`);
  }
  const archiveEntries = (await run('tar', ['-tzf', archivePath])).split('\n');
  assert.ok(archiveEntries.some((e) => e.startsWith(`${name}/trust/update-roots.json`)), 'the downloadable archive still carries trust/');
  assert.deepEqual(await readFile(archivePath), released, 'the downloadable archive is not modified');
});

test('a zip update payload carries no trust/', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-payload-trust-zip-'));
  const name = 'spacedatanetwork-1.2.3-windows-amd64';
  const tree = await releaseShapedTree(root, name);
  const archivePath = join(root, `${name}.zip`);
  await run('zip', ['-qry', archivePath, name], { cwd: tree });

  const payload = await buildPayload(root, archivePath, 'windows');
  const payloadPath = join(root, 'payload.zip');
  await writeFile(payloadPath, payload);
  const entries = (await run('unzip', ['-Z1', payloadPath])).split('\n').filter(Boolean).map((e) => e.replace(/\/$/, ''));
  assert.deepEqual(entries.filter((e) => /(^|\/)trust(\/|$)/.test(e)), [], `payload entries: ${entries.join(', ')}`);
  assert.ok(entries.includes(`${name}/runtime/sdn/spacedatanetwork`), `payload lost the daemon: ${entries.join(', ')}`);
});
