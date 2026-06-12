import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { generateKeyPairSync, verify } from 'node:crypto';
import { mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import {
  SIGNING_KEY_ENV,
  buildUpdateManifest,
  publicKeyBase64,
  signUpdateManifest,
} from './sign-update-manifest.mjs';

const require = createRequire(import.meta.url);
const { canonicalManifestBytes, sha256Hex } = require('../../desktop/src/sdn-updater/manifest');

const cliPath = fileURLToPath(new URL('./sign-update-manifest.mjs', import.meta.url));

function fixtureManifest(keys) {
  const bundleBytes = Buffer.from('fixture bundle bytes');
  const wasmBytes = Buffer.from('fixture carrier bytes');
  const manifest = buildUpdateManifest({
    updateId: 'cli-bundle-beta-linux-amd64-1.2.3',
    version: '1.2.3',
    sequence: 7,
    channel: 'beta',
    platform: 'linux',
    arch: 'amd64',
    kind: 'cli-bundle',
    bundleBytes,
    wasmBytes,
    keyId: 'sdn-test-root',
    publicKey: keys.publicKey,
    createdAt: '2026-06-10T00:00:00.000Z',
    expiresAt: '2026-09-08T00:00:00.000Z',
  });
  return { manifest, bundleBytes, wasmBytes };
}

test('buildUpdateManifest computes hashes, sizes, and schema fields', () => {
  const keys = generateKeyPairSync('ed25519');
  const { manifest, bundleBytes, wasmBytes } = fixtureManifest(keys);

  assert.equal(manifest.schema, 'org.spacedatanetwork.update.v1');
  assert.equal(manifest.update_id, 'cli-bundle-beta-linux-amd64-1.2.3');
  assert.equal(manifest.sequence, 7);
  assert.deepEqual(manifest.target, { platform: 'linux', arch: 'amd64', kind: 'cli-bundle' });
  assert.equal(manifest.bundle.hash, sha256Hex(bundleBytes));
  assert.equal(manifest.bundle.size, bundleBytes.length);
  assert.equal(manifest.bundle.format, 'tar.gz');
  assert.equal(manifest.wasm.hash, sha256Hex(wasmBytes));
  assert.equal(manifest.signing.key_id, 'sdn-test-root');
  assert.equal(manifest.signing.algorithm, 'Ed25519');
  assert.equal(manifest.signing.public_key, publicKeyBase64(keys.publicKey));
  assert.equal(manifest.signing.signature, undefined);
});

test('signUpdateManifest signature verifies over canonical manifest bytes', () => {
  const keys = generateKeyPairSync('ed25519');
  const { manifest } = fixtureManifest(keys);
  const signed = signUpdateManifest({ manifest, privateKey: keys.privateKey });

  assert.equal(typeof signed.signing.signature, 'string');
  const valid = verify(
    null,
    canonicalManifestBytes(signed),
    keys.publicKey,
    Buffer.from(signed.signing.signature, 'base64'),
  );
  assert.equal(valid, true);

  const tampered = { ...signed, version: '9.9.9' };
  const tamperedValid = verify(
    null,
    canonicalManifestBytes(tampered),
    keys.publicKey,
    Buffer.from(tampered.signing.signature, 'base64'),
  );
  assert.equal(tamperedValid, false);
});

test('signUpdateManifest accepts a PEM string and rejects non-Ed25519 keys', () => {
  const keys = generateKeyPairSync('ed25519');
  const { manifest } = fixtureManifest(keys);
  const pem = keys.privateKey.export({ type: 'pkcs8', format: 'pem' });
  const signed = signUpdateManifest({ manifest, privateKey: pem });
  assert.equal(
    verify(null, canonicalManifestBytes(signed), keys.publicKey, Buffer.from(signed.signing.signature, 'base64')),
    true,
  );

  const rsa = generateKeyPairSync('rsa', { modulusLength: 2048 });
  assert.throws(
    () => signUpdateManifest({ manifest, privateKey: rsa.privateKey }),
    /Ed25519 private key/,
  );
});

test('signed manifest passes the desktop verifier and rollback fields round-trip', () => {
  const keys = generateKeyPairSync('ed25519');
  const { validateUpdateManifest } = require('../../desktop/src/sdn-updater/manifest');
  const { manifest, bundleBytes } = fixtureManifest(keys);
  const rollback = buildUpdateManifest({
    updateId: manifest.update_id,
    version: manifest.version,
    sequence: manifest.sequence,
    channel: manifest.channel,
    platform: manifest.target.platform,
    arch: manifest.target.arch,
    kind: manifest.target.kind,
    bundleBytes,
    wasmBytes: Buffer.from('fixture carrier bytes'),
    keyId: manifest.signing.key_id,
    createdAt: manifest.created_at,
    expiresAt: manifest.expires_at,
    rollback: { previousSequence: 5, reason: 'bad release' },
  });
  assert.deepEqual(rollback.rollback, { previous_sequence: 5, reason: 'bad release' });

  const signed = signUpdateManifest({ manifest, privateKey: keys.privateKey });
  const result = validateUpdateManifest(signed, {
    platform: 'linux',
    arch: 'amd64',
    currentSequence: 6,
    bundleHash: signed.bundle.hash,
    trustedRoots: { 'sdn-test-root': publicKeyBase64(keys.publicKey) },
    now: new Date('2026-06-11T00:00:00.000Z'),
  });
  assert.equal(result.ok, true);
  assert.equal(result.targetKind, 'cli-bundle');
});

test('CLI signs an unsigned manifest using --key and env fallback', async () => {
  const keys = generateKeyPairSync('ed25519');
  const { manifest } = fixtureManifest(keys);
  const root = await mkdtemp(join(tmpdir(), 'sdn-sign-manifest-'));
  const unsignedPath = join(root, 'unsigned.json');
  const keyPath = join(root, 'key.pem');
  const pem = keys.privateKey.export({ type: 'pkcs8', format: 'pem' });
  await writeFile(unsignedPath, JSON.stringify(manifest, null, 2));
  await writeFile(keyPath, pem);

  const outPath = join(root, 'manifest.json');
  await runNode([cliPath, '--manifest', unsignedPath, '--key', keyPath, '--out', outPath]);
  const signed = JSON.parse(await readFile(outPath, 'utf8'));
  assert.equal(
    verify(null, canonicalManifestBytes(signed), keys.publicKey, Buffer.from(signed.signing.signature, 'base64')),
    true,
  );

  const envOutPath = join(root, 'manifest-env.json');
  await runNode([cliPath, '--manifest', unsignedPath, '--out', envOutPath], {
    env: { ...process.env, [SIGNING_KEY_ENV]: pem },
  });
  const envSigned = JSON.parse(await readFile(envOutPath, 'utf8'));
  assert.equal(envSigned.signing.signature, signed.signing.signature);
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
