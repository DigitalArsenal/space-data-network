// Tests for the BIP-39 mnemonic guard: scripts/check-no-mnemonics.sh (the
// pre-commit hook path, .husky/pre-commit) and scripts/check-no-mnemonics.mjs
// (the scanner it runs, also run by scripts/oss-preflight.sh in CI).
//
// IMPORTANT: this file must never contain a literal 12/15/18/21/24-word run of
// BIP-39 wordlist words as source text — the guard scans every tracked file, so
// a literal phrase here would trip it. Every phrase below is BUILT AT RUNTIME:
//   - the public all-"abandon" test vector, from Array(11).fill(...);
//   - a SYNTHETIC 24-word phrase derived from fixed public entropy
//     (sha256 of a label), checksum and all. It is not, and is not derived
//     from, any real seed — in particular not the SEC-001 leaked one.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { findMnemonicRuns, loadWordlist } from './check-no-mnemonics.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const WORDLIST = loadWordlist(join(repoRoot, 'scripts/wordlists/bip39-english-wordlist.txt'));

// Standard public BIP-39 test vector ("abandon" x11 + "about").
const publicTestMnemonic = Array(11).fill('abandon').concat(['about']).join(' ');

/** A checksum-valid BIP-39 phrase from fixed, public entropy. */
function syntheticPhrase(label, words = 24) {
  const entropy = createHash('sha256').update(label).digest().subarray(0, (words * 4) / 3);
  const checksum = createHash('sha256').update(entropy).digest();
  const bits =
    [...entropy].map((b) => b.toString(2).padStart(8, '0')).join('') +
    checksum[0].toString(2).padStart(8, '0').slice(0, words / 3);
  return Array.from({ length: words }, (_, i) => WORDLIST[Number.parseInt(bits.slice(i * 11, i * 11 + 11), 2)]).join(' ');
}

const synthetic24 = syntheticPhrase('SEC-001 synthetic detector test phrase (public entropy, not a seed)');

// The shape of the leaked file (sdn-js/verify-identity.mjs): an ES module with
// a 24-word phrase in a string constant. The phrase here is synthetic.
function leakedFileShape(phrase) {
  return [
    "import { initHDWallet } from './dist/index.mjs';",
    '',
    `const mnemonic = '${phrase}';`,
    'const wallet = await deriveIdentity(mnemonic);',
    'console.log(wallet.peerId);',
    '',
  ].join('\n');
}

function assertNoWordsLeak(output, phrase) {
  for (let i = 0; i + 3 <= phrase.split(' ').length; i += 3) {
    const triple = phrase.split(' ').slice(i, i + 3).join(' ');
    assert.doesNotMatch(output, new RegExp(triple), 'the guard printed part of the phrase');
  }
}

// ---------------------------------------------------------------------------
// The detector itself
// ---------------------------------------------------------------------------

test('the synthetic phrase is a checksum-valid 24-word BIP-39 phrase', () => {
  const [run] = findMnemonicRuns(`x = "${synthetic24}"`, WORDLIST);
  assert.equal(run.words, 24);
  assert.equal(run.checksumValid, true);
});

test('SEC-001: the leaked file pattern is caught, in a .mjs and in any file type', () => {
  for (const content of [
    leakedFileShape(synthetic24),
    Buffer.concat([Buffer.from([0, 1, 2, 0xff, 0]), Buffer.from(` ${synthetic24} `), Buffer.from([0, 0x7f, 0x80])]),
    `{"seed": "${synthetic24}"}`,
    synthetic24.split(' ').map((w, i) => `${i + 1}. ${w}`).join('\n'),
  ]) {
    const runs = findMnemonicRuns(content, WORDLIST);
    assert.equal(runs.length, 1, String(content).slice(0, 40));
    assert.equal(runs[0].checksumValid, true);
  }
});

// Verifier B1 (2026-10-15): the pre-SEC-001 staged guard turned every
// non-letter into a space, so it caught these shapes; a scanner that ended a
// run on any gap over 8 characters or any non-ASCII byte missed all four.
// Each is built at runtime from the synthetic phrase.
function shapesTheOldGuardCaught(phrase) {
  const words = phrase.split(' ');
  const goSlice = [
    'package seeds',
    '',
    'func vectors() [][]string {',
    '\treturn [][]string{',
    '\t\t{',
    ...words.map((w) => `            "${w}",`),
    '\t\t},',
    '\t}',
    '}',
  ].join('\n');
  const nestedJSON = JSON.stringify({ fleet: { node: { identity: { seed: words } } } }, null, 2);
  const nbspJoined = Buffer.from(words.join(' '), 'utf8');
  const bulletList = ['Recovery words:', ...words.map((w) => `• ${w}`)].join('\n');
  return { goSlice, nestedJSON, nbspJoined, bulletList };
}

test('B1: a Go slice indented 12 spaces, 4-level nested JSON, NBSP-joined words and a bullet list are all caught', () => {
  const shapes = shapesTheOldGuardCaught(synthetic24);
  // The nested JSON really is 4 levels deep with the words on their own lines.
  assert.match(shapes.nestedJSON, /\n {10}"/);
  assert.ok(shapes.nbspJoined.includes(Buffer.from([0xc2, 0xa0])));
  for (const [name, content] of Object.entries(shapes)) {
    const runs = findMnemonicRuns(content, WORDLIST);
    assert.equal(runs.length, 1, `${name}: want one run`);
    // "seed", the JSON key, is itself a wordlist word: 25 there.
    assert.ok(runs[0].words >= 24, `${name}: ${runs[0].words} words`);
    assert.equal(runs[0].checksumValid, true, name);
  }
});

test('B1: --staged, --tracked and --history all catch the four shapes, and never print a word', () => {
  const shapes = shapesTheOldGuardCaught(synthetic24);
  const files = {
    'seeds/vectors.go': shapes.goSlice,
    'config/fleet.json': shapes.nestedJSON,
    'notes/nbsp.txt': shapes.nbspJoined,
    'notes/bullets.md': shapes.bulletList,
  };
  const repo = createTempRepo();
  for (const [path, content] of Object.entries(files)) writeStagedFile(repo, path, content);
  const staged = runGuard(repo);
  assert.equal(staged.status, 1, staged.stderr);
  for (const path of Object.keys(files)) assert.match(staged.stderr, new RegExp(`${path.replace(/\./g, '\\.')}:\\d+: 2[45]-word run, BIP-39 CHECKSUM-VALID`));
  assertNoWordsLeak(staged.stdout + staged.stderr, synthetic24);

  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-qm', 'shapes']);
  for (const args of [['--tracked'], ['--history', 'HEAD']]) {
    const result = runGuard(repo, args);
    assert.equal(result.status, 1, `${args[0]}: ${result.stderr}`);
    for (const path of Object.keys(files)) assert.match(result.stderr, new RegExp(path.replace(/\./g, '\\.')), `${args[0]} ${path}`);
    assertNoWordsLeak(result.stdout + result.stderr, synthetic24);
  }
});

test('an embedded copy of the wordlist is not a phrase (NUL-separated, length-prefixed, or in order)', () => {
  const nulSeparated = Buffer.from(WORDLIST.slice(0, 400).join('\0'));
  // A compiled module's string table: each word prefixed by its length byte
  // (3..8, all C0 controls), shuffled so the ascending-order rule cannot help.
  const shuffled = WORDLIST.slice(0, 400).map((w, i) => WORDLIST[(i * 769) % 2048]);
  const lengthPrefixed = Buffer.concat(shuffled.map((w) => Buffer.concat([Buffer.from([w.length]), Buffer.from(w)])));
  const sortedRun = WORDLIST.slice(100, 140).join(' ');
  assert.deepEqual(findMnemonicRuns(nulSeparated, WORDLIST), []);
  assert.deepEqual(findMnemonicRuns(lengthPrefixed, WORDLIST), []);
  assert.deepEqual(findMnemonicRuns(sortedRun, WORDLIST), []);
});

test('a NUL or other control byte inside a phrase ends the run', () => {
  const words = synthetic24.split(' ');
  for (const byte of [0x00, 0x01, 0x1b, 0x7f]) {
    const split = Buffer.concat([
      Buffer.from(words.slice(0, 8).join(' ')),
      Buffer.from([byte]),
      Buffer.from(words.slice(8, 16).join(' ')),
      Buffer.from([byte]),
      Buffer.from(words.slice(16).join(' ')),
    ]);
    assert.deepEqual(findMnemonicRuns(split, WORDLIST), [], `byte 0x${byte.toString(16)}`);
  }
});

// ---------------------------------------------------------------------------
// The guard, end to end in a scratch repository
// ---------------------------------------------------------------------------

test('--tracked catches the leaked file pattern in a COMMITTED file the staged check never saw', () => {
  const repo = createTempRepo();
  writeFile(repo, 'sdn-js/verify-identity.mjs', leakedFileShape(synthetic24));
  git(repo, ['add', '.']);
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-qm', 'leak']);

  const staged = runGuard(repo);
  assert.equal(staged.status, 0, 'nothing is staged, so the staged check passes — the SEC-001 blind spot');

  const tracked = runGuard(repo, ['--tracked']);
  const out = tracked.stdout + tracked.stderr;
  assert.equal(tracked.status, 1, out);
  assert.match(out, /sdn-js\/verify-identity\.mjs:3: 24-word run, BIP-39 CHECKSUM-VALID/);
  assertNoWordsLeak(out, synthetic24);
});

test('--history catches a phrase a later commit deleted, naming the commit, never the words', () => {
  const repo = createTempRepo();
  writeFile(repo, 'sdn-js/verify-identity.mjs', leakedFileShape(synthetic24));
  git(repo, ['add', '.']);
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-qm', 'leak']);
  const leakCommit = git(repo, ['rev-parse', 'HEAD']).stdout.trim();
  git(repo, ['rm', '-q', 'sdn-js/verify-identity.mjs']);
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-qm', 'delete']);

  assert.equal(runGuard(repo, ['--tracked']).status, 0, 'deleting the file clears the tree');
  const history = runGuard(repo, ['--history', 'HEAD']);
  const out = history.stdout + history.stderr;
  assert.equal(history.status, 1, out);
  assert.match(out, new RegExp(`${leakCommit.slice(0, 12)} sdn-js/verify-identity\\.mjs`));
  assertNoWordsLeak(out, synthetic24);

  // Recorded as a known exposure, it warns; the post-purge --strict audit fails.
  writeFile(repo, 'scripts/mnemonic-scan-allowlist.txt', `exposed  commit:${leakCommit}:sdn-js/verify-identity.mjs\n`);
  const warned = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(warned.status, 0, warned.stderr);
  assert.match(warned.stderr, /KNOWN EXPOSURE/);
  assert.equal(runGuard(repo, ['--history', '--strict', 'HEAD']).status, 1);
});

test('blocks a staged public BIP-39 test phrase and does not leak it', () => {
  const repo = createTempRepo();
  writeStagedFile(repo, 'config/oops-wallet.env', `MNEMONIC="${publicTestMnemonic}"\n`);

  const result = runGuard(repo);
  const combined = `${result.stdout}${result.stderr}`;
  assert.notEqual(result.status, 0, combined);
  assert.match(combined, /config\/oops-wallet\.env/);
  assert.match(combined, /BLOCKED/);
  assert.doesNotMatch(combined, new RegExp(publicTestMnemonic));
  assert.doesNotMatch(combined, /abandon abandon abandon/);
});

test('a fingerprint on the allowlist passes only in the file it names', () => {
  const repo = createTempRepo();
  writeStagedFile(repo, 'fixtures/vector.txt', `${publicTestMnemonic}\n`);
  const blocked = runGuard(repo);
  assert.equal(blocked.status, 1);
  const fingerprint = /fingerprint ([0-9a-f]{16})/.exec(blocked.stderr)[1];

  writeFile(repo, 'scripts/mnemonic-scan-allowlist.txt', `${fingerprint}  fixtures/vector.txt  # public vector\n`);
  assert.equal(runGuard(repo).status, 0);
  writeStagedFile(repo, 'elsewhere/vector.txt', `${publicTestMnemonic}\n`);
  assert.equal(runGuard(repo).status, 1, 'the same phrase in another file is still blocked');
});

test('passes an innocent staged file', () => {
  const repo = createTempRepo();
  writeStagedFile(repo, 'README.md', 'This project has nothing to hide here.\n');
  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('passes when nothing is staged', () => {
  const repo = createTempRepo();
  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('does not self-trigger on its own vendored wordlist file', () => {
  const repo = createTempRepo();
  git(repo, ['add', 'scripts/wordlists/bip39-english-wordlist.txt']);
  assert.equal(runGuard(repo).status, 0);
  assert.equal(runGuard(repo, ['--tracked']).status, 0);
});

test('blocks a mnemonic marker inside a staged .env file', () => {
  const repo = createTempRepo();
  writeStagedFile(repo, 'config/some-wallet.env', 'mnemonic=whatever-placeholder\n');
  const result = runGuard(repo);
  const combined = result.stdout + result.stderr;
  assert.notEqual(result.status, 0, combined);
  assert.match(combined, /config\/some-wallet\.env/);
});

test('allows a run of BIP-39 words shorter than the minimum mnemonic length', () => {
  const repo = createTempRepo();
  writeStagedFile(repo, 'notes.txt', `${Array(10).fill('abandon').concat(['art']).join(' ')}\n`);
  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

// Distinctness filter (U1.2): minified wallet JS tokenizes into long runs of
// REPEATED wordlist words that are code identifiers, not secrets.
test('allows repetitive code-token runs (minified-JS false positive)', () => {
  const repo = createTempRepo();
  const minifiedish = 'return base.key.curve(this.base.key.curve),base.key.depth(this.base.key.depth,base.key)';
  const durations = 'candidates := []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour}';
  writeStagedFile(repo, 'bundle.html', `${minifiedish}\n`);
  writeStagedFile(repo, 'presets.go', `${durations}\n`);
  const result = runGuard(repo);
  assert.equal(result.status, 0, result.stdout + result.stderr);
});

test('still blocks a distinct-word mnemonic-shaped run (not checksum-valid)', () => {
  const repo = createTempRepo();
  // 'qq' separators keep THIS source file free of an adjacent 12-word run.
  const distinctWords = [
    'legal', 'qq', 'winner', 'qq', 'thank', 'qq', 'year', 'qq', 'wave', 'qq',
    'sausage', 'qq', 'worth', 'qq', 'useful', 'qq', 'lawsuit', 'qq', 'ugly',
    'qq', 'theme', 'qq', 'zone',
  ].filter((w) => w !== 'qq').join(' ');
  writeStagedFile(repo, 'notes.txt', `${distinctWords}\n`);
  assert.notEqual(runGuard(repo).status, 0);
});

test('still blocks the all-abandon public test vector', () => {
  const repo = createTempRepo();
  writeStagedFile(repo, 'notes.txt', `${publicTestMnemonic}\n`);
  assert.notEqual(runGuard(repo).status, 0);
});

test('this repository: no tracked file holds an unreviewed phrase', () => {
  const result = spawnSync(process.execPath, [join(repoRoot, 'scripts/check-no-mnemonics.mjs'), '--tracked'], {
    cwd: repoRoot,
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stderr);
});

// ---------------------------------------------------------------------------
// Verifier NB2 (round 3): a repeated key between the words, encoded
// separators, base64, UTF-16, and the history parser's holes.
// ---------------------------------------------------------------------------
function keyedShapes(phrase) {
  const w = phrase.split(' ');
  return {
    'XML <item> list': ['<items>', ...w.map((x) => `  <item>${x}</item>`), '</items>'].join('\n'),
    'JSON [{"index":i,"text":w}]': JSON.stringify(w.map((x, i) => ({ index: i, text: x }))),
    'JSON {"word1": w}': JSON.stringify(Object.fromEntries(w.map((x, i) => [`word${i + 1}`, x])), null, 2),
    'YAML word01: lines': w.map((x, i) => `word${String(i + 1).padStart(2, '0')}: ${x}`).join('\n'),
    'Go {Name: w}': ['var words = []entry{', ...w.map((x) => `\t{Name: "${x}"},`), '}'].join('\n'),
    // keys that are NOT wordlist words
    'JSON {"value": w}': JSON.stringify(w.map((x) => ({ value: x }))),
    'HTML <span> list': w.map((x) => `<span>${x}</span>`).join(''),
  };
}

function encodedShapes(phrase) {
  const w = phrase.split(' ');
  return {
    'escaped \\n in a string': `const s = "${w.join('\\n')}";`,
    'escaped \\t in a string': `const s = "${w.join('\\t')}";`,
    'escaped \\r\\n in a string': `const s = "${w.join('\\r\\n')}";`,
    'HTML <li> list': `<ul>${w.map((x) => `<li>${x}</li>`).join('')}</ul>`,
    'HTML tags between words': w.map((x, i) => (i % 2 ? `<b>${x}</b>` : x)).join(''),
    'percent-encoded %2C': w.join('%2C'),
    'percent-encoded %0A': `seed=${w.join('%0A')}`,
    '&nbsp; entities': w.join('&nbsp;'),
    '\\x20 escapes': `"${w.join('\\x20')}"`,
    '&#32; entities': w.join('&#32;'),
  };
}

test('NB2: a key repeated between the words (wordlist keys or not) is still caught, checksum and all', () => {
  for (const [name, content] of Object.entries(keyedShapes(synthetic24))) {
    const runs = findMnemonicRuns(content, WORDLIST);
    assert.ok(runs.length >= 1, `${name}: missed`);
    assert.ok(runs.some((r) => r.checksumValid), `${name}: no checksum-valid run`);
  }
});

test('NB2: separators encoded inside strings, markup or URLs are decoded', () => {
  for (const [name, content] of Object.entries(encodedShapes(synthetic24))) {
    const runs = findMnemonicRuns(content, WORDLIST);
    assert.ok(runs.some((r) => r.checksumValid && r.words >= 24), `${name}: missed`);
  }
});

test('NB2: base64 values (a Kubernetes Secret, base64url) and UTF-16 (LE, BE, with BOM) are decoded', () => {
  const b64 = Buffer.from(synthetic24).toString('base64');
  const secret = ['apiVersion: v1', 'kind: Secret', 'metadata:', '  name: node-seed', 'type: Opaque', 'data:', `  mnemonic: ${b64}`, ''].join('\n');
  const url = `token=${Buffer.from(`seed: ${synthetic24}`).toString('base64url')}`;
  const le = Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(`mnemonic = "${synthetic24}"\r\n`, 'utf16le')]);
  const be = Buffer.from(Buffer.from(`${synthetic24}\n`, 'utf16le')).swap16();
  for (const [name, content, line] of [['k8s Secret', secret, 7], ['base64url', url, 1], ['UTF-16LE + BOM', le, 1], ['UTF-16BE', be, 1]]) {
    const runs = findMnemonicRuns(content, WORDLIST);
    const hit = runs.find((r) => r.checksumValid);
    assert.ok(hit, `${name}: missed`);
    assert.equal(hit.line, line, `${name}: line`);
  }
});

test('NB2: no regression on the round-1 shapes, and sorted wordlist copies stay unflagged', () => {
  for (const [name, content] of Object.entries(shapesTheOldGuardCaught(synthetic24))) {
    assert.ok(findMnemonicRuns(content, WORDLIST).some((r) => r.checksumValid), name);
  }
  const sortedXml = WORDLIST.slice(0, 200).map((w) => `<item>${w}</item>`).join('\n');
  const sortedYaml = WORDLIST.slice(300, 500).map((w, i) => `word${i}: ${w}`).join('\n');
  assert.deepEqual(findMnemonicRuns(sortedXml, WORDLIST), []);
  assert.deepEqual(findMnemonicRuns(sortedYaml, WORDLIST), []);
});

test('NB2: every mode catches the keyed, encoded, base64 and UTF-16 shapes, printing no word', () => {
  const repo = createTempRepo();
  const files = {
    'fixtures/items.xml': keyedShapes(synthetic24)['XML <item> list'],
    'fixtures/words.yaml': keyedShapes(synthetic24)['YAML word01: lines'],
    'fixtures/escaped.js': encodedShapes(synthetic24)['escaped \\n in a string'],
    'k8s/secret.yaml': `data:\n  mnemonic: ${Buffer.from(synthetic24).toString('base64')}\n`,
    'notes/utf16.txt': Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(synthetic24, 'utf16le')]),
  };
  for (const [path, content] of Object.entries(files)) writeStagedFile(repo, path, content);
  const staged = runGuard(repo);
  assert.equal(staged.status, 1, staged.stderr);
  for (const path of Object.keys(files)) assert.match(staged.stderr, new RegExp(path.replace(/\./g, '\\.')), `--staged ${path}`);
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-qm', 'shapes']);
  for (const args of [['--tracked'], ['--history', 'HEAD']]) {
    const result = runGuard(repo, args);
    assert.equal(result.status, 1, `${args[0]}: ${result.stderr}`);
    for (const path of Object.keys(files)) assert.match(result.stderr, new RegExp(path.replace(/\./g, '\\.')), `${args[0]} ${path}`);
    assertNoWordsLeak(result.stdout + result.stderr, synthetic24);
  }
});

function commitAll(repo, message, extra = []) {
  git(repo, ['add', '-A']);
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-q', ...extra, '-m', message]);
}

test('NB2 history: a quoted filename is scanned', () => {
  const repo = createTempRepo();
  writeFile(repo, 'odd "name"\tfile.txt', `${synthetic24}\n`);
  commitAll(repo, 'quoted');
  const result = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /odd "name"\tfile\.txt \(added line 1\)/);
});

test('NB2 history: an added line starting "++ " does not end the file', () => {
  const repo = createTempRepo();
  writeFile(repo, 'notes/log.txt', ['++ a line that looks like a diff header', 'xyzzy plugh', `${synthetic24}`, ''].join('\n'));
  commitAll(repo, 'plusplus');
  const result = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /notes\/log\.txt \(added line 3\)/);
});

test('NB2 history: diff.noprefix and diff.dstPrefix in the repo config cannot blind the scan', () => {
  for (const config of [['diff.noprefix', 'true'], ['diff.dstPrefix', 'zz/'], ['diff.mnemonicPrefix', 'true']]) {
    const repo = createTempRepo();
    git(repo, ['config', ...config]);
    writeFile(repo, 'leak.txt', `${synthetic24}\n`);
    commitAll(repo, 'leak');
    const result = runGuard(repo, ['--history', 'HEAD']);
    assert.equal(result.status, 1, `${config.join('=')}: ${result.stderr}`);
    assert.match(result.stderr, /leak\.txt \(added line 1\)/, config.join('='));
  }
});

test('NB2 history: commit messages and annotated tag messages are scanned', () => {
  const repo = createTempRepo();
  writeFile(repo, 'README.md', 'innocent\n');
  commitAll(repo, `release notes\n\nrecovery: ${synthetic24}`);
  const commitHit = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(commitHit.status, 1, commitHit.stderr);
  assert.match(commitHit.stderr, /\(commit message\)/);

  const tagged = createTempRepo();
  writeFile(tagged, 'README.md', 'innocent\n');
  commitAll(tagged, 'plain');
  git(tagged, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'tag', '-a', 'v1', '-m', `seed ${synthetic24}`]);
  assert.equal(runGuard(tagged, ['--history', 'HEAD']).status, 0, 'a plain range does not read tags');
  const tagHit = runGuard(tagged, ['--history', '--all']);
  assert.equal(tagHit.status, 1, tagHit.stderr);
  assert.match(tagHit.stderr, /\(tag message refs\/tags\/v1\)/);
  assertNoWordsLeak(tagHit.stdout + tagHit.stderr, synthetic24);
});

test('NB2 history: content only a merge commit introduced (an evil merge) is scanned', () => {
  const repo = createTempRepo();
  writeFile(repo, 'a.txt', 'base\n');
  commitAll(repo, 'base');
  git(repo, ['checkout', '-q', '-b', 'side']);
  writeFile(repo, 'b.txt', 'side\n');
  commitAll(repo, 'side');
  git(repo, ['checkout', '-q', '-']);
  writeFile(repo, 'c.txt', 'main\n');
  commitAll(repo, 'main');
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'merge', '-q', '--no-commit', '--no-ff', 'side']);
  writeFile(repo, 'a.txt', `base\n${synthetic24}\n`);
  git(repo, ['add', 'a.txt']);
  git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'commit', '-qm', 'merge side']);
  const merge = git(repo, ['rev-parse', 'HEAD']).stdout.trim();
  const result = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, new RegExp(`${merge.slice(0, 12)} a\\.txt`));
});

// ---------------------------------------------------------------------------
// Verifier r3: the remaining holes, each with a test.
// ---------------------------------------------------------------------------
test('r3: line-wrapped base64 (76 and 64 columns, a PEM body) and base64 misaligned by 1-5 characters are decoded', () => {
  const b64 = Buffer.from(`recovery phrase: ${synthetic24}\n`).toString('base64');
  const wrap = (text, cols) => text.match(new RegExp(`.{1,${cols}}`, 'g')).join('\n');
  const shapes = {
    'MIME 76 columns': `Content-Transfer-Encoding: base64\n\n${wrap(b64, 76)}\n`,
    '64 columns, indented': `data: |\n${wrap(b64, 64).replace(/^/gm, '    ')}\n`,
    'PEM body': `-----BEGIN SEED PHRASE-----\n${wrap(b64, 64)}\n-----END SEED PHRASE-----\n`,
  };
  for (const prefix of ['a', 'ab', 'abc', 'abcd', 'seedX']) shapes[`glued to "${prefix}"`] = `value=${prefix}${b64}`;
  for (const [name, content] of Object.entries(shapes)) {
    assert.ok(findMnemonicRuns(content, WORDLIST).some((r) => r.checksumValid), `${name}: missed`);
  }
});

test('r3: the vendored wordlist is scanned (a phrase in its comments) and must be the official list', () => {
  const official = readFileSync(join(repoRoot, 'scripts/wordlists/bip39-english-wordlist.txt'), 'utf8');
  const repo = createTempRepo();
  writeStagedFile(repo, 'scripts/wordlists/bip39-english-wordlist.txt', `# ${synthetic24}\n${official}`);
  const comment = runGuard(repo);
  assert.equal(comment.status, 1, comment.stderr);
  assert.match(comment.stderr, /bip39-english-wordlist\.txt:1: .*CHECKSUM-VALID/);
  assertNoWordsLeak(comment.stdout + comment.stderr, synthetic24);

  const lines = official.split('\n');
  const firstWord = lines.findIndex((l) => l && !l.startsWith('#'));
  [lines[firstWord], lines[firstWord + 1]] = [lines[firstWord + 1], lines[firstWord]];
  writeStagedFile(repo, 'scripts/wordlists/bip39-english-wordlist.txt', lines.join('\n'));
  const reordered = runGuard(repo, ['--staged']);
  assert.notEqual(reordered.status, 0);
  assert.match(reordered.stderr, /not the official BIP-39 English wordlist/);
});

test('r3 history: log.showRoot=false cannot hide the root commit, nor a replace ref the leak', () => {
  const repo = createTempRepo();
  git(repo, ['config', 'log.showRoot', 'false']);
  writeFile(repo, 'leak.txt', `${synthetic24}\n`);
  commitAll(repo, 'root commit with the leak');
  const root = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(root.status, 1, root.stderr);
  assert.match(root.stderr, /leak\.txt \(added line 1\)/);

  // Replace the leaking blob with an innocent one: git log would follow the replacement.
  const leakBlob = git(repo, ['rev-parse', 'HEAD:leak.txt']).stdout.trim();
  writeFileSync(join(repo, 'innocent.txt'), 'nothing to see\n');
  const innocent = git(repo, ['hash-object', '-w', 'innocent.txt']).stdout.trim();
  git(repo, ['replace', leakBlob, innocent]);
  const replaced = runGuard(repo, ['--history', 'HEAD']);
  assert.equal(replaced.status, 1, replaced.stderr);
  assert.match(replaced.stderr, /leak\.txt \(added line 1\)/);
});

test('r3 history: tags pointing at a blob or a tree are scanned with --all', () => {
  for (const kind of ['blob', 'tree', 'annotated-blob']) {
    const repo = createTempRepo();
    writeFile(repo, 'README.md', 'innocent\n');
    commitAll(repo, 'plain');
    writeFileSync(join(repo, 'hidden.txt'), `${synthetic24}\n`);
    const blob = git(repo, ['hash-object', '-w', 'hidden.txt']).stdout.trim();
    let target = blob;
    if (kind === 'tree') {
      target = spawnSync('git', ['mktree'], { cwd: repo, input: `100644 blob ${blob}\tseed.txt\n`, encoding: 'utf8' }).stdout.trim();
    }
    if (kind === 'annotated-blob') git(repo, ['-c', 'user.name=t', '-c', 'user.email=t@e', 'tag', '-a', 'stash', blob, '-m', 'innocent']);
    else git(repo, ['tag', 'stash', target]);
    rmSync(join(repo, 'hidden.txt'));
    assert.equal(runGuard(repo, ['--history', 'HEAD']).status, 0, `${kind}: no commit holds it`);
    const all = runGuard(repo, ['--history', '--all']);
    assert.equal(all.status, 1, `${kind}: ${all.stderr}`);
    assert.match(all.stderr, kind === 'tree' ? /\(tree tagged refs\/tags\/stash\) seed\.txt/ : /\(blob tagged refs\/tags\/stash\)/);
    assertNoWordsLeak(all.stdout + all.stderr, synthetic24);
  }
});

test('r3 --staged: a type change (symlink to file) is scanned', () => {
  const repo = createTempRepo();
  symlinkSync('README.md', join(repo, 'notes.txt'));
  writeFile(repo, 'README.md', 'innocent\n');
  commitAll(repo, 'a symlink');
  rmSync(join(repo, 'notes.txt'));
  writeStagedFile(repo, 'notes.txt', `${synthetic24}\n`);
  const result = runGuard(repo);
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /notes\.txt:1/);
});

// Verifier S8: the public-test-vector files are excused from the MNEMONIC
// pattern only. A PEM key, AWS key or GitHub token in them still fails the
// preflight's secret scan (step 2 runs before anything that needs the repo).
test('S8: oss-preflight excuses docker-compose.dev.yaml from the mnemonic pattern only', () => {
  const repo = createTempRepo();
  copyFileSync(join(repoRoot, 'scripts/oss-preflight.sh'), join(repo, 'scripts/oss-preflight.sh'));
  const step2 = () => {
    const out = spawnSync('bash', ['scripts/oss-preflight.sh'], { cwd: repo, encoding: 'utf8' }).stdout;
    return out.slice(out.indexOf('2/11'), out.indexOf('3/11'));
  };
  writeStagedFile(repo, 'docker-compose.dev.yaml', `services:\n  x:\n    environment:\n      MNEMONIC: "${publicTestMnemonic}"\n`);
  assert.match(step2(), /PASS: no high-risk secret patterns/);
  // Built at runtime so this file never holds the token shape itself.
  const awsShaped = ['AK', 'IA', 'Q'.repeat(16)].join('');
  writeStagedFile(repo, 'docker-compose.dev.yaml', `services:\n  x:\n    environment:\n      KEY: ${awsShaped}\n`);
  const failed = step2();
  assert.match(failed, /FAIL: possible secret material detected/);
  assert.match(failed, /docker-compose\.dev\.yaml:4/);
  assert.doesNotMatch(failed, new RegExp(awsShaped), 'the matched text is withheld');
});

function createTempRepo() {
  const repo = mkdtempSync(join(tmpdir(), 'sdn-check-no-mnemonics-'));
  mkdirSync(join(repo, 'scripts/wordlists'), { recursive: true });
  for (const file of ['scripts/check-no-mnemonics.sh', 'scripts/check-no-mnemonics.mjs']) {
    copyFileSync(join(repoRoot, file), join(repo, file));
    chmodSync(join(repo, file), 0o755);
  }
  writeFileSync(
    join(repo, 'scripts/wordlists/bip39-english-wordlist.txt'),
    readFileSync(join(repoRoot, 'scripts/wordlists/bip39-english-wordlist.txt')),
  );
  git(repo, ['init', '-q']);
  return repo;
}

function writeFile(repo, relativePath, contents) {
  const absolutePath = join(repo, relativePath);
  mkdirSync(dirname(absolutePath), { recursive: true });
  writeFileSync(absolutePath, contents);
}

function writeStagedFile(repo, relativePath, contents) {
  writeFile(repo, relativePath, contents);
  git(repo, ['add', relativePath]);
}

function runGuard(repo, args = []) {
  return spawnSync('./scripts/check-no-mnemonics.sh', args, { cwd: repo, encoding: 'utf8' });
}

function git(repo, args) {
  const result = spawnSync('git', args, { cwd: repo, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  return result;
}
