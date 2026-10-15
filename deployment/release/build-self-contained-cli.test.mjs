import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { lstat, mkdtemp, mkdir, readFile, readlink, stat, symlink, writeFile } from 'node:fs/promises';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { assertStageableTrustRoots, createArchive, stageBundle } from './build-self-contained-cli.mjs';
import { ed25519Spki, loadAnchors, updateRootKeyId } from '../identity/anchors-lib.mjs';

// A synthetic update root (a fresh Ed25519 key, listed under its real key id:
// the first 12 hex of sha256 over the raw key). SEC-001: stageBundle refuses
// placeholder and denylisted roots on every channel, so tests name this one.
function syntheticTrustRoots() {
  const spki = generateKeyPairSync('ed25519').publicKey.export({ type: 'spki', format: 'der' });
  const keyId = createHash('sha256').update(spki.subarray(12)).digest('hex').slice(0, 12);
  return { [keyId]: spki.toString('base64') };
}
const TEST_TRUST_ROOTS = syntheticTrustRoots();
const TEST_TRUST_ROOTS_PATH = join(mkdtempSync(join(tmpdir(), 'sdn-cli-test-roots-')), 'update-roots.json');
writeFileSync(TEST_TRUST_ROOTS_PATH, JSON.stringify(TEST_TRUST_ROOTS));

test('stageBundle creates expected portable archive layout', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'bin'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'lib'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\necho "WASMEDGE_DIR=${WASMEDGE_DIR};ARGS=$*"\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'modules', 'hd-wallet-wasi.wasm'), 'wallet-wasm');
  await writeFile(join(inputs, 'wasmedge', 'bin', 'wasmedge'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'wasmedge', 'lib', 'libwasmedge.so.0.1.0'), 'libwasmedge');
  await symlink(
    join(inputs, 'wasmedge', 'lib', 'libwasmedge.so.0.1.0'),
    join(inputs, 'wasmedge', 'lib', 'libwasmedge.so.0'),
  );
  await symlink(
    join(inputs, 'wasmedge', 'lib', 'libwasmedge.so.0'),
    join(inputs, 'wasmedge', 'lib', 'libwasmedge.so'),
  );
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'linux',
    arch: 'amd64',
    channel: 'release',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    sdnUiPath: join(inputs, 'sdn-ui'),
    webUiPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    hdWalletWasmPath: join(inputs, 'modules', 'hd-wallet-wasi.wasm'),
    wasmedgePath: join(inputs, 'wasmedge'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
    trustRootsPath: TEST_TRUST_ROOTS_PATH,
  });

  assert.equal(staged.bundleName, 'spacedatanetwork-1.2.3-linux-amd64');
  await stat(join(staged.root, 'bin', 'spacedatanetwork'));
  await stat(join(staged.root, 'bin', 'sdn'));
  await stat(join(staged.root, 'runtime', 'sdn', 'spacedatanetwork'));
  await stat(join(staged.root, 'runtime', 'ui', 'sdn', 'index.html'));
  await stat(join(staged.root, 'runtime', 'ui', 'webui', 'index.html'));
  await stat(join(staged.root, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'));
  await stat(join(staged.root, 'runtime', 'modules', 'hd-wallet-wasi.wasm'));
  await stat(join(staged.root, 'runtime', 'wasmedge', 'bin', 'wasmedge'));
  await stat(join(staged.root, 'runtime', 'wasmedge', 'lib', 'libwasmedge.so.0'));
  assert.equal(await readlink(join(staged.root, 'runtime', 'wasmedge', 'lib', 'libwasmedge.so')), 'libwasmedge.so.0');
  assert.equal(
    await readlink(join(staged.root, 'runtime', 'wasmedge', 'lib', 'libwasmedge.so.0')),
    'libwasmedge.so.0.1.0',
  );
  const launcher = await readFile(join(staged.root, 'bin', 'spacedatanetwork'), 'utf8');
  assert.match(launcher, /LD_LIBRARY_PATH=/);
  assert.match(launcher, /WASMEDGE_DIR=/);
  assert.match(launcher, /readlink/);
  assert.equal((await lstat(join(staged.root, 'bin', 'sdn'))).isSymbolicLink(), true);

  const installBin = join(root, 'install-bin');
  await mkdir(installBin);
  await symlink(join(staged.root, 'bin', 'spacedatanetwork'), join(installBin, 'spacedatanetwork'));
  await symlink(join(staged.root, 'bin', 'sdn'), join(installBin, 'sdn'));
  const bundledEnv = { ...process.env };
  delete bundledEnv.WASMEDGE_DIR;
  for (const commandName of ['spacedatanetwork', 'sdn']) {
    const result = spawnSync(join(installBin, commandName), ['version'], { encoding: 'utf8', env: bundledEnv });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(
      result.stdout.trim(),
      `WASMEDGE_DIR=${join(staged.root, 'runtime', 'wasmedge')};ARGS=version`,
    );
    const override = join(inputs, 'selected runtime');
    const overridden = spawnSync(join(installBin, commandName), ['version'], {
      encoding: 'utf8', env: { ...bundledEnv, WASMEDGE_DIR: override },
    });
    assert.equal(overridden.status, 0, overridden.stderr);
    assert.equal(overridden.stdout.trim(), `WASMEDGE_DIR=${override};ARGS=version`);
  }

  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.schema, 'org.spacedatanetwork.bundle.v1');
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.channel, 'release');
  assert.equal(manifest.signature, 'test-signature');
  assert.deepEqual(manifest.update, {
    feedBaseUrl: 'https://sdn.spaceaware.io/updates',
    pubsubTopic: '/sdn/updates/v1/release',
    updaterModule: 'org.spacedatanetwork.updater',
    updaterWasm: 'runtime/modules/org.spacedatanetwork.updater.wasm',
  });
  assert.deepEqual(manifest.artifacts.map((artifact) => artifact.path), [
    'LICENSE',
    'README.md',
    'bin/sdn',
    'bin/spacedatanetwork',
    'runtime/modules/hd-wallet-wasi.wasm',
    'runtime/modules/org.spacedatanetwork.updater.wasm',
    'runtime/sdn/spacedatanetwork',
    'runtime/ui/sdn/index.html',
    'runtime/ui/webui/index.html',
    'runtime/wasmedge/bin/wasmedge',
    'runtime/wasmedge/lib/libwasmedge.so',
    'runtime/wasmedge/lib/libwasmedge.so.0',
    'runtime/wasmedge/lib/libwasmedge.so.0.1.0',
  ]);
  for (const artifact of manifest.artifacts) {
    const bytes = await readFile(join(staged.root, artifact.path));
    assert.equal(artifact.size, bytes.length);
    assert.equal(artifact.sha256, createHash('sha256').update(bytes).digest('hex'));
  }
  const checksums = await readFile(join(staged.root, 'checksums.txt'), 'utf8');
  assert.deepEqual(checksums.trimEnd().split('\n'), manifest.artifacts.map((artifact) => `${artifact.sha256}  ${artifact.path}`));

  const archive = await createArchive(staged);
  assert.equal(archive.archiveName, 'spacedatanetwork-1.2.3-linux-amd64.tar.gz');
  await stat(archive.path);
});

test('stageBundle creates Windows executable names and copied alias', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-windows-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'bin'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork.exe'), 'exe');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'modules', 'hd-wallet-wasi.wasm'), 'wallet-wasm');
  await writeFile(join(inputs, 'wasmedge', 'bin', 'wasmedge.dll'), 'dll');
  await writeFile(join(inputs, 'wasmedge', 'bin', 'wasmedge.exe'), 'exe');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'windows',
    arch: 'amd64',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork.exe'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    hdWalletWasmPath: join(inputs, 'modules', 'hd-wallet-wasi.wasm'),
    wasmedgePath: join(inputs, 'wasmedge'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
    trustRootsPath: TEST_TRUST_ROOTS_PATH,
  });

  await stat(join(staged.root, 'bin', 'spacedatanetwork.exe'));
  await stat(join(staged.root, 'bin', 'sdn.exe'));
  await stat(join(staged.root, 'bin', 'wasmedge.dll'));
  await stat(join(staged.root, 'runtime', 'modules', 'hd-wallet-wasi.wasm'));
  await stat(join(staged.root, 'runtime', 'wasmedge', 'bin', 'wasmedge.dll'));
  const alias = await readFile(join(staged.root, 'bin', 'sdn.exe'), 'utf8');
  assert.equal(alias, 'exe');
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.os, 'windows');
  assert.deepEqual(manifest.update, {
    feedBaseUrl: 'https://sdn.spaceaware.io/updates',
    pubsubTopic: '/sdn/updates/v1/release',
    updaterModule: 'org.spacedatanetwork.updater',
    updaterWasm: 'runtime/modules/org.spacedatanetwork.updater.wasm',
  });
  assert.deepEqual(manifest.artifacts.map((artifact) => artifact.path), [
    'LICENSE',
    'README.md',
    'bin/sdn.exe',
    'bin/spacedatanetwork.exe',
    'bin/wasmedge.dll',
    'runtime/modules/hd-wallet-wasi.wasm',
    'runtime/modules/org.spacedatanetwork.updater.wasm',
    'runtime/ui/sdn/index.html',
    'runtime/ui/webui/index.html',
    'runtime/wasmedge/bin/wasmedge.dll',
    'runtime/wasmedge/bin/wasmedge.exe',
  ]);
});

test('stageBundle stages trust roots outside manifest artifacts and checksums', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-trust-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'bin'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'modules', 'hd-wallet-wasi.wasm'), 'wallet-wasm');
  await writeFile(join(inputs, 'wasmedge', 'bin', 'wasmedge'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');
  const trustRoots = syntheticTrustRoots();
  await writeFile(join(inputs, 'update-roots.json'), `${JSON.stringify(trustRoots, null, 2)}\n`);

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'linux',
    arch: 'amd64',
    channel: 'release',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    hdWalletWasmPath: join(inputs, 'modules', 'hd-wallet-wasi.wasm'),
    wasmedgePath: join(inputs, 'wasmedge'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
    trustRootsPath: join(inputs, 'update-roots.json'),
  });

  const stagedRoots = JSON.parse(await readFile(join(staged.root, 'trust', 'update-roots.json'), 'utf8'));
  assert.deepEqual(stagedRoots, trustRoots);
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.artifacts.some((artifact) => artifact.path.startsWith('trust/')), false);
  assert.deepEqual(manifest.artifacts.map((artifact) => artifact.path), [
    'LICENSE',
    'README.md',
    'bin/sdn',
    'bin/spacedatanetwork',
    'runtime/modules/hd-wallet-wasi.wasm',
    'runtime/modules/org.spacedatanetwork.updater.wasm',
    'runtime/sdn/spacedatanetwork',
    'runtime/ui/sdn/index.html',
    'runtime/ui/webui/index.html',
    'runtime/wasmedge/bin/wasmedge',
  ]);
  const checksums = await readFile(join(staged.root, 'checksums.txt'), 'utf8');
  assert.equal(checksums.includes('trust/'), false);
  assert.equal(checksums.includes('update-roots.json'), false);
});

test('stageBundle rejects path traversal in bundle name fields', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-invalid-'));
  await assert.rejects(
    () => stageBundle({
      version: '../../../bad',
      os: 'linux',
      arch: 'amd64',
      outputDir: root,
      binaryPath: join(root, 'missing-spacedatanetwork'),
      sdnUIPath: join(root, 'missing-sdn-ui'),
      webUIPath: join(root, 'missing-webui'),
      updaterWasmPath: join(root, 'missing-updater.wasm'),
      licensePath: join(root, 'missing-license'),
      readmePath: join(root, 'missing-readme'),
      manifestSignature: 'test-signature',
    trustRootsPath: TEST_TRUST_ROOTS_PATH,
    }),
    /version contains unsupported characters/,
  );
});

// OPS-02: a bundle staged without naming a roots file still carries the
// fleet's update trust roots — without trust/update-roots.json the shipped
// bundle could never verify an update and would be stranded on its version.
test('stageBundle stages the fleet trust roots when none are named', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-default-trust-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'bin'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'modules', 'hd-wallet-wasi.wasm'), 'wallet-wasm');
  await writeFile(join(inputs, 'wasmedge', 'bin', 'wasmedge'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');

  const options = {
    version: '1.2.3',
    os: 'linux',
    arch: 'amd64',
    channel: 'release',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    hdWalletWasmPath: join(inputs, 'modules', 'hd-wallet-wasi.wasm'),
    wasmedgePath: join(inputs, 'wasmedge'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
  };
  const fleetRoots = JSON.parse(await readFile(new URL('./fleet-trust-roots.json', import.meta.url), 'utf8'));
  assert.ok(Object.keys(fleetRoots).length >= 1, 'the fleet roots file names at least one root');
  if (JSON.stringify(fleetRoots).includes('PLACEHOLDER-')) {
    // SEC-001 (verifier S4): until the owner's new root lands, no channel
    // stages the placeholder roots — such a node could never verify an update.
    await assert.rejects(stageBundle(options), /PLACEHOLDER update root/);
    return;
  }
  const staged = await stageBundle(options);
  const stagedRoots = JSON.parse(await readFile(join(staged.root, 'trust', 'update-roots.json'), 'utf8'));
  assert.deepEqual(stagedRoots, fleetRoots);
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.artifacts.some((artifact) => artifact.path.startsWith('trust/')), false);
  assert.equal(manifest.update.feedBaseUrl, 'https://sdn.spaceaware.io/updates');
});

test('SEC-001 S4: every channel refuses placeholder, mislabelled, malformed and denylisted trust roots', async () => {
  const [[keyId, spki]] = Object.entries(TEST_TRUST_ROOTS);
  assert.doesNotThrow(() => assertStageableTrustRoots(TEST_TRUST_ROOTS));
  assert.throws(() => assertStageableTrustRoots({}), /names no update root/);
  assert.throws(() => assertStageableTrustRoots({ 'PLACEHOLDER-SEC001-UPDATE-ROOT-KEY-ID': 'PLACEHOLDER-SEC001-UPDATE-ROOT-SPKI' }), /PLACEHOLDER/);
  assert.throws(() => assertStageableTrustRoots({ 'sdn-test-root': spki }), /under key id/);
  assert.throws(() => assertStageableTrustRoots({ [keyId]: 'MCowBQYDK2VwAyEA' }), /not an Ed25519 key/);
  // The published root, read from the anchors denylist (never written out here).
  const anchors = loadAnchors();
  const leakedHex = anchors.denylist.ed25519PublicKeys.find((k) => anchors.denylist.updateRootKeyIds.includes(updateRootKeyId(k)));
  assert.ok(leakedHex, 'the denylist names the published update root');
  assert.throws(() => assertStageableTrustRoots({ [updateRootKeyId(leakedHex)]: ed25519Spki(leakedHex) }), /denylisted/);
});

// INST-03: a release bundle carries the wallet sign-in assets so a fresh
// install's dashboard can sign in without an operator staging them by hand.
test('stageBundle stages wallet sign-in assets under runtime/ui when given', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-wallet-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  for (const dir of ['sdn-ui', 'webui', 'modules', join('wasmedge', 'bin'), join('wallet-wasm', 'runtime'), join('wallet-ui', 'compat')]) {
    await mkdir(join(inputs, dir), { recursive: true });
  }
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'modules', 'hd-wallet-wasi.wasm'), 'wallet-wasm');
  await writeFile(join(inputs, 'wasmedge', 'bin', 'wasmedge'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');
  await writeFile(join(inputs, 'wallet-wasm', 'runtime', 'index.mjs'), 'export {};');
  await writeFile(join(inputs, 'wallet-ui', 'compat', 'index.js'), 'export {};');

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'linux',
    arch: 'amd64',
    channel: 'release',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    hdWalletWasmPath: join(inputs, 'modules', 'hd-wallet-wasi.wasm'),
    wasmedgePath: join(inputs, 'wasmedge'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
    trustRootsPath: TEST_TRUST_ROOTS_PATH,
    walletWasmPath: join(inputs, 'wallet-wasm'),
    walletUIPath: join(inputs, 'wallet-ui'),
  });

  assert.equal(await readFile(join(staged.root, 'runtime', 'ui', 'wallet-wasm', 'runtime', 'index.mjs'), 'utf8'), 'export {};');
  assert.equal(await readFile(join(staged.root, 'runtime', 'ui', 'wallet-ui', 'compat', 'index.js'), 'utf8'), 'export {};');
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  const paths = manifest.artifacts.map((artifact) => artifact.path);
  assert.ok(paths.includes('runtime/ui/wallet-wasm/runtime/index.mjs'), 'wallet-wasm is a checksummed bundle artifact');
  assert.ok(paths.includes('runtime/ui/wallet-ui/compat/index.js'), 'wallet-ui is a checksummed bundle artifact');
});

// A statically linked daemon carries WasmEdge. Omitting wasmedgePath must
// produce a bundle with NOTHING to set up at run time: no runtime/wasmedge
// tree, no wasmedge.dll beside the exe, and a launcher that exports no library
// path. Each platform can adopt the static binary independently, so the two
// shapes have to coexist.
test('a static bundle stages no WasmEdge runtime and no library path', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-static-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork'), 'bin');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'modules', 'hd-wallet-wasi.wasm'), 'wallet-wasm');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'linux',
    arch: 'amd64',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    hdWalletWasmPath: join(inputs, 'modules', 'hd-wallet-wasi.wasm'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
    trustRootsPath: TEST_TRUST_ROOTS_PATH,
  });

  await assert.rejects(
    stat(join(staged.root, 'runtime', 'wasmedge')),
    'a static bundle must not carry a WasmEdge runtime tree',
  );
  const launcher = await readFile(join(staged.root, 'bin', 'spacedatanetwork'), 'utf8');
  assert.ok(!launcher.includes('runtime/wasmedge'), 'the launcher must not point at a bundled runtime');
  assert.match(launcher, /statically linked/, 'the launcher should say why it sets nothing up');
  await stat(join(staged.root, 'runtime', 'sdn', 'spacedatanetwork'));
});

test('stageBundle refuses to stamp a bundle with the internal fleet dev lane channel', async () => {
  // The bundle's channel is what the installed updater resolves its feed
  // index from. Stamping a distributable bundle 'beta' points it at the
  // internal fleet lane, whose binary-only payloads carry no runtime/ — the
  // 2026-09-16 bricking. The builder refuses instead of silently correcting,
  // so a release lane that asks for the wrong channel fails in CI.
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-channel-'));
  await assert.rejects(
    stageBundle({
      version: '1.2.3',
      os: 'linux',
      arch: 'amd64',
      channel: 'beta',
      outputDir: join(root, 'out'),
      binaryPath: join(root, 'spacedatanetwork'),
      sdnUIPath: join(root, 'sdn-ui'),
      webUIPath: join(root, 'webui'),
      updaterWasmPath: join(root, 'updater.wasm'),
      hdWalletWasmPath: join(root, 'wallet.wasm'),
      licensePath: join(root, 'LICENSE'),
      readmePath: join(root, 'README.md'),
      manifestSignature: 'test-signature',
    trustRootsPath: TEST_TRUST_ROOTS_PATH,
    }),
    /internal fleet dev lane/,
  );
});
