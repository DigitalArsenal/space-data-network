import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { QUARANTINE, findSuites, orphans, runnerText, staleQuarantine } from './check-no-orphan-test-suites.mjs';

const SCRIPT = resolve(dirname(fileURLToPath(import.meta.url)), 'check-no-orphan-test-suites.mjs');

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'orphan-suites-'));
  mkdirSync(join(root, 'scripts'), { recursive: true });
  mkdirSync(join(root, '.github/workflows'), { recursive: true });
  writeFileSync(join(root, 'package.json'), '{"scripts":{}}');
  return root;
}

test('a suite named by ci-local.sh is not an orphan', () => {
  const root = fixture();
  writeFileSync(join(root, 'scripts/wired.test.mjs'), '');
  writeFileSync(join(root, 'scripts/ci-local.sh'), 'node --test scripts/wired.test.mjs\n');
  assert.deepEqual(orphans(findSuites(root), runnerText(root), new Map()), []);
});

test('a suite named by nothing IS an orphan', () => {
  const root = fixture();
  writeFileSync(join(root, 'scripts/lonely.test.mjs'), '');
  writeFileSync(join(root, 'scripts/ci-local.sh'), 'echo hi\n');
  assert.deepEqual(orphans(findSuites(root), runnerText(root), new Map()), ['scripts/lonely.test.mjs']);
});

test('a workflow counts as a runner', () => {
  const root = fixture();
  writeFileSync(join(root, 'scripts/via-workflow.test.mjs'), '');
  writeFileSync(join(root, 'scripts/ci-local.sh'), 'echo hi\n');
  writeFileSync(join(root, '.github/workflows/ci.yml'), 'run: node --test scripts/via-workflow.test.mjs\n');
  assert.deepEqual(orphans(findSuites(root), runnerText(root), new Map()), []);
});

test('quarantine excuses a suite, and only the ones it names', () => {
  const root = fixture();
  writeFileSync(join(root, 'scripts/broken.test.mjs'), '');
  writeFileSync(join(root, 'scripts/also-broken.test.mjs'), '');
  writeFileSync(join(root, 'scripts/ci-local.sh'), 'echo hi\n');
  const quarantine = new Map([['scripts/broken.test.mjs', 'red: known']]);
  assert.deepEqual(orphans(findSuites(root), runnerText(root), quarantine), ['scripts/also-broken.test.mjs']);
});

test('a quarantine entry for a deleted file is itself a failure', () => {
  const root = fixture();
  writeFileSync(join(root, 'scripts/ci-local.sh'), 'echo hi\n');
  const quarantine = new Map([['scripts/gone.test.mjs', 'red: known']]);
  assert.deepEqual(staleQuarantine(findSuites(root), quarantine), ['scripts/gone.test.mjs']);
});

test('node_modules is not searched', () => {
  const root = fixture();
  mkdirSync(join(root, 'scripts/node_modules'), { recursive: true });
  writeFileSync(join(root, 'scripts/node_modules/vendor.test.mjs'), '');
  writeFileSync(join(root, 'scripts/ci-local.sh'), 'echo hi\n');
  assert.deepEqual(findSuites(root), []);
});

test('every quarantined suite states a reason', () => {
  for (const [suite, reason] of QUARANTINE) {
    assert.ok(reason && reason.length > 20, `${suite} has no usable reason`);
  }
});

test('the repository itself passes', () => {
  const result = spawnSync(process.execPath, [SCRIPT], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /suites are wired in/);
});
