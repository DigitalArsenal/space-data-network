#!/usr/bin/env node
// check-no-denylisted-identity.mjs — no tracked file may carry a public value
// of a denylisted identity (deployment/identity/anchors.json denylist) outside
// the places that exist to name it.
//
//   node scripts/check-no-denylisted-identity.mjs
//
// SEC-001: the old sdn.spaceaware.io identity's PeerID, keys, xpub, update
// root, discovery CID and chain addresses were pinned in ~60 files. A copy
// left in a config, a fixture or a doc is how a retired identity gets trusted
// again, so CI (scripts/oss-preflight.sh) fails on any copy outside:
//   - the anchors file and its generated copies (they hold the denylist);
//   - the regression tests that assert the denylist still names them;
//   - the rotation runbook;
//   - PENDING files below, each with the lane that must edit it.
import { execFileSync } from 'node:child_process';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { base58Decode, ed25519Spki, loadAnchors } from '../deployment/identity/anchors-lib.mjs';

const B32 = 'abcdefghijklmnopqrstuvwxyz234567';
const B36 = '0123456789abcdefghijklmnopqrstuvwxyz';

/**
 * A PeerID's CIDv1 (libp2p-key, 0x72) spellings, as IPNS names and gateways
 * show them: multibase base32 (`b…`) and base36 (`k…`). Verifier N2.
 */
export function peerIdCidForms(peerId) {
  const cid = Buffer.concat([Buffer.from([0x01, 0x72]), base58Decode(peerId)]);
  let bits = 0;
  let acc = 0;
  let b32 = '';
  for (const byte of cid) {
    acc = (acc << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      b32 += B32[(acc >>> (bits - 5)) & 31];
      bits -= 5;
    }
    acc &= (1 << bits) - 1;
  }
  if (bits > 0) b32 += B32[(acc << (5 - bits)) & 31];
  let n = BigInt(`0x${cid.toString('hex')}`);
  let b36 = '';
  while (n > 0n) {
    b36 = B36[Number(n % 36n)] + b36;
    n /= 36n;
  }
  for (const byte of cid) {
    if (byte !== 0) break;
    b36 = `0${b36}`;
  }
  return [`b${b32}`, `k${b36}`];
}
import { indexBlobs } from './check-no-mnemonics.mjs';

export const ALLOWED = [
  /^deployment\/identity\//, // anchors.json, its tests, ROTATION.md
  /^sdn-server\/internal\/trustanchors\//, // embedded copy + denylist regression test
  /^sdn-js\/src\/identity-anchors\.generated\.ts$/,
  /^sdn-js\/src\/identity-denylist\.test\.ts$/,
  /^desktop\/src\/common\/identity-anchors\.json$/,
];

// Historical data, not trust: these name the old PeerID as the PRODUCER TAG of
// records already stored (benchset fixtures.t6w; host-02's store shape). They
// stay until that data is re-recorded; nothing dials or trusts them.
export const HISTORICAL = new Map([
  ['sdn-server/internal/storage/format4proof/coverage_reads.go', 'producer tag of recorded benchset fixtures.t6w'],
  ['sdn-server/internal/stress/production_shape.go', "producer table names of host-02's existing store"],
]);

// Edits owed by another lane (their files are claimed there). Remove an entry
// when its file is fixed; the check then holds it to the rule.
export const PENDING = new Map([
  ['docs/OPERATOR-ENROLLMENT.md', 'sdn-security-auth-20261015 holds it: replace the sample peer_id'],
  ['sdn-server/cmd/spacedatanetwork/embedded/docs/operator-enrollment.html', 'regenerate from docs/OPERATOR-ENROLLMENT.md (docs/build-docs.mjs)'],
  ['sdn-server/cmd/spacedatanetwork/embedded/docs/operator-enrolment.html', 'regenerate from docs/OPERATOR-ENROLLMENT.md (docs/build-docs.mjs)'],
  ['sdn-server/cmd/spacedatanetwork/bond_attestation_test.go', 'sdn-security-auth-20261015 holds it: use a synthetic BTC address'],
]);

/** Every denylisted value as a {value, caseInsensitive} needle. */
export function needles(anchors = loadAnchors()) {
  const d = anchors.denylist;
  const out = [];
  const add = (value, ci) => value && out.push({ value, caseInsensitive: ci });
  for (const v of d.peerIds) {
    add(v, false);
    for (const form of peerIdCidForms(v)) add(form, false);
  }
  for (const v of d.xpubs) add(v, false);
  for (const v of d.secp256k1PublicKeys) add(v, true);
  for (const v of d.ed25519PublicKeys) {
    add(v, true);
    add(ed25519Spki(v), false);
  }
  for (const v of d.x25519PublicKeys || []) {
    add(v, true);
    add(Buffer.from(v, 'hex').toString('base64'), false);
  }
  for (const v of d.updateRootKeyIds) add(v, true);
  for (const v of d.discoveryCids) add(v, true);
  for (const v of d.chainAddresses) add(v, /^(bc1|0x)/i.test(v));
  return out;
}

export function denylistedValuesIn(content, list) {
  const text = Buffer.isBuffer(content) ? content.toString('latin1') : String(content);
  const lower = text.toLowerCase();
  return list.filter((n) => (n.caseInsensitive ? lower.includes(n.value.toLowerCase()) : text.includes(n.value)));
}

export function main() {
  const root = execFileSync('git', ['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim();
  process.chdir(root);
  const list = needles(loadAnchors(join(root, 'deployment/identity/anchors.json')));
  const hits = [];
  const pending = [];
  for (const { path, content } of indexBlobs()) {
    if (ALLOWED.some((re) => re.test(path)) || HISTORICAL.has(path)) continue;
    const found = denylistedValuesIn(content, list);
    if (!found.length) continue;
    const line = `${path}: ${found.map((n) => n.value.slice(0, 16)).join(', ')}`;
    if (PENDING.has(path)) pending.push(`${line}  (${PENDING.get(path)})`);
    else hits.push(line);
  }
  if (pending.length) {
    console.error(`[denylisted-identity] ${pending.length} file(s) still name the SEC-001 identity, owed by another lane:`);
    for (const p of pending) console.error(`  ${p}`);
  }
  if (hits.length) {
    console.error('[denylisted-identity] FAIL: these tracked files carry a public value of a DENYLISTED identity (SEC-001).');
    console.error('Read the identity from deployment/identity/anchors.json (or a copy sync-anchors.mjs writes) instead:');
    for (const h of hits) console.error(`  ${h}`);
    return 1;
  }
  console.log('[denylisted-identity] PASS: no denylisted identity value outside the anchors, its tests and the runbook');
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(main());
}
