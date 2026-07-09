import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

function readRepoFile(relativePath) {
  return readFileSync(join(repoRoot, relativePath), 'utf8');
}

test('docs network ecosystem mounts the interactive sandbox demo', () => {
  const source = readRepoFile('docs/docs.html');

  assert.match(source, /id="network-ecosystem"/);
  assert.match(source, /data-sdn-network-ecosystem-demo/);
  assert.match(source, /network-ecosystem-demo\.css/);
  assert.match(source, /network-ecosystem-demo\.mjs/);
  assert.match(source, /Sandbox mode/);
  assert.match(source, /Live mode/);
  assert.match(source, /Circle/);
  assert.match(source, /Triangle/);
  assert.match(source, /Square/);
});

test('homepage links to the interactive network ecosystem demo', () => {
  const source = readRepoFile('docs/index.html');

  assert.match(source, /Interactive network ecosystem/);
  assert.match(source, /docs\.html#network-ecosystem/);
  assert.match(source, /signed data/);
  assert.match(source, /module listings/);
});
