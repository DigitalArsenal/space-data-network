import assert from 'node:assert/strict';
import { lstat, mkdtemp, mkdir, readFile, readlink, stat, symlink, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { createArchive, stageBundle } from './build-self-contained-cli.mjs';

test('stageBundle creates expected portable archive layout', async () => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-cli-bundle-'));
  const inputs = join(root, 'inputs');
  const out = join(root, 'out');
  await mkdir(join(inputs, 'sdn-ui'), { recursive: true });
  await mkdir(join(inputs, 'webui'), { recursive: true });
  await mkdir(join(inputs, 'modules'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'bin'), { recursive: true });
  await mkdir(join(inputs, 'wasmedge', 'lib'), { recursive: true });
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'ipfs'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
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
    channel: 'beta',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork'),
    kuboPath: join(inputs, 'ipfs'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    wasmedgePath: join(inputs, 'wasmedge'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
  });

  assert.equal(staged.bundleName, 'spacedatanetwork-1.2.3-linux-amd64');
  await stat(join(staged.root, 'bin', 'spacedatanetwork'));
  await stat(join(staged.root, 'bin', 'sdn'));
  await stat(join(staged.root, 'runtime', 'sdn', 'spacedatanetwork'));
  await stat(join(staged.root, 'runtime', 'kubo', 'ipfs'));
  await stat(join(staged.root, 'runtime', 'ui', 'sdn', 'index.html'));
  await stat(join(staged.root, 'runtime', 'ui', 'webui', 'index.html'));
  await stat(join(staged.root, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'));
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
  assert.equal((await lstat(join(staged.root, 'bin', 'sdn'))).isSymbolicLink(), true);
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.schema, 'org.spacedatanetwork.bundle.v1');
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.channel, 'beta');
  assert.equal(manifest.signature, 'test-signature');
  assert.deepEqual(manifest.artifacts.map((artifact) => artifact.path), [
    'LICENSE',
    'README.md',
    'bin/sdn',
    'bin/spacedatanetwork',
    'runtime/kubo/ipfs',
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
  await writeFile(join(inputs, 'spacedatanetwork.exe'), 'exe');
  await writeFile(join(inputs, 'ipfs.exe'), 'ipfs');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
  await writeFile(join(inputs, 'LICENSE'), 'license');
  await writeFile(join(inputs, 'README.md'), 'readme');

  const staged = await stageBundle({
    version: '1.2.3',
    os: 'windows',
    arch: 'amd64',
    outputDir: out,
    binaryPath: join(inputs, 'spacedatanetwork.exe'),
    kuboPath: join(inputs, 'ipfs.exe'),
    sdnUIPath: join(inputs, 'sdn-ui'),
    webUIPath: join(inputs, 'webui'),
    updaterWasmPath: join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'),
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
  });

  await stat(join(staged.root, 'bin', 'spacedatanetwork.exe'));
  await stat(join(staged.root, 'bin', 'sdn.exe'));
  await stat(join(staged.root, 'runtime', 'kubo', 'ipfs.exe'));
  const alias = await readFile(join(staged.root, 'bin', 'sdn.exe'), 'utf8');
  assert.equal(alias, 'exe');
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.os, 'windows');
  assert.deepEqual(manifest.artifacts.map((artifact) => artifact.path), [
    'LICENSE',
    'README.md',
    'bin/sdn.exe',
    'bin/spacedatanetwork.exe',
    'runtime/kubo/ipfs.exe',
    'runtime/modules/org.spacedatanetwork.updater.wasm',
    'runtime/ui/sdn/index.html',
    'runtime/ui/webui/index.html',
  ]);
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
      kuboPath: join(root, 'missing-ipfs'),
      sdnUIPath: join(root, 'missing-sdn-ui'),
      webUIPath: join(root, 'missing-webui'),
      updaterWasmPath: join(root, 'missing-updater.wasm'),
      licensePath: join(root, 'missing-license'),
      readmePath: join(root, 'missing-readme'),
      manifestSignature: 'test-signature',
    }),
    /version contains unsupported characters/,
  );
});
