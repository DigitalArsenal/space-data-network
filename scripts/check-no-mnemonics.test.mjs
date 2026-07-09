// Tests for scripts/check-no-mnemonics.sh, the pre-commit guard that blocks
// staging content shaped like a BIP-39 mnemonic (see .husky/pre-commit).
//
// IMPORTANT: this file must never contain a literal 12/15/18/21/24-word
// run of real BIP-39 wordlist words as source text -- if it did, staging
// *this test file* in the real repo would itself trip the guard it tests.
// The standard public "all-abandon" BIP-39 test vector is therefore built
// at runtime (Array(11).fill('abandon') + 'about'), so the word "abandon"
// only ever appears once, literally, in this source file. That phrase is
// the well-known public test mnemonic used across the BIP-39 ecosystem; it
// is unrelated to and never derived from this repo's real (gitignored,
// rotation-out-of-scope-here) dev wallet secret.
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync, chmodSync, copyFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

// Standard public BIP-39 test vector ("abandon" x11 + "about"), assembled
// at runtime -- see file-level comment above.
const publicTestMnemonic = Array(11).fill('abandon').concat(['about']).join(' ');

test('check-no-mnemonics blocks a staged public BIP-39 test phrase and does not leak it', () => {
  const repo = createTempRepo();

  writeStagedFile(repo, 'config/oops-wallet.env', `MNEMONIC="${publicTestMnemonic}"\n`);

  const result = runGuard(repo);
  const combined = `${result.stdout}${result.stderr}`;

  assert.notEqual(result.status, 0, combined);
  assert.match(combined, /config\/oops-wallet\.env/);
  assert.match(combined, /BLOCKED/);
  assert.doesNotMatch(combined, new RegExp(publicTestMnemonic));
  // Belt and suspenders: the guard's own remediation text must not repeat
  // even a long run of the repeated placeholder word.
  assert.doesNotMatch(combined, /abandon abandon abandon/);
});

test('check-no-mnemonics passes an innocent staged file', () => {
  const repo = createTempRepo();

  writeStagedFile(repo, 'README.md', 'This project has nothing to hide here.\n');

  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('check-no-mnemonics passes when nothing is staged', () => {
  const repo = createTempRepo();

  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('check-no-mnemonics does not self-trigger on its own vendored wordlist file', () => {
  const repo = createTempRepo();

  git(repo, ['add', 'scripts/wordlists/bip39-english-wordlist.txt']);

  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('check-no-mnemonics blocks a mnemonic marker inside a staged .env file', () => {
  const repo = createTempRepo();

  writeStagedFile(repo, 'config/some-wallet.env', 'mnemonic=whatever-placeholder\n');

  const result = runGuard(repo);
  const combined = result.stdout + result.stderr;
  assert.notEqual(result.status, 0, combined);
  assert.match(combined, /config\/some-wallet\.env/);
});

test('check-no-mnemonics allows a run of BIP-39 words shorter than the minimum mnemonic length', () => {
  const repo = createTempRepo();
  const shortRun = Array(10).fill('abandon').concat(['art']).join(' ');

  writeStagedFile(repo, 'notes.txt', `${shortRun}\n`);

  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

function createTempRepo() {
  const repo = mkdtempSync(join(tmpdir(), 'sdn-check-no-mnemonics-'));
  mkdirSync(join(repo, 'scripts/wordlists'), { recursive: true });
  copyFileSync(
    join(repoRoot, 'scripts/check-no-mnemonics.sh'),
    join(repo, 'scripts/check-no-mnemonics.sh'),
  );
  chmodSync(join(repo, 'scripts/check-no-mnemonics.sh'), 0o755);
  copyFileSync(
    join(repoRoot, 'scripts/wordlists/bip39-english-wordlist.txt'),
    join(repo, 'scripts/wordlists/bip39-english-wordlist.txt'),
  );
  git(repo, ['init']);
  return repo;
}

function writeStagedFile(repo, relativePath, contents) {
  const absolutePath = join(repo, relativePath);
  mkdirSync(dirname(absolutePath), { recursive: true });
  writeFileSync(absolutePath, contents);
  git(repo, ['add', relativePath]);
}

function runGuard(repo) {
  return spawnSync('./scripts/check-no-mnemonics.sh', {
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
  return result;
}
