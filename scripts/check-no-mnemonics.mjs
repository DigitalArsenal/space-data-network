#!/usr/bin/env node
// check-no-mnemonics.mjs — find BIP-39 mnemonic phrases in this repository.
//
//   node scripts/check-no-mnemonics.mjs --staged            # pre-commit (.husky/pre-commit)
//   node scripts/check-no-mnemonics.mjs --tracked           # every tracked file (CI: oss-preflight)
//   node scripts/check-no-mnemonics.mjs --history <range>   # every line ADDED by the commits in <range>
//   node scripts/check-no-mnemonics.mjs --history --strict --all   # post-purge audit: known exposures fail too
//
// SEC-001: a real 24-word node seed sat in sdn-js/verify-identity.mjs, in the
// public repository, because the old guard only looked at STAGED content and
// only ran where the commit hook was installed. This scanner covers the index,
// the whole tracked tree, and git history, in ANY file type (text or binary:
// letters are tokenised from the raw bytes), and CI runs the tracked and
// history modes through scripts/oss-preflight.sh.
//
// WHAT IT FLAGS. A run of 12+ consecutive BIP-39 wordlist words — real phrases
// are 12/15/18/21/24 words, and 12 is the minimum — that is mnemonic-shaped:
//   - at least 9 DISTINCT words in some 12-word window (independent draws
//     from 2048 words almost never repeat that much; minified code tokens
//     like "base key curve this base key" do), or the degenerate all-zero
//     test vector ("abandon" x11), and
//   - NOT in strictly ascending order (an embedded copy of the wordlist itself,
//     e.g. inside a wasm module, is sorted; a random phrase is sorted with
//     probability 1/12!).
// A window that also passes the BIP-39 checksum is reported as CHECKSUM-VALID.
//
// It NEVER prints the matched words: only the path (and commit), the line
// number, the run length and a sha256 fingerprint of the run, which is what
// scripts/mnemonic-scan-allowlist.txt records for a known PUBLIC test vector or
// a reviewed false positive.
import { execFileSync, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
export const WORDLIST_REL = 'scripts/wordlists/bip39-english-wordlist.txt';
export const ALLOWLIST_REL = 'scripts/mnemonic-scan-allowlist.txt';

const MIN_WORDS = 12;
const MIN_DISTINCT = 9;
const VALID_LENGTHS = [24, 21, 18, 15, 12];

export function loadWordlist(path = join(HERE, 'wordlists', 'bip39-english-wordlist.txt')) {
  const words = readFileSync(path, 'utf8')
    .split('\n')
    .map((w) => w.trim())
    .filter((w) => w && !w.startsWith('#'));
  if (words.length !== 2048) throw new Error(`BIP-39 wordlist has ${words.length} words, want 2048`);
  return words;
}

/**
 * Parse the allowlist: `<sha256> <path or *>  # reason` per line. A path of
 * `*` allows a fingerprint anywhere (a public test vector); otherwise only in
 * that file. History entries use `commit:<sha>:<path>` with the fingerprint `*`.
 */
export function loadAllowlist(path) {
  const entries = [];
  let text = '';
  try {
    text = readFileSync(path, 'utf8');
  } catch {
    return entries;
  }
  for (const raw of text.split('\n')) {
    const line = raw.replace(/#.*/, '').trim();
    if (!line) continue;
    const [fingerprint, where] = line.split(/\s+/);
    if (fingerprint && where) entries.push({ fingerprint, where });
  }
  return entries;
}

/**
 * `exposed commit:<sha>:<path>` entries name REAL or unconfirmed phrases that
 * history already published and the purge (ROTATION.md) will remove. They are
 * never "allowed": history mode reports them as known exposures, and
 * --strict (the post-purge verification) fails on them.
 */
function isKnownExposure(allowlist, { path, commit }) {
  return Boolean(commit) && allowlist.some((e) => e.fingerprint === 'exposed' && e.where === `commit:${commit}:${path}`);
}

function fingerprintMatches(entry, fingerprint) {
  // Entries record the 16-hex-digit prefix the report prints (64 bits).
  return /^[0-9a-f]{16,64}$/.test(entry) && fingerprint.startsWith(entry);
}

function isAllowed(allowlist, { fingerprint, path, commit }) {
  return allowlist.some(
    (e) =>
      (fingerprintMatches(e.fingerprint, fingerprint) && (e.where === '*' || e.where === path)) ||
      (commit && e.fingerprint === '*' && e.where === `commit:${commit}:${path}`),
  );
}

function checksumValid(words, index) {
  const bits = words.map((w) => index.get(w).toString(2).padStart(11, '0')).join('');
  const entropyBits = (words.length * 11 * 32) / 33;
  const entropy = Buffer.alloc(entropyBits / 8);
  for (let i = 0; i < entropy.length; i++) entropy[i] = Number.parseInt(bits.slice(i * 8, i * 8 + 8), 2);
  const checksum = createHash('sha256').update(entropy).digest()[0].toString(2).padStart(8, '0');
  return bits.slice(entropyBits) === checksum.slice(0, words.length / 3);
}

function windowFlags(run, index) {
  let shaped = false;
  let valid = false;
  for (let start = 0; start + MIN_WORDS <= run.length; start++) {
    const window = run.slice(start, start + MIN_WORDS).map((t) => t.word);
    const distinct = new Set(window).size;
    const abandons = window.filter((w) => w === 'abandon').length;
    const ascending = window.every((w, i) => i === 0 || window[i - 1] < w);
    if (ascending) continue;
    if (distinct >= MIN_DISTINCT || abandons >= MIN_WORDS - 1) {
      shaped = true;
      for (const length of VALID_LENGTHS) {
        if (start + length > run.length) continue;
        if (checksumValid(run.slice(start, start + length).map((t) => t.word), index)) {
          valid = true;
          break;
        }
      }
      if (valid) break;
    }
  }
  return { shaped, valid };
}

/**
 * Mnemonic-shaped runs in a buffer or string. Returns [{line, words,
 * checksumValid, fingerprint}], never the words themselves.
 */
export function findMnemonicRuns(content, wordlist) {
  const set = wordlist instanceof Map ? wordlist : new Map(wordlist.map((w, i) => [w, i]));
  const text = Buffer.isBuffer(content) ? content.toString('latin1') : String(content);
  const findings = [];
  let run = [];
  const flush = () => {
    if (run.length >= MIN_WORDS) {
      const { shaped, valid } = windowFlags(run, set);
      if (shaped) {
        findings.push({
          line: run[0].line,
          words: run.length,
          checksumValid: valid,
          fingerprint: createHash('sha256').update(run.map((t) => t.word).join(' ')).digest('hex'),
        });
      }
    }
    run = [];
  };
  let line = 1;
  const re = /[A-Za-z]+|\n|[^A-Za-z\n]+/g;
  let match;
  while ((match = re.exec(text)) !== null) {
    const token = match[0];
    if (token === '\n') {
      line += 1;
      continue;
    }
    if (/^[A-Za-z]+$/.test(token)) {
      const word = token.toLowerCase();
      if (set.has(word)) {
        run.push({ word, line });
        continue;
      }
      flush();
      continue;
    }
    // A separator joins two words only when it is short printable text — a
    // space, `", "`, ` 7. ` in a numbered list. Anything else (a NUL, other
    // binary bytes, a long gap) ends the run: compiled modules embed wordlists
    // as NUL-separated tables, which are not phrases.
    if (token.length > 8 || /[^\x20-\x7e\t\r]/.test(token)) flush();
    for (const ch of token) if (ch === '\n') line += 1;
  }
  flush();
  return findings;
}

function git(args, options = {}) {
  return execFileSync('git', args, { maxBuffer: 1 << 30, ...options });
}

function listStaged() {
  return git(['diff', '--cached', '--name-only', '--diff-filter=ACMR', '-z'], { encoding: 'utf8' })
    .split('\0')
    .filter(Boolean);
}

export function listTracked() {
  return git(['ls-files', '-z'], { encoding: 'utf8' }).split('\0').filter(Boolean);
}

/**
 * The index blobs of the given paths, read in one `git cat-file --batch`
 * process. Gitlinks (submodules) have no blob in this repository and are
 * skipped.
 */
export function* indexBlobs(paths = null) {
  // null = every index entry; [] = none (`git ls-files --` alone lists everything).
  if (paths && paths.length === 0) return;
  const wanted = paths ? new Set(paths) : null;
  const stage = git(['ls-files', '-s', '-z'], { encoding: 'utf8' })
    .split('\0')
    .filter(Boolean)
    .map((entry) => {
      const [meta, path] = entry.split('\t');
      const [mode, oid] = meta.split(' ');
      return { mode, oid, path };
    })
    .filter((e) => e.mode !== '160000' && (!wanted || wanted.has(e.path)));
  const BATCH = 2000;
  for (let i = 0; i < stage.length; i += BATCH) {
    const chunk = stage.slice(i, i + BATCH);
    const out = git(['cat-file', '--batch'], { input: Buffer.from(chunk.map((e) => e.oid).join('\n') + '\n') });
    let offset = 0;
    for (const entry of chunk) {
      const headerEnd = out.indexOf(0x0a, offset);
      const header = out.subarray(offset, headerEnd).toString('latin1').split(' ');
      const size = Number(header[2]);
      const start = headerEnd + 1;
      yield { path: entry.path, content: out.subarray(start, start + size) };
      offset = start + size + 1;
    }
  }
}

/**
 * Lines added by each commit in a range, grouped per (commit, path). Streamed:
 * a full history's patch text is far larger than one string can hold.
 */
async function* historyAdditions(range) {
  const child = spawn(
    'git',
    ['log', '-p', '--no-color', '--no-ext-diff', '--text', '--format=commit %H', ...range.split(/\s+/).filter(Boolean)],
    { stdio: ['ignore', 'pipe', 'inherit'] },
  );
  child.stdout.setEncoding('latin1');
  const exited = new Promise((resolveExit) => child.on('close', resolveExit));
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  let commit = '';
  let path = '';
  let added = [];
  const emit = () => (commit && path && added.length ? { commit, path, content: added.join('\n') } : null);
  for await (const line of lines) {
    if (/^commit [0-9a-f]{40}$/.test(line)) {
      const e = emit();
      if (e) yield e;
      commit = line.slice(7);
      path = '';
      added = [];
    } else if (line.startsWith('+++ ')) {
      const e = emit();
      if (e) yield e;
      path = line.startsWith('+++ b/') ? line.slice(6) : '';
      added = [];
    } else if (line.startsWith('+') && path) {
      added.push(line.slice(1));
    }
  }
  const e = emit();
  if (e) yield e;
  const code = await exited;
  if (code !== 0) throw new Error(`git log ${range} exited ${code}`);
}

// .env-style files: a mnemonic/seed_phrase key, an xprv, or a PEM private key
// is a secret whatever its words (the pre-SEC-001 guard's second check).
function isEnvLike(path) {
  const base = path.split('/').pop();
  return /^\.env(\..*)?$/.test(base) || /\.env(\.local|\.production)?$/.test(base);
}

const ENV_SECRET_MARKERS =
  /-----BEGIN [A-Z ]*PRIVATE KEY-----|xprv[0-9A-Za-z]{10,}|(^|[^a-z_])mnemonic([^a-z_]|$)|(^|[^a-z_])seed_phrase([^a-z_]|$)/i;

function describe(f) {
  return `${f.words}-word run${f.checksumValid ? ', BIP-39 CHECKSUM-VALID' : ''}, fingerprint ${f.fingerprint.slice(0, 16)}`;
}

export async function main(argv = process.argv.slice(2)) {
  const root = execFileSync('git', ['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim();
  process.chdir(root);
  const wordlist = new Map(loadWordlist(join(root, WORDLIST_REL)).map((w, i) => [w, i]));
  const allowlist = loadAllowlist(join(root, ALLOWLIST_REL));
  const mode = argv[0] || '--staged';
  const hits = [];

  if (mode === '--staged' || mode === '--tracked') {
    const paths = mode === '--staged' ? listStaged() : null;
    for (const { path, content } of indexBlobs(paths)) {
      if (path === WORDLIST_REL) continue;
      if (isEnvLike(path) && ENV_SECRET_MARKERS.test(content.toString('latin1'))) {
        hits.push(`${path}: env-style file holds a mnemonic/seed_phrase/xprv/PRIVATE KEY marker`);
      }
      for (const f of findMnemonicRuns(content, wordlist)) {
        if (!isAllowed(allowlist, { fingerprint: f.fingerprint, path })) hits.push(`${path}:${f.line}: ${describe(f)}`);
      }
    }
  } else if (mode === '--history') {
    const strict = argv.includes('--strict');
    const range = argv.slice(1).filter((a) => a !== '--strict').join(' ') || 'HEAD';
    const exposures = [];
    for await (const { commit, path, content } of historyAdditions(range)) {
      if (path === WORDLIST_REL) continue;
      for (const f of findMnemonicRuns(content, wordlist)) {
        if (isAllowed(allowlist, { fingerprint: f.fingerprint, path, commit })) continue;
        const where = `${commit.slice(0, 12)} ${path} (added line ${f.line}): ${describe(f)}`;
        if (!strict && isKnownExposure(allowlist, { path, commit })) exposures.push(where);
        else hits.push(where);
      }
    }
    if (exposures.length) {
      console.error(`[check-no-mnemonics] ${exposures.length} KNOWN EXPOSURE(S) in history, awaiting the purge (deployment/identity/ROTATION.md):`);
      for (const e of exposures) console.error(`  ${e}`);
    }
  } else {
    console.error('usage: check-no-mnemonics.mjs [--staged | --tracked | --history [--strict] <rev-range>]');
    return 2;
  }

  if (hits.length) {
    console.error(`[check-no-mnemonics] BLOCKED (${mode}): mnemonic-shaped BIP-39 word runs found. The words are not shown.`);
    for (const hit of hits) console.error(`  ${hit}`);
    console.error(
      '[check-no-mnemonics] Remove the phrase. A real seed that reached git is COMPROMISED: rotate it ' +
        '(deployment/identity/ROTATION.md) — deleting the line does not un-publish it. A public test vector or a ' +
        `reviewed false positive is allowlisted by fingerprint in ${ALLOWLIST_REL}.`,
    );
    return 1;
  }
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(
    (code) => process.exit(code),
    (error) => {
      console.error(`[check-no-mnemonics] ${error.message}`);
      process.exit(2);
    },
  );
}
