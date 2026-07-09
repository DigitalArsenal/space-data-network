import assert from 'node:assert/strict';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const fakeGoogleApiKey = `AIza${'a'.repeat(35)}`;

test('oss preflight ignores generated docs ecosystem bundle without ignoring ordinary files', () => {
  const repo = createTempRepo();

  writeTrackedFile(repo, 'docs/network-ecosystem-demo.mjs', `const wasmPayload = "${fakeGoogleApiKey}";\n`);
  git(repo, ['add', '.']);

  const generatedOnly = runPreflight(repo);
  assert.equal(generatedOnly.status, 0, generatedOnly.stdout + generatedOnly.stderr);

  writeTrackedFile(repo, 'src/config.js', `export const key = "${fakeGoogleApiKey}";\n`);
  git(repo, ['add', 'src/config.js']);

  const ordinarySecret = runPreflight(repo);
  assert.notEqual(ordinarySecret.status, 0, ordinarySecret.stdout + ordinarySecret.stderr);
  assert.match(ordinarySecret.stdout, /src\/config\.js/);
  assert.doesNotMatch(ordinarySecret.stdout, /docs\/network-ecosystem-demo\.mjs/);
});

test('oss preflight still reports production endpoints in generated docs ecosystem bundle', () => {
  const repo = createTempRepo();

  const productionEndpoint = 'https://api.' + 'spaceaware.io';
  writeTrackedFile(repo, 'docs/network-ecosystem-demo.mjs', `export const endpoint = "${productionEndpoint}";\n`);
  git(repo, ['add', '.']);

  const generatedEndpoint = runPreflight(repo);
  assert.notEqual(generatedEndpoint.status, 0, generatedEndpoint.stdout + generatedEndpoint.stderr);
  assert.match(generatedEndpoint.stdout, /production endpoint or host references detected/);
  assert.match(generatedEndpoint.stdout, /docs\/network-ecosystem-demo\.mjs/);
});

function createTempRepo() {
  const repo = mkdtempSync(join(tmpdir(), 'sdn-oss-preflight-'));
  mkdirSync(join(repo, 'scripts'), { recursive: true });
  copyFileSync(join(repoRoot, 'scripts/oss-preflight.sh'), join(repo, 'scripts/oss-preflight.sh'));
  chmodSync(join(repo, 'scripts/oss-preflight.sh'), 0o755);
  git(repo, ['init']);
  return repo;
}

function writeTrackedFile(repo, relativePath, contents) {
  const absolutePath = join(repo, relativePath);
  mkdirSync(dirname(absolutePath), { recursive: true });
  writeFileSync(absolutePath, contents);
}

function runPreflight(repo) {
  return spawnSync('./scripts/oss-preflight.sh', {
    cwd: repo,
    encoding: 'utf8',
  });
}

function git(repo, args) {
  const result = spawnSync('git', args, {
    cwd: repo,
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
}
