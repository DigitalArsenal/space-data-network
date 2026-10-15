// SEC-001 identity rotation: the anchors file, its release gate, its derived
// copies, the denylist at every JS trust site, and the owner-run generator.
//
// Run: node --test deployment/identity/anchors.test.mjs
// Release gate: SDN_IDENTITY_RELEASE=1 makes the committed anchors' placeholder
// check FAIL (a release must not ship with a placeholder anchor).
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createPublicKey } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
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
import { assertTrustRootsMatchAnchors, resolveReleaseKeyId } from '../release/sign-update-manifest.mjs';
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

  // No in-band root migration (owner, 2026-10-15): the published root signs
  // nothing, whatever the release carries.
  const newRoot = { [updateRootKeyId(VECTOR.distributionPublicKey)]: ed25519Spki(VECTOR.distributionPublicKey) };
  assert.throws(() => resolveReleaseKeyId(OLD.rootKeyId, { rootMigration: { reason: 'SEC-001', trustRoots: newRoot } }), /denylisted/);
});

test('S7: --trust-roots must equal the anchored update roots exactly, each key id matching its key', () => {
  const [[newId, newSpki]] = Object.entries({ [updateRootKeyId(VECTOR.distributionPublicKey)]: ed25519Spki(VECTOR.distributionPublicKey) });
  const released = { ...loadAnchors(), updateRoots: { [newId]: newSpki } };
  assert.doesNotThrow(() => assertTrustRootsMatchAnchors({ [newId]: newSpki }, released));
  assert.throws(() => assertTrustRootsMatchAnchors({}, released), /names no root/);
  assert.throws(() => assertTrustRootsMatchAnchors({ abcdef012345: newSpki }, released), /under key id/);
  assert.throws(() => assertTrustRootsMatchAnchors({ [OLD.rootKeyId]: OLD.rootSpki }, released), /denylisted/);
  assert.throws(() => assertTrustRootsMatchAnchors({ 'PLACEHOLDER-X': 'PLACEHOLDER-Y' }, released), /placeholder/);
  // A well-formed root that is not the committed one (a swapped file).
  const otherHex = '11'.repeat(32);
  assert.throws(() => assertTrustRootsMatchAnchors({ [updateRootKeyId(otherHex)]: ed25519Spki(otherHex) }, released), /exactly/);
  // An extra root beside the committed one.
  assert.throws(
    () => assertTrustRootsMatchAnchors({ [newId]: newSpki, [updateRootKeyId(otherHex)]: ed25519Spki(otherHex) }, released),
    /exactly/,
  );
  // While anchors.json is still a placeholder, no --trust-roots passes.
  assert.throws(() => assertTrustRootsMatchAnchors({ [newId]: newSpki }), /exactly/);
});

test('the desktop shell refuses to package a bundle trusting the leaked update root', async () => {
  const { deniedUpdateRoots } = require('../../desktop/src/common/identity-denylist.js');
  assert.deepEqual(deniedUpdateRoots({ [OLD.rootKeyId]: 'x', other: OLD.rootSpki, ok: ed25519Spki(VECTOR.distributionPublicKey) }), [OLD.rootKeyId, 'other']);

  const { assertNoDeniedUpdateRoots: desktopGate } = await import('../../desktop/scripts/stage-node-bundle.mjs');
  const bundle = mkdtempSync(join(tmpdir(), 'sdn-desktop-bundle-'));
  mkdirSync(join(bundle, 'trust'));
  writeFileSync(join(bundle, 'trust', 'update-roots.json'), JSON.stringify({ [OLD.rootKeyId]: OLD.rootSpki }));
  assert.throws(() => desktopGate(bundle), /denylisted/);
  writeFileSync(join(bundle, 'trust', 'update-roots.json'), JSON.stringify({ 'PLACEHOLDER-SEC001-UPDATE-ROOT-KEY-ID': 'PLACEHOLDER-SEC001-UPDATE-ROOT-SPKI' }));
  assert.throws(() => desktopGate(bundle), /PLACEHOLDER/);
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

// ---------------------------------------------------------------------------
// The fleet UPDATE ROOT generator (owner, 2026-10-15): a standalone Ed25519
// key, separate from the node identity, moved to the fleet out-of-band.
// ---------------------------------------------------------------------------
test('update-root generator --dry-run prints public values only and self-tests against the fleet verifier', () => {
  const result = spawnSync(process.execPath, [join(HERE, 'new-update-root.mjs'), '--dry-run'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /self-test: a manifest signed with this key verifies/);
  assert.match(result.stdout, /updateRootKeyId\s+[0-9a-f]{12}\n/);
  assert.doesNotMatch(result.stdout + result.stderr, /PRIVATE KEY|BEGIN/);
});

test('update-root generator refuses non-interactive runs and a --key-out inside a git checkout, before any key exists', () => {
  const tty = spawnSync(process.execPath, [join(HERE, 'new-update-root.mjs'), '--key-out', join(tmpdir(), 'never.pem')], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  assert.equal(tty.status, 2);
  assert.match(tty.stderr, /REFUSED: the update-root generator runs only in an interactive terminal/);
  const inRepo = join(HERE, 'sec001-update-root-test.pem');
  const result = spawnSync(process.execPath, [join(HERE, 'new-update-root.mjs'), '--dry-run', '--key-out', inRepo], { encoding: 'utf8' });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /inside the git work tree/);
  assert.equal(existsSync(inRepo), false);
});

test('update-root generator writes a 0600 PKCS#8 PEM the publisher scripts and the Updater page read, and public files only beside it', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-update-root-'));
  try {
    const keyOut = join(dir, 'keys', 'sdn-update-root.pem');
    const result = spawnSync(process.execPath, [join(HERE, 'new-update-root.mjs'), '--dry-run', '--key-out', keyOut], { encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(statSync(keyOut).mode & 0o777, 0o600);
    assert.equal(statSync(dirname(keyOut)).mode & 0o777, 0o700);
    // sign-update-manifest.mjs --key / SDN_UPDATE_SIGNING_KEY_PEM
    const { normalizeSigningKey, resolveSigningKeyPem } = await import('../release/sign-update-manifest.mjs');
    const key = normalizeSigningKey(await resolveSigningKeyPem(keyOut));
    // The Updater page's openKey: a 48-byte RFC 8410 PKCS#8 DER with the fixed prefix.
    const der = key.export({ type: 'pkcs8', format: 'der' });
    assert.equal(der.length, 48);
    assert.equal(der.subarray(0, 16).toString('hex'), '302e020100300506032b657004220420');
    const publicHex = createPublicKey(key).export({ type: 'spki', format: 'der' }).subarray(12).toString('hex');
    const roots = JSON.parse(readFileSync(join(dirname(keyOut), 'update-roots.json'), 'utf8'));
    assert.deepEqual(roots, { [updateRootKeyId(publicHex)]: ed25519Spki(publicHex) });
    const anchors = JSON.parse(readFileSync(join(dirname(keyOut), 'anchors.json'), 'utf8'));
    assert.deepEqual(anchors.updateRoots, roots);
    for (const name of ['update-roots.json', 'anchors.json', 'public-update-root.json']) {
      assert.doesNotMatch(readFileSync(join(dirname(keyOut), name), 'utf8'), /PRIVATE|BEGIN/);
    }
    // Never overwrites a key.
    const again = spawnSync(process.execPath, [join(HERE, 'new-update-root.mjs'), '--dry-run', '--key-out', keyOut], { encoding: 'utf8' });
    assert.equal(again.status, 2);
    assert.match(again.stderr, /exists; refusing to overwrite/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('update-root generator refuses the leaked root and any node key as the new root', async () => {
  const { rootProblems } = await import('./new-update-root.mjs');
  const anchors = loadAnchors();
  assert.match(rootProblems({ keyId: OLD.rootKeyId, publicKeyHex: OLD.publisher }, anchors).join(), /denylist/);
  const withNode = { ...anchors, provider: { ...anchors.provider, publisherPublicKey: VECTOR.publisherPublicKey } };
  assert.match(rootProblems({ keyId: updateRootKeyId(VECTOR.publisherPublicKey), publicKeyHex: VECTOR.publisherPublicKey }, withNode).join(), /separate/);
  assert.deepEqual(rootProblems({ keyId: updateRootKeyId(VECTOR.distributionPublicKey), publicKeyHex: VECTOR.distributionPublicKey }, anchors), []);
});

test('node generator: --kind distribution is withdrawn and --out-dir inside a checkout is refused before a phrase exists', () => {
  const dist = spawnSync(process.execPath, [join(HERE, 'new-node-identity.mjs'), '--dry-run', '--kind', 'distribution'], { encoding: 'utf8' });
  assert.notEqual(dist.status, 0);
  assert.match(dist.stderr, /new-update-root\.mjs/);
  const inRepo = join(HERE, 'sec001-node-test');
  const result = spawnSync(process.execPath, [join(HERE, 'new-node-identity.mjs'), '--dry-run', '--out-dir', inRepo], { encoding: 'utf8' });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /inside the git work tree/);
  assert.equal(existsSync(inRepo), false);
});

// ---------------------------------------------------------------------------
// The owner's per-node check and out-of-band root swap (ROTATION.md 0b, 3).
// ---------------------------------------------------------------------------
test('sec001-node-check.py lists changes since the exposure and swaps the roots out-of-band, refusing the published root', () => {
  const python = spawnSync('python3', ['--version'], { encoding: 'utf8' });
  if (python.status !== 0) return;
  const dir = mkdtempSync(join(tmpdir(), 'sdn-node-check-'));
  try {
    const bundle = join(dir, 'sdn-bundle');
    mkdirSync(join(bundle, 'bin'), { recursive: true });
    mkdirSync(join(bundle, 'trust'));
    mkdirSync(join(bundle, 'updates', 'staged', '1.0.6-updatelane.feedface0'), { recursive: true });
    writeFileSync(join(bundle, 'manifest.json'), '{}');
    writeFileSync(join(bundle, 'bin', 'spacedatanetwork'), 'not the recorded binary');
    writeFileSync(join(bundle, 'trust', 'update-roots.json'), JSON.stringify({ [OLD.rootKeyId]: OLD.rootSpki }));
    writeFileSync(
      join(bundle, 'updates', 'staged', '1.0.6-updatelane.feedface0', 'manifest.json'),
      JSON.stringify({ update_id: 'sdn-cli-bundle-1.0.6-updatelane.feedface0', version: '1.0.6-updatelane.feedface0', sequence: 1, signing: { key_id: OLD.rootKeyId } }),
    );
    writeFileSync(
      join(bundle, 'deploy-ledger.jsonl'),
      [
        { recorded_at: '2026-07-01T00:00:00Z', action: 'apply', update_id: 'before-exposure', version: 'a', sequence: 1 },
        { recorded_at: '2026-10-09T20:40:00Z', action: 'apply', update_id: 'sdn-cli-bundle-1.0.6-updatelane.24c0512bc', version: '1.0.6-updatelane.24c0512bc', sequence: 2 },
      ].map((e) => JSON.stringify(e)).join('\n'),
    );
    const script = join(HERE, 'sec001-node-check.py');
    const run = (...args) => spawnSync('python3', ['-I', script, '--bundle', bundle, ...args], { encoding: 'utf8' });

    const report = run();
    assert.equal(report.status, 0, report.stderr);
    assert.match(report.stdout, /NOT one of the values recorded at containment/);
    assert.match(report.stdout, /still trusts the PUBLISHED root/);
    assert.match(report.stdout, /sdn-cli-bundle-1\.0\.6-updatelane\.24c0512bc/);
    assert.doesNotMatch(report.stdout, /before-exposure/);
    assert.match(report.stdout, /\[SINCE-EXPOSURE\] staged\/1\.0\.6-updatelane\.feedface0 .*signer=d4a971a7e534/);

    const newRoots = join(dir, 'update-roots.json');
    const newRoot = { [updateRootKeyId(VECTOR.distributionPublicKey)]: ed25519Spki(VECTOR.distributionPublicKey) };
    writeFileSync(newRoots, JSON.stringify(newRoot));
    const evidence = join(dir, 'evidence');
    assert.equal(run('--swap-roots', newRoots).status, 0, 'validation only');
    assert.deepEqual(JSON.parse(readFileSync(join(bundle, 'trust', 'update-roots.json'), 'utf8')), { [OLD.rootKeyId]: OLD.rootSpki }, 'nothing changes without --apply');
    const applied = run('--swap-roots', newRoots, '--apply', '--evidence-dir', evidence);
    assert.equal(applied.status, 0, applied.stderr);
    assert.deepEqual(JSON.parse(readFileSync(join(bundle, 'trust', 'update-roots.json'), 'utf8')), newRoot);
    assert.match(applied.stdout, /old file kept at/);

    // Refusals: the published root (under its own id or another), a mislabelled key.
    for (const bad of [{ [OLD.rootKeyId]: OLD.rootSpki }, { abcdef012345: OLD.rootSpki }, { abcdef012345: newRoot[Object.keys(newRoot)[0]] }]) {
      writeFileSync(newRoots, JSON.stringify(bad));
      const refused = run('--swap-roots', newRoots, '--apply', '--evidence-dir', evidence);
      assert.notEqual(refused.status, 0);
      assert.match(refused.stderr, /REFUSED/);
    }
    assert.deepEqual(JSON.parse(readFileSync(join(bundle, 'trust', 'update-roots.json'), 'utf8')), newRoot, 'a refused swap leaves the roots alone');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('N2: the identity check also matches the leaked PeerID as a CIDv1 (base32 b…, base36 k…)', async () => {
  const { needles, peerIdCidForms, denylistedValuesIn } = await import('../../scripts/check-no-denylisted-identity.mjs');
  // The forms public IPNS gateways and delegated routing printed for it.
  assert.deepEqual(peerIdCidForms(OLD.peerId), [
    'bafzaajiiaijccasx3grz7ldz2tbw4al3hnurh5qgqrmgmbplxe3qz5ax55cl6d342i',
    'kzwfwjn5ji4pulv1dujjdqt1y4a9iy424ibh6zpez5u2c3h8xrrfbsok98qrbgy',
  ]);
  const list = needles(loadAnchors());
  for (const form of peerIdCidForms(OLD.peerId)) {
    assert.equal(denylistedValuesIn(`https://${form}.ipns.dweb.link/`, list).length, 1, form);
  }
});
