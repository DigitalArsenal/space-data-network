#!/usr/bin/env node
/**
 * End-to-end verification of persisted SDN node identity (HERMES ruling, #113).
 *
 * Acceptance:
 * 1. Booting SDNNode twice against the same temp dir via
 *    `loadOrCreateIdentity(fileIdentityStore(keyPath))` yields the exact same
 *    libp2p PeerID.
 * 2. Key file has mode 0o600 and parent dir has mode 0o700 on POSIX (owner-only
 *    ACL on Windows).
 * 3. Neither stdout nor stderr prints any word of a mnemonic or hex of the seed.
 * 4. A corrupted identity file throws on `loadOrCreateIdentity` and stays
 *    byte-identical on disk (never silently replaced).
 * 5. A pinned 64-byte seed test vector derives the exact secp256k1 PeerID that
 *    the Go node produces for the same compressed public key at m/44'/0'/0'.
 */

import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

const distIndexUrl = pathToFileURL(path.join(packageRoot, 'dist/index.mjs')).href;
const distNodeUrl = pathToFileURL(path.join(packageRoot, 'dist/node/index.mjs')).href;

const { SDNNode, loadOrCreateIdentity, IDENTITY_SEED_LENGTH } = await import(distIndexUrl);
const { fileIdentityStore } = await import(distNodeUrl);

// Pinned 64-byte master seed (0x00..0x3f) -> m/44'/0'/0' secp256k1 compressed
// public key 03d1b191e17ab9c69d1e1a0596bd607da04bb0db4cd3097b0c4ebf27f8333d12e5
// -> libp2p PeerID (matches Go libp2p/core/peer.IDFromPublicKey).
const PINNED_SEED = Uint8Array.from({ length: IDENTITY_SEED_LENGTH }, (_, i) => i);
const PINNED_PEER_ID = '16Uiu2HAmSmYbwC7G8xqHgGa244jcU9PTyEWVBArKhtA8ABFjBdXS';

const tmpRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'sdn-node-identity-e2e-'));

try {
  // 1. Two separate SDNNode boots against the same key path keep the same PeerID.
  const keyDir = path.join(tmpRoot, 'config');
  const keyPath = path.join(keyDir, 'identity.key');
  const store = fileIdentityStore(keyPath);

  const id1 = await loadOrCreateIdentity(store);
  const node1 = await SDNNode.create({
    enableStorage: false,
    enableRelayProbing: false,
    connectionMonitor: false,
    identity: id1,
    bootstrapPeers: [],
  });
  const peerId1 = node1.peerId;
  await node1.stop();

  const id2 = await loadOrCreateIdentity(fileIdentityStore(keyPath));
  const node2 = await SDNNode.create({
    enableStorage: false,
    enableRelayProbing: false,
    connectionMonitor: false,
    identity: id2,
    bootstrapPeers: [],
  });
  const peerId2 = node2.peerId;
  await node2.stop();

  assert.equal(peerId1, id1.peerId, 'first boot SDNNode PeerID matches DerivedIdentity');
  assert.equal(peerId2, peerId1, 'second boot SDNNode PeerID matches first boot');
  assert.equal(id2.peerId, id1.peerId, 'reloaded DerivedIdentity matches first boot');

  // 2. Permissions: 0700 dir and 0600 file on POSIX (owner-only ACL on Windows).
  if (process.platform !== 'win32') {
    const dirStat = await fs.stat(keyDir);
    const fileStat = await fs.stat(keyPath);
    assert.equal(dirStat.mode & 0o777, 0o700, `expected dir mode 0700, got ${(dirStat.mode & 0o777).toString(8)}`);
    assert.equal(fileStat.mode & 0o777, 0o600, `expected file mode 0600, got ${(fileStat.mode & 0o777).toString(8)}`);
  } else {
    const { stdout: aclOut } = await execFileAsync('icacls', [keyPath], { windowsHide: true });
    assert.ok(aclOut.includes('(F)'), `expected owner full-control ACL on Windows: ${aclOut}`);
    assert.ok(!/Everyone:/i.test(aclOut), `Windows key ACL must not grant Everyone: ${aclOut}`);
  }

  // 3. Stdout/stderr silence check: creating a brand-new identity in a child
  // process emits neither seed hex nor any mnemonic phrase.
  const silentKeyPath = path.join(tmpRoot, 'silent', 'identity.key');
  const childScript = `
    const { loadOrCreateIdentity } = await import(${JSON.stringify(distIndexUrl)});
    const { fileIdentityStore } = await import(${JSON.stringify(distNodeUrl)});
    const id = await loadOrCreateIdentity(fileIdentityStore(${JSON.stringify(silentKeyPath)}));
    if (!id.peerId) process.exit(2);
  `;
  const { stdout: childStdout, stderr: childStderr } = await execFileAsync(
    process.execPath,
    ['--input-type=module', '--eval', childScript],
    { cwd: packageRoot, timeout: 30_000 },
  );
  const rawSaved = await fs.readFile(silentKeyPath);
  const savedSeedHex = rawSaved.subarray(8, 72).toString('hex');
  const combinedOutput = `${childStdout}\n${childStderr}`;
  assert.ok(!combinedOutput.includes(savedSeedHex), 'child output must never contain seed hex');
  assert.ok(!combinedOutput.includes(savedSeedHex.slice(0, 16)), 'child output must never contain seed prefix');
  assert.equal(childStdout.trim(), '', 'loadOrCreateIdentity must print nothing to stdout');

  // 4. Corrupted file throws and stays byte-identical on disk.
  const corruptPath = path.join(tmpRoot, 'corrupt', 'identity.key');
  await fs.mkdir(path.dirname(corruptPath), { recursive: true });
  const originalCorrupt = Buffer.from('corrupted-identity-bytes-must-not-be-overwritten', 'utf8');
  await fs.writeFile(corruptPath, originalCorrupt, { mode: 0o600 });

  await assert.rejects(
    () => loadOrCreateIdentity(fileIdentityStore(corruptPath)),
    /corrupted|refusing to replace/i,
    'corrupted identity file must throw',
  );
  const afterCorrupt = await fs.readFile(corruptPath);
  assert.deepEqual(afterCorrupt, originalCorrupt, 'corrupted identity file must remain byte-identical');

  // Same check for a bit-flipped valid envelope (exact same 104-byte length).
  const flipped = Buffer.from(await fs.readFile(keyPath));
  flipped[20] ^= 0x01;
  const flippedPath = path.join(tmpRoot, 'flipped', 'identity.key');
  await fs.mkdir(path.dirname(flippedPath), { recursive: true });
  await fs.writeFile(flippedPath, flipped, { mode: 0o600 });
  await assert.rejects(
    () => loadOrCreateIdentity(fileIdentityStore(flippedPath)),
    /checksum|corrupted/i,
    'bit-flipped identity envelope must fail checksum',
  );
  assert.deepEqual(await fs.readFile(flippedPath), flipped, 'bit-flipped identity file must remain byte-identical');

  // 5. Pinned test vector matches Go node PeerID derivation.
  const pinnedPath = path.join(tmpRoot, 'pinned', 'identity.key');
  const pinnedStore = fileIdentityStore(pinnedPath);
  await pinnedStore.save(PINNED_SEED);
  const pinnedId = await loadOrCreateIdentity(pinnedStore);
  assert.equal(pinnedId.peerId, PINNED_PEER_ID, 'pinned seed must derive the Go-matching PeerID');

  const pinnedNode = await SDNNode.create({
    enableStorage: false,
    enableRelayProbing: false,
    connectionMonitor: false,
    identity: pinnedId,
    bootstrapPeers: [],
  });
  try {
    assert.equal(pinnedNode.peerId, PINNED_PEER_ID, 'SDNNode PeerID must match pinned vector');
  } finally {
    await pinnedNode.stop();
  }

  console.log(`PASS node-identity.e2e.mjs (stable=${peerId1}, pinned=${PINNED_PEER_ID})`);
} finally {
  await fs.rm(tmpRoot, { recursive: true, force: true });
}
process.exit(0);
