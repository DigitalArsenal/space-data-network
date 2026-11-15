// The update roots the release lane signs for, and the checks the release
// workflow runs on its signing key and on the feed it builds.
//
// SECURITY FINDING (2026-11-15): fleet-trust-roots.json shipped one root,
// d4a971a7e534, whose raw key 0d80e1fd…fc8c is the SEC-001 leaked node key
// (derived from a published seed). It is replaced by the demo root.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { buildCliUpdatePayload } from './build-cli-update-payload.mjs';
import { buildCarrier } from './build-update-carrier.mjs';
import { checkUpdateSigningKey, loadSec001Keys, rootKeyId } from './check-update-signing-key.mjs';
import { buildUpdateManifest, signUpdateManifest } from './sign-update-manifest.mjs';
import { verifyCliUpdateFeed } from './verify-cli-update-feed.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '..', '..');
const DEMO_ROOT = { '12cb25de07c0': 'MCowBQYDK2VwAyEAi0cVzY4ICSRzddR7HYHKgEPXmXXFh1uvFizfMb/V5ZM=' };
const LEAKED_ID = 'd4a971a7e534';
const LEAKED_SPKI = 'MCowBQYDK2VwAyEADYDh/V+aTjTf3zag4VK9maZc//i8xsqydXtISuRC/Iw=';

function spkiBase64(publicKey) {
  return publicKey.export({ type: 'spki', format: 'der' }).toString('base64');
}

function pemOf(privateKey) {
  return privateKey.export({ type: 'pkcs8', format: 'pem' });
}

test('fleet-trust-roots.json is exactly the demo root, and the compiled-in roots are the same bytes', async () => {
  const fleetBytes = await readFile(join(here, 'fleet-trust-roots.json'));
  assert.deepEqual(JSON.parse(fleetBytes), DEMO_ROOT);
  assert.equal(Object.hasOwn(JSON.parse(fleetBytes), LEAKED_ID), false, 'the SEC-001 root is gone');
  const compiled = await readFile(join(repoRoot, 'sdn-server', 'internal', 'update', 'default_update_roots.json'));
  assert.deepEqual(compiled, fleetBytes);
  // Listed under its own key id.
  const raw = Buffer.from(DEMO_ROOT['12cb25de07c0'], 'base64').subarray(-32);
  assert.equal(createHash('sha256').update(raw).digest('hex').slice(0, 12), '12cb25de07c0');
});

test('the fleet and UI publishers default to the demo root', async () => {
  for (const script of ['publish-fleet-update.mjs', 'publish-ui-update.mjs']) {
    const text = await readFile(join(here, script), 'utf8');
    const defaults = [...text.matchAll(/arg\('key-id', '([^']*)'\)/g)].map((m) => m[1]);
    assert.deepEqual(defaults, ['12cb25de07c0'], `${script} --key-id default`);
    assert.doesNotMatch(text, new RegExp(LEAKED_ID), `${script} still names the SEC-001 root`);
  }
});

test('the SEC-001 denylist the release lane reads names the leaked node key', async () => {
  const denied = await loadSec001Keys();
  assert.ok(denied.has('0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c'));
  assert.equal(denied.size, 3);
});

test('check-update-signing-key: a stable release without the key fails; a beta warns', async () => {
  const denied = await loadSec001Keys();
  await assert.rejects(checkUpdateSigningKey({ pem: '', keyId: '', maturity: 'stable', roots: DEMO_ROOT, denied }), /stable release must ship its signed self-upgrade feed/);
  const beta = await checkUpdateSigningKey({ pem: undefined, keyId: '', maturity: 'beta', roots: DEMO_ROOT, denied });
  assert.equal(beta.available, false);
  assert.match(beta.warning, /no self-upgrade feed/);
  await assert.rejects(checkUpdateSigningKey({ pem: '', maturity: 'nightly', roots: DEMO_ROOT, denied }), /--maturity must be stable or beta/);
});

test('check-update-signing-key: the key id is derived from the PEM, must equal SDN_UPDATE_KEY_ID, and must be a fleet root', async () => {
  const denied = await loadSec001Keys();
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const id = rootKeyId(publicKey);
  const roots = { [id]: spkiBase64(publicKey) };
  const pem = pemOf(privateKey);

  assert.deepEqual(await checkUpdateSigningKey({ pem, keyId: id, maturity: 'stable', roots, denied }), { available: true, keyId: id });
  await assert.rejects(checkUpdateSigningKey({ pem, keyId: '', maturity: 'stable', roots, denied }), /SDN_UPDATE_KEY_ID is not set[\s\S]*no fallback/);
  await assert.rejects(checkUpdateSigningKey({ pem, keyId: 'sdn-beta-release', maturity: 'beta', roots, denied }), new RegExp(`is sdn-beta-release, but the signing key's id is ${id}`));
  await assert.rejects(checkUpdateSigningKey({ pem, keyId: id, maturity: 'stable', roots: DEMO_ROOT, denied }), /is not an update root in fleet-trust-roots\.json/);
  const other = generateKeyPairSync('ed25519');
  await assert.rejects(checkUpdateSigningKey({ pem, keyId: id, maturity: 'stable', roots: { [id]: spkiBase64(other.publicKey) }, denied }), /different public key/);
  await assert.rejects(checkUpdateSigningKey({ pem: 'not a key', keyId: id, maturity: 'stable', roots, denied }), /not a readable private key/);
  const rsa = generateKeyPairSync('rsa', { modulusLength: 1024 });
  await assert.rejects(checkUpdateSigningKey({ pem: pemOf(rsa.privateKey), keyId: id, maturity: 'stable', roots, denied }), /Ed25519 only/);
});

test('check-update-signing-key: a SEC-001 key is refused as the signer and as a root', async () => {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const id = rootKeyId(publicKey);
  const raw = publicKey.export({ type: 'spki', format: 'der' }).subarray(-32).toString('hex');
  // The leaked key's private half is not used here (it must not be); a
  // throwaway key stands in by being added to the denylist.
  await assert.rejects(
    checkUpdateSigningKey({ pem: pemOf(privateKey), keyId: id, maturity: 'stable', roots: { [id]: spkiBase64(publicKey) }, denied: new Set([raw]) }),
    /SEC-001/,
  );
  await assert.rejects(
    checkUpdateSigningKey({ pem: pemOf(privateKey), keyId: id, maturity: 'stable', roots: { [id]: spkiBase64(publicKey), [LEAKED_ID]: LEAKED_SPKI }, denied: await loadSec001Keys() }),
    /fleet-trust-roots\.json lists d4a971a7e534, a key of the SEC-001 leaked identity/,
  );
});

test('check-update-signing-key CLI never prints the key and writes its outputs', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-signing-key-check-'));
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const id = rootKeyId(publicKey);
  const rootsPath = join(root, 'roots.json');
  await writeFile(rootsPath, JSON.stringify({ [id]: spkiBase64(publicKey) }));
  const outputPath = join(root, 'github-output');
  const pem = pemOf(privateKey);
  const run = (env, maturity) => new Promise((resolvePromise) => {
    const child = spawn(process.execPath, [join(here, 'check-update-signing-key.mjs'), '--maturity', maturity, '--roots', rootsPath, '--github-output', outputPath], {
      env: { PATH: process.env.PATH, ...env }, stdio: ['ignore', 'pipe', 'pipe'],
    });
    let text = '';
    child.stdout.on('data', (chunk) => { text += chunk; });
    child.stderr.on('data', (chunk) => { text += chunk; });
    child.on('close', (code) => resolvePromise({ code, text }));
  });
  const ok = await run({ SDN_UPDATE_SIGNING_KEY_PEM: pem, SDN_UPDATE_KEY_ID: id }, 'stable');
  assert.equal(ok.code, 0, ok.text);
  assert.match(await readFile(outputPath, 'utf8'), new RegExp(`available=true\\nkey_id=${id}\\n`));
  const mismatch = await run({ SDN_UPDATE_SIGNING_KEY_PEM: pem, SDN_UPDATE_KEY_ID: 'sdn-beta-release' }, 'stable');
  assert.equal(mismatch.code, 1);
  const missing = await run({}, 'stable');
  assert.equal(missing.code, 1);
  assert.match(missing.text, /::error title=Update signing key::/);
  for (const { text } of [ok, mismatch, missing]) {
    assert.equal(text.includes(pem.split('\n')[1]), false, 'the key material was printed');
  }
});

async function makeArchive(root, name, { trust }) {
  const tree = join(root, `tree-${name}`);
  await mkdir(join(tree, name, 'bin'), { recursive: true });
  await writeFile(join(tree, name, 'bin', 'spacedatanetwork'), 'daemon');
  await writeFile(join(tree, name, 'manifest.json'), '{"schema":"org.spacedatanetwork.bundle.v1"}\n');
  if (trust) {
    await mkdir(join(tree, name, 'trust'), { recursive: true });
    await writeFile(join(tree, name, 'trust', 'update-roots.json'), JSON.stringify(DEMO_ROOT));
  }
  const archive = join(root, `${name}.tar.gz`);
  await new Promise((resolvePromise, reject) => {
    const child = spawn('tar', ['-czf', archive, '-C', tree, name], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
    child.on('close', (code) => (code === 0 ? resolvePromise() : reject(new Error(`tar exited ${code}`))));
  });
  return archive;
}

async function writeFeed(root, payloads) {
  const feedDir = join(root, 'update-feed');
  const args = [join(here, 'build-sdn-update-feed.js'), '--out-dir', feedDir, '--generated-at', '2026-11-15T00:00:00.000Z'];
  for (const p of payloads) {
    args.push('--entry', `${p.manifestPath}:${p.carrierPath}`);
  }
  await new Promise((resolvePromise, reject) => {
    const child = spawn(process.execPath, args, { stdio: ['ignore', 'ignore', 'pipe'] });
    let stderr = '';
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('close', (code) => (code === 0 ? resolvePromise() : reject(new Error(stderr))));
  });
  return feedDir;
}

test('verify-cli-update-feed: a feed the release lane builds verifies against the roots that sign it, and only those', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-feed-verify-'));
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const id = rootKeyId(publicKey);
  const denied = await loadSec001Keys();
  const payloads = [];
  for (const [platform, arch] of [['linux', 'amd64'], ['darwin', 'arm64']]) {
    const archive = await makeArchive(root, `spacedatanetwork-1.1.2-${platform}-${arch}`, { trust: true });
    payloads.push(await buildCliUpdatePayload({
      bundleArchive: archive, version: '1.1.2', sequence: 90, channel: 'release', platform, arch,
      keyId: id, privateKey, createdAt: new Date().toISOString(), outDir: join(root, `payload-${platform}-${arch}`),
    }));
  }
  const feedDir = await writeFeed(root, payloads);
  const verified = await verifyCliUpdateFeed({ feedDir, roots: { [id]: spkiBase64(publicKey) }, denied, expectVersion: '1.1.2', expectCount: 2 });
  assert.equal(verified.length, 2);
  assert.ok(verified.every((v) => v.keyId === id));

  await assert.rejects(verifyCliUpdateFeed({ feedDir, roots: DEMO_ROOT, denied }), /untrusted update signing key/);
  await assert.rejects(verifyCliUpdateFeed({ feedDir, roots: { [id]: spkiBase64(publicKey) }, denied, expectCount: 5 }), /verified 2 manifests, want 5/);
  await assert.rejects(verifyCliUpdateFeed({ feedDir, roots: { [id]: spkiBase64(publicKey), [LEAKED_ID]: LEAKED_SPKI }, denied }), /SEC-001/);
});

test('verify-cli-update-feed: a payload that carries trust/ is refused', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-feed-verify-trust-'));
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const id = rootKeyId(publicKey);
  // Wrapped without the payload builder, the way the feed used to be built.
  const archive = await makeArchive(root, 'spacedatanetwork-1.1.2-linux-amd64', { trust: true });
  const bundleBytes = await readFile(archive);
  const wasmBytes = buildCarrier(bundleBytes);
  const manifest = signUpdateManifest({
    privateKey,
    manifest: buildUpdateManifest({
      updateId: 'cli-bundle-release-linux-amd64-1.1.2', version: '1.1.2', sequence: 90, channel: 'release',
      platform: 'linux', arch: 'amd64', kind: 'cli-bundle', format: 'tar.gz', bundleBytes, wasmBytes,
      keyId: id, publicKey, createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 86400000).toISOString(),
    }),
  });
  const manifestPath = join(root, 'manifest.json');
  const carrierPath = join(root, 'update.wasm');
  await writeFile(manifestPath, JSON.stringify(manifest));
  await writeFile(carrierPath, wasmBytes);
  const feedDir = await writeFeed(root, [{ manifestPath, carrierPath }]);
  await assert.rejects(
    verifyCliUpdateFeed({ feedDir, roots: { [id]: spkiBase64(publicKey) }, denied: await loadSec001Keys() }),
    /carries spacedatanetwork-1\.1\.2-linux-amd64\/trust, which every node refuses/,
  );
});
