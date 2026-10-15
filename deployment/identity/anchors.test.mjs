// SEC-001 identity rotation: the anchors file, its release gate, its derived
// copies, the denylist at every JS trust site, and the owner-run generator.
//
// Run: node --test deployment/identity/anchors.test.mjs
// Release gate: SDN_IDENTITY_RELEASE=1 makes the committed anchors' placeholder
// check FAIL (a release must not ship with a placeholder anchor).
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import {
  bootstrapMultiaddrs,
  discoveryCidFor,
  ed25519Spki,
  isDenied,
  loadAnchors,
  peerIdFromSecp256k1,
  publicKeyFromXpub,
  releaseProblems,
  secp256k1FromPeerId,
  structuralProblems,
  updateRootKeyId,
} from './anchors-lib.mjs';
import { drift } from './sync-anchors.mjs';
import { findInstalledHDWallet, hchacha20, mergeAnchors, openMnemonic, sealMnemonic } from './new-node-identity.mjs';
import { assertNoDeniedUpdateRoots, assertReleaseAnchors } from '../release/build-self-contained-cli.mjs';
import { resolveReleaseKeyId } from '../release/sign-update-manifest.mjs';
import { findMnemonicRuns, loadWordlist } from '../../scripts/check-no-mnemonics.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO = resolve(HERE, '../..');
const require = createRequire(import.meta.url);

// The PUBLIC values of the identity whose seed was published (SEC-001).
const OLD = {
  peerId: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  identityPublicKey: '0257d9a39fac79d4c36e017b3b6913f60684586605ebb9370cf417ef44bf0f7cd2',
  xpub: 'xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa',
  publisher: '0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c',
  rootKeyId: 'd4a971a7e534',
  rootSpki: 'MCowBQYDK2VwAyEADYDh/V+aTjTf3zag4VK9maZc//i8xsqydXtISuRC/Iw=',
  grant: '8e2f6db0048925ce413512df5b4261cee5ad7e04e65588d1a9af26cd4254fa2b',
  discoveryCid: 'bafkreiceqr2v4fvjqddussy5wydruetwmupxzvlett4ezl3zywg4ndl2di',
};

// Public values the NODE derives from the public BIP-39 test vector
// (sdn-server/internal/wasm/rotation_vector_test.go writes this file).
const VECTOR = JSON.parse(readFileSync(join(HERE, 'testdata/derivation-vector.json'), 'utf8'));

test('the anchors library reproduces the leaked identity from public values alone', () => {
  assert.equal(peerIdFromSecp256k1(OLD.identityPublicKey), OLD.peerId);
  assert.equal(secp256k1FromPeerId(OLD.peerId), OLD.identityPublicKey);
  assert.equal(discoveryCidFor(OLD.identityPublicKey), OLD.discoveryCid);
  assert.equal(publicKeyFromXpub(OLD.xpub), OLD.identityPublicKey);
  assert.equal(updateRootKeyId(OLD.publisher), OLD.rootKeyId);
  assert.equal(ed25519Spki(OLD.publisher), OLD.rootSpki);
});

test('the denylist names every public value of the leaked identity', () => {
  const anchors = loadAnchors();
  for (const value of Object.values(OLD)) {
    if (value === OLD.rootSpki) continue; // listed as its raw key + key id
    assert.equal(isDenied(anchors, value), true, value);
  }
  assert.equal(isDenied(anchors, OLD.grant.toUpperCase()), true);
  assert.equal(isDenied(anchors, anchors.peers[0].peerId), false, 'celestrak.eth is not part of SEC-001');
});

test('the committed anchors are sound, and never dial a placeholder or a denylisted peer', () => {
  const anchors = loadAnchors();
  assert.deepEqual(structuralProblems(anchors), []);
  for (const addr of [...bootstrapMultiaddrs(anchors), ...bootstrapMultiaddrs(anchors, 'browserTransports')]) {
    assert.doesNotMatch(addr, /PLACEHOLDER-/);
    assert.doesNotMatch(addr, new RegExp(OLD.peerId));
  }
});

test('RELEASE GATE: the committed anchors carry no placeholder at release time', () => {
  const problems = releaseProblems(loadAnchors());
  if (process.env.SDN_IDENTITY_RELEASE === '1') {
    assert.deepEqual(problems, [], 'a release must not ship with placeholder identity anchors');
  } else if (problems.length) {
    // Expected until the owner's anchors land; reported, not failed.
    console.log(`# not release-ready yet (${problems.length} problem(s)); SDN_IDENTITY_RELEASE=1 fails on this`);
  }
});

test('the release gate refuses placeholders, the leaked identity, and a non-separate update root', () => {
  const base = loadAnchors();
  assert.ok(releaseProblems(base).some((p) => p.includes('placeholder')));

  const filled = mergeAnchors(base, {
    node: { ...VECTOR, chainAddresses: undefined },
    distribution: {
      keyId: updateRootKeyId(VECTOR.distributionPublicKey),
      spki: ed25519Spki(VECTOR.distributionPublicKey),
    },
  });
  assert.equal(filled.status, 'released');
  assert.deepEqual(releaseProblems(filled), [], 'a complete, consistent set of new anchors passes');

  const leaked = structuredClone(filled);
  leaked.provider.peerId = OLD.peerId;
  assert.ok(releaseProblems(leaked).some((p) => p.includes('DENYLISTED')));

  const sameKey = structuredClone(filled);
  sameKey.updateRoots = { [updateRootKeyId(VECTOR.publisherPublicKey)]: ed25519Spki(VECTOR.publisherPublicKey) };
  assert.ok(releaseProblems(sameKey).some((p) => p.includes('separate from the node identity')));

  const oldRoot = structuredClone(filled);
  oldRoot.updateRoots = { [OLD.rootKeyId]: OLD.rootSpki };
  assert.ok(releaseProblems(oldRoot).some((p) => p.includes('DENYLISTED')));

  const mismatched = structuredClone(filled);
  mismatched.provider.discoveryCid = OLD.discoveryCid;
  assert.ok(releaseProblems(mismatched).length > 0);
});

test('every derived copy matches anchors.json (sync-anchors --check)', () => {
  assert.deepEqual(drift(), []);
});

test('the public-release bundle builder refuses placeholder anchors and the leaked update root', () => {
  assert.throws(() => assertReleaseAnchors('release', loadAnchors()), /not release-ready/);
  assert.doesNotThrow(() => assertReleaseAnchors('live-dht', loadAnchors()));
  assert.throws(() => assertNoDeniedUpdateRoots({ [OLD.rootKeyId]: OLD.rootSpki }), /denylisted/);
  assert.throws(() => assertNoDeniedUpdateRoots({ relabelled: OLD.rootSpki }), /denylisted/);
  assert.doesNotThrow(() => assertNoDeniedUpdateRoots({
    [updateRootKeyId(VECTOR.distributionPublicKey)]: ed25519Spki(VECTOR.distributionPublicKey),
  }));
});

test('release signing never defaults to, or accepts, the leaked update root', () => {
  assert.throws(() => resolveReleaseKeyId(OLD.rootKeyId, { dryRun: true }), /denylisted/);
  assert.throws(() => resolveReleaseKeyId('', { dryRun: false }), /placeholder/);
  assert.equal(resolveReleaseKeyId('', { dryRun: true }).startsWith('PLACEHOLDER-'), true);
  assert.equal(resolveReleaseKeyId('abcdef012345', { dryRun: false }), 'abcdef012345');

  // The one exception: the release that moves installed nodes to the new root.
  const newRoot = { [updateRootKeyId(VECTOR.distributionPublicKey)]: ed25519Spki(VECTOR.distributionPublicKey) };
  assert.equal(resolveReleaseKeyId(OLD.rootKeyId, { rootMigration: { reason: 'SEC-001', trustRoots: newRoot } }), OLD.rootKeyId);
  assert.throws(() => resolveReleaseKeyId(OLD.rootKeyId, { rootMigration: { reason: 'SEC-001', trustRoots: {} } }), /trust-roots/);
  assert.throws(() => resolveReleaseKeyId(OLD.rootKeyId, { rootMigration: { reason: '', trustRoots: newRoot } }), /reason/);
  assert.throws(
    () => resolveReleaseKeyId(OLD.rootKeyId, { rootMigration: { reason: 'x', trustRoots: { [OLD.rootKeyId]: OLD.rootSpki } } }),
    /denylisted/,
  );
});

test('the desktop shell refuses to package a bundle trusting the leaked update root', async () => {
  const { deniedUpdateRoots } = require('../../desktop/src/common/identity-denylist.js');
  assert.deepEqual(deniedUpdateRoots({ [OLD.rootKeyId]: 'x', other: OLD.rootSpki, ok: ed25519Spki(VECTOR.distributionPublicKey) }), [OLD.rootKeyId, 'other']);

  const { assertNoDeniedUpdateRoots: desktopGate } = await import('../../desktop/scripts/stage-node-bundle.mjs');
  const bundle = mkdtempSync(join(tmpdir(), 'sdn-desktop-bundle-'));
  mkdirSync(join(bundle, 'trust'));
  writeFileSync(join(bundle, 'trust', 'update-roots.json'), JSON.stringify({ [OLD.rootKeyId]: OLD.rootSpki }));
  assert.throws(() => desktopGate(bundle), /denylisted/);
  writeFileSync(join(bundle, 'trust', 'update-roots.json'), JSON.stringify({ k: ed25519Spki(VECTOR.distributionPublicKey) }));
  assert.doesNotThrow(() => desktopGate(bundle));
});

// draft-irtf-cfrg-xchacha-03 §2.2.1 HChaCha20 test vector.
test('HChaCha20 matches the published test vector (XChaCha20-Poly1305 backup format)', () => {
  const key = Buffer.from(Array.from({ length: 32 }, (_, i) => i));
  const nonce = Buffer.from('000000090000004a0000000031415927', 'hex');
  assert.equal(
    hchacha20(key, nonce).toString('hex'),
    '82413b4227b27bfed30e42508a877d73a0f9e4d58a74a853c12ec41326d3ecdc',
  );
});

test('the backup the generator writes opens with the same password and not with another', () => {
  const phrase = 'synthetic placeholder text for the sealing round trip';
  const sealed = sealMnemonic(phrase, 'correct horse battery staple');
  assert.equal(sealed.length, 32 + 24 + Buffer.byteLength(phrase) + 16);
  assert.equal(openMnemonic(sealed, 'correct horse battery staple'), phrase);
  assert.throws(() => openMnemonic(sealed, 'wrong password'));
});

const HD_WALLET = process.env.SDN_HD_WALLET_RUNTIME || '';
function generatorAvailable() {
  return Boolean(HD_WALLET || findInstalledHDWallet());
}

test('generator --dry-run prints public values only and agrees with the node', { skip: !generatorAvailable() && 'hd-wallet-wasm not installed' }, () => {
  const args = [join(HERE, 'new-node-identity.mjs'), '--dry-run', ...(HD_WALLET ? ['--hd-wallet', HD_WALLET] : [])];
  const result = spawnSync(process.execPath, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /self-test: derivation matches the node/);
  assert.match(result.stdout, /peerId\s+16Uiu2/);
  const out = result.stdout + result.stderr;
  assert.deepEqual(findMnemonicRuns(out, loadWordlist()), [], 'the dry run printed a mnemonic-shaped run');
  assert.doesNotMatch(out, /xprv|PRIVATE KEY/);
});

test('generator refuses to run non-interactively', { skip: !generatorAvailable() && 'hd-wallet-wasm not installed' }, () => {
  const args = [join(HERE, 'new-node-identity.mjs'), ...(HD_WALLET ? ['--hd-wallet', HD_WALLET] : [])];
  const result = spawnSync(process.execPath, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /REFUSED: the identity generator runs only in an interactive terminal/);
});
