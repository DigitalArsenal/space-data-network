import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, stat, writeFile } from 'node:fs/promises';
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
  await writeFile(join(inputs, 'spacedatanetwork'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'ipfs'), '#!/bin/sh\n');
  await writeFile(join(inputs, 'sdn-ui', 'index.html'), '<html>sdn</html>');
  await writeFile(join(inputs, 'webui', 'index.html'), '<html>webui</html>');
  await writeFile(join(inputs, 'modules', 'org.spacedatanetwork.updater.wasm'), 'wasm');
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
    licensePath: join(inputs, 'LICENSE'),
    readmePath: join(inputs, 'README.md'),
    manifestSignature: 'test-signature',
  });

  assert.equal(staged.bundleName, 'spacedatanetwork-1.2.3-linux-amd64');
  await stat(join(staged.root, 'bin', 'spacedatanetwork'));
  await stat(join(staged.root, 'bin', 'sdn'));
  await stat(join(staged.root, 'runtime', 'kubo', 'ipfs'));
  await stat(join(staged.root, 'runtime', 'ui', 'sdn', 'index.html'));
  await stat(join(staged.root, 'runtime', 'ui', 'webui', 'index.html'));
  await stat(join(staged.root, 'runtime', 'modules', 'org.spacedatanetwork.updater.wasm'));
  const manifest = JSON.parse(await readFile(join(staged.root, 'manifest.json'), 'utf8'));
  assert.equal(manifest.schema, 'org.spacedatanetwork.bundle.v1');
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.channel, 'beta');
  assert.equal(manifest.signature, 'test-signature');
  assert.ok(manifest.artifacts.some((artifact) => artifact.path === 'bin/spacedatanetwork'));
  assert.ok(manifest.artifacts.some((artifact) => artifact.path === 'runtime/kubo/ipfs'));
  assert.ok(manifest.artifacts.some((artifact) => artifact.path === 'runtime/ui/sdn/index.html'));
  assert.ok(!manifest.artifacts.some((artifact) => artifact.path === 'manifest.json'));
  const checksums = await readFile(join(staged.root, 'checksums.txt'), 'utf8');
  assert.match(checksums, /bin\/spacedatanetwork/);
  assert.match(checksums, /runtime\/kubo\/ipfs/);

  const archive = await createArchive(staged);
  assert.equal(archive.archiveName, 'spacedatanetwork-1.2.3-linux-amd64.tar.gz');
  await stat(archive.path);
});
