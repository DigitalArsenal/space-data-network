#!/usr/bin/env node
// new-update-root.mjs — OWNER-RUN, OFFLINE generator for the fleet UPDATE ROOT
// (SEC-001 rotation).
//
//   node deployment/identity/new-update-root.mjs --key-out <file outside any git checkout> \
//        [--anchors <anchors.json to extend>] [--out-dir <dir for the public files>]
//   node deployment/identity/new-update-root.mjs --dry-run      # self-test, no owner
//
// WHY A SEPARATE KEY. Until SEC-001 the fleet update root WAS host-01's node
// key (Ed25519 0d80e1fd…, key id d4a971a7e534), derived from the seed that was
// published. The owner ruled on 2026-10-15 that the new root is a standalone
// Ed25519 keypair, generated offline, never derived from any node seed, and
// that it reaches the fleet OUT-OF-BAND: each node's trust/update-roots.json is
// replaced directly (deployment/identity/ROTATION.md). Nothing is ever signed
// with the old root again.
//
// WHAT IT DOES (real mode, interactive terminal only)
//   1. Refuses unless stdin and stdout are a TTY and CI is unset; refuses a
//      --key-out inside any git work tree, or one that already exists — all
//      BEFORE a key exists.
//   2. Asks you to go offline (type OFFLINE to continue with interfaces up).
//   3. Generates a fresh Ed25519 keypair (node:crypto, the OS CSPRNG).
//   4. Self-tests it: signs a synthetic update manifest the way the Updater
//      page and sign-update-manifest.mjs do (Ed25519 over
//      SDN-UPDATE-MANIFEST-V1 || 0x00 || sha256(canonical manifest)) and
//      verifies it with the fleet's own JS verifier
//      (deployment/release/sdn-updater/manifest.js) under {key_id: SPKI}.
//   5. Refuses a key that is denylisted or equals any node key in anchors.json.
//   6. Writes the PRIVATE key to --key-out as an unencrypted PKCS#8 PEM, mode
//      0600 (exclusive create), in a 0700 directory: the exact format
//      `sign-update-manifest.mjs --key`, SDN_UPDATE_SIGNING_KEY_PEM and the
//      Updater page's "upload a key" read.
//   7. Prints ONLY public values (key id, SPKI, raw public key) and writes
//      public-update-root.json, update-roots.json (the file each node's
//      trust/update-roots.json is replaced with) and anchors.json with
//      updateRoots set to this root.
//
// It never prints, logs or sends the private key. --dry-run uses a throwaway
// in-memory key, runs every check, prints public values only and writes
// nothing (with --key-out it writes the throwaway key there, to prove the file
// modes, and the caller deletes it).
import { generateKeyPairSync, randomBytes, sign as edSign } from 'node:crypto';
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  ANCHORS_PATH,
  ed25519Spki,
  isDenied,
  isPlaceholder,
  loadAnchors,
  releaseProblems,
  structuralProblems,
  updateRootKeyId,
} from './anchors-lib.mjs';
import { assertOutsideGitWorkTree, onlineInterfaces, readLine, realResolve, writeOwnerFile } from './new-node-identity.mjs';

const require = createRequire(import.meta.url);
const { canonicalManifestBytes, signatureStatement, validateUpdateManifest } = require('../release/sdn-updater/manifest.js');

const MANIFEST_DOMAIN = 'SDN-UPDATE-MANIFEST-V1';

/** A fresh Ed25519 update root: the PEM (secret) and its public values. */
export function generateUpdateRoot() {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const spkiDer = publicKey.export({ type: 'spki', format: 'der' });
  const publicKeyHex = spkiDer.subarray(12).toString('hex');
  return {
    privateKey,
    pem: privateKey.export({ type: 'pkcs8', format: 'pem' }),
    publicKeyHex,
    keyId: updateRootKeyId(publicKeyHex),
    spki: ed25519Spki(publicKeyHex),
  };
}

/**
 * Sign a synthetic manifest with the key exactly as the fleet lane does and
 * verify it with the fleet's verifier under {keyId: spki}. Throws on any
 * mismatch: a root the fleet cannot verify must never be installed.
 */
export function selfTestUpdateRoot({ privateKey, keyId, spki }) {
  const now = new Date();
  const bundle = randomBytes(32);
  const { createHash } = require('node:crypto');
  const manifest = {
    schema: 'org.spacedatanetwork.update.v1',
    update_id: 'sec001-update-root-self-test',
    version: '0.0.0-self-test',
    sequence: 1,
    channel: 'self-test',
    created_at: now.toISOString(),
    expires_at: new Date(now.getTime() + 3600_000).toISOString(),
    target: { platform: 'linux', arch: 'amd64', kind: 'cli-bundle' },
    bundle: { hash: createHash('sha256').update(bundle).digest('hex'), size: bundle.length, format: 'tar.gz' },
    wasm: { hash: createHash('sha256').update(randomBytes(32)).digest('hex') },
    signing: { key_id: keyId, algorithm: 'Ed25519', public_key: spki, statement_domain: MANIFEST_DOMAIN },
  };
  const preimage = signatureStatement(MANIFEST_DOMAIN, canonicalManifestBytes(manifest));
  manifest.signing.signature = edSign(null, preimage, privateKey).toString('base64');
  validateUpdateManifest(manifest, {
    platform: 'linux',
    arch: 'amd64',
    currentSequence: 0,
    bundleHash: manifest.bundle.hash,
    trustedRoots: { [keyId]: spki },
  });
  // A different key under the same key id must fail (the verifier really checks).
  const other = generateKeyPairSync('ed25519').publicKey.export({ type: 'spki', format: 'der' }).toString('base64');
  let rejected = false;
  try {
    validateUpdateManifest(manifest, { platform: 'linux', arch: 'amd64', currentSequence: 0, bundleHash: manifest.bundle.hash, trustedRoots: { [keyId]: other } });
  } catch {
    rejected = true;
  }
  if (!rejected) throw new Error('self-test: the verifier accepted a signature under the wrong key');
}

/** Every Ed25519/secp256k1 node key anchors.json names (the root must differ from all). */
function nodeKeys(anchors) {
  const p = anchors.provider || {};
  return [p.publisherPublicKey, p.grantVerifierPublicKey, p.rootAdminPublicKey, p.x25519PublicKey]
    .filter((v) => typeof v === 'string' && !isPlaceholder(v))
    .map((v) => v.toLowerCase());
}

export function rootProblems(root, anchors) {
  const problems = [];
  if (isDenied(anchors, root.keyId) || isDenied(anchors, root.publicKeyHex)) problems.push('the key is on the SEC-001 denylist');
  if (nodeKeys(anchors).includes(root.publicKeyHex)) problems.push('the key equals a node key in anchors.json; the update root must be separate');
  return problems;
}

export function mergeUpdateRoot(base, root) {
  const anchors = JSON.parse(JSON.stringify(base));
  anchors.updateRoots = { [root.keyId]: root.spki };
  anchors.status = JSON.stringify(anchors).includes('"PLACEHOLDER-') ? 'placeholder' : 'released';
  return anchors;
}

function isSymlink(path) {
  try {
    return lstatSync(path).isSymbolicLink();
  } catch {
    return false;
  }
}

function writeExclusive(path, contents, mode) {
  // O_CREAT|O_EXCL|O_NOFOLLOW, refusing any pre-existing node (verifier r3).
  writeOwnerFile(path, contents, mode);
}

function parseArgs(argv) {
  const opts = { dryRun: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const value = () => {
      const next = argv[++i];
      if (next === undefined) throw new Error(`${arg} needs a value`);
      return next;
    };
    if (arg === '--dry-run' || arg === '--self-test') opts.dryRun = true;
    else if (arg === '--key-out') opts.keyOut = value();
    else if (arg === '--out-dir') opts.outDir = value();
    else if (arg === '--anchors') opts.anchors = value();
    else if (arg === '--help' || arg === '-h') opts.help = true;
    else throw new Error(`unknown argument ${arg}`);
  }
  return opts;
}

const USAGE = `usage:
  node deployment/identity/new-update-root.mjs --key-out <file outside any git checkout>
       [--anchors <anchors.json to extend>] [--out-dir <dir for the public files>]
  node deployment/identity/new-update-root.mjs --dry-run [--key-out <file>]
Real mode runs only in an interactive terminal, offline. See deployment/identity/ROTATION.md.`;

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
        'REFUSED: the update-root generator runs only in an interactive terminal (stdin and stdout a TTY, not CI).\n' +
          'Use --dry-run for an automated self-test with a throwaway key.\n',
      );
      return 2;
    }
    if (!opts.keyOut) {
      process.stderr.write('REFUSED: --key-out <file> is required (outside every git checkout).\n');
      return 2;
    }
  }

  // Every path check happens BEFORE a key exists.
  const keyOut = opts.keyOut ? resolve(opts.keyOut) : null;
  const outDir = opts.outDir ? resolve(opts.outDir) : keyOut ? dirname(keyOut) : null;
  try {
    if (keyOut) {
      // The key path as written and as it resolves through any symlink.
      assertOutsideGitWorkTree(dirname(keyOut), '--key-out');
      assertOutsideGitWorkTree(realResolve(keyOut), '--key-out (resolved)');
      if (isSymlink(keyOut)) throw new Error(`${keyOut} is a symlink; name the real file`);
      if (existsSync(keyOut)) throw new Error(`${keyOut} exists; refusing to overwrite`);
    }
    if (outDir) assertOutsideGitWorkTree(realResolve(outDir), '--out-dir (resolved)');
    if (outDir) {
      for (const name of ['public-update-root.json', 'update-roots.json', 'anchors.json']) {
        if (existsSync(join(outDir, name)) || isSymlink(join(outDir, name))) throw new Error(`${join(outDir, name)} exists (or is a symlink); refusing to overwrite (use another --out-dir)`);
      }
    }
  } catch (error) {
    process.stderr.write(`REFUSED: ${error.message.replace(/\.$/, '')}. Nothing was generated.\n`);
    return 2;
  }

  const base = loadAnchors(opts.anchors ? resolve(opts.anchors) : ANCHORS_PATH);
  const baseProblems = structuralProblems(base);
  if (baseProblems.length) {
    process.stderr.write(`REFUSED: the anchors file is unsound:\n  ${baseProblems.join('\n  ')}\n`);
    return 3;
  }

  if (!opts.dryRun) {
    const online = onlineInterfaces();
    if (online.length) {
      process.stdout.write(
        `\nThis machine has network interfaces up (${online.join(', ')}).\n` +
          'Generate the update root OFFLINE: disconnect the network, then continue.\n',
      );
      const answer = await readLine('Type OFFLINE to continue anyway, anything else to stop: ');
      if (answer.trim() !== 'OFFLINE') return 1;
    }
  }

  let root = generateUpdateRoot();
  let dryDir = null;
  try {
    selfTestUpdateRoot(root);
    process.stdout.write('self-test: a manifest signed with this key verifies under {key_id: SPKI} with the fleet verifier\n');
    const problems = rootProblems(root, base);
    if (problems.length) throw new Error(`refusing this key: ${problems.join('; ')}`);

    const files = [];
    if (keyOut) {
      if (!existsSync(dirname(keyOut))) mkdirSync(dirname(keyOut), { recursive: true, mode: 0o700 });
      const parentMode = statSync(dirname(keyOut)).mode & 0o777;
      if (parentMode & 0o077) {
        process.stdout.write(`note: ${dirname(keyOut)} is mode ${parentMode.toString(8)}; the key file itself is 0600.\n`);
      }
      writeExclusive(keyOut, root.pem, 0o600);
      files.push(`${keyOut}  (PRIVATE, 0600)`);
    }
    root.pem = null;

    const publicValues = { updateRootKeyId: root.keyId, updateRootSpki: root.spki, updateRootPublicKey: root.publicKeyHex };
    const anchors = mergeUpdateRoot(base, root);
    dryDir = opts.dryRun && !outDir ? mkdtempSync(join(tmpdir(), 'sdn-update-root-dry-')) : null;
    const dir = outDir || dryDir;
    mkdirSync(dir, { recursive: true });
    const publicFiles = {
      'public-update-root.json': { kind: 'update-root', ...publicValues },
      // Exactly what each node's trust/update-roots.json becomes (ROTATION.md step 3).
      'update-roots.json': { [root.keyId]: root.spki },
      'anchors.json': anchors,
    };
    for (const [name, value] of Object.entries(publicFiles)) {
      writeExclusive(join(dir, name), `${JSON.stringify(value, null, 2)}\n`, 0o644);
      if (!dryDir) files.push(join(dir, name));
    }

    const lines = ['', 'PUBLIC values only — fleet update root'];
    for (const [key, value] of Object.entries(publicValues)) lines.push(`  ${key.padEnd(24)} ${value}`);
    if (files.length) lines.push('', ...files.map((f) => `  wrote ${f}`));
    process.stdout.write(`${lines.join('\n')}\n`);

    const remaining = releaseProblems(anchors).filter((p) => !p.startsWith('status'));
    process.stdout.write(
      remaining.length
        ? `\nanchors.json still has ${remaining.length} placeholder(s)${dryDir ? '.' : ': run new-node-identity.mjs --anchors on it, or merge.'}\n`
        : '\nanchors.json is complete: send it (public) to the repository. Keep the private key offline.\n',
    );
    if (opts.dryRun) process.stdout.write(`dry-run: throwaway key discarded${keyOut ? ` (except ${basename(keyOut)}, which the caller deletes)` : ''}\n`);
    return 0;
  } finally {
    if (dryDir) rmSync(dryDir, { recursive: true, force: true });
    root = null;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then(
    (code) => process.exit(code),
    (error) => {
      process.stderr.write(`new-update-root: ${error.message}\n`);
      process.exit(1);
    },
  );
}
