#!/usr/bin/env node
// new-node-identity.mjs — OWNER-RUN, OFFLINE generator for a new SDN node
// identity (SEC-001 rotation) and for the separate fleet distribution key.
//
//   node deployment/identity/new-node-identity.mjs --out-dir <dir outside any git checkout>
//   node deployment/identity/new-node-identity.mjs --dry-run    # self-test, no owner
//   node deployment/identity/new-node-identity.mjs --describe-backup <old.backup>
//
// The fleet UPDATE ROOT is no longer derived here: it is a standalone Ed25519
// key from deployment/identity/new-update-root.mjs (owner, 2026-10-15), so
// `--kind distribution` is refused.
//
// WHAT IT DOES (real mode, interactive terminal only)
//   1. Proves its derivation equals the node's: derives the PUBLIC BIP-39 test
//      vector and compares it with testdata/derivation-vector.json, which the
//      node's own Go code writes (sdn-server/internal/wasm/rotation_vector_test.go).
//   2. Generates a fresh 24-word BIP-39 mnemonic with hd-wallet-wasm.
//   3. Shows it ONCE on the terminal for offline backup, clears the screen, and
//      makes you retype it with echo off.
//   4. Derives the node identity set the node derives from that seed:
//      PeerID, identity key, xpub, secp256k1 signing + encryption keys,
//      Ed25519 publisher key, X25519 key, licensing-grant key, root-admin
//      sign-in key, discovery CID, bond addresses. (--kind distribution
//      derives only the update/distribution root at m/44'/0'/0'/3'/0'.)
//   5. Node kind only: writes node-identity.backup — the encrypted file
//      `spacedatanetwork key import --format backup` reads (Argon2id +
//      XChaCha20-Poly1305, salt||nonce||ciphertext, internal/keys/mnemonic.go),
//      sealed under the password in --password-file (the node's
//      SDN_KEY_PASSWORD_FILE mechanism) or one you type.
//   6. Prints ONLY public values and writes public-identity.json plus an
//      updated anchors.json (deployment/identity/anchors.json with this
//      identity's anchors filled in) for the repo.
//
// It never prints, logs, writes in plaintext or sends the mnemonic or any
// private key. The only file that holds the seed is the encrypted backup.
//
// --dry-run uses a throwaway in-memory mnemonic, runs every step except the
// show/retype, verifies the backup decrypts, optionally imports it into a
// throwaway node (--verify-with-node <spacedatanetwork binary>), prints only
// public values, and leaves nothing behind unless --out-dir is given.
import { spawnSync } from 'node:child_process';
import {
  argon2Sync,
  createCipheriv,
  createDecipheriv,
  createPrivateKey,
  createPublicKey,
  randomBytes,
} from 'node:crypto';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readlinkSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { networkInterfaces, tmpdir } from 'node:os';
import { basename, dirname, join, parse as parsePath, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import {
  ANCHORS_PATH,
  discoveryCidFor,
  ed25519Spki,
  isDenied,
  isPlaceholder,
  loadAnchors,
  peerIdFromSecp256k1,
  releaseProblems,
  structuralProblems,
  updateRootKeyId,
} from './anchors-lib.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(HERE, '../..');
export const VECTOR_PATH = join(HERE, 'testdata', 'derivation-vector.json');

// Derivation paths: sdn-server/internal/wasm/hdwallet_identity.go,
// hdwallet_purpose.go and hdwallet_coins.go. Account 0, as the daemon uses.
export const PATHS = Object.freeze({
  identity: "m/44'/0'/0'",
  signing: "m/44'/0'/0'/0/0", // EPM signing_pubkey_hex (xpub /0/0)
  encryption: "m/44'/0'/0'/1/0", // EPM encryption_pubkey_hex, sealed /api/rpc transport
  publisher: "m/44'/0'/0'/0'/0'", // Ed25519 node signing / module publisher (purpose 0)
  x25519: "m/44'/0'/0'/1'/0'", // X25519 wrapping key (purpose 1)
  grant: "m/44'/0'/0'/2'/0'", // licensing-grant signing key (purpose 2)
  rootAdmin: "m/44'/0'/0'/0/0", // legacy sign-in key: BIP-32 scalar used as an Ed25519 seed
  distribution: "m/44'/0'/0'/3'/0'", // fleet distribution / update root (purpose 3, separate phrase)
  bitcoin: "m/84'/0'/0'/0/0",
  ethereum: "m/44'/60'/0'/0/0",
  solana: "m/44'/501'/0'/0'",
});

// internal/keys/mnemonic.go
const ARGON2 = Object.freeze({ passes: 3, memory: 64 * 1024, parallelism: 4, tagLength: 32 });
const SALT_SIZE = 32;
const XNONCE_SIZE = 24;

const CURVE_SECP256K1 = 0;
const BITCOIN_P2WPKH_CANDIDATES = ['P2WPKH', 'p2wpkh', 'SEGWIT', 'NATIVE_SEGWIT', 'BECH32'];

// ---------------------------------------------------------------------------
// XChaCha20-Poly1305, as golang.org/x/crypto/chacha20poly1305.NewX: HChaCha20
// derives a subkey from the first 16 nonce bytes, then IETF ChaCha20-Poly1305
// runs with nonce 0x00000000 || nonce[16:24].
// ---------------------------------------------------------------------------
function rotl(v, n) {
  return ((v << n) | (v >>> (32 - n))) >>> 0;
}

export function hchacha20(key, nonce16) {
  const s = new Uint32Array(16);
  s[0] = 0x61707865; s[1] = 0x3320646e; s[2] = 0x79622d32; s[3] = 0x6b206574;
  for (let i = 0; i < 8; i++) s[4 + i] = key.readUInt32LE(i * 4);
  for (let i = 0; i < 4; i++) s[12 + i] = nonce16.readUInt32LE(i * 4);
  const qr = (a, b, c, d) => {
    s[a] = (s[a] + s[b]) >>> 0; s[d] = rotl(s[d] ^ s[a], 16);
    s[c] = (s[c] + s[d]) >>> 0; s[b] = rotl(s[b] ^ s[c], 12);
    s[a] = (s[a] + s[b]) >>> 0; s[d] = rotl(s[d] ^ s[a], 8);
    s[c] = (s[c] + s[d]) >>> 0; s[b] = rotl(s[b] ^ s[c], 7);
  };
  for (let i = 0; i < 10; i++) {
    qr(0, 4, 8, 12); qr(1, 5, 9, 13); qr(2, 6, 10, 14); qr(3, 7, 11, 15);
    qr(0, 5, 10, 15); qr(1, 6, 11, 12); qr(2, 7, 8, 13); qr(3, 4, 9, 14);
  }
  const out = Buffer.alloc(32);
  for (let i = 0; i < 4; i++) out.writeUInt32LE(s[i], i * 4);
  for (let i = 0; i < 4; i++) out.writeUInt32LE(s[12 + i], 16 + i * 4);
  s.fill(0);
  return out;
}

function xchachaParts(key, nonce) {
  const subkey = hchacha20(key, nonce.subarray(0, 16));
  const iv = Buffer.concat([Buffer.alloc(4), nonce.subarray(16, 24)]);
  return { subkey, iv };
}

function passwordKey(password, salt) {
  return argon2Sync('argon2id', {
    message: Buffer.from(password, 'utf8'),
    nonce: salt,
    passes: ARGON2.passes,
    memory: ARGON2.memory,
    parallelism: ARGON2.parallelism,
    tagLength: ARGON2.tagLength,
  });
}

/** keys.EncryptMnemonic: salt(32) || nonce(24) || ciphertext||tag. */
export function sealMnemonic(mnemonic, password) {
  const salt = randomBytes(SALT_SIZE);
  const nonce = randomBytes(XNONCE_SIZE);
  const key = passwordKey(password, salt);
  const { subkey, iv } = xchachaParts(key, nonce);
  const cipher = createCipheriv('chacha20-poly1305', subkey, iv, { authTagLength: 16 });
  const plaintext = Buffer.from(mnemonic, 'utf8');
  const sealed = Buffer.concat([salt, nonce, cipher.update(plaintext), cipher.final(), cipher.getAuthTag()]);
  plaintext.fill(0);
  key.fill(0);
  subkey.fill(0);
  return sealed;
}

/** keys.DecryptMnemonic. */
export function openMnemonic(sealed, password) {
  if (sealed.length < SALT_SIZE + XNONCE_SIZE + 16 + 1) throw new Error('sealed mnemonic too short');
  const salt = sealed.subarray(0, SALT_SIZE);
  const nonce = sealed.subarray(SALT_SIZE, SALT_SIZE + XNONCE_SIZE);
  const body = sealed.subarray(SALT_SIZE + XNONCE_SIZE);
  const key = passwordKey(password, salt);
  const { subkey, iv } = xchachaParts(key, nonce);
  const decipher = createDecipheriv('chacha20-poly1305', subkey, iv, { authTagLength: 16 });
  decipher.setAuthTag(body.subarray(body.length - 16));
  const plaintext = Buffer.concat([decipher.update(body.subarray(0, body.length - 16)), decipher.final()]);
  key.fill(0);
  subkey.fill(0);
  const text = plaintext.toString('utf8');
  plaintext.fill(0);
  return text;
}

// ---------------------------------------------------------------------------
// hd-wallet-wasm
// ---------------------------------------------------------------------------
/** The ESM entry of an installed hd-wallet-wasm (repo root, then sdn-js), or ''. */
export function findInstalledHDWallet() {
  for (const base of [REPO_ROOT, join(REPO_ROOT, 'sdn-js')]) {
    const pkgPath = join(base, 'node_modules', 'hd-wallet-wasm', 'package.json');
    if (!existsSync(pkgPath)) continue;
    const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
    const entry = pkg.exports?.['.']?.import || pkg.module || pkg.main;
    if (entry) return join(dirname(pkgPath), entry);
  }
  return '';
}

export async function loadHDWallet(runtimePath) {
  const specifier = runtimePath || process.env.SDN_HD_WALLET_RUNTIME || findInstalledHDWallet();
  if (!specifier) {
    throw new Error(
      'hd-wallet-wasm not found. Pass --hd-wallet <hd-wallet-wasm>/wasm/dist/runtime/index.mjs ' +
        '(the 2.1.1 build) or run `npm ci` in the repository root.',
    );
  }
  const url = specifier.startsWith('file:') ? specifier : pathToFileURL(resolve(specifier)).href;
  const mod = await import(url);
  const wallet = await mod.default();
  const version = wallet.getVersion();
  const [major, minor] = version.split('.').map(Number);
  if (major < 2 || (major === 2 && minor < 1)) {
    throw new Error(`hd-wallet-wasm ${version} is too old: use 2.1.1 (or at least 2.1.0)`);
  }
  return { wallet, mod, version, specifier: url };
}

const hex = (bytes) => Buffer.from(bytes).toString('hex');

function ed25519PublicFromSeed(seed32) {
  // RFC 8032 key from a 32-byte seed — what Go's ed25519.NewKeyFromSeed and
  // libp2p crypto.GenerateEd25519Key(seed) compute on the node.
  const pkcs8 = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), Buffer.from(seed32)]);
  const priv = createPrivateKey({ key: pkcs8, format: 'der', type: 'pkcs8' });
  const spki = createPublicKey(priv).export({ format: 'der', type: 'spki' });
  pkcs8.fill(0);
  return spki.subarray(spki.length - 32).toString('hex');
}

function slip10Ed25519Public(wallet, seed, path) {
  const derived = wallet.slip10.deriveEd25519Path(Uint8Array.from(seed), path);
  try {
    return ed25519PublicFromSeed(derived.privateKey);
  } finally {
    wallet.utils.secureWipe(derived.privateKey);
    wallet.utils.secureWipe(derived.chainCode);
  }
}

function bitcoinP2WPKH(wallet, mod, publicKey) {
  const types = mod.BitcoinAddressType || {};
  for (const name of BITCOIN_P2WPKH_CANDIDATES) {
    if (types[name] === undefined) continue;
    const address = wallet.bitcoin.getAddress(publicKey, types[name]);
    if (typeof address === 'string' && address.startsWith('bc1q')) return address;
  }
  throw new Error('hd-wallet-wasm has no P2WPKH address type');
}

/** The node identity set's PUBLIC values for a seed. */
export function deriveNodePublicIdentity(wallet, mod, seed) {
  const root = wallet.hdkey.fromSeed(Uint8Array.from(seed), CURVE_SECP256K1);
  const keys = [];
  const at = (path) => {
    const key = root.derivePath(path);
    keys.push(key);
    return key;
  };
  try {
    const account = at(PATHS.identity);
    const identityPublicKey = hex(account.publicKey());
    const xpub = account.toXpub();
    const signingPublicKey = hex(at(PATHS.signing).publicKey());
    const encryptionPublicKey = hex(at(PATHS.encryption).publicKey());

    const rootAdminScalar = at(PATHS.rootAdmin).privateKey();
    const rootAdminPublicKey = ed25519PublicFromSeed(rootAdminScalar);
    wallet.utils.secureWipe(rootAdminScalar);

    const x25519Derived = wallet.slip10.deriveEd25519Path(Uint8Array.from(seed), PATHS.x25519);
    const x25519PublicKey = hex(wallet.curves.x25519.publicKey(x25519Derived.privateKey));
    wallet.utils.secureWipe(x25519Derived.privateKey);
    wallet.utils.secureWipe(x25519Derived.chainCode);

    const bitcoin = bitcoinP2WPKH(wallet, mod, at(PATHS.bitcoin).publicKey());
    const ethereum = wallet.ethereum.getAddress(at(PATHS.ethereum).publicKey());
    const solanaPublic = slip10Ed25519Public(wallet, seed, PATHS.solana);
    const solana = wallet.solana.getAddress(Buffer.from(solanaPublic, 'hex'));

    return {
      peerId: peerIdFromSecp256k1(identityPublicKey),
      identityPublicKey,
      xpub,
      signingPublicKey,
      encryptionPublicKey,
      publisherPublicKey: slip10Ed25519Public(wallet, seed, PATHS.publisher),
      x25519PublicKey,
      grantVerifierPublicKey: slip10Ed25519Public(wallet, seed, PATHS.grant),
      rootAdminPublicKey,
      discoveryCid: discoveryCidFor(identityPublicKey),
      chainAddresses: { bitcoin, ethereum, solana },
    };
  } finally {
    for (const key of keys) key.wipe?.();
    root.wipe?.();
  }
}

/** The fleet distribution (update root) key's public values for a seed. */
export function deriveDistributionPublic(wallet, seed) {
  const publicKey = slip10Ed25519Public(wallet, seed, PATHS.distribution);
  return { distributionPublicKey: publicKey, keyId: updateRootKeyId(publicKey), spki: ed25519Spki(publicKey) };
}

/**
 * Derive the PUBLIC BIP-39 test vector and compare it with the values the
 * node's own Go derivation committed. A mismatch means this tool would pin an
 * identity the node never becomes, so every mode refuses to continue.
 */
export function selfTestAgainstNode(wallet, mod) {
  const vector = JSON.parse(readFileSync(VECTOR_PATH, 'utf8'));
  const phrase = Array(11).fill('abandon').concat(['about']).join(' ');
  const seed = wallet.mnemonic.toSeed(phrase, '');
  try {
    const node = deriveNodePublicIdentity(wallet, mod, seed);
    const distribution = deriveDistributionPublic(wallet, seed);
    const mismatches = [];
    for (const [field, value] of Object.entries(node)) {
      const want = vector[field];
      if (JSON.stringify(value) !== JSON.stringify(want)) mismatches.push(field);
    }
    if (distribution.distributionPublicKey !== vector.distributionPublicKey) mismatches.push('distributionPublicKey');
    return mismatches;
  } finally {
    wallet.utils.secureWipe(seed);
  }
}

// ---------------------------------------------------------------------------
// Terminal I/O. Only used in real mode, which requires a TTY on both ends.
// ---------------------------------------------------------------------------
export function readLine(prompt) {
  return readTTY(prompt, { echo: true });
}

export function readHidden(prompt) {
  return readTTY(prompt, { echo: false });
}

function readTTY(prompt, { echo }) {
  return new Promise((resolvePromise, reject) => {
    const stdin = process.stdin;
    process.stdout.write(prompt);
    let buffer = '';
    stdin.setRawMode(true);
    stdin.resume();
    stdin.setEncoding('utf8');
    const finish = (err) => {
      stdin.setRawMode(false);
      stdin.pause();
      stdin.removeListener('data', onData);
      process.stdout.write('\n');
      if (err) reject(err);
      else resolvePromise(buffer);
      buffer = '';
    };
    const onData = (chunk) => {
      for (const ch of chunk) {
        if (ch === '\u0003') return finish(new Error('cancelled'));
        if (ch === '\r' || ch === '\n') return finish();
        if (ch === '\u007f' || ch === '\b') {
          if (buffer.length > 0) {
            buffer = buffer.slice(0, -1);
            if (echo) process.stdout.write('\b \b');
          }
          continue;
        }
        if (ch === '\u0015') {
          buffer = '';
          continue;
        }
        if (ch < ' ') continue;
        buffer += ch;
        if (echo) process.stdout.write(ch);
      }
      return undefined;
    };
    stdin.on('data', onData);
  });
}

function clearScreen() {
  // Clear the visible screen AND the scrollback.
  process.stdout.write('\u001b[2J\u001b[3J\u001b[H');
}

function normalizePhrase(text) {
  return text.trim().toLowerCase().split(/\s+/).join(' ');
}

async function showOnceAndConfirm(mnemonic, label) {
  const words = mnemonic.split(' ');
  clearScreen();
  process.stdout.write(
    `\n  ${label}: ${words.length}-word BIP-39 recovery phrase. Write it down OFFLINE now.\n` +
      '  It is shown ONCE and is not stored in plaintext anywhere.\n\n',
  );
  for (let i = 0; i < words.length; i += 4) {
    process.stdout.write(
      `  ${words
        .slice(i, i + 4)
        .map((w, j) => `${String(i + j + 1).padStart(2)}. ${w.padEnd(10)}`)
        .join('  ')}\n`,
    );
  }
  await readLine('\n  Press Enter once it is written down (the screen is then cleared). ');
  clearScreen();
  for (let attempt = 1; attempt <= 3; attempt++) {
    const typed = await readHidden(`  Retype the ${words.length} words to confirm (input hidden), attempt ${attempt}/3: `);
    if (normalizePhrase(typed) === mnemonic) return;
    process.stdout.write('  That does not match.\n');
  }
  throw new Error('the phrase was not confirmed; nothing was written');
}

function readPasswordFile(path) {
  const mode = statSync(path).mode & 0o777;
  if (mode & 0o077) {
    throw new Error(`${path} is mode ${mode.toString(8)}; the node requires 0600 for SDN_KEY_PASSWORD_FILE (chmod 600 it)`);
  }
  // config.readSecretFile: trailing CR/LF trimmed, nothing else.
  const secret = readFileSync(path, 'utf8').replace(/[\r\n]+$/, '');
  if (!secret) throw new Error(`${path} is empty`);
  return secret;
}

async function promptNewPassword() {
  for (let attempt = 1; attempt <= 3; attempt++) {
    const first = await readHidden('  Backup password (input hidden): ');
    if (first.length < 16) {
      process.stdout.write('  Use at least 16 characters.\n');
      continue;
    }
    const second = await readHidden('  Repeat the backup password: ');
    if (first === second) return first;
    process.stdout.write('  The passwords differ.\n');
  }
  throw new Error('no backup password was set; nothing was written');
}

export function onlineInterfaces() {
  const up = [];
  for (const [name, addrs] of Object.entries(networkInterfaces())) {
    for (const addr of addrs || []) {
      if (addr.internal) continue;
      if (addr.family === 'IPv6' && addr.address.startsWith('fe80')) continue;
      up.push(`${name} ${addr.address}`);
    }
  }
  return up;
}

/**
 * The git work tree containing `path` (or its nearest existing ancestor), or
 * ''. Verifier S5: an encrypted seed backup or a private key written inside a
 * checkout is one `git add -A` away from being published, so secret outputs
 * must live outside every work tree. Walks up looking for `.git` (a directory,
 * or the file a worktree or submodule has), so it needs no git binary.
 */
export function enclosingGitWorkTree(path) {
  // Verifier r2: a symlinked output path (or a symlinked ancestor) can point
  // into a checkout, so the path is checked as written AND as it resolves.
  for (const candidate of new Set([resolve(path), realResolve(path)])) {
    let dir = candidate;
    const { root } = parsePath(dir);
    for (;;) {
      if (existsSync(join(dir, '.git'))) return dir;
      if (dir === root) break;
      dir = dirname(dir);
    }
  }
  return '';
}

/**
 * `path` with every symlink resolved: the nearest existing ancestor (the path
 * itself, if it exists — a dangling symlink included) through realpath, plus
 * the part that does not exist yet.
 */
export function realResolve(path) {
  let existing = resolve(path);
  const rest = [];
  for (;;) {
    let link = null;
    try {
      link = lstatSync(existing);
    } catch {
      link = null;
    }
    if (link) {
      if (link.isSymbolicLink() && !existsSync(existing)) {
        // a dangling symlink: follow its target lexically
        return realResolve(join(resolve(dirname(existing), readlinkSync(existing)), ...rest));
      }
      return join(realpathSync(existing), ...rest);
    }
    const parent = dirname(existing);
    if (parent === existing) return join(existing, ...rest);
    rest.unshift(basename(existing));
    existing = parent;
  }
}

/** Throws unless `dir` is outside every git work tree. */
export function assertOutsideGitWorkTree(dir, what) {
  const tree = enclosingGitWorkTree(dir);
  if (tree) {
    throw new Error(
      `${what} ${dir} is inside the git work tree ${tree}; a stray \`git add\` would publish it. ` +
        'Choose a directory outside every checkout (removable media on the offline machine).',
    );
  }
}

// ---------------------------------------------------------------------------
// Outputs
// ---------------------------------------------------------------------------
export function mergeAnchors(base, { node, distribution }) {
  const anchors = JSON.parse(JSON.stringify(base));
  if (node) {
    for (const field of [
      'peerId',
      'identityPublicKey',
      'xpub',
      'signingPublicKey',
      'encryptionPublicKey',
      'publisherPublicKey',
      'x25519PublicKey',
      'grantVerifierPublicKey',
      'rootAdminPublicKey',
      'discoveryCid',
    ]) {
      anchors.provider[field] = node[field];
    }
  }
  if (distribution) {
    anchors.updateRoots = { [distribution.keyId]: distribution.spki };
  }
  const stillPlaceholder = JSON.stringify(anchors).includes('"PLACEHOLDER-');
  anchors.status = stillPlaceholder ? 'placeholder' : 'released';
  return anchors;
}

function writeOwnerFile(path, contents, mode) {
  writeFileSync(path, contents, { mode });
  chmodSync(path, mode);
}

function printPublic(kind, publicValues, files) {
  const lines = [];
  lines.push('');
  lines.push(`PUBLIC values only — ${kind === 'node' ? 'node identity' : 'distribution (update root) key'}`);
  for (const [key, value] of Object.entries(publicValues)) {
    lines.push(`  ${key.padEnd(24)} ${typeof value === 'string' ? value : JSON.stringify(value)}`);
  }
  if (files.length) {
    lines.push('');
    for (const file of files) lines.push(`  wrote ${file}`);
  }
  process.stdout.write(`${lines.join('\n')}\n`);
}

/**
 * Import the backup into a THROWAWAY node and read its identity back with the
 * node's own CLI. Dry-run only: it writes the (throwaway, encrypted) seed into
 * a temporary directory, which real mode never does.
 */
export function verifyWithNode(binary, backupPath, password, expected) {
  const work = mkdtempSync(join(tmpdir(), 'sdn-identity-verify-'));
  try {
    const passwordFile = join(work, 'key-password');
    writeOwnerFile(passwordFile, `${password}\n`, 0o600);
    const config = join(work, 'config.yaml');
    writeFileSync(config, `storage:\n  path: ${join(work, 'data', 'store')}\n`);
    const env = { ...process.env, SDN_KEY_PASSWORD_FILE: passwordFile, HOME: work };
    // The CLI loads the WASI module from disk; use the bytes the binary embeds.
    env.HD_WALLET_WASM_PATH ||= join(REPO_ROOT, 'sdn-server/internal/wasm/hd-wallet-wasi.wasm');
    delete env.SDN_KEY_PASSWORD;
    delete env.SDN_MNEMONIC_FILE;
    const imported = spawnSync(binary, ['key', 'import', '--format', 'backup', '--input', backupPath, '--config', config], {
      env,
      encoding: 'utf8',
    });
    if (imported.status !== 0) throw new Error(`node key import failed: ${(imported.stderr || '').trim().split('\n').pop()}`);
    const read = (format) => {
      const out = spawnSync(binary, ['key', 'export', '--format', format, '--config', config], { env, encoding: 'utf8' });
      if (out.status !== 0) throw new Error(`node key export ${format} failed: ${(out.stderr || '').trim().split('\n').pop()}`);
      return out.stdout.trim();
    };
    const peerId = read('peerid');
    const xpub = read('xpub');
    if (peerId !== expected.peerId) throw new Error(`the node derived PeerID ${peerId}, the generator ${expected.peerId}`);
    if (xpub !== expected.xpub) throw new Error('the node derived a different xpub');
    return { peerId, xpub };
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------
function parseArgs(argv) {
  const opts = { kind: 'node', dryRun: false, words: 24 };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const value = () => {
      const next = argv[++i];
      if (next === undefined) throw new Error(`${arg} needs a value`);
      return next;
    };
    if (arg === '--dry-run' || arg === '--self-test') opts.dryRun = true;
    else if (arg === '--kind') opts.kind = value();
    else if (arg === '--out-dir') opts.outDir = value();
    else if (arg === '--password-file') opts.passwordFile = value();
    else if (arg === '--hd-wallet') opts.hdWallet = value();
    else if (arg === '--anchors') opts.anchors = value();
    else if (arg === '--verify-with-node') opts.verifyWithNode = value();
    else if (arg === '--describe-backup') opts.describeBackup = value();
    else if (arg === '--help' || arg === '-h') opts.help = true;
    else throw new Error(`unknown argument ${arg}`);
  }
  if (opts.kind === 'distribution') {
    throw new Error(
      'the fleet update root is no longer a seed-derived key (owner, 2026-10-15): generate it with ' +
        'deployment/identity/new-update-root.mjs, a standalone Ed25519 key separate from the node identity',
    );
  }
  if (opts.kind !== 'node') throw new Error('--kind is node');
  return opts;
}

const USAGE = `usage:
  node deployment/identity/new-node-identity.mjs --out-dir <dir outside any git checkout>
       [--password-file <SDN_KEY_PASSWORD_FILE>]
       [--anchors <anchors.json to extend>] [--hd-wallet <hd-wallet-wasm runtime/index.mjs>]
  node deployment/identity/new-node-identity.mjs --dry-run [--verify-with-node <binary>]
  node deployment/identity/new-node-identity.mjs --describe-backup <backup> [--password-file <file>]
       (public values of an EXISTING identity, e.g. the retired one, for the denylist)
Real modes run only in an interactive terminal. The update root comes from new-update-root.mjs.
See deployment/identity/ROTATION.md.`;

/**
 * --describe-backup: the PUBLIC values of an existing identity backup (as
 * `spacedatanetwork key export --format backup` writes it). For the retired
 * SEC-001 identity this yields the two values no public source carries — the
 * X25519 wrapping key and the full legacy root-admin key (fingerprint
 * c9e84f37) — for anchors.json's denylist (verifier S6). Prints public values
 * only; writes nothing.
 */
async function describeBackup(opts, wallet, mod, base) {
  const sealed = readFileSync(resolve(opts.describeBackup));
  let password = opts.passwordFile ? readPasswordFile(resolve(opts.passwordFile)) : await readHidden('  Backup password (input hidden): ');
  let mnemonic = openMnemonic(sealed, password);
  password = null;
  let seed = wallet.mnemonic.toSeed(mnemonic, '');
  mnemonic = null;
  try {
    const values = deriveNodePublicIdentity(wallet, mod, seed);
    printPublic('node', values, []);
    const deny = base.denylist || {};
    const missing = (list, value) => !(list || []).map((v) => v.toLowerCase()).includes(String(value).toLowerCase());
    const additions = {};
    if (missing(deny.x25519PublicKeys, values.x25519PublicKey)) additions.x25519PublicKeys = [values.x25519PublicKey];
    const ed = [values.rootAdminPublicKey, values.publisherPublicKey, values.grantVerifierPublicKey].filter((v) => missing(deny.ed25519PublicKeys, v));
    if (ed.length) additions.ed25519PublicKeys = ed;
    const secp = [values.identityPublicKey, values.signingPublicKey, values.encryptionPublicKey].filter((v) => missing(deny.secp256k1PublicKeys, v));
    if (secp.length) additions.secp256k1PublicKeys = secp;
    if (missing(deny.peerIds, values.peerId)) additions.peerIds = [values.peerId];
    process.stdout.write(
      Object.keys(additions).length
        ? `\nAdd to anchors.json "denylist" (public values; then run sync-anchors.mjs):\n${JSON.stringify(additions, null, 2)}\n`
        : '\nEvery public value of this identity is already on the denylist.\n',
    );
    return 0;
  } finally {
    wallet.utils.secureWipe(seed);
    seed = null;
  }
}

export async function main(argv = process.argv.slice(2)) {
  const opts = parseArgs(argv);
  if (opts.help) {
    process.stdout.write(`${USAGE}\n`);
    return 0;
  }

  if (!opts.dryRun) {
    const interactive = process.stdin.isTTY && process.stdout.isTTY && typeof process.stdin.setRawMode === 'function';
    if (!interactive || process.env.CI) {
      process.stderr.write(
        'REFUSED: the identity generator runs only in an interactive terminal (stdin and stdout a TTY, not CI).\n' +
          'Use --dry-run for an automated self-test with a throwaway mnemonic.\n',
      );
      return 2;
    }
  }

  // Every secret output lives outside every git work tree (verifier S5), and
  // is checked BEFORE a phrase exists, so a refusal never discards a phrase
  // the owner has already written down (verifier N3).
  if (!opts.dryRun && !opts.describeBackup && !opts.outDir) {
    process.stderr.write('REFUSED: --out-dir is required: a directory outside every git checkout (removable media).\n');
    return 2;
  }
  const outDir = opts.outDir ? resolve(opts.outDir) : null;
  if (outDir) {
    try {
      assertOutsideGitWorkTree(outDir, '--out-dir');
    } catch (error) {
      process.stderr.write(`REFUSED: ${error.message}\n`);
      return 2;
    }
    const backupPath = join(outDir, 'node-identity.backup');
    if (existsSync(backupPath)) {
      process.stderr.write(`REFUSED: ${backupPath} exists; refusing to overwrite. Nothing was generated.\n`);
      return 2;
    }
  }

  const { wallet, mod, version, specifier } = await loadHDWallet(opts.hdWallet);
  process.stdout.write(`hd-wallet-wasm ${version} (${specifier})\n`);

  const mismatches = selfTestAgainstNode(wallet, mod);
  if (mismatches.length) {
    process.stderr.write(
      `REFUSED: this hd-wallet-wasm build derives ${mismatches.join(', ')} differently from the node ` +
        `(${VECTOR_PATH}). Nothing was generated.\n`,
    );
    return 3;
  }
  process.stdout.write('self-test: derivation matches the node on the public BIP-39 test vector\n');

  const base = loadAnchors(opts.anchors ? resolve(opts.anchors) : ANCHORS_PATH);
  const baseProblems = structuralProblems(base);
  if (baseProblems.length) {
    process.stderr.write(`REFUSED: the anchors file is unsound:\n  ${baseProblems.join('\n  ')}\n`);
    return 3;
  }

  if (opts.describeBackup) {
    if (opts.dryRun) throw new Error('--describe-backup is not a dry-run mode');
    return describeBackup(opts, wallet, mod, base);
  }

  if (!opts.dryRun) {
    const online = onlineInterfaces();
    if (online.length) {
      process.stdout.write(
        `\nThis machine has network interfaces up (${online.join(', ')}).\n` +
          'Generate the identity OFFLINE: disconnect the network, then continue.\n',
      );
      const answer = await readLine('Type OFFLINE to continue anyway, anything else to stop: ');
      if (answer.trim() !== 'OFFLINE') return 1;
    }
  }

  // The backup password, before the phrase is shown (verifier N3).
  let password;
  if (opts.passwordFile) password = readPasswordFile(resolve(opts.passwordFile));
  else if (opts.dryRun) password = randomBytes(24).toString('base64url');
  else password = await promptNewPassword();

  // Extra entropy into the module's DRBG; it mixes rather than replaces its own.
  const extra = randomBytes(64);
  wallet.injectEntropy(extra);
  extra.fill(0);

  let mnemonic = wallet.mnemonic.generate(opts.words);
  if (!wallet.mnemonic.validate(mnemonic)) throw new Error('generated phrase failed validation');
  let seed = wallet.mnemonic.toSeed(mnemonic, '');

  const files = [];
  let dryDir = null;
  let completed = false;
  try {
    const publicValues = deriveNodePublicIdentity(wallet, mod, seed);
    const anchors = mergeAnchors(base, { node: publicValues });
    for (const value of JSON.stringify(publicValues).match(/[0-9A-Za-z]{20,}/g) || []) {
      if (isDenied(base, value)) throw new Error('a derived value is on the SEC-001 denylist; refusing');
    }
    if (Object.keys(base.updateRoots || {}).includes(updateRootKeyId(publicValues.publisherPublicKey))) {
      throw new Error('the node publisher key equals the anchored update root; they must be separate keys');
    }
    wallet.utils.secureWipe(seed);
    seed = null;

    if (!opts.dryRun) await showOnceAndConfirm(mnemonic, 'NEW NODE IDENTITY');

    dryDir = opts.dryRun && !outDir ? mkdtempSync(join(tmpdir(), 'sdn-identity-dry-')) : null;
    const dir = outDir || dryDir;
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    chmodSync(dir, 0o700);

    const backupPath = join(dir, 'node-identity.backup');
    if (existsSync(backupPath)) throw new Error(`${backupPath} exists; refusing to overwrite`);
    const sealed = sealMnemonic(mnemonic, password);
    if (openMnemonic(sealed, password) !== mnemonic) throw new Error('the backup did not decrypt back to the phrase');
    writeOwnerFile(backupPath, sealed, 0o600);
    files.push(backupPath);

    if (opts.verifyWithNode) {
      if (!opts.dryRun) throw new Error('--verify-with-node is a dry-run check only');
      verifyWithNode(resolve(opts.verifyWithNode), backupPath, password, publicValues);
      process.stdout.write('node check: `spacedatanetwork key import --format backup` + `key export` agree on PeerID and xpub\n');
    }

    const publicPath = join(dir, 'public-identity.json');
    writeOwnerFile(publicPath, `${JSON.stringify({ kind: 'node', ...publicValues }, null, 2)}\n`, 0o644);
    files.push(publicPath);
    const anchorsPath = join(dir, 'anchors.json');
    writeOwnerFile(anchorsPath, `${JSON.stringify(anchors, null, 2)}\n`, 0o644);
    files.push(anchorsPath);

    printPublic('node', publicValues, opts.dryRun && dryDir ? [] : files);
    const remaining = releaseProblems(anchors).filter((p) => !p.startsWith('status'));
    if (remaining.length) {
      process.stdout.write(
        `\nanchors.json still has ${remaining.length} placeholder(s)` +
          (dryDir ? '.\n' : `; run deployment/identity/new-update-root.mjs --anchors ${anchorsPath}\n`),
      );
    } else {
      process.stdout.write('\nanchors.json is complete: send it (public) to the repo; keep the backup and the phrase offline.\n');
    }
    if (opts.dryRun) process.stdout.write('dry-run: throwaway phrase discarded; nothing secret was kept\n');
    completed = true;
    return 0;
  } finally {
    // A dry run leaves nothing behind, failed or not (verifier r2): its temp
    // directory goes, and a failed dry run removes the throwaway backup it
    // wrote to an --out-dir too.
    if (dryDir) rmSync(dryDir, { recursive: true, force: true });
    if (opts.dryRun && !completed) for (const file of files) rmSync(file, { force: true });
    if (seed) wallet.utils.secureWipe(seed);
    mnemonic = null;
    password = null;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(
    (code) => process.exit(code),
    (error) => {
      process.stderr.write(`new-node-identity: ${error.message}\n`);
      process.exit(1);
    },
  );
}
