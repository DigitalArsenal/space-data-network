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
// The only bytes that end a run of wordlist words: NUL and the other C0
// controls except whitespace (\t \n \v \f \r), and DEL.
const RUN_BREAK = /[\x00-\x08\x0e-\x1f\x7f]/;

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

/** A sorted copy of the wordlist (a dictionary table), not a phrase. */
function mostlyAscending(words) {
  if (words.length < MIN_WORDS) return false;
  let up = 0;
  for (let i = 1; i < words.length; i++) if (words[i - 1] < words[i]) up += 1;
  return up / (words.length - 1) >= 0.9;
}

/**
 * The run with its repeated "key" tokens removed (`item` in an XML list,
 * `index`/`text` in JSON objects, `word` in `word01:` YAML keys): any word
 * that occurs 3+ times. A real phrase repeats a word at most rarely.
 */
function keyStripped(words) {
  const counts = new Map();
  for (const w of words) counts.set(w, (counts.get(w) || 0) + 1);
  return words.filter((w) => counts.get(w) < 3);
}

/** A checksum-valid 12..24-word window anywhere in `words` that is not sorted and has 9+ distinct words. */
function hasValidWindow(words, index) {
  for (let start = 0; start + MIN_WORDS <= words.length; start++) {
    for (const length of VALID_LENGTHS) {
      if (start + length > words.length) continue;
      const window = words.slice(start, start + length);
      if (new Set(window).size < MIN_DISTINCT && window.filter((w) => w === 'abandon').length < MIN_WORDS - 1) continue;
      if (window.every((w, i) => i === 0 || window[i - 1] < w)) continue;
      if (checksumValid(window, index)) return true;
    }
  }
  return false;
}

/**
 * Verifier NB2: the windowed rule alone (9 distinct in every 12-word window)
 * is defeated by a key repeated between the words. The pre-SEC-001 guard's
 * WHOLE-RUN rule — 12+ consecutive wordlist words with 9+ distinct among the
 * whole run, or "abandon" 11+ times — is restored beside it; a run that is a
 * sorted wordlist copy is still not a phrase.
 */
function runFlags(run, index) {
  let { shaped, valid } = windowFlags(run, index);
  const words = run.map((t) => t.word);
  if (!shaped && words.length >= MIN_WORDS) {
    const distinct = new Set(words).size;
    const abandons = words.filter((w) => w === 'abandon').length;
    if ((distinct >= MIN_DISTINCT && !mostlyAscending(keyStripped(words))) || abandons >= MIN_WORDS - 1) shaped = true;
  }
  if (shaped && !valid) valid = hasValidWindow(keyStripped(words), index);
  return { shaped, valid };
}

const fingerprintOf = (words) => createHash('sha256').update(words.join(' ')).digest('hex');

/**
 * Letter tokens of a text, as segments: a NUL or another C0/DEL control byte
 * (not \t \n \v \f \r) ends a segment. ANY run of non-letters between two
 * tokens of one segment joins them, as the pre-SEC-001 guard did (it turned
 * every non-letter into a space): a space, `", "`, a Go slice indented 12
 * spaces, a nested JSON array, an NBSP, a `•` bullet list. Compiled modules
 * embed wordlists as NUL- or length-byte-separated tables, which are not
 * phrases (verifier B1).
 */
function* segments(text) {
  let line = 1;
  let tokens = [];
  const re = /[A-Za-z]+|\n|[^A-Za-z\n]+/g;
  let match;
  while ((match = re.exec(text)) !== null) {
    const token = match[0];
    if (token === '\n') {
      line += 1;
    } else if (/^[A-Za-z]/.test(token)) {
      tokens.push({ word: token.toLowerCase(), line });
    } else {
      if (RUN_BREAK.test(token)) {
        if (tokens.length) yield tokens;
        tokens = [];
      }
      for (const ch of token) if (ch === '\n') line += 1;
    }
  }
  if (tokens.length) yield tokens;
}

/** Runs of consecutive wordlist words in a segment. */
function* wordlistRuns(tokens, index) {
  let run = [];
  for (const t of tokens) {
    if (index.has(t.word)) run.push(t);
    else {
      if (run.length >= MIN_WORDS) yield run;
      run = [];
    }
  }
  if (run.length >= MIN_WORDS) yield run;
}

/**
 * Records with constant keys that are NOT wordlist words — `{"value": w}`,
 * `<span>w</span>` — break a run at every key. Take every s-th token (s = 2..4)
 * where the tokens in between repeat identically record after record, and look
 * for a checksum-valid phrase in what is left.
 */
function* keyedRuns(tokens, index) {
  const sameBetween = (p, q, stride) => {
    for (let k = 1; k < stride; k++) {
      const a = tokens[p + k];
      const b = tokens[q + k];
      if (!a || !b || a.word !== b.word || index.has(a.word)) return false;
    }
    return true;
  };
  for (let stride = 2; stride <= 4; stride++) {
    for (let offset = 0; offset < stride; offset++) {
      let run = [];
      let first = -1;
      for (let p = offset; p < tokens.length; p += stride) {
        const t = tokens[p];
        const extends_ = index.has(t.word) && (first < 0 || sameBetween(first, p, stride));
        if (extends_) {
          if (first < 0) {
            // a record's keys must not be wordlist words themselves
            if (!sameBetween(p, p, stride)) continue;
            first = p;
          }
          run.push(t);
        } else {
          if (run.length >= MIN_WORDS) yield run;
          run = [];
          first = -1;
          if (index.has(t.word) && sameBetween(p, p, stride)) {
            first = p;
            run.push(t);
          }
        }
      }
      if (run.length >= MIN_WORDS) yield run;
    }
  }
}

// Encoded separators between words (verifier NB2): escapes inside string
// literals (\n \t \r\n \x20  ), percent-encoding (%2C %0A %20), HTML
// entities (&nbsp; &#32; &#x20;) and markup tags (<li>, <b>, <item>). They
// become a space, keeping real newlines so line numbers hold.
const ENCODED_SEPARATORS = /\\[nrtvf0]|\\x[0-9A-Fa-f]{2}|\\u[0-9A-Fa-f]{4}|%[0-9A-Fa-f]{2}|&#?[A-Za-z0-9]{1,8};|<\/?[A-Za-z][^<>\n]{0,80}>/g;

function decodeSeparators(text) {
  return text.replace(ENCODED_SEPARATORS, ' ');
}

function looksPrintable(buf) {
  if (buf.length < 40) return false;
  let ok = 0;
  for (const b of buf) if ((b >= 0x20 && b < 0x7f) || b === 0x0a || b === 0x0d || b === 0x09) ok += 1;
  return ok / buf.length >= 0.95;
}

/** Base64 and base64url values (a Kubernetes Secret's data, an env blob) that decode to text. */
function* base64Texts(text) {
  const re = /[A-Za-z0-9+/_-]{44,65536}={0,2}/g;
  let match;
  while ((match = re.exec(text)) !== null) {
    const value = match[0];
    const decoded = Buffer.from(value.replace(/-/g, '+').replace(/_/g, '/'), 'base64');
    if (!looksPrintable(decoded)) continue;
    let line = 1;
    for (let i = text.lastIndexOf('\n', match.index); i >= 0; i = text.lastIndexOf('\n', i - 1)) line += 1;
    yield { text: decoded.toString('latin1'), lineOffset: line - 1 };
  }
}

/**
 * UTF-16 text (LE or BE, with or without BOM): most code units in a sample are
 * printable ASCII with a zero high byte. Media and executables fail this
 * cheaply, so they are never decoded.
 */
function* utf16Texts(buf) {
  if (buf.length < 48) return;
  const sample = Math.min(buf.length, 65536) & ~1;
  let le = 0;
  let be = 0;
  for (let i = 0; i + 1 < sample; i += 2) {
    const a = buf[i];
    const b = buf[i + 1];
    if (b === 0 && ((a >= 0x20 && a < 0x7f) || a === 0x0a || a === 0x0d || a === 0x09)) le += 1;
    if (a === 0 && ((b >= 0x20 && b < 0x7f) || b === 0x0a || b === 0x0d || b === 0x09)) be += 1;
  }
  const pairs = sample / 2;
  const even = buf.subarray(0, buf.length & ~1);
  if (le / pairs >= 0.5) yield even.toString('utf16le');
  if (be / pairs >= 0.5) yield Buffer.from(even).swap16().toString('utf16le');
}

/** Text, as git judges it: no NUL in the first 8000 bytes. */
function isTextLike(buf) {
  return !buf.subarray(0, 8000).includes(0);
}

function scanText(text, index, { encoding = '', needValid = false, lineOffset = 0, keyed = true } = {}, findings, seen) {
  const report = (run, valid) => {
    const words = run.map((t) => t.word);
    const fingerprint = fingerprintOf(words);
    if (seen.has(fingerprint)) return;
    seen.add(fingerprint);
    findings.push({ line: run[0].line + lineOffset, words: run.length, checksumValid: valid, fingerprint, encoding });
  };
  for (const tokens of segments(text)) {
    for (const run of wordlistRuns(tokens, index)) {
      const { shaped, valid } = runFlags(run, index);
      if (shaped && (valid || !needValid)) report(run, valid);
    }
    if (!keyed || tokens.length < MIN_WORDS * 2) continue;
    for (const run of keyedRuns(tokens, index)) {
      const words = run.map((t) => t.word);
      if (new Set(words).size >= MIN_DISTINCT && hasValidWindow(words, index)) report(run, true);
    }
  }
}

/**
 * Mnemonic-shaped runs in a buffer or string. Returns [{line, words,
 * checksumValid, fingerprint, encoding}], never the words themselves.
 *
 * Views scanned: the raw bytes; the text with encoded separators turned into
 * spaces; UTF-16 decodings; base64 values that decode to text. Derived binary
 * views (UTF-16, base64) and keyed records report only checksum-valid phrases.
 */
export function findMnemonicRuns(content, wordlist) {
  const index = wordlist instanceof Map ? wordlist : new Map(wordlist.map((w, i) => [w, i]));
  const buf = Buffer.isBuffer(content) ? content : Buffer.from(String(content), 'utf8');
  const text = Buffer.isBuffer(content) ? content.toString('latin1') : String(content);
  const findings = [];
  const seen = new Set();
  const textLike = isTextLike(buf);
  // Raw bytes, always (any file type). Keyed records only in text.
  scanText(text, index, { keyed: textLike }, findings, seen);
  const views = [];
  if (textLike) {
    views.push({ text, encoding: '' });
    for (const { text: decoded, lineOffset } of base64Texts(text)) views.push({ text: decoded, encoding: 'base64', needValid: true, lineOffset, raw: true });
  }
  for (const decoded of utf16Texts(buf)) views.push({ text: decoded, encoding: 'UTF-16', needValid: true, raw: true });
  for (const view of views) {
    if (view.raw) scanText(view.text, index, view, findings, seen);
    const unescaped = decodeSeparators(view.text);
    if (unescaped !== view.text) scanText(unescaped, index, { ...view, encoding: view.encoding || 'encoded separators' }, findings, seen);
  }
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

/** A path from a diff header: `b/path`, or a C-quoted `"b/pa\"th"`. */
export function parseDiffPath(raw) {
  let value = raw.replace(/\t$/, '');
  if (value.startsWith('"') && value.endsWith('"')) {
    const bytes = [];
    const body = value.slice(1, -1);
    for (let i = 0; i < body.length; i++) {
      const ch = body[i];
      if (ch !== '\\') {
        bytes.push(...Buffer.from(ch, 'latin1'));
        continue;
      }
      const next = body[++i];
      if (/[0-7]/.test(next)) {
        bytes.push(Number.parseInt(body.slice(i, i + 3), 8));
        i += 2;
      } else {
        bytes.push({ n: 10, t: 9, r: 13, a: 7, b: 8, f: 12, v: 11, '\\': 92, '"': 34 }[next] ?? next.charCodeAt(0));
      }
    }
    value = Buffer.from(bytes).toString('utf8');
  }
  if (value === '/dev/null') return '';
  if (!value.startsWith('b/')) throw new Error(`unexpected diff header path (prefix forced to b/): ${JSON.stringify(raw.slice(0, 80))}`);
  return value.slice(2);
}

// Diff output this parser can rely on whatever the user's git config says
// (verifier NB2: diff.noprefix / diff.dstPrefix made the scan exit 0 having
// read nothing; core.quotePath only changes quoting, handled above).
const GIT_DIFF_CONFIG = [
  '-c', 'diff.noprefix=false', '-c', 'diff.mnemonicPrefix=false', '-c', 'diff.srcPrefix=a/', '-c', 'diff.dstPrefix=b/',
  '-c', 'diff.external=', '-c', 'core.quotePath=true', '-c', 'log.showSignature=false',
];

/**
 * Lines added by each commit in a range, grouped per (commit, path), plus each
 * commit's message. Streamed: a full history's patch text is far larger than
 * one string can hold.
 *
 * A header (`diff --git`, `diff --cc`) opens a file; its `+++` line names it;
 * the first `@@` starts the hunks, and from then on EVERY line is content until
 * the next `diff ` header — an added line that itself starts "++ " is content,
 * not a new file (verifier NB2). Merge commits are shown as dense combined
 * diffs (`--cc`), so content a merge introduced that neither parent had — an
 * evil merge — is scanned too: a combined line is added when its parent-column
 * prefix has a '+' and no '-'.
 */
async function* historyAdditions(range) {
  const MARK = `--sec001-scan-${process.pid}-${Date.now()}--`;
  const child = spawn(
    'git',
    [
      ...GIT_DIFF_CONFIG,
      'log', '-p', '--cc', '--no-color', '--no-ext-diff', '--no-textconv', '--text', '--no-renames',
      '--src-prefix=a/', '--dst-prefix=b/',
      `--format=${MARK} %H%n%B%n${MARK}`,
      ...range.split(/\s+/).filter(Boolean),
    ],
    { stdio: ['ignore', 'pipe', 'inherit'] },
  );
  child.stdout.setEncoding('latin1');
  const exited = new Promise((resolveExit) => child.on('close', resolveExit));
  const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
  let commit = '';
  let path = '';
  let added = [];
  let state = 'none'; // none | message | header | hunks
  let message = [];
  let parents = 1;
  let commits = 0;
  const emit = () => (commit && path && added.length ? { commit, path, content: added.join('\n') } : null);
  for await (const line of lines) {
    if (state === 'message') {
      if (line === MARK) {
        yield { commit, path: '(commit message)', content: message.join('\n'), message: true };
        message = [];
        state = 'none';
      } else message.push(line);
      continue;
    }
    if (line.startsWith(`${MARK} `) && /^[0-9a-f]{40}$/.test(line.slice(MARK.length + 1))) {
      const e = emit();
      if (e) yield e;
      commit = line.slice(MARK.length + 1);
      commits += 1;
      path = '';
      added = [];
      state = 'message';
      continue;
    }
    if (/^diff --(git|cc|combined) /.test(line)) {
      const e = emit();
      if (e) yield e;
      path = '';
      added = [];
      state = 'header';
      parents = 1;
      continue;
    }
    if (state === 'header') {
      if (line.startsWith('+++ ')) path = parseDiffPath(line.slice(4));
      else if (line.startsWith('@@')) {
        parents = Math.max(1, (/^@+/.exec(line)[0].length) - 1);
        state = 'hunks';
      }
      continue;
    }
    if (state === 'hunks' && path) {
      if (line.startsWith('@@')) continue;
      const prefix = line.slice(0, parents);
      if (prefix.includes('+') && !prefix.includes('-')) added.push(line.slice(parents));
    }
  }
  const e = emit();
  if (e) yield e;
  const code = await exited;
  if (code !== 0) throw new Error(`git log ${range} exited ${code}`);
  // HEAD or --all always has commits in a repository with history; reading none
  // means the output format was not what this parser expects. (An empty
  // A..B range is legitimately empty.)
  if (commits === 0 && /(^|\s)(HEAD|--all)(\s|$)/.test(range) && !range.includes('..')) {
    throw new Error(`git log ${range} produced no commits this parser could read`);
  }
}

/** Annotated tag messages (every tag object reachable from refs/tags). */
function* tagMessages() {
  const refs = git(['for-each-ref', '--format=%(objecttype) %(refname)', 'refs/tags'], { encoding: 'utf8' })
    .split('\n')
    .filter((l) => l.startsWith('tag '))
    .map((l) => l.slice(4));
  for (const ref of refs) {
    const body = git(['cat-file', 'tag', ref], { encoding: 'latin1' });
    const at = body.indexOf('\n\n');
    yield { commit: '', path: `(tag message ${ref})`, content: at >= 0 ? body.slice(at + 2) : '', message: true };
  }
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
  return `${f.words}-word run${f.checksumValid ? ', BIP-39 CHECKSUM-VALID' : ''}${f.encoding ? ` (${f.encoding})` : ''}, fingerprint ${f.fingerprint.slice(0, 16)}`;
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
    const scan = ({ commit, path, content }) => {
      if (path === WORDLIST_REL) return;
      for (const f of findMnemonicRuns(Buffer.from(content, 'latin1'), wordlist)) {
        if (isAllowed(allowlist, { fingerprint: f.fingerprint, path, commit })) continue;
        const where = `${commit ? `${commit.slice(0, 12)} ` : ''}${path} (added line ${f.line}): ${describe(f)}`;
        if (!strict && isKnownExposure(allowlist, { path, commit })) exposures.push(where);
        else hits.push(where);
      }
    };
    for await (const addition of historyAdditions(range)) scan(addition);
    // Tag messages: with --all (every ref), or when the range names tags.
    if (/(^|\s)--(all|tags)(\s|$)/.test(range)) for (const t of tagMessages()) scan(t);
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
