// End-to-end test for .husky/pre-push (owner 2026-10-16: a change gets the E2E
// for that change, then deploys). The real hook and the real mnemonic scan run
// in a throwaway repository; only the two heavy entry points are stubs that
// leave a marker file, so the test sees exactly which of them the hook runs:
//   - scripts/oss-preflight.sh (secret and infrastructure scan)
//   - scripts/ci-local.sh      (the local CI gate)
// By default the hook runs the scans only; SDN_PREPUSH_CI=1 adds ci-local quick.
//
// This file must never contain a literal run of 12 BIP-39 words (staging it
// would trip the guard it tests); the phrase is assembled at runtime.
import assert from 'node:assert/strict';
import {
  chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const ZERO = '0'.repeat(40);

// Distinct wordlist members, 'qq' separators keep this source file free of an
// adjacent run; they are filtered out of the fixture.
const mnemonicShaped = [
  'legal', 'qq', 'winner', 'qq', 'thank', 'qq', 'year', 'qq', 'wave', 'qq',
  'sausage', 'qq', 'worth', 'qq', 'useful', 'qq', 'lawsuit', 'qq', 'ugly',
  'qq', 'theme', 'qq', 'zone',
].filter((w) => w !== 'qq').join(' ');

test('by default the pre-push hook runs the secret and mnemonic scans and not the local CI gate', () => {
  const fx = createFixture();
  const sha = commit(fx, 'README.md', 'nothing to hide\n');

  const result = runHook(fx, pushLine(sha), {});

  assert.equal(result.status, 0, result.output);
  assert.equal(marker(fx, 'preflight'), 'ran');
  assert.equal(marker(fx, 'ci'), null, 'ci-local.sh must not run by default');
  assert.match(result.output, /mnemonic scan over the pushed commits/);
});

test('SDN_PREPUSH_CI=1 adds ci-local.sh quick', () => {
  const fx = createFixture();
  const sha = commit(fx, 'README.md', 'nothing to hide\n');

  const result = runHook(fx, pushLine(sha), { SDN_PREPUSH_CI: '1' });

  assert.equal(result.status, 0, result.output);
  assert.equal(marker(fx, 'preflight'), 'ran');
  assert.equal(marker(fx, 'ci'), 'quick');
});

test('a pushed commit holding a mnemonic-shaped phrase is refused, without echoing it, even with the CI gate off', () => {
  const fx = createFixture();
  const sha = commit(fx, 'notes.txt', `${mnemonicShaped}\n`);

  const result = runHook(fx, pushLine(sha), {});

  assert.notEqual(result.status, 0, result.output);
  assert.match(result.output, /notes\.txt/);
  assert.match(result.output, /BLOCKED/);
  assert.doesNotMatch(result.output, new RegExp(mnemonicShaped));
  assert.equal(marker(fx, 'ci'), null);
});

test('only the pushed range is scanned: a phrase already on the remote does not block a later push', () => {
  const fx = createFixture();
  const old = commit(fx, 'notes.txt', `${mnemonicShaped}\n`);
  const next = commit(fx, 'README.md', 'a later change\n');

  const result = runHook(fx, `refs/heads/main ${next} refs/heads/main ${old}\n`, {});

  assert.equal(result.status, 0, result.output);
});

test('SKIP_LOCAL_CI=1 bypasses the hook', () => {
  const fx = createFixture();
  const sha = commit(fx, 'notes.txt', `${mnemonicShaped}\n`);

  const result = runHook(fx, pushLine(sha), { SKIP_LOCAL_CI: '1' });

  assert.equal(result.status, 0, result.output);
  assert.equal(marker(fx, 'preflight'), null);
});

function pushLine(sha) {
  return `refs/heads/feature ${sha} refs/heads/feature ${ZERO}\n`;
}

function createFixture() {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-prepush-hook-'));
  mkdirSync(join(dir, 'scripts/wordlists'), { recursive: true });
  mkdirSync(join(dir, '.husky'), { recursive: true });
  mkdirSync(join(dir, '.markers'), { recursive: true });
  copyFileSync(join(repoRoot, '.husky/pre-push'), join(dir, '.husky/pre-push'));
  copyFileSync(join(repoRoot, 'scripts/check-no-mnemonics.sh'), join(dir, 'scripts/check-no-mnemonics.sh'));
  copyFileSync(
    join(repoRoot, 'scripts/wordlists/bip39-english-wordlist.txt'),
    join(dir, 'scripts/wordlists/bip39-english-wordlist.txt'),
  );
  writeStub(dir, 'scripts/oss-preflight.sh', 'echo ran > "$(dirname "$0")/../.markers/preflight"');
  writeStub(dir, 'scripts/ci-local.sh', 'echo "$1" > "$(dirname "$0")/../.markers/ci"');
  chmodSync(join(dir, 'scripts/check-no-mnemonics.sh'), 0o755);
  git(dir, ['init', '-q']);
  git(dir, ['config', 'user.email', 'test@example.invalid']);
  git(dir, ['config', 'user.name', 'test']);
  // The throwaway repo has no origin/main, so a new-branch push falls back to
  // scanning everything the pushed commit holds.
  return dir;
}

function writeStub(dir, relativePath, body) {
  const path = join(dir, relativePath);
  writeFileSync(path, `#!/usr/bin/env bash\n${body}\n`);
  chmodSync(path, 0o755);
}

function commit(dir, file, contents) {
  writeFileSync(join(dir, file), contents);
  git(dir, ['add', file]);
  git(dir, ['commit', '-q', '-m', `add ${file}`]);
  return git(dir, ['rev-parse', 'HEAD']).stdout.trim();
}

function marker(dir, name) {
  const path = join(dir, '.markers', name);
  return existsSync(path) ? readFileSync(path, 'utf8').trim() : null;
}

function runHook(dir, stdin, env) {
  const clean = { ...process.env };
  delete clean.SDN_PREPUSH_CI;
  delete clean.SKIP_LOCAL_CI;
  delete clean.CHECK_NO_MNEMONICS_RANGE;
  const result = spawnSync('sh', ['.husky/pre-push', 'origin', 'file:///nowhere'], {
    cwd: dir,
    input: stdin,
    encoding: 'utf8',
    env: { ...clean, ...env },
  });
  return { status: result.status, output: `${result.stdout}${result.stderr}` };
}

function git(dir, args) {
  const result = spawnSync('git', args, { cwd: dir, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  return result;
}
